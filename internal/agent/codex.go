package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type codexAdapter struct{}

func (codexAdapter) Name() string { return "codex" }

func (codexAdapter) Detect(string) bool {
	return hasCodex()
}

func (codexAdapter) Setup(home string) (string, error) {
	return setupCodex(home)
}

func setupCodex(home string) (string, error) {
	forgePath := ForgePath()

	mcpRegistered, mcpErr := registerCodexMCP(forgePath)
	if mcpRegistered {
		fmt.Println("    MCP registration: installed via 'codex mcp add'")
	} else if mcpErr != "" {
		fmt.Printf("    MCP registration: %s\n", mcpErr)
	}

	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}

	skillDir := filepath.Join(codexHome, "skills")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		return "", fmt.Errorf("create skills dir %s: %w", skillDir, err)
	}
	if err := probeWritable(skillDir); err != nil {
		return "", err
	}

	skillContent := map[string]any{
		"forge_skill_version": 2,
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
			"verification": map[string]any{
				"commands": []string{"codex mcp get forge", "forge status"},
				"note":     "Do not guess whether Forge is installed or running. Verify MCP registration and daemon health with these exact commands before suggesting install or repair steps.",
			},
			"mcp_repair": map[string]any{
				"commands": []string{fmt.Sprintf("codex mcp add forge -- %s mcp", forgePath), "codex mcp get forge"},
				"note":     "If Codex MCP is missing, install it with the exact command above and verify it immediately with 'codex mcp get forge'.",
			},
		},
		"instructions": []string{
			"Do not guess Forge installation state. Verify it with 'codex mcp get forge' and 'forge status'.",
			"Use exact installation commands when repair is needed; do not paraphrase or infer them from memory.",
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
			"inject_principles": map[string]any{
				"description": "Injects relevant past lessons into a given prompt. Pass the prompt and get back the enhanced version.",
				"when_to_use": "Before sending a prompt to a code agent — prepends past lessons automatically.",
			},
			"get_session_summaries": map[string]any{
				"description": "Returns synthesized summaries of recent work sessions.",
				"when_to_use": "User asks what they were working on before a break or yesterday.",
			},
		},
		"hooks": map[string]any{
			"PostToolUse":      fmt.Sprintf(`%s hook --event PostToolUse`, forgePath),
			"UserPromptSubmit": fmt.Sprintf(`%s hook --event UserPromptSubmit`, forgePath),
			"Stop":             fmt.Sprintf(`%s hook --event Stop`, forgePath),
			// "SessionStart":     fmt.Sprintf(`FORGE_SOURCE_TOOL=codex %s inject-check`, forgePath),
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
	if _, err := os.Stat(skillPath); err != nil {
		return "", fmt.Errorf("skill written but not found at %s: %w", skillPath, err)
	}

	return skillPath, nil
}

// registerCodexMCP attempts to register Forge as an MCP server via Codex's native CLI.
// Returns (true, "") on success, (false, errorMessage) on failure.
func registerCodexMCP(forgePath string) (bool, string) {
	if !hasCodex() {
		return false, "Codex not found in PATH"
	}

	cmd := exec.Command("codex", "mcp", "add", "forge", "--", forgePath, "mcp")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Sprintf("failed: %s (use 'codex mcp add forge -- %s mcp' manually)", strings.TrimSpace(string(output)), forgePath)
	}

	cmd = exec.Command("codex", "mcp", "get", "forge")
	if err := cmd.Run(); err != nil {
		return true, "registered but verification failed (run 'codex mcp list' to confirm)"
	}

	return true, ""
}

// probeWritable creates and removes a temp file inside dir to verify the
// current process can create files there.
func probeWritable(dir string) error {
	probe := filepath.Join(dir, ".forge-probe")
	err := os.WriteFile(probe, []byte{}, 0o600)
	os.Remove(probe)
	if err == nil {
		return nil
	}
	if os.IsPermission(err) {
		return fmt.Errorf("skills dir not writable (permission denied): %s\n  Grant your user write access to that directory, or set CODEX_HOME to a directory you own.", dir)
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
