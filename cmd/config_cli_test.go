package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forge/forge/internal/config"
)

func TestRunConfig_DefaultModelAnthropicHaiku(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	runConfig([]string{"--provider", "anthropic", "--api-key", "sk-ant-test", "--no-validate"})

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if cfg.Model != "claude-haiku-4-5-20251001" {
		t.Fatalf("model = %q, want haiku default", cfg.Model)
	}
}

func TestRunConfig_ShowJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfgPath := filepath.Join(home, ".forge", "config")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(cfgPath, []byte("FORGE_PROVIDER=openai\nFORGE_API_KEY=sk-test\nFORGE_MODEL=gpt-4o\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	out := captureStdout(func() { runConfig([]string{"--show", "--json"}) })
	var got map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); err != nil {
		t.Fatalf("show --json should return JSON: %v, out=%q", err, out)
	}
	if got["provider"] != "openai" {
		t.Fatalf("provider = %v, want openai", got["provider"])
	}
}

func TestRunConfig_Antigravity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	runConfig([]string{"--provider", "antigravity", "--no-validate"})

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if cfg.Provider != "antigravity" {
		t.Fatalf("provider = %q, want antigravity", cfg.Provider)
	}
	if cfg.Model != "flash" {
		t.Fatalf("model = %q, want flash default", cfg.Model)
	}
}

func TestRunConfig_DistillBatchSize(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	runConfig([]string{"--provider", "anthropic", "--api-key", "sk-ant-test", "--distill-batch-size", "500", "--no-validate"})

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if cfg.DistillBatchSize != 500 {
		t.Fatalf("DistillBatchSize = %d, want 500", cfg.DistillBatchSize)
	}
}

func TestRunConfig_DistillBatchSizeBelowMinimum(t *testing.T) {
	if os.Getenv("FORGE_TEST_SUBPROCESS") == "1" {
		home := t.TempDir()
		t.Setenv("HOME", home)
		runConfig([]string{"--provider", "anthropic", "--api-key", "sk-ant-test", "--distill-batch-size", "150", "--no-validate"})
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run", "TestRunConfig_DistillBatchSizeBelowMinimum")
	cmd.Env = append(os.Environ(), "FORGE_TEST_SUBPROCESS=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected non-zero exit for --distill-batch-size below 300, got success: %s", out)
	}
	if !strings.Contains(string(out), "must be 0 (default) or >= 300") {
		t.Fatalf("unexpected error output: %s", out)
	}
}
