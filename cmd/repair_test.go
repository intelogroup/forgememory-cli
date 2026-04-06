package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/forge/forge/internal/service"
)

// withRepairSeamsReset resets the service CLI seams and installs a spy for
// ReadInstalledBinaryPath via a fake Manager. It returns mutable counters for
// install/uninstall/start/stop call counts.
func withRepairSeamsReset(t *testing.T) {
	t.Helper()
	withServiceCLISeamsReset(t)
}

// TestRepairServiceIfNeeded_PathsMatch — no repair when stored == current.
func TestRepairServiceIfNeeded_PathsMatch(t *testing.T) {
	withRepairSeamsReset(t)

	installCalls := 0
	uninstallCalls := 0

	serviceNew = func() (*service.Manager, error) {
		return &service.Manager{BinaryPath: "/same/forge", HomeDir: t.TempDir()}, nil
	}
	serviceIsInstalled = func(m *service.Manager) bool { return true }
	serviceInstall = func(m *service.Manager) error { installCalls++; return nil }
	serviceUninstall = func(m *service.Manager) error { uninstallCalls++; return nil }

	// Pre-write a plist whose binary path matches BinaryPath.
	mgr := &service.Manager{BinaryPath: "/same/forge", HomeDir: t.TempDir()}
	home := mgr.HomeDir
	plistDir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(plistDir, 0o700); err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
    <key>ProgramArguments</key>
    <array>
        <string>/same/forge</string>
        <string>daemon</string>
    </array>
</dict></plist>`
	if err := os.WriteFile(filepath.Join(plistDir, "com.forge.daemon.plist"), []byte(plist), 0o600); err != nil {
		t.Fatal(err)
	}

	// Return manager that uses the same home so ReadInstalledBinaryPath finds the plist.
	serviceNew = func() (*service.Manager, error) {
		return &service.Manager{BinaryPath: "/same/forge", HomeDir: home}, nil
	}

	if err := repairServiceIfNeeded(); err != nil {
		t.Fatalf("repairServiceIfNeeded: %v", err)
	}
	if installCalls != 0 || uninstallCalls != 0 {
		t.Errorf("expected no install/uninstall, got install=%d uninstall=%d", installCalls, uninstallCalls)
	}
}

// TestRepairServiceIfNeeded_PathMismatch — reinstalls when paths differ, prints message.
func TestRepairServiceIfNeeded_PathMismatch(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("plist-based repair only applies to macOS")
	}
	withRepairSeamsReset(t)

	home := t.TempDir()

	// Write plist with old binary path.
	plistDir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(plistDir, 0o700); err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
    <key>ProgramArguments</key>
    <array>
        <string>/old/path/forge</string>
        <string>daemon</string>
    </array>
</dict></plist>`
	if err := os.WriteFile(filepath.Join(plistDir, "com.forge.daemon.plist"), []byte(plist), 0o600); err != nil {
		t.Fatal(err)
	}

	installCalls := 0
	uninstallCalls := 0
	stopCalls := 0
	startCalls := 0

	serviceNew = func() (*service.Manager, error) {
		return &service.Manager{BinaryPath: "/new/path/forge", HomeDir: home}, nil
	}
	serviceInstall = func(m *service.Manager) error { installCalls++; return nil }
	serviceUninstall = func(m *service.Manager) error { uninstallCalls++; return nil }
	serviceStart = func(m *service.Manager) error { startCalls++; return nil }
	serviceStop = func(m *service.Manager) error { stopCalls++; return nil }

	out := captureStdout(func() {
		if err := repairServiceIfNeeded(); err != nil {
			t.Fatalf("repairServiceIfNeeded: %v", err)
		}
	})

	if uninstallCalls != 1 {
		t.Errorf("want 1 uninstall call, got %d", uninstallCalls)
	}
	if installCalls != 1 {
		t.Errorf("want 1 install call, got %d", installCalls)
	}
	if !strings.Contains(out, "Repaired service") {
		t.Errorf("expected repair message, got %q", out)
	}
	if !strings.Contains(out, "/new/path/forge") {
		t.Errorf("expected new path in message, got %q", out)
	}
}

// TestRepairServiceIfNeeded_NotInstalled — no-op when plist doesn't exist.
func TestRepairServiceIfNeeded_NotInstalled(t *testing.T) {
	withRepairSeamsReset(t)

	installCalls := 0
	serviceNew = func() (*service.Manager, error) {
		return &service.Manager{BinaryPath: "/forge", HomeDir: t.TempDir()}, nil
	}
	serviceInstall = func(m *service.Manager) error { installCalls++; return nil }

	if err := repairServiceIfNeeded(); err != nil {
		t.Fatalf("repairServiceIfNeeded: %v", err)
	}
	if installCalls != 0 {
		t.Errorf("want no install when not installed, got %d", installCalls)
	}
}

// TestRepairServiceIfNeeded_InstallError — returns error when reinstall fails.
func TestRepairServiceIfNeeded_InstallError(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("plist-based repair only applies to macOS")
	}
	withRepairSeamsReset(t)

	home := t.TempDir()

	plistDir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(plistDir, 0o700); err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
    <key>ProgramArguments</key>
    <array>
        <string>/old/forge</string>
        <string>daemon</string>
    </array>
</dict></plist>`
	if err := os.WriteFile(filepath.Join(plistDir, "com.forge.daemon.plist"), []byte(plist), 0o600); err != nil {
		t.Fatal(err)
	}

	serviceNew = func() (*service.Manager, error) {
		return &service.Manager{BinaryPath: "/new/forge", HomeDir: home}, nil
	}
	serviceUninstall = func(m *service.Manager) error { return nil }
	serviceInstall = func(m *service.Manager) error { return os.ErrPermission }
	serviceStart = func(m *service.Manager) error { return nil }
	serviceStop = func(m *service.Manager) error { return nil }

	err := repairServiceIfNeeded()
	if err == nil {
		t.Fatal("expected error when install fails, got nil")
	}
	if !strings.Contains(err.Error(), "reinstall service") {
		t.Errorf("expected reinstall service error, got %q", err)
	}
}

// ---------------------------------------------------------------------------
// writeAddr atomic write tests
// ---------------------------------------------------------------------------

// TestWriteAddr_RoundTrip verifies writeAddr writes and readAddr reads correctly.
func TestWriteAddr_RoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	writeAddr("unix:/tmp/forge.sock")
	got := readAddr()
	if got != "unix:/tmp/forge.sock" {
		t.Errorf("readAddr() = %q, want %q", got, "unix:/tmp/forge.sock")
	}
}

// TestWriteAddr_SetsEnvVar verifies FORGE_PIPE_ADDR is set after writeAddr.
func TestWriteAddr_SetsEnvVar(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	writeAddr("tcp://127.0.0.1:9999")
	if got := os.Getenv("FORGE_PIPE_ADDR"); got != "tcp://127.0.0.1:9999" {
		t.Errorf("FORGE_PIPE_ADDR = %q, want tcp://127.0.0.1:9999", got)
	}
}

// TestWriteAddr_FilePermissions verifies the addr file is created with mode 0600.
func TestWriteAddr_FilePermissions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeAddr("test-addr")

	info, err := os.Stat(filepath.Join(home, ".forge", "forge.addr"))
	if err != nil {
		t.Fatalf("stat forge.addr: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("forge.addr permissions = %04o, want 0600", perm)
	}
}

// TestWriteAddr_Overwrite verifies writeAddr overwrites a previous value cleanly.
func TestWriteAddr_Overwrite(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	writeAddr("first-addr")
	writeAddr("second-addr")

	if got := readAddr(); got != "second-addr" {
		t.Errorf("readAddr() = %q after overwrite, want second-addr", got)
	}
}

// TestWriteAddr_NoTempFileLeftBehind verifies no forge.addr.tmp.* files remain after write.
func TestWriteAddr_NoTempFileLeftBehind(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	writeAddr("clean-addr")

	dir := filepath.Join(home, ".forge")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), "forge.addr.tmp") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

// TestWriteAddr_AtomicNoPartialRead verifies concurrent reads never see a partial address.
// Run with -race to exercise the data-race detector.
func TestWriteAddr_AtomicNoPartialRead(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	const addr = "unix:/tmp/forge-test-atomic.sock"
	done := make(chan struct{})

	// Writer goroutine: repeatedly writes.
	go func() {
		for i := 0; i < 500; i++ {
			writeAddr(addr)
		}
		close(done)
	}()

	// Reader goroutine: every read must be either "" (pre-first-write) or the full addr.
	for {
		select {
		case <-done:
			return
		default:
			got := readAddr()
			if got != "" && got != addr {
				t.Errorf("partial read detected: %q", got)
			}
		}
	}
}
