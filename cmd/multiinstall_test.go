package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// queryForgeVersion
// ---------------------------------------------------------------------------

func TestQueryForgeVersion_ValidOutput(t *testing.T) {
	// Build a tiny shell script that prints "forge 1.2.3".
	if runtime.GOOS == "windows" {
		t.Skip("shell script not supported on Windows")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "forge")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'forge 1.2.3'\n"), 0o755); err != nil {
		t.Fatalf("WriteFile script: %v", err)
	}
	got := queryForgeVersion(script)
	if got != "1.2.3" {
		t.Errorf("queryForgeVersion = %q, want 1.2.3", got)
	}
}

func TestQueryForgeVersion_DevBuild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script not supported on Windows")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "forge")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'forge dev'\n"), 0o755); err != nil {
		t.Fatalf("WriteFile script: %v", err)
	}
	got := queryForgeVersion(script)
	if got != "dev" {
		t.Errorf("queryForgeVersion = %q, want dev", got)
	}
}

func TestQueryForgeVersion_NonExistent(t *testing.T) {
	got := queryForgeVersion("/nonexistent/path/forge")
	if got != "unknown" {
		t.Errorf("queryForgeVersion for missing binary = %q, want unknown", got)
	}
}

func TestQueryForgeVersion_ExitNonZero(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script not supported on Windows")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "forge")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("WriteFile script: %v", err)
	}
	got := queryForgeVersion(script)
	if got != "unknown" {
		t.Errorf("queryForgeVersion for failing binary = %q, want unknown", got)
	}
}

// ---------------------------------------------------------------------------
// isManagedForgePath
// ---------------------------------------------------------------------------

func TestIsManagedForgePath_ManagedPaths(t *testing.T) {
	home, _ := os.UserHomeDir()
	managed := []string{
		filepath.Join(home, ".forgememo", "bin", "forge"),
		filepath.Join(home, ".local", "bin", "forge"),
		filepath.Join(home, "go", "bin", "forge"),
		filepath.Join(home, "bin", "forge"),
		filepath.Join(home, ".nvm", "versions", "node", "v20.11.0", "bin", "forge"),
	}
	for _, p := range managed {
		if !isManagedForgePath(p) {
			t.Errorf("isManagedForgePath(%q) = false, want true", p)
		}
	}
}

func TestIsManagedForgePath_SystemPaths(t *testing.T) {
	system := []string{
		"/usr/local/bin/forge",
		"/opt/homebrew/bin/forge",
		"/usr/bin/forge",
	}
	for _, p := range system {
		if isManagedForgePath(p) {
			t.Errorf("isManagedForgePath(%q) = true, want false (system path)", p)
		}
	}
}

func TestIsManagedForgePath_PrefixBoundary(t *testing.T) {
	home, _ := os.UserHomeDir()
	// A path that starts with ~/.local but is NOT under ~/.local/bin
	notManaged := filepath.Join(home, ".localbinary", "forge")
	if isManagedForgePath(notManaged) {
		t.Errorf("isManagedForgePath(%q) = true, want false (prefix boundary)", notManaged)
	}
}

// ---------------------------------------------------------------------------
// findForgeInstalls
// ---------------------------------------------------------------------------

func TestFindForgeInstalls_EmptyPATH(t *testing.T) {
	t.Setenv("PATH", "")
	installs := findForgeInstalls()
	if len(installs) != 0 {
		t.Errorf("findForgeInstalls with empty PATH = %d installs, want 0", len(installs))
	}
}

func TestFindForgeInstalls_SingleBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script not supported on Windows")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "forge")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'forge 0.4.20'\n"), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv("PATH", dir)

	installs := findForgeInstalls()
	if len(installs) != 1 {
		t.Fatalf("findForgeInstalls = %d, want 1", len(installs))
	}
	if installs[0].Path != script {
		t.Errorf("Path = %q, want %q", installs[0].Path, script)
	}
	if installs[0].Version != "0.4.20" {
		t.Errorf("Version = %q, want 0.4.20", installs[0].Version)
	}
}

func TestFindForgeInstalls_DeduplicatesSameRealPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks may require elevated privileges on Windows")
	}
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	// Create one real binary in dir1.
	real := filepath.Join(dir1, "forge")
	if err := os.WriteFile(real, []byte("#!/bin/sh\necho 'forge 0.4.20'\n"), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Symlink to the same binary from dir2.
	link := filepath.Join(dir2, "forge")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	t.Setenv("PATH", dir1+string(filepath.ListSeparator)+dir2)

	installs := findForgeInstalls()
	if len(installs) != 1 {
		t.Errorf("findForgeInstalls should deduplicate symlinks, got %d installs", len(installs))
	}
}

func TestFindForgeInstalls_MultipleBinaries(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script not supported on Windows")
	}
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	for _, tc := range []struct {
		dir, ver string
	}{
		{dir1, "0.4.19"},
		{dir2, "0.4.20"},
	} {
		script := filepath.Join(tc.dir, "forge")
		content := fmt.Sprintf("#!/bin/sh\necho 'forge %s'\n", tc.ver)
		if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}

	t.Setenv("PATH", dir1+string(filepath.ListSeparator)+dir2)

	installs := findForgeInstalls()
	if len(installs) != 2 {
		t.Fatalf("findForgeInstalls = %d, want 2", len(installs))
	}

	versions := map[string]bool{}
	for _, fi := range installs {
		versions[fi.Version] = true
	}
	if !versions["0.4.19"] || !versions["0.4.20"] {
		t.Errorf("expected versions 0.4.19 and 0.4.20, got %v", versions)
	}
}

func TestFindForgeInstalls_FindsForgememoName(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script not supported on Windows")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "forgememo")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'forge 0.3.0'\n"), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	t.Setenv("PATH", dir)

	installs := findForgeInstalls()
	if len(installs) != 1 {
		t.Fatalf("findForgeInstalls should find 'forgememo' binary, got %d", len(installs))
	}
	if !strings.HasSuffix(installs[0].Path, "forgememo") {
		t.Errorf("Path = %q, expected to end with forgememo", installs[0].Path)
	}
}

func TestFindForgeInstalls_IgnoresDirectories(t *testing.T) {
	dir := t.TempDir()
	// Create a directory named "forge" — should be ignored.
	if err := os.Mkdir(filepath.Join(dir, "forge"), 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	t.Setenv("PATH", dir)

	installs := findForgeInstalls()
	if len(installs) != 0 {
		t.Errorf("findForgeInstalls should ignore directories named forge, got %d", len(installs))
	}
}
