package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// ForgePath returns the absolute path to the forge binary.
func ForgePath() string {
	exe, err := os.Executable()
	if err != nil {
		return "forge"
	}
	return exe
}

// DetectAgents returns which AI agents are installed.
func DetectAgents(home string) []string {
	var agents []string
	if _, err := os.Stat(filepath.Join(home, ".claude")); err == nil {
		agents = append(agents, "claude")
	}
	if _, err := os.Stat(filepath.Join(home, ".gemini")); err == nil {
		agents = append(agents, "gemini")
	}
	if _, err := os.Stat(filepath.Join(home, ".codex")); err == nil {
		agents = append(agents, "codex")
	}
	return agents
}

// SetupAgent configures an agent to work with Forge.
func SetupAgent(agent string, home string) error {
	switch agent {
	case "claude":
		return setupClaude(home)
	case "gemini":
		return setupGemini(home)
	case "codex":
		return setupCodex(home)
	default:
		return fmt.Errorf("unknown agent: %s", agent)
	}
}

// --- Claude Code Integration ---

func setupClaude(home string) error {
	settingsPath := filepath.Join(home, ".claude", "settings.json")
	forgePath := ForgePath()

	// Read existing settings
	settings := make(map[string]any)
	if data, err := os.ReadFile(settingsPath); err == nil {
		_ = json.Unmarshal(data, &settings)
	}

	// Register MCP server
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

	// Register hooks
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = make(map[string]any)
	}

	// PostToolUse hook — capture every tool call
	hooks["PostToolUse"] = []any{
		map[string]any{
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
		},
	}

	// Stop hook — trigger distillation at end of session
	hooks["Stop"] = []any{
		map[string]any{
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
		},
	}

	// SessionEnd hook — clean up
	hooks["SessionEnd"] = []any{
		map[string]any{
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
		},
	}

	settings["hooks"] = hooks

	// Write settings
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		return fmt.Errorf("create settings dir: %w", err)
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	if err := os.WriteFile(settingsPath, data, 0o600); err != nil {
		return fmt.Errorf("write settings: %w", err)
	}

	// Install skill file
	skillDir := filepath.Join(home, ".claude", "skills")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		return fmt.Errorf("create skills dir: %w", err)
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
		return fmt.Errorf("write skill: %w", err)
	}

	return nil
}

// --- Gemini Integration ---

func setupGemini(home string) error {
	settingsPath := filepath.Join(home, ".gemini", "settings.json")
	forgePath := ForgePath()

	// Read existing settings
	settings := make(map[string]any)
	if data, err := os.ReadFile(settingsPath); err == nil {
		_ = json.Unmarshal(data, &settings)
	}

	// Register MCP server
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

	// Write settings
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		return fmt.Errorf("create settings dir: %w", err)
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	if err := os.WriteFile(settingsPath, data, 0o600); err != nil {
		return fmt.Errorf("write settings: %w", err)
	}

	// Install skill file
	skillDir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		return fmt.Errorf("create skills dir: %w", err)
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
		return fmt.Errorf("write skill: %w", err)
	}

	return nil
}

// --- Codex Integration ---

func setupCodex(home string) error {
	forgePath := ForgePath()

	// Install skill file
	skillDir := filepath.Join(home, ".codex", "skills")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		return fmt.Errorf("create skills dir: %w", err)
	}

	skillContent := map[string]any{
		"forge_skill_version": 1,
		"description":         "Forge — Silent Memory Forger for AI agents",
		"setup": map[string]any{
			"first_time": map[string]any{
				"commands": []string{"forge init", "forge start"},
				"note":     "Run 'forge init' to set up the database and hooks, then 'forge start' to launch the daemon.",
			},
			"daemon_not_running": map[string]any{
				"commands": []string{"forge start"},
				"note":     "Run after a reboot or if any tool returns 'Daemon unreachable'.",
			},
		},
		"tools": map[string]any{
			"get_recent_context": map[string]any{
				"description": "Returns distilled memories and session summaries from past work.",
				"when_to_use": "User asks about past work, decisions, or patterns.",
			},
			"search_memories": map[string]any{
				"description": "Full-text search on event payloads.",
				"when_to_use": "User asks 'did I fix this before?' or about past errors.",
			},
		},
		"hooks": map[string]any{
			"UserPromptSubmit": fmt.Sprintf(`%s hook --event UserPromptSubmit`, forgePath),
			"Stop":             fmt.Sprintf(`%s hook --event Stop`, forgePath),
		},
	}

	data, err := json.MarshalIndent(skillContent, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal skill: %w", err)
	}

	skillPath := filepath.Join(skillDir, "forge.json")
	if err := os.WriteFile(skillPath, data, 0o600); err != nil {
		return fmt.Errorf("write skill: %w", err)
	}

	return nil
}

// OS detection helpers

func IsWindows() bool {
	return runtime.GOOS == "windows"
}

func IsMacOS() bool {
	return runtime.GOOS == "darwin"
}

func IsLinux() bool {
	return runtime.GOOS == "linux"
}
