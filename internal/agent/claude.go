package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

type claudeAdapter struct{}

func (claudeAdapter) Name() string { return "claude" }

func (claudeAdapter) Detect(home string) bool {
	_, err := os.Stat(filepath.Join(home, ".claude"))
	return err == nil
}

func (claudeAdapter) Setup(home string) (string, error) {
	return setupClaude(home)
}

// upsertHookArray returns a new hook array where the Forge entry for
// forgeEventType is inserted (if absent) or replaced (if present), while all
// other entries are left untouched. Duplicate Forge entries are collapsed.
func upsertHookArray(existing []any, newEntry map[string]any, forgeEventType string) []any {
	var result []any
	inserted := false
	for _, item := range existing {
		if isForgeHookItem(item, forgeEventType) {
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

// isForgeHookItem reports whether item is a hook array entry that Forge owns
// for the given event type, identified by FORGE_EVENT_TYPE in the hook's env.
func isForgeHookItem(item any, forgeEventType string) bool {
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
		env, _ := hm["env"].(map[string]any)
		if v, _ := env["FORGE_EVENT_TYPE"].(string); v == forgeEventType {
			return true
		}
	}
	return false
}

func setupClaude(home string) (string, error) {
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	forgePath := ForgePath()

	settings := make(map[string]any)
	var existingBytes []byte
	if data, err := os.ReadFile(settingsPath); err == nil {
		existingBytes = data
		if err := json.Unmarshal(data, &settings); err != nil {
			log.Printf("Failed to parse Claude settings at %s: %v", settingsPath, err)
		}
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

	postToolUseEntry := map[string]any{
		"matcher": "*",
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": fmt.Sprintf(`"%s" hook`, forgePath),
				"async":   true,
				"env": map[string]string{
					"FORGE_SOURCE_TOOL": "claude",
					"FORGE_EVENT_TYPE":  "PostToolUse",
				},
			},
		},
	}
	stopEntry := map[string]any{
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": fmt.Sprintf(`"%s" hook`, forgePath),
				"async":   true,
				"env": map[string]string{
					"FORGE_SOURCE_TOOL": "claude",
					"FORGE_EVENT_TYPE":  "Stop",
				},
			},
		},
	}
	sessionEndEntry := map[string]any{
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": fmt.Sprintf(`"%s" hook`, forgePath),
				"async":   true,
				"env": map[string]string{
					"FORGE_SOURCE_TOOL": "claude",
					"FORGE_EVENT_TYPE":  "SessionEnd",
				},
			},
		},
	}
	userPromptEntry := map[string]any{
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": fmt.Sprintf(`"%s" hook`, forgePath),
				"async":   true,
				"env": map[string]string{
					"FORGE_SOURCE_TOOL": "claude",
					"FORGE_EVENT_TYPE":  "UserPromptSubmit",
				},
			},
		},
	}

	existing, _ := hooks["PostToolUse"].([]any)
	hooks["PostToolUse"] = upsertHookArray(existing, postToolUseEntry, "PostToolUse")
	existing, _ = hooks["UserPromptSubmit"].([]any)
	hooks["UserPromptSubmit"] = upsertHookArray(existing, userPromptEntry, "UserPromptSubmit")
	existing, _ = hooks["Stop"].([]any)
	hooks["Stop"] = upsertHookArray(existing, stopEntry, "Stop")
	existing, _ = hooks["SessionEnd"].([]any)
	hooks["SessionEnd"] = upsertHookArray(existing, sessionEndEntry, "SessionEnd")
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

	skillDir := filepath.Join(home, ".claude", "skills")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		return "", fmt.Errorf("create skills dir: %w", err)
	}

	skillContent := "# Forge — Silent Memory Forger\n\n" +
		"Forge captures your work sessions and distills them into lasting memories.\n\n" +
		"## How It Works\n\n" +
		"- **Capture**: Every tool use is logged automatically via hooks.\n" +
		"- **Distill**: The daemon runs every 10 minutes to summarize raw events into high-level insights.\n" +
		"- **Inject**: When you ask about past work, Forge provides context via the MCP tool.\n\n" +
		"## MCP Tools\n\n" +
		"### `get_recent_context`\n" +
		"Returns distilled memories and session summaries.\n\n" +
		"**When to use:**\n" +
		"- User asks \"what was I doing before the break?\"\n" +
		"- User asks \"what did we work on yesterday?\"\n" +
		"- User asks about past decisions or patterns\n\n" +
		"### `search_memories`\n" +
		"Full-text search on event payloads.\n\n" +
		"**When to use:**\n" +
		"- User asks \"did I fix this before?\"\n" +
		"- User asks \"what errors have we seen?\"\n\n" +
		"### `get_principles`\n" +
		"Returns distilled high-level principles (architecture decisions, patterns, preferences).\n\n" +
		"## Hooks\n\n" +
		"Forge hooks run automatically. You don't need to do anything — they capture:\n" +
		"- `PostToolUse` — every tool invocation\n" +
		"- `Stop` — when the agent finishes responding\n" +
		"- `SessionEnd` — when the session closes\n\n" +
		"## Commands\n\n" +
		"```\nforge status\nforge search query\nforge distill\n```\n"
	skillPath := filepath.Join(skillDir, "forge.md")
	if err := os.WriteFile(skillPath, []byte(skillContent), 0o600); err != nil {
		return "", fmt.Errorf("write skill: %w", err)
	}

	return skillPath, nil
}
