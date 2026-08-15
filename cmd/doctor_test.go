package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFindStaleCodexMCPConfig_DeletedBinary_Stale(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(home, ".codex")
	t.Setenv("CODEX_HOME", codexHome)
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}

	staleConfig := `[mcp_servers.forge]
command = "/var/folders/xx/yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy/T/TestCheckAndRepairIntegrationPaths_TriggersOnStale/001/bin/forge"
args = ["mcp"]
`
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(staleConfig), 0o600); err != nil {
		t.Fatal(err)
	}

	ref, stale := findStaleCodexMCPConfig(home)
	if !stale {
		t.Fatal("expected a config.toml referencing a deleted binary to be flagged stale")
	}
	if ref.path != filepath.Join(codexHome, "config.toml") {
		t.Errorf("ref.path = %q, want %q", ref.path, filepath.Join(codexHome, "config.toml"))
	}
}

func TestFindStaleCodexMCPConfig_MatchesCurrentBinary_NotStale(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(home, ".codex")
	t.Setenv("CODEX_HOME", codexHome)
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}

	binDir := filepath.Join(home, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	binaryName := "forge"
	if runtime.GOOS == "windows" {
		binaryName = "forge.exe"
	}
	stableForge := filepath.Join(binDir, binaryName)
	if err := os.WriteFile(stableForge, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	config := `[mcp_servers.forge]
command = "` + filepath.ToSlash(stableForge) + `"
args = ["mcp"]
`
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, stale := findStaleCodexMCPConfig(home); stale {
		t.Error("expected config.toml matching the active binary to not be flagged stale")
	}
}

func TestFindStaleCodexMCPConfig_NoConfigFile_NotStale(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	if _, stale := findStaleCodexMCPConfig(home); stale {
		t.Error("expected no config.toml to not be flagged stale")
	}
}
