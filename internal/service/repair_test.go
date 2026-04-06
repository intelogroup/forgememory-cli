package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReadInstalledBinaryPath_NotInstalled returns empty string when no plist/unit exists.
func TestReadInstalledBinaryPath_NotInstalled(t *testing.T) {
	withServiceSeamsReset(t)
	m := &Manager{BinaryPath: "/new/forge", HomeDir: t.TempDir()}

	for _, goos := range []string{"darwin", "linux"} {
		currentGOOS = func() string { return goos }
		got, err := m.ReadInstalledBinaryPath()
		if err != nil {
			t.Errorf("%s: unexpected error: %v", goos, err)
		}
		if got != "" {
			t.Errorf("%s: want empty string when not installed, got %q", goos, got)
		}
	}
}

// TestReadInstalledBinaryPath_UnsupportedOS returns empty string on unsupported OS.
func TestReadInstalledBinaryPath_UnsupportedOS(t *testing.T) {
	withServiceSeamsReset(t)
	currentGOOS = func() string { return "plan9" }
	m := &Manager{BinaryPath: "/tmp/forge", HomeDir: t.TempDir()}

	got, err := m.ReadInstalledBinaryPath()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Fatalf("want empty string for unsupported OS, got %q", got)
	}
}

// TestReadLaunchdBinaryPath_RoundTrip installs a plist and reads back the binary path.
func TestReadLaunchdBinaryPath_RoundTrip(t *testing.T) {
	withServiceSeamsReset(t)
	home := t.TempDir()
	m := &Manager{BinaryPath: "/usr/local/bin/forge", HomeDir: home}
	currentGOOS = func() string { return "darwin" }

	if err := m.installLaunchd(); err != nil {
		t.Fatalf("installLaunchd: %v", err)
	}

	got, err := m.ReadInstalledBinaryPath()
	if err != nil {
		t.Fatalf("ReadInstalledBinaryPath: %v", err)
	}
	if got != "/usr/local/bin/forge" {
		t.Errorf("want /usr/local/bin/forge, got %q", got)
	}
}

// TestReadLaunchdBinaryPath_MismatchDetected detects when stored path differs from current.
func TestReadLaunchdBinaryPath_MismatchDetected(t *testing.T) {
	withServiceSeamsReset(t)
	home := t.TempDir()

	// Install with old path.
	old := &Manager{BinaryPath: "/old/path/forge", HomeDir: home}
	if err := old.installLaunchd(); err != nil {
		t.Fatalf("installLaunchd: %v", err)
	}

	// Read with new path manager.
	current := &Manager{BinaryPath: "/new/path/forge", HomeDir: home}
	currentGOOS = func() string { return "darwin" }
	got, err := current.ReadInstalledBinaryPath()
	if err != nil {
		t.Fatalf("ReadInstalledBinaryPath: %v", err)
	}
	if got != "/old/path/forge" {
		t.Errorf("want /old/path/forge, got %q", got)
	}
	if got == current.BinaryPath {
		t.Error("should detect mismatch: stored path should differ from current")
	}
}

// TestReadSystemdBinaryPath_RoundTrip installs a systemd unit and reads back the binary path.
func TestReadSystemdBinaryPath_RoundTrip(t *testing.T) {
	withServiceSeamsReset(t)
	home := t.TempDir()
	m := &Manager{BinaryPath: "/opt/forge/forge", HomeDir: home}
	var cmds []cmdCall
	runCommand = func(name string, args ...string) error {
		cmds = append(cmds, cmdCall{name: name, args: args})
		return nil
	}
	currentGOOS = func() string { return "linux" }

	if err := m.installSystemd(); err != nil {
		t.Fatalf("installSystemd: %v", err)
	}

	got, err := m.ReadInstalledBinaryPath()
	if err != nil {
		t.Fatalf("ReadInstalledBinaryPath: %v", err)
	}
	if got != "/opt/forge/forge" {
		t.Errorf("want /opt/forge/forge, got %q", got)
	}
}

// TestReadSystemdBinaryPath_MissingExecStart returns error when ExecStart is absent.
func TestReadSystemdBinaryPath_MissingExecStart(t *testing.T) {
	withServiceSeamsReset(t)
	home := t.TempDir()
	currentGOOS = func() string { return "linux" }
	m := &Manager{BinaryPath: "/tmp/forge", HomeDir: home}

	unitDir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(unitDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(unitDir, "forge.service"), []byte("[Unit]\nDescription=Forge\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := m.ReadInstalledBinaryPath()
	if err == nil || !strings.Contains(err.Error(), "ExecStart not found") {
		t.Fatalf("want ExecStart not found error, got %v", err)
	}
}

// TestReadLaunchdBinaryPath_MalformedPlist returns error on plist without ProgramArguments.
func TestReadLaunchdBinaryPath_MalformedPlist(t *testing.T) {
	withServiceSeamsReset(t)
	home := t.TempDir()
	currentGOOS = func() string { return "darwin" }
	m := &Manager{BinaryPath: "/tmp/forge", HomeDir: home}

	plistDir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(plistDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(plistDir, "com.forge.daemon.plist"), []byte("<plist><dict></dict></plist>"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := m.ReadInstalledBinaryPath()
	if err == nil {
		t.Fatal("want error for malformed plist, got nil")
	}
}
