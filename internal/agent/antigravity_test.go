package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAntigravityAdapter_DetectByDir(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".gemini", "antigravity"), 0o700); err != nil {
		t.Fatal(err)
	}
	a := antigravityAdapter{}
	if !a.Detect(home) {
		t.Error("Detect should return true when ~/.gemini/antigravity exists")
	}
}

func TestAntigravityAdapter_DetectMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PATH", t.TempDir()) // isolate path

	a := antigravityAdapter{}
	if a.Detect(home) {
		t.Error("Detect should return false when dir and binary are absent")
	}
}

func TestSetupAntigravity_WritesMcpAndHooks(t *testing.T) {
	home := t.TempDir()

	// Perform setup
	if _, err := setupAntigravity(home); err != nil {
		t.Fatalf("setupAntigravity: %v", err)
	}

	// 1. Verify mcp_config.json
	mcpPath := filepath.Join(home, ".gemini", "antigravity", "mcp_config.json")
	mcpData, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatalf("read mcp_config.json: %v", err)
	}

	var mcpConfig map[string]any
	if err := json.Unmarshal(mcpData, &mcpConfig); err != nil {
		t.Fatalf("unmarshal mcp_config.json: %v", err)
	}

	mcpServers, _ := mcpConfig["mcpServers"].(map[string]any)
	if mcpServers == nil {
		t.Fatal("expected 'mcpServers' in mcp_config.json")
	}
	forgeServer, _ := mcpServers["forge"].(map[string]any)
	if forgeServer == nil {
		t.Fatal("expected 'forge' entry in mcpServers")
	}
	args, _ := forgeServer["args"].([]any)
	if len(args) != 1 || args[0] != "mcp" {
		t.Errorf("expected args ['mcp'], got %v", args)
	}

	// 2. Verify hooks.json
	hooksPath := filepath.Join(home, ".gemini", "config", "hooks.json")
	hooksData, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("read hooks.json: %v", err)
	}

	var hooksConfig map[string]any
	if err := json.Unmarshal(hooksData, &hooksConfig); err != nil {
		t.Fatalf("unmarshal hooks.json: %v", err)
	}

	forgeHooks, _ := hooksConfig["forge"].(map[string]any)
	if forgeHooks == nil {
		t.Fatal("expected 'forge' entry in hooks.json")
	}

	events := []string{"PreInvocation", "PostToolUse", "Stop"}
	for _, event := range events {
		hList, _ := forgeHooks[event].([]any)
		if len(hList) != 1 {
			t.Fatalf("expected 1 handler for event %q, got %d", event, len(hList))
		}
		item, _ := hList[0].(map[string]any)
		if item == nil {
			t.Fatalf("expected map for event handler %q", event)
		}
		hooksArr, _ := item["hooks"].([]any)
		if len(hooksArr) != 1 {
			t.Fatalf("expected 1 hook for %q, got %d", event, len(hooksArr))
		}
		hook, _ := hooksArr[0].(map[string]any)
		if hook == nil {
			t.Fatalf("expected hook map for %q", event)
		}
		if hook["type"] != "command" {
			t.Errorf("hook type = %v, want 'command'", hook["type"])
		}
		cmd, _ := hook["command"].(string)
		if !strings.Contains(cmd, "hook --source antigravity") {
			t.Errorf("hook command = %q, expected --source antigravity", cmd)
		}
	}
}
