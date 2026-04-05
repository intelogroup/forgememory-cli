package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/forge/forge/internal/service"
)

type exitPanic struct {
	code int
}

func withServiceCLISeamsReset(t *testing.T) {
	t.Helper()
	prevNew := serviceNew
	prevInstalled := serviceIsInstalled
	prevInstall := serviceInstall
	prevUninstall := serviceUninstall
	prevStart := serviceStart
	prevStop := serviceStop
	prevExit := exitWithCode
	t.Cleanup(func() {
		serviceNew = prevNew
		serviceIsInstalled = prevInstalled
		serviceInstall = prevInstall
		serviceUninstall = prevUninstall
		serviceStart = prevStart
		serviceStop = prevStop
		exitWithCode = prevExit
	})
}

func expectExitCode(t *testing.T, want int, fn func()) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected exit code %d, got no exit", want)
		}
		p, ok := r.(exitPanic)
		if !ok {
			t.Fatalf("expected exit panic, got %T", r)
		}
		if p.code != want {
			t.Fatalf("exit code = %d, want %d", p.code, want)
		}
	}()
	fn()
}

func TestRunServiceInstall_AlreadyInstalled(t *testing.T) {
	withServiceCLISeamsReset(t)
	serviceNew = func() (*service.Manager, error) { return &service.Manager{}, nil }
	serviceIsInstalled = func(*service.Manager) bool { return true }

	out := captureStdout(func() { runServiceInstall(nil) })
	if !strings.Contains(out, "Service already installed.") {
		t.Fatalf("output = %q, want already installed", out)
	}
}

func TestRunServiceInstall_InstallErrorExits(t *testing.T) {
	withServiceCLISeamsReset(t)
	serviceNew = func() (*service.Manager, error) { return &service.Manager{}, nil }
	serviceIsInstalled = func(*service.Manager) bool { return false }
	serviceInstall = func(*service.Manager) error { return errors.New("boom") }
	exitWithCode = func(code int) { panic(exitPanic{code: code}) }

	expectExitCode(t, 1, func() { runServiceInstall(nil) })
}

func TestRunServiceUninstall_NotInstalled(t *testing.T) {
	withServiceCLISeamsReset(t)
	serviceNew = func() (*service.Manager, error) { return &service.Manager{}, nil }
	serviceIsInstalled = func(*service.Manager) bool { return false }

	out := captureStdout(func() { runServiceUninstall(nil) })
	if !strings.Contains(out, "Service not installed.") {
		t.Fatalf("output = %q, want service not installed", out)
	}
}

func TestRunServiceStart_StartErrorExits(t *testing.T) {
	withServiceCLISeamsReset(t)
	serviceNew = func() (*service.Manager, error) { return &service.Manager{}, nil }
	serviceStart = func(*service.Manager) error { return errors.New("cannot start") }
	exitWithCode = func(code int) { panic(exitPanic{code: code}) }

	expectExitCode(t, 1, func() { runServiceStart(nil) })
}

func TestRunServiceStop_Success(t *testing.T) {
	withServiceCLISeamsReset(t)
	serviceNew = func() (*service.Manager, error) { return &service.Manager{}, nil }
	serviceStop = func(*service.Manager) error { return nil }

	out := captureStdout(func() { runServiceStop(nil) })
	if !strings.Contains(out, "Service stopped.") {
		t.Fatalf("output = %q, want service stopped", out)
	}
}

func TestRunServiceStart_ManagerCreationErrorExits(t *testing.T) {
	withServiceCLISeamsReset(t)
	serviceNew = func() (*service.Manager, error) { return nil, errors.New("new failed") }
	exitWithCode = func(code int) { panic(exitPanic{code: code}) }

	expectExitCode(t, 1, func() { runServiceStart(nil) })
}
