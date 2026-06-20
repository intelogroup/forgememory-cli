package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

type antigravityAdapter struct{}

func (antigravityAdapter) Name() string { return "antigravity" }

func (antigravityAdapter) Detect(home string) bool {
	if _, err := os.Stat(filepath.Join(home, ".gemini", "antigravity")); err == nil {
		return true
	}
	if _, err := exec.LookPath("agentapi"); err == nil {
		return true
	}
	return false
}

func (antigravityAdapter) Setup(home string) (string, error) {
	return setupAntigravity(home)
}

func setupAntigravity(home string) (string, error) {
	forgePath := ForgePath()
	configDir := filepath.Join(home, ".gemini", "antigravity")

	// 1. Setup MCP config inside ~/.gemini/antigravity/mcp_config.json
	mcpConfigPath := filepath.Join(configDir, "mcp_config.json")
	mcpConfig := make(map[string]any)
	var existingMcpBytes []byte
	if data, err := os.ReadFile(mcpConfigPath); err == nil {
		existingMcpBytes = data
		_ = json.Unmarshal(data, &mcpConfig)
	}

	mcpServers, _ := mcpConfig["mcpServers"].(map[string]any)
	if mcpServers == nil {
		mcpServers = make(map[string]any)
	}
	mcpServers["forge"] = map[string]any{
		"command": forgePath,
		"args":    []string{"mcp"},
		"env":     map[string]string{},
	}
	mcpConfig["mcpServers"] = mcpServers

	mcpData, err := json.MarshalIndent(mcpConfig, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal mcp config: %w", err)
	}
	if !bytes.Equal(existingMcpBytes, mcpData) {
		if err := os.MkdirAll(configDir, 0o700); err != nil {
			return "", fmt.Errorf("create antigravity config dir: %w", err)
		}
		if err := os.WriteFile(mcpConfigPath, mcpData, 0o600); err != nil {
			return "", fmt.Errorf("write mcp config: %w", err)
		}
	}

	// 2. Setup hooks.json inside ~/.gemini/config/hooks.json
	globalConfigDir := filepath.Join(home, ".gemini", "config")
	hooksPath := filepath.Join(globalConfigDir, "hooks.json")
	hooksConfig := make(map[string]any)
	var existingHooksBytes []byte
	if data, err := os.ReadFile(hooksPath); err == nil {
		existingHooksBytes = data
		_ = json.Unmarshal(data, &hooksConfig)
	}

	// Define our forge hooks
	forgeHooks := map[string]any{
		"PreInvocation": []any{
			map[string]any{
				"matcher": ".*",
				"hooks": []any{
					map[string]any{
						"type":    "command",
						"command": fmt.Sprintf(`"%s" hook --source antigravity --event UserPromptSubmit`, forgePath),
					},
				},
			},
		},
		"PostToolUse": []any{
			map[string]any{
				"matcher": ".*",
				"hooks": []any{
					map[string]any{
						"type":    "command",
						"command": fmt.Sprintf(`"%s" hook --source antigravity --event PostToolUse`, forgePath),
					},
				},
			},
		},
		"Stop": []any{
			map[string]any{
				"matcher": ".*",
				"hooks": []any{
					map[string]any{
						"type":    "command",
						"command": fmt.Sprintf(`"%s" hook --source antigravity --event Stop`, forgePath),
					},
				},
			},
		},
	}

	hooksConfig["forge"] = forgeHooks

	hooksData, err := json.MarshalIndent(hooksConfig, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal hooks config: %w", err)
	}
	if !bytes.Equal(existingHooksBytes, hooksData) {
		if err := os.MkdirAll(globalConfigDir, 0o700); err != nil {
			return "", fmt.Errorf("create config dir: %w", err)
		}
		if err := os.WriteFile(hooksPath, hooksData, 0o600); err != nil {
			return "", fmt.Errorf("write hooks config: %w", err)
		}
	}

	return hooksPath, nil
}
