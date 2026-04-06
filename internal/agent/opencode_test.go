package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpencodeAdapter_DetectByDir(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "") // isolate from CI runner env
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".config", "opencode"), 0o700); err != nil {
		t.Fatal(err)
	}
	a := opencodeAdapter{}
	if !a.Detect(home) {
		t.Error("Detect should return true when ~/.config/opencode exists")
	}
}

func TestOpencodeAdapter_DetectByXDG(t *testing.T) {
	home := t.TempDir()
	xdgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdgHome)
	if err := os.MkdirAll(filepath.Join(xdgHome, "opencode"), 0o700); err != nil {
		t.Fatal(err)
	}
	a := opencodeAdapter{}
	if !a.Detect(home) {
		t.Error("Detect should return true when $XDG_CONFIG_HOME/opencode exists")
	}
}

func TestOpencodeAdapter_DetectMissing(t *testing.T) {
	home := t.TempDir()
	// Ensure XDG_CONFIG_HOME doesn't accidentally point somewhere with opencode.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	// Narrow PATH so there's no opencode binary.
	t.Setenv("PATH", t.TempDir())

	a := opencodeAdapter{}
	if a.Detect(home) {
		t.Error("Detect should return false when opencode dir and binary are absent")
	}
}

func TestSetupOpencode_WritesMCPConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "") // isolate from CI runner env
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".config", "opencode"), 0o700); err != nil {
		t.Fatal(err)
	}

	if _, err := setupOpencode(home); err != nil {
		t.Fatalf("setupOpencode: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(home, ".config", "opencode", "opencode.json"))
	if err != nil {
		t.Fatalf("read opencode.json: %v", err)
	}

	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	mcp, _ := config["mcp"].(map[string]any)
	if mcp == nil {
		t.Fatal("expected 'mcp' key in opencode.json")
	}
	forge, _ := mcp["forge"].(map[string]any)
	if forge == nil {
		t.Fatal("expected mcp.forge entry")
	}
	if forge["type"] != "local" {
		t.Errorf("mcp.forge.type = %q, want %q", forge["type"], "local")
	}

	cmd, _ := forge["command"].([]any)
	if len(cmd) < 2 {
		t.Fatalf("mcp.forge.command should have at least 2 elements, got %v", cmd)
	}
	if cmd[1] != "mcp" {
		t.Errorf("mcp.forge.command[1] = %q, want %q", cmd[1], "mcp")
	}
}

func TestSetupOpencode_WritesPlugin(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "") // isolate from CI runner env
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".config", "opencode"), 0o700); err != nil {
		t.Fatal(err)
	}

	pluginPath, err := setupOpencode(home)
	if err != nil {
		t.Fatalf("setupOpencode: %v", err)
	}

	if _, err := os.Stat(pluginPath); err != nil {
		t.Fatalf("plugin file missing at %s: %v", pluginPath, err)
	}

	content, err := os.ReadFile(pluginPath)
	if err != nil {
		t.Fatalf("read plugin: %v", err)
	}
	plugin := string(content)

	for _, want := range []string{
		"tool.execute.after",
		"session.updated",
		"session.deleted",
		"session.created",
		"PostToolUse",
		"Stop",
		"SessionEnd",
		"UserPromptSubmit",
		ForgePath(),
	} {
		if !strings.Contains(plugin, want) {
			t.Errorf("plugin missing %q", want)
		}
	}

	// Must only fire Stop when status === "idle".
	if !strings.Contains(plugin, `"idle"`) {
		t.Error("plugin should gate Stop on status === \"idle\"")
	}
}

func TestSetupOpencode_IdempotentConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "") // isolate from CI runner env
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".config", "opencode"), 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(home, ".config", "opencode", "opencode.json")

	if _, err := setupOpencode(home); err != nil {
		t.Fatalf("first setup: %v", err)
	}
	info1, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("stat after first setup: %v", err)
	}

	if _, err := setupOpencode(home); err != nil {
		t.Fatalf("second setup: %v", err)
	}
	info2, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("stat after second setup: %v", err)
	}

	if !info2.ModTime().Equal(info1.ModTime()) {
		t.Error("opencode.json was rewritten on second init — should skip when unchanged")
	}
}

func TestSetupOpencode_PreservesExistingConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "") // isolate from CI runner env
	home := t.TempDir()
	configDir := filepath.Join(home, ".config", "opencode")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}

	initial := `{"theme":"dark","mcp":{"other-tool":{"type":"local","command":["other"]}}}`
	if err := os.WriteFile(filepath.Join(configDir, "opencode.json"), []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := setupOpencode(home); err != nil {
		t.Fatalf("setupOpencode: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(configDir, "opencode.json"))
	content := string(data)

	if !strings.Contains(content, `"theme"`) {
		t.Error("user key 'theme' was removed")
	}
	if !strings.Contains(content, `"other-tool"`) {
		t.Error("user MCP server 'other-tool' was removed")
	}
	if !strings.Contains(content, `"forge"`) {
		t.Error("forge MCP server missing")
	}
}

func TestSetupOpencode_CreatesConfigDirIfMissing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "") // isolate from CI runner env
	home := t.TempDir()
	// No .config/opencode dir — setup must create it.

	pluginPath, err := setupOpencode(home)
	if err != nil {
		t.Fatalf("setupOpencode with missing dir: %v", err)
	}
	if _, err := os.Stat(pluginPath); err != nil {
		t.Errorf("plugin not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "opencode", "opencode.json")); err != nil {
		t.Errorf("config not created: %v", err)
	}
}

func TestSetupOpencode_RespectsXDGConfigHome(t *testing.T) {
	home := t.TempDir()
	xdgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdgHome)

	pluginPath, err := setupOpencode(home)
	if err != nil {
		t.Fatalf("setupOpencode with XDG_CONFIG_HOME: %v", err)
	}

	expectedPluginPath := filepath.Join(xdgHome, "opencode", "plugins", "forge.js")
	if pluginPath != expectedPluginPath {
		t.Errorf("pluginPath = %q, want %q", pluginPath, expectedPluginPath)
	}
	if _, err := os.Stat(pluginPath); err != nil {
		t.Errorf("plugin file missing at XDG path: %v", err)
	}
	// Must NOT write to the default location.
	defaultConfig := filepath.Join(home, ".config", "opencode", "opencode.json")
	if _, err := os.Stat(defaultConfig); err == nil {
		t.Errorf("should not write to default path when XDG_CONFIG_HOME is set")
	}
}
