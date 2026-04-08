package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

func TestSave_RoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := Config{
		Provider:        "openai",
		APIKey:          "sk-test",
		Model:           "gpt-4o",
		BaseURL:         "https://api.example.com",
		Context7APIKey:  "ctx7-key",
		ExaAPIKey:       "exa-key",
		TavilyAPIKey:    "tavily-key",
		Timeout:         "30s",
		Retries:         3,
		DistillInterval: "10m",
	}
	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load after Save: %v", err)
	}
	if got.Provider != cfg.Provider {
		t.Fatalf("Provider = %q, want %q", got.Provider, cfg.Provider)
	}
	if got.APIKey != cfg.APIKey {
		t.Fatalf("APIKey = %q, want %q", got.APIKey, cfg.APIKey)
	}
	if got.Model != cfg.Model {
		t.Fatalf("Model = %q, want %q", got.Model, cfg.Model)
	}
	if got.Retries != cfg.Retries {
		t.Fatalf("Retries = %d, want %d", got.Retries, cfg.Retries)
	}
	if got.DistillInterval != cfg.DistillInterval {
		t.Fatalf("DistillInterval = %q, want %q", got.DistillInterval, cfg.DistillInterval)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(Path())
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("file mode = %o, want 600", info.Mode().Perm())
		}
	}
}

func TestSave_EmptyFieldsOmitted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := Save(Config{Provider: "anthropic"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, err := os.ReadFile(Path())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "FORGE_PROVIDER=anthropic") {
		t.Fatalf("missing provider line: %q", content)
	}
	if strings.Contains(content, "FORGE_API_KEY") {
		t.Fatalf("empty APIKey should not appear in file: %q", content)
	}
}

func TestSetEnvFromFile_SetsEnvVars(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("FORGE_PROVIDER", "")
	t.Setenv("FORGE_API_KEY", "")
	t.Setenv("FORGE_MODEL", "")

	if err := Save(Config{Provider: "openai", APIKey: "sk-env-test", Model: "gpt-4o-mini"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := SetEnvFromFile(); err != nil {
		t.Fatalf("SetEnvFromFile: %v", err)
	}
	if got := os.Getenv("FORGE_PROVIDER"); got != "openai" {
		t.Fatalf("FORGE_PROVIDER = %q, want openai", got)
	}
	if got := os.Getenv("FORGE_API_KEY"); got != "sk-env-test" {
		t.Fatalf("FORGE_API_KEY = %q, want sk-env-test", got)
	}
	if got := os.Getenv("FORGE_MODEL"); got != "gpt-4o-mini" {
		t.Fatalf("FORGE_MODEL = %q, want gpt-4o-mini", got)
	}
}
