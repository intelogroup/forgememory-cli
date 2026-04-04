package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestStableExecutablePath_RejectsEphemeralBuildArtifacts(t *testing.T) {
	cases := []string{
		"/var/folders/tmp/go-build1234/b001/exe/forge",
		"/var/folders/tmp/forge-bin-12345/forge",
		"/tmp/go-build999/agent.test",
	}

	for _, candidate := range cases {
		if stableExecutablePath(candidate) {
			t.Fatalf("stableExecutablePath(%q) = true, want false", candidate)
		}
	}
}

func TestStableExecutablePath_AcceptsInstalledBinary(t *testing.T) {
	cases := []string{
		"/usr/local/bin/forge",
		filepath.Join(os.TempDir(), "forge"),
		filepath.Join(string(filepath.Separator), "Users", "me", "bin", "forge"),
	}

	for _, candidate := range cases {
		if !stableExecutablePath(candidate) {
			t.Fatalf("stableExecutablePath(%q) = false, want true", candidate)
		}
	}
}

// ---- upsertHookArray ----

func TestUpsertHookArray_AppendsWhenEmpty(t *testing.T) {
	entry := map[string]any{
		"hooks": []any{
			map[string]any{"env": map[string]any{"FORGE_EVENT_TYPE": "Stop"}},
		},
	}
	result := upsertHookArray(nil, entry, "Stop")
	if len(result) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(result))
	}
}

func TestUpsertHookArray_ReplacesExistingForgeEntry(t *testing.T) {
	old := map[string]any{
		"hooks": []any{
			map[string]any{"env": map[string]any{"FORGE_EVENT_TYPE": "Stop"}, "command": "old"},
		},
	}
	newEntry := map[string]any{
		"hooks": []any{
			map[string]any{"env": map[string]any{"FORGE_EVENT_TYPE": "Stop"}, "command": "new"},
		},
	}
	result := upsertHookArray([]any{old}, newEntry, "Stop")
	if len(result) != 1 {
		t.Fatalf("expected 1 entry after upsert, got %d (no duplicates)", len(result))
	}
}

func TestUpsertHookArray_PreservesOtherEntries(t *testing.T) {
	userEntry := map[string]any{"hooks": []any{map[string]any{"command": "other-tool"}}}
	forgeEntry := map[string]any{
		"hooks": []any{
			map[string]any{"env": map[string]any{"FORGE_EVENT_TYPE": "Stop"}},
		},
	}
	result := upsertHookArray([]any{userEntry}, forgeEntry, "Stop")
	if len(result) != 2 {
		t.Fatalf("expected 2 entries (user + forge), got %d", len(result))
	}
}

func TestUpsertHookArray_CollapseDuplicates(t *testing.T) {
	forge1 := map[string]any{
		"hooks": []any{map[string]any{"env": map[string]any{"FORGE_EVENT_TYPE": "Stop"}, "command": "v1"}},
	}
	forge2 := map[string]any{
		"hooks": []any{map[string]any{"env": map[string]any{"FORGE_EVENT_TYPE": "Stop"}, "command": "v2"}},
	}
	newEntry := map[string]any{
		"hooks": []any{map[string]any{"env": map[string]any{"FORGE_EVENT_TYPE": "Stop"}, "command": "v3"}},
	}
	// Start with two Forge entries (shouldn't happen, but must be handled cleanly).
	result := upsertHookArray([]any{forge1, forge2}, newEntry, "Stop")
	if len(result) != 1 {
		t.Fatalf("expected duplicates collapsed to 1, got %d", len(result))
	}
}

// ---- setupClaude idempotency ----

func TestSetupClaude_IdempotentInit(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(home, ".claude", "settings.json")

	if _, err := setupClaude(home); err != nil {
		t.Fatalf("first setupClaude: %v", err)
	}
	info1, err := os.Stat(settingsPath)
	if err != nil {
		t.Fatalf("stat after first init: %v", err)
	}
	mtime1 := info1.ModTime()

	if _, err := setupClaude(home); err != nil {
		t.Fatalf("second setupClaude: %v", err)
	}
	info2, _ := os.Stat(settingsPath)
	if !info2.ModTime().Equal(mtime1) {
		t.Error("settings.json was rewritten on second init — should be skipped when unchanged")
	}
}

func TestSetupClaude_PreservesUserSettings(t *testing.T) {
	home := t.TempDir()
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// Write settings with a user-owned key that Forge must not remove.
	initial := `{"theme":"dark","mcpServers":{"other-tool":{"command":"other"}}}`
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := setupClaude(home); err != nil {
		t.Fatalf("setupClaude: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	content := string(data)
	if !strings.Contains(content, `"theme"`) {
		t.Error("user key 'theme' was removed from settings.json")
	}
	if !strings.Contains(content, `"other-tool"`) {
		t.Error("user MCP server 'other-tool' was removed from settings.json")
	}
	if !strings.Contains(content, `"forge"`) {
		t.Error("forge MCP server missing from settings.json")
	}
}

func TestSetupClaude_NoDuplicateHooks(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}

	// Run init twice to simulate the user running `forge init` again.
	if _, err := setupClaude(home); err != nil {
		t.Fatalf("first setupClaude: %v", err)
	}
	if _, err := setupClaude(home); err != nil {
		t.Fatalf("second setupClaude: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("unmarshal settings: %v", err)
	}

	hooks, _ := settings["hooks"].(map[string]any)
	for _, event := range []string{"PostToolUse", "Stop", "SessionEnd"} {
		arr, _ := hooks[event].([]any)
		forgeCount := 0
		for _, item := range arr {
			if isForgeHookItem(item, event) {
				forgeCount++
			}
		}
		if forgeCount != 1 {
			t.Errorf("hooks[%s]: expected exactly 1 Forge entry, found %d", event, forgeCount)
		}
	}
}

// ---- probeWritable ----

func TestProbeWritable_WritableDir(t *testing.T) {
	dir := t.TempDir()
	if err := probeWritable(dir); err != nil {
		t.Errorf("expected no error for writable dir, got: %v", err)
	}
	// Probe file must not be left behind.
	if _, err := os.Stat(filepath.Join(dir, ".forge-probe")); err == nil {
		t.Error("probe file should have been removed after successful probe")
	}
}

func TestProbeWritable_ReadOnlyDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod read-only dirs behave differently on Windows — covered by integration")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o444); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	defer os.Chmod(dir, 0o700) // restore so TempDir cleanup can remove it

	err := probeWritable(dir)
	if err == nil {
		t.Fatal("expected error for read-only dir")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("expected 'permission denied' in error, got: %v", err)
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("expected dir path in error, got: %v", err)
	}
}

// ---- setupCodex ----

func TestSetupCodex_CreatesSkillFile(t *testing.T) {
	home := t.TempDir()
	// Pre-create .codex so DetectAgents would find it, and setupCodex has a home.
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
		t.Fatalf("mkdir .codex: %v", err)
	}

	installedPath, err := setupCodex(home)
	if err != nil {
		t.Fatalf("setupCodex: %v", err)
	}

	expectedPath := filepath.Join(home, ".codex", "skills", "forge.json")

	// Returned path must match the actual on-disk location.
	if installedPath != expectedPath {
		t.Errorf("returned path = %q, want %q", installedPath, expectedPath)
	}

	// Post-write verification: file must exist at the reported path.
	if _, err := os.Stat(installedPath); err != nil {
		t.Errorf("skill file not found at reported path %s: %v", installedPath, err)
	}
}

func TestSetupCodex_HonorsCODEX_HOME(t *testing.T) {
	home := t.TempDir()
	customCodexHome := t.TempDir()
	t.Setenv("CODEX_HOME", customCodexHome)

	installedPath, err := setupCodex(home)
	if err != nil {
		t.Fatalf("setupCodex with CODEX_HOME: %v", err)
	}

	expectedPath := filepath.Join(customCodexHome, "skills", "forge.json")
	if installedPath != expectedPath {
		t.Errorf("returned path = %q, want %q", installedPath, expectedPath)
	}
	if _, err := os.Stat(installedPath); err != nil {
		t.Errorf("skill file not found at reported CODEX_HOME path %s: %v", installedPath, err)
	}
	// Must NOT write to the default location.
	defaultSkillPath := filepath.Join(home, ".codex", "skills", "forge.json")
	if _, err := os.Stat(defaultSkillPath); err == nil {
		t.Errorf("skill file should not exist at default path %s", defaultSkillPath)
	}
}

func TestSetupCodex_WritesExplicitVerificationAndPostToolUseHook(t *testing.T) {
	home := t.TempDir()
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)

	installedPath, err := setupCodex(home)
	if err != nil {
		t.Fatalf("setupCodex: %v", err)
	}

	data, err := os.ReadFile(installedPath)
	if err != nil {
		t.Fatalf("read skill file: %v", err)
	}

	var skill map[string]any
	if err := json.Unmarshal(data, &skill); err != nil {
		t.Fatalf("unmarshal skill: %v", err)
	}

	hooks, _ := skill["hooks"].(map[string]any)
	if _, ok := hooks["PostToolUse"].(string); !ok {
		t.Fatalf("expected PostToolUse hook in Codex skill, got: %#v", hooks["PostToolUse"])
	}

	setup, _ := skill["setup"].(map[string]any)
	verification, _ := setup["verification"].(map[string]any)
	note, _ := verification["note"].(string)
	if !strings.Contains(note, "Do not guess whether Forge is installed or running") {
		t.Fatalf("verification note missing exact instruction, got: %q", note)
	}
}

func TestSetupCodex_PermissionDenied(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod read-only dirs behave differently on Windows — covered by integration")
	}
	home := t.TempDir()
	codexHome := filepath.Join(home, ".codex")
	skillDir := filepath.Join(codexHome, "skills")
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		t.Fatalf("mkdir .codex: %v", err)
	}
	if err := os.Mkdir(skillDir, 0o444); err != nil {
		t.Fatalf("mkdir skills read-only: %v", err)
	}
	defer os.Chmod(skillDir, 0o700)

	_, err := setupCodex(home)
	if err == nil {
		t.Fatal("expected error for read-only skills dir")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("expected 'permission denied' in error, got: %v", err)
	}
}
