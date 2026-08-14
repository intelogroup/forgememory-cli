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
	// checkAndRepairIntegrationPaths -> syncIntegrations -> setupCodex shells
	// out to a real `codex` CLI if one is on PATH. Pin CODEX_HOME so that, if
	// present, it registers against this sandboxed home instead of silently
	// mutating the developer's real ~/.codex/config.toml (this is how a
	// t.TempDir()-shaped path can otherwise leak into a real Codex install —
	// see issue #42).
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

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

func TestCodexMCPForgeCommand_ExtractsCommand(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	content := `model = "gpt-5"

[mcp_servers.other]
command = "/usr/local/bin/other"
args = ["serve"]

[mcp_servers.forge]
command = "/var/folders/xx/yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy/T/TestCheckAndRepairIntegrationPaths_TriggersOnStale/001/bin/forge"
args = ["mcp"]
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	got, ok := codexMCPForgeCommand(path)
	if !ok {
		t.Fatal("expected command to be found")
	}
	want := "/var/folders/xx/yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy/T/TestCheckAndRepairIntegrationPaths_TriggersOnStale/001/bin/forge"
	if got != want {
		t.Errorf("codexMCPForgeCommand() = %q, want %q", got, want)
	}
}

func TestCodexMCPForgeCommand_NoForgeTable_NotFound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	content := `[mcp_servers.other]
command = "/usr/local/bin/other"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, ok := codexMCPForgeCommand(path); ok {
		t.Error("expected no command found when [mcp_servers.forge] table is absent")
	}
}

func TestCodexMCPForgeCommand_MissingFile_NotFound(t *testing.T) {
	if _, ok := codexMCPForgeCommand(filepath.Join(t.TempDir(), "config.toml")); ok {
		t.Error("expected no command found for a missing file")
	}
}

// TestCheckAndRepairIntegrationPaths_DetectsStaleCodexMCPConfig is the
// regression test for issue #42: Codex's native MCP registration
// (~/.codex/config.toml, managed by `codex mcp add`/`codex mcp get`, distinct
// from the forge.json skill file) can point at a deleted temporary Forge
// binary path. checkAndRepairIntegrationPaths must detect that and trigger a
// repair, even though config.toml is TOML rather than JSON like every other
// integration source it scans.
func TestCheckAndRepairIntegrationPaths_DetectsStaleCodexMCPConfig(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(home, ".codex")
	t.Setenv("CODEX_HOME", codexHome)

	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}

	// Exact path shape reported in issue #42: a Codex MCP entry pointing at a
	// deleted temporary/test binary.
	staleConfig := `[mcp_servers.forge]
command = "/var/folders/xx/yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy/T/TestCheckAndRepairIntegrationPaths_TriggersOnStale/001/bin/forge"
args = ["mcp"]
`
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(staleConfig), 0o600); err != nil {
		t.Fatal(err)
	}

	// Create a stable forge binary in PATH so ForgePath() resolves to it.
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

	checkAndRepairIntegrationPaths(home)

	// Repair re-runs syncIntegrations, which (re)writes the Codex skill file
	// as a side effect. Its presence confirms the stale config.toml entry
	// was detected and drove a repair, rather than being silently ignored.
	skillPath := filepath.Join(codexHome, "skills", "forge.json")
	if _, err := os.Stat(skillPath); err != nil {
		t.Errorf("expected stale config.toml to trigger repair (skill file missing): %v", err)
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
