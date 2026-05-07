package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
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
