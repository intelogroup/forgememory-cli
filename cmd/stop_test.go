package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/forge/forge/internal/service"
)

// bogusPID is a PID very unlikely to correspond to a real process, so
// killProcess treats it as "already gone" and safely no-ops — these tests
// only care whether runStop *attempts* the kill path, not that a real
// process dies.
const bogusPID = 999999

func writeBogusPIDFile(t *testing.T, home string) {
	t.Helper()
	dir := filepath.Join(home, ".forge")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "forge.pid"), []byte("999999"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestRunStop_NoServiceInstalled_KillsProcessOnly covers the existing,
// pre-issue-#51 behavior: no service installed (plain background daemon) —
// serviceStop must never be called, and PID-file cleanup still happens.
func TestRunStop_NoServiceInstalled_KillsProcessOnly(t *testing.T) {
	withServiceCLISeamsReset(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeBogusPIDFile(t, home)

	stopCalls := 0
	serviceNew = func() (*service.Manager, error) { return &service.Manager{}, nil }
	serviceIsInstalled = func(*service.Manager) bool { return false }
	serviceStop = func(*service.Manager) error { stopCalls++; return nil }

	captureStdout(func() { runStop(nil) })

	if stopCalls != 0 {
		t.Errorf("serviceStop called %d times, want 0 (no service installed)", stopCalls)
	}
	if _, err := os.Stat(filepath.Join(home, ".forge", "forge.pid")); !os.IsNotExist(err) {
		t.Errorf("forge.pid should be removed after stop, stat err = %v", err)
	}
}

// TestRunStop_ServiceInstalled_CallsServiceStop is the core issue-#51
// regression guard: when Forge is installed as a launchd/systemd/Windows
// service, runStop must ask the service manager to stop it (not just SIGTERM
// the raw PID), or the supervisor's KeepAlive/Restart policy can relaunch the
// daemon behind forge stop's back.
func TestRunStop_ServiceInstalled_CallsServiceStop(t *testing.T) {
	withServiceCLISeamsReset(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeBogusPIDFile(t, home)

	stopCalls := 0
	serviceNew = func() (*service.Manager, error) { return &service.Manager{}, nil }
	serviceIsInstalled = func(*service.Manager) bool { return true }
	serviceStop = func(*service.Manager) error { stopCalls++; return nil }

	captureStdout(func() { runStop(nil) })

	if stopCalls != 1 {
		t.Errorf("serviceStop called %d times, want 1", stopCalls)
	}
	if _, err := os.Stat(filepath.Join(home, ".forge", "forge.pid")); !os.IsNotExist(err) {
		t.Errorf("forge.pid should be removed after stop, stat err = %v", err)
	}
}

// TestRunStop_ServiceStopFails_StillAttemptsKillAndCleanup ensures a service
// manager error doesn't abort the fallback PID kill / state cleanup — a
// manually-started daemon coexisting with an installed-but-idle service must
// still be reclaimed.
func TestRunStop_ServiceStopFails_StillAttemptsKillAndCleanup(t *testing.T) {
	withServiceCLISeamsReset(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeBogusPIDFile(t, home)
	if err := os.WriteFile(filepath.Join(home, ".forge", "forge.addr"), []byte("127.0.0.1:0"), 0o600); err != nil {
		t.Fatal(err)
	}

	serviceNew = func() (*service.Manager, error) { return &service.Manager{}, nil }
	serviceIsInstalled = func(*service.Manager) bool { return true }
	serviceStop = func(*service.Manager) error { return os.ErrPermission }

	captureStdout(func() { runStop(nil) })

	if _, err := os.Stat(filepath.Join(home, ".forge", "forge.pid")); !os.IsNotExist(err) {
		t.Errorf("forge.pid should still be removed when serviceStop fails, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".forge", "forge.addr")); !os.IsNotExist(err) {
		t.Errorf("forge.addr should still be removed when serviceStop fails, stat err = %v", err)
	}
}

// TestRunStop_ServiceInstalledButNoRunningPID covers a daemon known only to
// the service (no forge.pid on record) — serviceStop must still be called,
// and the absence of a PID file must not panic the fallback kill path.
func TestRunStop_ServiceInstalledButNoRunningPID(t *testing.T) {
	withServiceCLISeamsReset(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Deliberately no forge.pid file.

	stopCalls := 0
	serviceNew = func() (*service.Manager, error) { return &service.Manager{}, nil }
	serviceIsInstalled = func(*service.Manager) bool { return true }
	serviceStop = func(*service.Manager) error { stopCalls++; return nil }

	captureStdout(func() { runStop(nil) })

	if stopCalls != 1 {
		t.Errorf("serviceStop called %d times, want 1", stopCalls)
	}
}

// TestRunStop_ServiceNewErrors_FallsBackToKillOnly guards the case where the
// service manager itself can't be constructed (e.g. os.UserHomeDir fails) —
// runStop must not abort the whole command, just skip the service-stop path.
func TestRunStop_ServiceNewErrors_FallsBackToKillOnly(t *testing.T) {
	withServiceCLISeamsReset(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeBogusPIDFile(t, home)

	serviceNew = func() (*service.Manager, error) { return nil, os.ErrNotExist }

	out := captureStdout(func() { runStop(nil) })

	if _, err := os.Stat(filepath.Join(home, ".forge", "forge.pid")); !os.IsNotExist(err) {
		t.Errorf("forge.pid should still be removed when serviceNew fails, stat err = %v", err)
	}
	if out == "" {
		t.Error("expected some stop output even when serviceNew fails")
	}
}
