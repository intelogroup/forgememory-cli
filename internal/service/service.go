package service

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

var (
	currentGOOS = func() string { return runtime.GOOS }
	runCommand  = func(name string, args ...string) error {
		return exec.Command(name, args...).Run()
	}
	outputCommand = func(name string, args ...string) ([]byte, error) {
		return exec.Command(name, args...).Output()
	}
	runCommandWithIO = func(stdout, stderr io.Writer, name string, args ...string) error {
		cmd := exec.Command(name, args...)
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		return cmd.Run()
	}
	mkdirAll   = os.MkdirAll
	writeFile  = os.WriteFile
	removeFile = os.Remove
	statFile   = os.Stat
)

// Manager handles system service registration.
type Manager struct {
	BinaryPath string
	HomeDir    string
}

// New creates a new service Manager.
func New() (*Manager, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return &Manager{
		BinaryPath: exe,
		HomeDir:    home,
	}, nil
}

// Install registers Forge as a system service.
func (m *Manager) Install() error {
	switch currentGOOS() {
	case "darwin":
		return m.installLaunchd()
	case "linux":
		return m.installSystemd()
	case "windows":
		return m.installWindowsService()
	default:
		return fmt.Errorf("unsupported OS: %s", currentGOOS())
	}
}

// Uninstall removes Forge system service.
func (m *Manager) Uninstall() error {
	switch currentGOOS() {
	case "darwin":
		return m.uninstallLaunchd()
	case "linux":
		return m.uninstallSystemd()
	case "windows":
		return m.uninstallWindowsService()
	default:
		return fmt.Errorf("unsupported OS: %s", currentGOOS())
	}
}

// Start starts the Forge service.
func (m *Manager) Start() error {
	switch currentGOOS() {
	case "darwin":
		return runCommand("launchctl", "load", m.launchdPlistPath())
	case "linux":
		return runCommand("systemctl", "--user", "start", "forge")
	case "windows":
		return runCommand("sc", "start", "forge")
	default:
		return fmt.Errorf("unsupported OS: %s", currentGOOS())
	}
}

// Stop stops the Forge service.
func (m *Manager) Stop() error {
	switch currentGOOS() {
	case "darwin":
		return runCommand("launchctl", "unload", m.launchdPlistPath())
	case "linux":
		return runCommand("systemctl", "--user", "stop", "forge")
	case "windows":
		return runCommand("sc", "stop", "forge")
	default:
		return fmt.Errorf("unsupported OS: %s", currentGOOS())
	}
}

// --- macOS launchd ---

func (m *Manager) installLaunchd() error {
	plistDir := filepath.Join(m.HomeDir, "Library", "LaunchAgents")
	if err := mkdirAll(plistDir, 0o700); err != nil {
		return err
	}

	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.forge.daemon</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>daemon</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>%s/.forge/logs/forge.log</string>
    <key>StandardErrorPath</key>
    <string>%s/.forge/logs/forge.log</string>
</dict>
</plist>`, m.BinaryPath, m.HomeDir, m.HomeDir)

	plistPath := m.launchdPlistPath()
	if err := writeFile(plistPath, []byte(plist), 0o600); err != nil {
		return err
	}

	fmt.Printf("  Installed launchd agent: %s\n", plistPath)
	return nil
}

func (m *Manager) uninstallLaunchd() error {
	plistPath := m.launchdPlistPath()
	_ = runCommand("launchctl", "unload", plistPath)
	return removeFile(plistPath)
}

func (m *Manager) launchdPlistPath() string {
	return filepath.Join(m.HomeDir, "Library", "LaunchAgents", "com.forge.daemon.plist")
}

// --- Linux systemd ---

func (m *Manager) installSystemd() error {
	unitDir := filepath.Join(m.HomeDir, ".config", "systemd", "user")
	if err := mkdirAll(unitDir, 0o700); err != nil {
		return err
	}

	unit := fmt.Sprintf(`[Unit]
Description=Forge — Silent Memory Forger
After=network.target

[Service]
Type=simple
ExecStart=%s daemon
Restart=on-failure
RestartSec=5
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=default.target
`, m.BinaryPath)

	unitPath := filepath.Join(unitDir, "forge.service")
	if err := writeFile(unitPath, []byte(unit), 0o600); err != nil {
		return err
	}

	fmt.Printf("  Installed systemd service: %s\n", unitPath)
	return runCommand("systemctl", "--user", "daemon-reload")
}

func (m *Manager) uninstallSystemd() error {
	_ = runCommand("systemctl", "--user", "stop", "forge")
	_ = runCommand("systemctl", "--user", "disable", "forge")
	unitPath := filepath.Join(m.HomeDir, ".config", "systemd", "user", "forge.service")
	return removeFile(unitPath)
}

// --- Windows Service ---

func (m *Manager) installWindowsService() error {
	// Use sc.exe to create service
	err := runCommandWithIO(os.Stdout, os.Stderr, "sc", "create", "forge",
		"binPath=", fmt.Sprintf(`"%s" daemon`, m.BinaryPath),
		"start=", "auto",
		"DisplayName=", "Forge Memory Forger",
	)
	if err != nil {
		// Fallback: create a scheduled task
		return m.installWindowsScheduledTask()
	}
	fmt.Println("  Installed Windows service: forge")
	return nil
}

func (m *Manager) uninstallWindowsService() error {
	_ = runCommand("sc", "stop", "forge")
	return runCommand("sc", "delete", "forge")
}

func (m *Manager) installWindowsScheduledTask() error {
	// Fallback: use schtasks for Windows
	err := runCommandWithIO(os.Stdout, os.Stderr, "schtasks", "/create",
		"/tn", "Forge Daemon",
		"/tr", fmt.Sprintf(`"%s" daemon`, m.BinaryPath),
		"/sc", "onlogon",
		"/rl", "limited",
		"/f",
	)
	if err != nil {
		return fmt.Errorf("failed to install Windows service: %w", err)
	}
	fmt.Println("  Installed Windows scheduled task: Forge Daemon")
	return nil
}

// --- Helpers ---

// IsServiceInstalled checks if the service is installed.
func (m *Manager) IsServiceInstalled() bool {
	switch currentGOOS() {
	case "darwin":
		_, err := statFile(m.launchdPlistPath())
		return err == nil
	case "linux":
		unitPath := filepath.Join(m.HomeDir, ".config", "systemd", "user", "forge.service")
		_, err := statFile(unitPath)
		return err == nil
	case "windows":
		out, _ := outputCommand("sc", "query", "forge")
		return strings.Contains(string(out), "SERVICE_NAME")
	default:
		return false
	}
}

// ServiceStatus returns the service status.
func (m *Manager) ServiceStatus() string {
	if !m.IsServiceInstalled() {
		return "not installed"
	}

	switch currentGOOS() {
	case "darwin":
		out, _ := outputCommand("launchctl", "list", "com.forge.daemon")
		if strings.Contains(string(out), "com.forge.daemon") {
			return "installed"
		}
	case "linux":
		out, _ := outputCommand("systemctl", "--user", "is-active", "forge")
		return strings.TrimSpace(string(out))
	case "windows":
		out, _ := outputCommand("sc", "query", "forge")
		if strings.Contains(string(out), "RUNNING") {
			return "running"
		}
		return "installed"
	}
	return "unknown"
}
