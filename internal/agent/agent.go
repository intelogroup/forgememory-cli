package agent

import (
	"bytes"
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
// SetupAgent configures an agent to work with Forge and returns the path of
// the installed skill file (empty string for agents that don't write one).
func SetupAgent(agent string, home string) (string, error) {
	switch agent {
	case "claude":
		return setupClaude(home)
	case "gemini":
		return setupGemini(home)
	case "codex":
		return setupCodex(home)
	default:
		return "", fmt.Errorf("unknown agent: %s", agent)
	}
}

// --- Claude Code Integration ---

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
			// drop duplicate Forge entries
		} else {
			result = append(result, item)
		}
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

	// Read existing settings, preserving the raw bytes for the idempotency check.
	settings := make(map[string]any)
	var existingBytes []byte
	if data, err := os.ReadFile(settingsPath); err == nil {
		existingBytes = data
		_ = json.Unmarshal(data, &settings)
	}

	// Register MCP server — keyed by "forge", other servers untouched.
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

	// Register hooks — upsert so other tools' hook entries are preserved.
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

	existing, _ := hooks["PostToolUse"].([]any)
	hooks["PostToolUse"] = upsertHookArray(existing, postToolUseEntry, "PostToolUse")
	existing, _ = hooks["Stop"].([]any)
	hooks["Stop"] = upsertHookArray(existing, stopEntry, "Stop")
	existing, _ = hooks["SessionEnd"].([]any)
	hooks["SessionEnd"] = upsertHookArray(existing, sessionEndEntry, "SessionEnd")

	settings["hooks"] = hooks

	// Marshal and write only if the content has changed.
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

	// Install skill file
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

// --- Gemini Integration ---

func setupGemini(home string) (string, error) {
	settingsPath := filepath.Join(home, ".gemini", "settings.json")
	forgePath := ForgePath()

	// Read existing settings, preserving the raw bytes for the idempotency check.
	settings := make(map[string]any)
	var existingBytes []byte
	if data, err := os.ReadFile(settingsPath); err == nil {
		existingBytes = data
		_ = json.Unmarshal(data, &settings)
	}

	// Register MCP server — keyed by "forge", other servers untouched.
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

	// Marshal and write only if the content has changed.
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

	// Install skill file
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

// --- Codex Integration ---

func setupCodex(home string) (string, error) {
	forgePath := ForgePath()

	// Honour CODEX_HOME env var, fall back to ~/.codex
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}

	// Install skill file — create the skills directory first
	skillDir := filepath.Join(codexHome, "skills")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		return "", fmt.Errorf("create skills dir %s: %w", skillDir, err)
	}

	// Probe writability before doing any real work — catches Windows ACL issues
	// early with a targeted message instead of a generic "Access is denied" on
	// the final write.
	if err := probeWritable(skillDir); err != nil {
		return "", err
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
			"get_principles": map[string]any{
				"description": "Returns distilled high-level principles (architecture decisions, patterns, preferences).",
				"when_to_use": "User asks about project conventions or past decisions.",
			},
			"get_session_summaries": map[string]any{
				"description": "Returns synthesized summaries of recent work sessions.",
				"when_to_use": "User asks what they were working on before a break or yesterday.",
			},
		},
		"hooks": map[string]any{
			"UserPromptSubmit": fmt.Sprintf(`%s hook --event UserPromptSubmit`, forgePath),
			"Stop":             fmt.Sprintf(`%s hook --event Stop`, forgePath),
		},
	}

	data, err := json.MarshalIndent(skillContent, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal skill: %w", err)
	}

	skillPath := filepath.Join(skillDir, "forge.json")
	if err := os.WriteFile(skillPath, data, 0o600); err != nil {
		fallback := manualCodexFallback(skillPath, data)
		if os.IsPermission(err) {
			return "", fmt.Errorf("write skill: permission denied writing %s\n  Manual fallback:\n%s", skillPath, fallback)
		}
		return "", fmt.Errorf("write skill: %w\n  Manual fallback:\n%s", err, fallback)
	}

	// Post-write verification: confirm the file landed at the expected path.
	if _, err := os.Stat(skillPath); err != nil {
		return "", fmt.Errorf("skill written but not found at %s: %w", skillPath, err)
	}

	return skillPath, nil
}

// probeWritable creates and removes a temp file inside dir to verify the
// current process can create files there. Returns a targeted error on failure
// so callers get "permission denied on <dir>" rather than a generic write error
// on the actual payload file after doing all the marshaling work.
func probeWritable(dir string) error {
	probe := filepath.Join(dir, ".forge-probe")
	err := os.WriteFile(probe, []byte{}, 0o600)
	os.Remove(probe) // best-effort cleanup regardless of outcome
	if err == nil {
		return nil
	}
	if os.IsPermission(err) {
		return fmt.Errorf("skills dir not writable (permission denied): %s\n"+
			"  Grant your user write access to that directory, or set CODEX_HOME to a directory you own.", dir)
	}
	return fmt.Errorf("skills dir not writable: %w", err)
}

// manualCodexFallback returns shell commands to install the skill file manually.
func manualCodexFallback(skillPath string, data []byte) string {
	dir := filepath.Dir(skillPath)
	if IsWindows() {
		return fmt.Sprintf(
			"    New-Item -ItemType Directory -Force \"%s\"\n"+
				"    Set-Content -Path \"%s\" -Value '%s'",
			dir, skillPath, string(data))
	}
	return fmt.Sprintf(
		"    mkdir -p \"%s\"\n"+
			"    cat > \"%s\" << 'EOF'\n%s\nEOF",
		dir, skillPath, string(data))
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
