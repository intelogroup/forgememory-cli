package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// isAnyForgeCommandHookItem reports whether item is any Forge hook entry for
// the given event, regardless of the binary path. Used to remove stale entries
// where the forge binary has moved.
func isAnyForgeCommandHookItem(item any, eventName string) bool {
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
		if cmd, _ := hm["command"].(string); strings.Contains(cmd, "hook --source gemini --event "+eventName) {
			return true
		}
	}
	return false
}

// upsertCommandHookArray is like upsertHookArray but identifies Forge-owned
// entries by event pattern (not exact path) so stale entries with old binary
// paths are replaced rather than kept alongside the new entry.
func upsertCommandHookArray(existing []any, newEntry map[string]any, forgeCommand string) []any {
	// Extract event name from command for stale-path matching.
	eventName := ""
	if idx := strings.LastIndex(forgeCommand, "--event "); idx >= 0 {
		eventName = strings.TrimSpace(forgeCommand[idx+8:])
	}

	var result []any
	inserted := false
	for _, item := range existing {
		// Treat any forge hook for this event as owned by us (handles stale paths).
		if eventName != "" && isAnyForgeCommandHookItem(item, eventName) {
			if !inserted {
				result = append(result, newEntry)
				inserted = true
			}
			continue // drop stale entry
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
	if data, err := os.ReadFile(settingsPath); err == nil {
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
	// sessionStartCommand := fmt.Sprintf(`FORGE_SOURCE_TOOL=gemini "%s" inject-check`, forgePath)

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
	// SessionStart inject-check disabled — can be noisy on remote/sandboxed agents
	// sessionStartEntry := map[string]any{
	// 	"hooks": []any{map[string]any{"type": "command", "command": sessionStartCommand}},
	// }

	existingHooks, _ := hooks["BeforeAgent"].([]any)
	hooks["BeforeAgent"] = upsertCommandHookArray(existingHooks, beforeAgentEntry, beforeAgentCommand)
	existingHooks, _ = hooks["AfterTool"].([]any)
	hooks["AfterTool"] = upsertCommandHookArray(existingHooks, afterToolEntry, afterToolCommand)
	existingHooks, _ = hooks["AfterAgent"].([]any)
	hooks["AfterAgent"] = upsertCommandHookArray(existingHooks, afterAgentEntry, afterAgentCommand)
	existingHooks, _ = hooks["SessionEnd"].([]any)
	hooks["SessionEnd"] = upsertCommandHookArray(existingHooks, sessionEndEntry, sessionEndCommand)
	// SessionStart disabled
	// existingHooks, _ = hooks["SessionStart"].([]any)
	// hooks["SessionStart"] = upsertCommandHookArray(existingHooks, sessionStartEntry, sessionStartCommand)
	settings["hooks"] = hooks

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal settings: %w", err)
	}
	if err := WriteFileConfirm(settingsPath, data, 0o600); err != nil {
		return "", fmt.Errorf("write settings: %w", err)
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
		"- **Inject**: Past lessons are auto-injected at session start and available on demand.\n\n" +
		"## MCP Tools\n\n" +
		"### `get_recent_context` — session start (always)\n" +
		"### `search_memories` — before solving errors or re-implementing features\n" +
		"### `get_principles` — before architecture/design decisions\n" +
		"### `inject_principles` — before sending a prompt to a code agent\n" +
		"### `get_active_failures` — when something is mysteriously broken\n" +
		"### `get_session_summaries` — when you need a narrative of recent work\n\n" +
		"## Auto-Injection\n\n" +
		"At every session start, Forge auto-injects past lessons into context.\n" +
		"You don't need to call any tool — if principles exist, they appear automatically.\n\n" +
		"## Commands\n\n" +
		"```\nforge status\nforge search query\nforge distill\n```\n"
	skillPath := filepath.Join(skillDir, "forge-skill.md")
	if err := WriteFileConfirm(skillPath, []byte(skillContent), 0o600); err != nil {
		return "", fmt.Errorf("write skill: %w", err)
	}

	return skillPath, nil
}
