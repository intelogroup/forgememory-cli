package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type geminiAdapter struct{}

func (geminiAdapter) Name() string { return "gemini" }

func (geminiAdapter) Detect(home string) bool {
	_, err := os.Stat(filepath.Join(home, ".gemini"))
	return err == nil
}

func (geminiAdapter) Setup(home string) (string, error) {
	return setupGemini(home)
}

// isForgeCommandHookItem reports whether item is a hook array entry that Forge
// owns, identified by the exact command string used for agents like Gemini.
func isForgeCommandHookItem(item any, forgeCommand string) bool {
	m, ok := item.(map[string]any)
	if !ok {
		return false
	}
	hooks, _ := m["hooks"].([]any)
	for _, h := range hooks {
		hm, ok := h.(map[string]any)
		if !ok {
			continue
		}
		if cmd, _ := hm["command"].(string); cmd == forgeCommand {
			return true
		}
	}
	return false
}

// upsertCommandHookArray is like upsertHookArray but identifies Forge-owned
// entries by their command string rather than env metadata.
func upsertCommandHookArray(existing []any, newEntry map[string]any, forgeCommand string) []any {
	var result []any
	inserted := false
	for _, item := range existing {
		if isForgeCommandHookItem(item, forgeCommand) {
			if !inserted {
				result = append(result, newEntry)
				inserted = true
			}
			continue
		}
		result = append(result, item)
	}
	if !inserted {
		result = append(result, newEntry)
	}
	return result
}

func setupGemini(home string) (string, error) {
	settingsPath := filepath.Join(home, ".gemini", "settings.json")
	forgePath := ForgePath()

	settings := make(map[string]any)
	var existingBytes []byte
	if data, err := os.ReadFile(settingsPath); err == nil {
		existingBytes = data
		_ = json.Unmarshal(data, &settings)
	}

	mcpServers, _ := settings["mcpServers"].(map[string]any)
	if mcpServers == nil {
		mcpServers = make(map[string]any)
	}
	mcpServers["forge"] = map[string]any{
		"command": forgePath,
		"args":    []string{"mcp"},
		"env":     map[string]string{},
	}
	settings["mcpServers"] = mcpServers

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = make(map[string]any)
	}

	beforeAgentCommand := fmt.Sprintf(`"%s" hook --source gemini --event UserPromptSubmit`, forgePath)
	afterToolCommand := fmt.Sprintf(`"%s" hook --source gemini --event PostToolUse`, forgePath)
	afterAgentCommand := fmt.Sprintf(`"%s" hook --source gemini --event Stop`, forgePath)
	sessionEndCommand := fmt.Sprintf(`"%s" hook --source gemini --event SessionEnd`, forgePath)

	beforeAgentEntry := map[string]any{
		"hooks": []any{map[string]any{"type": "command", "command": beforeAgentCommand}},
	}
	afterToolEntry := map[string]any{
		"matcher": ".*",
		"hooks":   []any{map[string]any{"type": "command", "command": afterToolCommand}},
	}
	afterAgentEntry := map[string]any{
		"hooks": []any{map[string]any{"type": "command", "command": afterAgentCommand}},
	}
	sessionEndEntry := map[string]any{
		"hooks": []any{map[string]any{"type": "command", "command": sessionEndCommand}},
	}

	existingHooks, _ := hooks["BeforeAgent"].([]any)
	hooks["BeforeAgent"] = upsertCommandHookArray(existingHooks, beforeAgentEntry, beforeAgentCommand)
	existingHooks, _ = hooks["AfterTool"].([]any)
	hooks["AfterTool"] = upsertCommandHookArray(existingHooks, afterToolEntry, afterToolCommand)
	existingHooks, _ = hooks["AfterAgent"].([]any)
	hooks["AfterAgent"] = upsertCommandHookArray(existingHooks, afterAgentEntry, afterAgentCommand)
	existingHooks, _ = hooks["SessionEnd"].([]any)
	hooks["SessionEnd"] = upsertCommandHookArray(existingHooks, sessionEndEntry, sessionEndCommand)
	settings["hooks"] = hooks

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal settings: %w", err)
	}
	if !bytes.Equal(existingBytes, data) {
		if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
			return "", fmt.Errorf("create settings dir: %w", err)
		}
		if err := os.WriteFile(settingsPath, data, 0o600); err != nil {
			return "", fmt.Errorf("write settings: %w", err)
		}
	}

	skillDir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		return "", fmt.Errorf("create skills dir: %w", err)
	}

	skillContent := "# Forge — Silent Memory Forger\n\n" +
		"Forge captures your work sessions and distills them into lasting memories.\n\n" +
		"## How It Works\n\n" +
		"- **Capture**: Every tool use is logged automatically via hooks.\n" +
		"- **Distill**: The daemon runs every 10 minutes to summarize raw events.\n" +
		"- **Inject**: When you ask about past work, Forge provides context.\n\n" +
		"## Commands\n\n" +
		"```\nforge status\nforge search query\nforge distill\n```\n"
	skillPath := filepath.Join(skillDir, "forge-skill.md")
	if err := os.WriteFile(skillPath, []byte(skillContent), 0o600); err != nil {
		return "", fmt.Errorf("write skill: %w", err)
	}

	return skillPath, nil
}
