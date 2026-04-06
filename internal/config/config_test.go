package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPath_UsesHOMEWhenSet(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got := Path()
	want := filepath.Join(home, ".forge", "config")
	if got != want {
		t.Fatalf("Path() = %q, want %q", got, want)
	}
}

func TestLoad_UsesHOMEWhenSet(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	configPath := filepath.Join(home, ".forge", "config")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(configPath, []byte("FORGE_PROVIDER=openai\nFORGE_API_KEY=test-key\nFORGE_TIMEOUT=45s\nFORGE_RETRIES=4\nFORGE_DISTILL_INTERVAL=5m\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Provider != "openai" {
		t.Fatalf("Provider = %q, want openai", cfg.Provider)
	}
	if cfg.APIKey != "test-key" {
		t.Fatalf("APIKey = %q, want test-key", cfg.APIKey)
	}
	if cfg.Timeout != "45s" {
		t.Fatalf("Timeout = %q, want 45s", cfg.Timeout)
	}
	if cfg.Retries != 4 {
		t.Fatalf("Retries = %d, want 4", cfg.Retries)
	}
	if cfg.DistillInterval != "5m" {
		t.Fatalf("DistillInterval = %q, want 5m", cfg.DistillInterval)
	}
}
