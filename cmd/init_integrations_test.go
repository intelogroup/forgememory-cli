package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// settingsFromHooks builds the nested settings.json structure expected by
// findStaleForgePathInHooks, given a single PostToolUse command string.
func settingsFromHooks(command string) map[string]any {
	return map[string]any{
		"hooks": map[string]any{
			"PostToolUse": []any{
				map[string]any{
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": command,
						},
					},
				},
			},
		},
	}
}

// writeFakeForge writes bytes to path and returns the path. Tests use this to
// stand in for a forge binary on disk.
func writeFakeForge(t *testing.T, path string, contents []byte) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFindStaleForgePathInHooks_ExactMatch_NotStale(t *testing.T) {
	dir := t.TempDir()
	forge := writeFakeForge(t, filepath.Join(dir, "forge"), []byte("forge-v1"))
	settings := settingsFromHooks(`"` + forge + `" hook`)

	if got := findStaleForgePathInHooks(settings, forge); got != "" {
		t.Errorf("expected no stale path, got %q", got)
	}
}

func TestFindStaleForgePathInHooks_DifferentPathSameHash_NotStale(t *testing.T) {
	// Simulates the npm-wrapper case: two different paths point to byte-identical
	// forge binaries. Pre-fix, this triggered a re-integration on every boot.
	dir := t.TempDir()
	contents := []byte("forge-v1-bytes")
	registered := writeFakeForge(t, filepath.Join(dir, "old", "forge"), contents)
	current := writeFakeForge(t, filepath.Join(dir, "new", "forge"), contents)
	settings := settingsFromHooks(`"` + registered + `" hook`)

	if got := findStaleForgePathInHooks(settings, current); got != "" {
		t.Errorf("identical-content paths should not be flagged stale; got %q", got)
	}
}

func TestFindStaleForgePathInHooks_DifferentContent_Stale(t *testing.T) {
	dir := t.TempDir()
	registered := writeFakeForge(t, filepath.Join(dir, "old", "forge"), []byte("v0.4.36"))
	current := writeFakeForge(t, filepath.Join(dir, "new", "forge"), []byte("v0.4.37-different"))
	settings := settingsFromHooks(`"` + registered + `" hook`)

	if got := findStaleForgePathInHooks(settings, current); got != registered {
		t.Errorf("expected stale path %q, got %q", registered, got)
	}
}

func TestFindStaleForgePathInHooks_RegisteredPathMissing_Stale(t *testing.T) {
	dir := t.TempDir()
	current := writeFakeForge(t, filepath.Join(dir, "forge"), []byte("forge"))
	settings := settingsFromHooks(`"/no/such/path/forge" hook`)

	if got := findStaleForgePathInHooks(settings, current); got != "/no/such/path/forge" {
		t.Errorf("expected missing path to be flagged stale, got %q", got)
	}
}

func TestFindStaleForgePathInHooks_Symlink_NotStale(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require admin on Windows")
	}
	dir := t.TempDir()
	real := writeFakeForge(t, filepath.Join(dir, "real", "forge"), []byte("real-forge"))
	linkPath := filepath.Join(dir, "link", "forge")
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, linkPath); err != nil {
		t.Fatal(err)
	}
	settings := settingsFromHooks(`"` + linkPath + `" hook`)

	if got := findStaleForgePathInHooks(settings, real); got != "" {
		t.Errorf("symlink to current forge should not be stale; got %q", got)
	}
}

func TestFindStaleForgePathInHooks_NonForgePath_Ignored(t *testing.T) {
	dir := t.TempDir()
	current := writeFakeForge(t, filepath.Join(dir, "forge"), []byte("forge"))
	// Hook command for a totally unrelated tool — must not be flagged.
	settings := settingsFromHooks(`"/usr/bin/jq" .`)

	if got := findStaleForgePathInHooks(settings, current); got != "" {
		t.Errorf("non-forge command should be ignored; got %q", got)
	}
}

func TestCheckAndRepairIntegrationPaths_TriggersOnStale(t *testing.T) {
	home := t.TempDir()

	// Write a stale settings file for Gemini to trigger repair
	geminiDir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(geminiDir, 0o700); err != nil {
		t.Fatal(err)
	}
	staleSettings := `{"hooks":{"BeforeAgent":[{"hooks":[{"type":"command","command":"\"/old/path/forge\" hook --source gemini --event UserPromptSubmit"}]}]}}`
	if err := os.WriteFile(filepath.Join(geminiDir, "settings.json"), []byte(staleSettings), 0o600); err != nil {
		t.Fatal(err)
	}

	// Create stable forge binary in PATH so ForgePath() resolves to it
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

	// Verify repair runs and updates settings to use the current path
	checkAndRepairIntegrationPaths(home)

	data, err := os.ReadFile(filepath.Join(geminiDir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if strings.Contains(content, "/old/path/forge") {
		t.Errorf("repair did not replace stale path: %s", content)
	}
}

// Sanity-check the JSON structure we feed into the function matches what
// claude settings.json produces. This guards against silent contract drift.
func TestSettingsFromHooks_MatchesProductionShape(t *testing.T) {
	settings := settingsFromHooks(`"/forge" hook`)
	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"hooks":{"PostToolUse":[{"hooks":[{"command":"\"/forge\" hook","type":"command"}]}]}}`
	if string(data) != want {
		t.Errorf("settings shape drift:\n got: %s\nwant: %s", data, want)
	}
}
