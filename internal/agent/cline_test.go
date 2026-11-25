package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestClineAdapter_DetectByDir(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".cline"), 0o700); err != nil {
		t.Fatal(err)
	}
	a := clineAdapter{}
	if !a.Detect(home) {
		t.Error("Detect should return true when ~/.cline exists")
	}
}

func TestClineAdapter_DetectMissing(t *testing.T) {
	home := t.TempDir()
	a := clineAdapter{}
	if a.Detect(home) {
		t.Error("Detect should return false when neither ~/.cline nor the VS Code storage dir exists")
	}
}

func TestClineAdapter_Registered(t *testing.T) {
	for _, adapter := range registeredAdapters() {
		if adapter.Name() == "cline" {
			return
		}
	}
	t.Fatal("expected cline adapter in registeredAdapters()")
}

func TestSetupCline_WritesMCPAndPreservesOtherServers(t *testing.T) {
	home := t.TempDir()

	settingsPath := filepath.Join(home, ".cline", "data", "settings", "cline_mcp_settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	initial := `{"mcpServers":{"other-tool":{"command":"other"}}}`
	if err := os.WriteFile(settingsPath, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := setupCline(home); err != nil {
		t.Fatalf("setupCline: %v", err)
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("unmarshal settings: %v", err)
	}

	mcpServers, _ := settings["mcpServers"].(map[string]any)
	if _, ok := mcpServers["other-tool"]; !ok {
		t.Error("user MCP server 'other-tool' was removed")
	}
	forgeServer, _ := mcpServers["forge"].(map[string]any)
	if forgeServer == nil {
		t.Fatal("expected forge MCP server in settings")
	}
	if command, _ := forgeServer["command"].(string); command != ForgePath() {
		t.Errorf("forge command = %q, want %q", command, ForgePath())
	}
	args, _ := forgeServer["args"].([]any)
	if len(args) != 1 || args[0] != "mcp" {
		t.Errorf("expected args ['mcp'], got %v", args)
	}
}

func TestSetupCline_PreservesExistingForgeExtras(t *testing.T) {
	home := t.TempDir()

	settingsPath := filepath.Join(home, ".cline", "data", "settings", "cline_mcp_settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	initial := `{"mcpServers":{"forge":{"command":"/stale/path","args":["mcp"],"autoApprove":["get_principles"]}}}`
	if err := os.WriteFile(settingsPath, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := setupCline(home); err != nil {
		t.Fatalf("setupCline: %v", err)
	}

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("unmarshal settings: %v", err)
	}
	mcpServers, _ := settings["mcpServers"].(map[string]any)
	forgeServer, _ := mcpServers["forge"].(map[string]any)
	if forgeServer == nil {
		t.Fatal("expected forge MCP server")
	}
	if command, _ := forgeServer["command"].(string); command != ForgePath() {
		t.Errorf("command should be refreshed to ForgePath(), got %q", command)
	}
	autoApprove, _ := forgeServer["autoApprove"].([]any)
	if len(autoApprove) != 1 || autoApprove[0] != "get_principles" {
		t.Errorf("user-added autoApprove list was dropped: %v", autoApprove)
	}
}

func TestSetupCline_SkipsVSCodeWhenExtensionAbsent(t *testing.T) {
	home := t.TempDir()
	if _, err := setupCline(home); err != nil {
		t.Fatalf("setupCline: %v", err)
	}
	vsPath := filepath.Join(vsCodeClineSettingsDir(home), "cline_mcp_settings.json")
	if _, err := os.Stat(vsPath); !os.IsNotExist(err) {
		t.Errorf("VS Code settings file should not be created when the extension storage is absent (stat err=%v)", err)
	}
}
