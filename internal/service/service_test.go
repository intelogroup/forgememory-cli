package service

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type cmdCall struct {
	name string
	args []string
}

func withServiceSeamsReset(t *testing.T) {
	t.Helper()
	prevGOOS := currentGOOS
	prevRun := runCommand
	prevOutput := outputCommand
	prevRunWithIO := runCommandWithIO
	prevMkdirAll := mkdirAll
	prevWriteFile := writeFile
	prevRemoveFile := removeFile
	prevStatFile := statFile
	t.Cleanup(func() {
		currentGOOS = prevGOOS
		runCommand = prevRun
		outputCommand = prevOutput
		runCommandWithIO = prevRunWithIO
		mkdirAll = prevMkdirAll
		writeFile = prevWriteFile
		removeFile = prevRemoveFile
		statFile = prevStatFile
	})
}

func TestNewManager(t *testing.T) {
	m, err := New()
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if m.BinaryPath == "" {
		t.Fatal("New().BinaryPath should not be empty")
	}
	if m.HomeDir == "" {
		t.Fatal("New().HomeDir should not be empty")
	}
}

func TestInstallAndUninstall_UnsupportedOS(t *testing.T) {
	withServiceSeamsReset(t)
	currentGOOS = func() string { return "plan9" }
	m := &Manager{BinaryPath: "/tmp/forge", HomeDir: t.TempDir()}

	if err := m.Install(); err == nil || !strings.Contains(err.Error(), "unsupported OS") {
		t.Fatalf("Install() error = %v, want unsupported OS", err)
	}
	if err := m.Uninstall(); err == nil || !strings.Contains(err.Error(), "unsupported OS") {
		t.Fatalf("Uninstall() error = %v, want unsupported OS", err)
	}
}

func TestStartStopCommandRouting(t *testing.T) {
	withServiceSeamsReset(t)
	home := t.TempDir()
	m := &Manager{BinaryPath: "/tmp/forge", HomeDir: home}
	var calls []cmdCall
	runCommand = func(name string, args ...string) error {
		calls = append(calls, cmdCall{name: name, args: append([]string(nil), args...)})
		return nil
	}

	tests := []struct {
		goos      string
		startName string
		startArgs []string
		stopName  string
		stopArgs  []string
	}{
		{
			goos:      "darwin",
			startName: "launchctl",
			startArgs: []string{"load", filepath.Join(home, "Library", "LaunchAgents", "com.forge.daemon.plist")},
			stopName:  "launchctl",
			stopArgs:  []string{"unload", filepath.Join(home, "Library", "LaunchAgents", "com.forge.daemon.plist")},
		},
		{
			goos:      "linux",
			startName: "systemctl",
			startArgs: []string{"--user", "start", "forge"},
			stopName:  "systemctl",
			stopArgs:  []string{"--user", "stop", "forge"},
		},
		{
			goos:      "windows",
			startName: "sc",
			startArgs: []string{"start", "forge"},
			stopName:  "sc",
			stopArgs:  []string{"stop", "forge"},
		},
	}

	for _, tc := range tests {
		calls = nil
		currentGOOS = func() string { return tc.goos }
		if err := m.Start(); err != nil {
			t.Fatalf("Start() %s: %v", tc.goos, err)
		}
		if len(calls) != 1 || calls[0].name != tc.startName || strings.Join(calls[0].args, " ") != strings.Join(tc.startArgs, " ") {
			t.Fatalf("Start() %s calls = %#v, want %s %v", tc.goos, calls, tc.startName, tc.startArgs)
		}

		calls = nil
		if err := m.Stop(); err != nil {
			t.Fatalf("Stop() %s: %v", tc.goos, err)
		}
		if len(calls) != 1 || calls[0].name != tc.stopName || strings.Join(calls[0].args, " ") != strings.Join(tc.stopArgs, " ") {
			t.Fatalf("Stop() %s calls = %#v, want %s %v", tc.goos, calls, tc.stopName, tc.stopArgs)
		}
	}
}

func TestStartStop_UnsupportedOS(t *testing.T) {
	withServiceSeamsReset(t)
	currentGOOS = func() string { return "plan9" }
	m := &Manager{BinaryPath: "/tmp/forge", HomeDir: t.TempDir()}
	if err := m.Start(); err == nil {
		t.Fatal("Start() on unsupported OS should fail")
	}
	if err := m.Stop(); err == nil {
		t.Fatal("Stop() on unsupported OS should fail")
	}
}

func TestInstallLaunchdWritesPlist(t *testing.T) {
	withServiceSeamsReset(t)
	home := t.TempDir()
	m := &Manager{BinaryPath: "/usr/local/bin/forge", HomeDir: home}
	if err := m.installLaunchd(); err != nil {
		t.Fatalf("installLaunchd() error: %v", err)
	}

	plistPath := filepath.Join(home, "Library", "LaunchAgents", "com.forge.daemon.plist")
	data, err := os.ReadFile(plistPath)
	if err != nil {
		t.Fatalf("ReadFile plist: %v", err)
	}
	content := string(data)
	for _, want := range []string{
		"com.forge.daemon",
		"<string>/usr/local/bin/forge</string>",
		"<string>daemon</string>",
		filepath.ToSlash(filepath.Join(home, ".forge", "logs", "forge.log")),
	} {
		if !strings.Contains(strings.ReplaceAll(content, "\\", "/"), strings.ReplaceAll(want, "\\", "/")) {
			t.Fatalf("plist missing %q\ncontent:\n%s", want, content)
		}
	}
}

func TestUninstallLaunchdUnloadsAndRemoves(t *testing.T) {
	withServiceSeamsReset(t)
	home := t.TempDir()
	m := &Manager{BinaryPath: "/tmp/forge", HomeDir: home}
	plistPath := m.launchdPlistPath()
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(plistPath, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile plist: %v", err)
	}

	var calls []cmdCall
	runCommand = func(name string, args ...string) error {
		calls = append(calls, cmdCall{name: name, args: append([]string(nil), args...)})
		return nil
	}

	if err := m.uninstallLaunchd(); err != nil {
		t.Fatalf("uninstallLaunchd() error: %v", err)
	}
	if len(calls) != 1 || calls[0].name != "launchctl" || strings.Join(calls[0].args, " ") != "unload "+plistPath {
		t.Fatalf("uninstallLaunchd() calls = %#v", calls)
	}
	if _, err := os.Stat(plistPath); !os.IsNotExist(err) {
		t.Fatalf("plist should be removed, stat err = %v", err)
	}
}

func TestInstallSystemdWritesUnitAndReloads(t *testing.T) {
	withServiceSeamsReset(t)
	home := t.TempDir()
	m := &Manager{BinaryPath: "/opt/forge/forge", HomeDir: home}
	var calls []cmdCall
	runCommand = func(name string, args ...string) error {
		calls = append(calls, cmdCall{name: name, args: append([]string(nil), args...)})
		return nil
	}
	if err := m.installSystemd(); err != nil {
		t.Fatalf("installSystemd() error: %v", err)
	}

	unitPath := filepath.Join(home, ".config", "systemd", "user", "forge.service")
	data, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("ReadFile unit: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "ExecStart=/opt/forge/forge daemon") {
		t.Fatalf("unit missing ExecStart, content:\n%s", content)
	}
	if len(calls) != 1 || calls[0].name != "systemctl" || strings.Join(calls[0].args, " ") != "--user daemon-reload" {
		t.Fatalf("installSystemd() calls = %#v", calls)
	}
}

func TestUninstallSystemdStopsDisablesAndRemoves(t *testing.T) {
	withServiceSeamsReset(t)
	home := t.TempDir()
	m := &Manager{BinaryPath: "/tmp/forge", HomeDir: home}
	unitPath := filepath.Join(home, ".config", "systemd", "user", "forge.service")
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(unitPath, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	var calls []cmdCall
	runCommand = func(name string, args ...string) error {
		calls = append(calls, cmdCall{name: name, args: append([]string(nil), args...)})
		return nil
	}

	if err := m.uninstallSystemd(); err != nil {
		t.Fatalf("uninstallSystemd() error: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("uninstallSystemd() calls len = %d, want 2", len(calls))
	}
	if calls[0].name != "systemctl" || strings.Join(calls[0].args, " ") != "--user stop forge" {
		t.Fatalf("first call = %#v", calls[0])
	}
	if calls[1].name != "systemctl" || strings.Join(calls[1].args, " ") != "--user disable forge" {
		t.Fatalf("second call = %#v", calls[1])
	}
	if _, err := os.Stat(unitPath); !os.IsNotExist(err) {
		t.Fatalf("unit should be removed, stat err = %v", err)
	}
}

func TestInstallWindowsServiceFallbackToScheduledTask(t *testing.T) {
	withServiceSeamsReset(t)
	m := &Manager{BinaryPath: `C:\forge\forge.exe`, HomeDir: t.TempDir()}
	var calls []cmdCall
	runCommandWithIO = func(_, _ io.Writer, name string, args ...string) error {
		calls = append(calls, cmdCall{name: name, args: append([]string(nil), args...)})
		if name == "sc" {
			return errors.New("sc create failed")
		}
		return nil
	}
	if err := m.installWindowsService(); err != nil {
		t.Fatalf("installWindowsService() error: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("calls len = %d, want 2", len(calls))
	}
	if calls[0].name != "sc" || len(calls[0].args) < 2 || calls[0].args[0] != "create" || calls[0].args[1] != "forge" {
		t.Fatalf("first call = %#v", calls[0])
	}
	if calls[1].name != "schtasks" || len(calls[1].args) == 0 || calls[1].args[0] != "/create" {
		t.Fatalf("second call = %#v", calls[1])
	}
}

func TestInstallWindowsScheduledTaskError(t *testing.T) {
	withServiceSeamsReset(t)
	m := &Manager{BinaryPath: `C:\forge\forge.exe`, HomeDir: t.TempDir()}
	runCommandWithIO = func(_, _ io.Writer, _ string, _ ...string) error {
		return errors.New("schtasks failed")
	}
	err := m.installWindowsScheduledTask()
	if err == nil || !strings.Contains(err.Error(), "failed to install Windows service") {
		t.Fatalf("installWindowsScheduledTask() error = %v", err)
	}
}

func TestUninstallWindowsServiceCallsStopAndDelete(t *testing.T) {
	withServiceSeamsReset(t)
	m := &Manager{BinaryPath: `C:\forge\forge.exe`, HomeDir: t.TempDir()}
	var calls []cmdCall
	runCommand = func(name string, args ...string) error {
		calls = append(calls, cmdCall{name: name, args: append([]string(nil), args...)})
		return nil
	}
	if err := m.uninstallWindowsService(); err != nil {
		t.Fatalf("uninstallWindowsService() error: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("calls len = %d, want 2", len(calls))
	}
	if calls[0].name != "sc" || strings.Join(calls[0].args, " ") != "stop forge" {
		t.Fatalf("first call = %#v", calls[0])
	}
	if calls[1].name != "sc" || strings.Join(calls[1].args, " ") != "delete forge" {
		t.Fatalf("second call = %#v", calls[1])
	}
}

func TestIsServiceInstalledCases(t *testing.T) {
	withServiceSeamsReset(t)
	m := &Manager{BinaryPath: "/tmp/forge", HomeDir: t.TempDir()}

	currentGOOS = func() string { return "darwin" }
	statFile = func(_ string) (os.FileInfo, error) { return nil, nil }
	if !m.IsServiceInstalled() {
		t.Fatal("darwin IsServiceInstalled should be true when stat succeeds")
	}

	currentGOOS = func() string { return "linux" }
	statFile = func(_ string) (os.FileInfo, error) { return nil, os.ErrNotExist }
	if m.IsServiceInstalled() {
		t.Fatal("linux IsServiceInstalled should be false when stat fails")
	}

	currentGOOS = func() string { return "windows" }
	outputCommand = func(_ string, _ ...string) ([]byte, error) {
		return []byte("SERVICE_NAME: forge"), nil
	}
	if !m.IsServiceInstalled() {
		t.Fatal("windows IsServiceInstalled should be true with SERVICE_NAME output")
	}

	currentGOOS = func() string { return "plan9" }
	if m.IsServiceInstalled() {
		t.Fatal("unsupported OS IsServiceInstalled should be false")
	}
}

func TestServiceStatusCases(t *testing.T) {
	withServiceSeamsReset(t)
	m := &Manager{BinaryPath: "/tmp/forge", HomeDir: t.TempDir()}

	currentGOOS = func() string { return "linux" }
	statFile = func(_ string) (os.FileInfo, error) { return nil, os.ErrNotExist }
	if got := m.ServiceStatus(); got != "not installed" {
		t.Fatalf("ServiceStatus() = %q, want not installed", got)
	}

	currentGOOS = func() string { return "linux" }
	statFile = func(_ string) (os.FileInfo, error) { return nil, nil }
	outputCommand = func(name string, args ...string) ([]byte, error) {
		if name == "systemctl" && strings.Join(args, " ") == "--user is-active forge" {
			return []byte("active\n"), nil
		}
		return nil, errors.New("unexpected command")
	}
	if got := m.ServiceStatus(); got != "active" {
		t.Fatalf("linux ServiceStatus() = %q, want active", got)
	}

	currentGOOS = func() string { return "darwin" }
	statFile = func(_ string) (os.FileInfo, error) { return nil, nil }
	outputCommand = func(_ string, _ ...string) ([]byte, error) {
		return []byte("com.forge.daemon"), nil
	}
	if got := m.ServiceStatus(); got != "installed" {
		t.Fatalf("darwin ServiceStatus() = %q, want installed", got)
	}

	currentGOOS = func() string { return "windows" }
	outputCommand = func(_ string, _ ...string) ([]byte, error) {
		return []byte("SERVICE_NAME: forge\nSTATE : 4 RUNNING"), nil
	}
	if got := m.ServiceStatus(); got != "running" {
		t.Fatalf("windows running ServiceStatus() = %q, want running", got)
	}

	currentGOOS = func() string { return "windows" }
	outputCommand = func(_ string, _ ...string) ([]byte, error) {
		return []byte("SERVICE_NAME: forge\nSTATE : 1 STOPPED"), nil
	}
	if got := m.ServiceStatus(); got != "installed" {
		t.Fatalf("windows stopped ServiceStatus() = %q, want installed", got)
	}
}
