package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

type clineAdapter struct{}

func (clineAdapter) Name() string { return "cline" }

// Detect reports whether Cline (CLI/desktop or VS Code extension) is present.
func (clineAdapter) Detect(home string) bool {
	if _, err := os.Stat(filepath.Join(home, ".cline")); err == nil {
		return true
	}
	_, err := os.Stat(vsCodeClineSettingsDir(home))
	return err == nil
}

func (clineAdapter) Setup(home string) (string, error) {
	return setupCline(home)
}

// vsCodeClineSettingsDir returns the Cline VS Code extension settings directory.
func vsCodeClineSettingsDir(home string) string {
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(home, "AppData", "Roaming", "Code", "User", "globalStorage", "saoudrizwan.claude-dev", "settings")
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Code", "User", "globalStorage", "saoudrizwan.claude-dev", "settings")
	default:
		return filepath.Join(home, ".config", "Code", "User", "globalStorage", "saoudrizwan.claude-dev", "settings")
	}
}

// setupCline registers the forge MCP server in Cline's MCP settings files.
// Cline has no hook system, so MCP registration is read/inject-only capture-wise;
// use `forge cline-import` to backfill task history into memory.
func setupCline(home string) (string, error) {
	forgePath := ForgePath()

	// 1. Cline CLI / desktop app: ~/.cline/data/settings/cline_mcp_settings.json
	cliSettingsPath := filepath.Join(home, ".cline", "data", "settings", "cline_mcp_settings.json")
	if err := writeClineMCPSettings(cliSettingsPath, forgePath); err != nil {
		return "", fmt.Errorf("write Cline CLI MCP settings: %w", err)
	}

	// 2. VS Code extension — only touch it when the extension storage exists.
	vsSettingsPath := filepath.Join(vsCodeClineSettingsDir(home), "cline_mcp_settings.json")
	if _, err := os.Stat(filepath.Dir(vsSettingsPath)); err == nil {
		if err := writeClineMCPSettings(vsSettingsPath, forgePath); err != nil {
			return "", fmt.Errorf("write Cline VS Code MCP settings: %w", err)
		}
	}

	return cliSettingsPath, nil
}

// writeClineMCPSettings merges the forge MCP server into a Cline settings file,
// preserving unrelated servers and any extra keys (e.g. autoApprove) the user
// already added to the forge entry.
func writeClineMCPSettings(path, forgePath string) error {
	settings := make(map[string]any)
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &settings)
	}

	mcpServers, _ := settings["mcpServers"].(map[string]any)
	if mcpServers == nil {
		mcpServers = make(map[string]any)
	}

	server := map[string]any{
		"command": forgePath,
		"args":    []string{"mcp"},
		"env":     map[string]string{},
	}
	// Preserve custom keys the user added (autoApprove lists, disabled flags…).
	if existing, ok := mcpServers["forge"].(map[string]any); ok {
		for key, value := range existing {
			if _, ours := server[key]; !ours {
				server[key] = value
			}
		}
	}
	mcpServers["forge"] = server
	settings["mcpServers"] = mcpServers

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal Cline MCP settings: %w", err)
	}
	if err := WriteFileConfirm(path, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
