package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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
	switch runtime.GOOS {
	case "darwin":
		return m.installLaunchd()
	case "linux":
		return m.installSystemd()
	case "windows":
		return m.installWindowsService()
	default:
		return fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
}

// Uninstall removes Forge system service.
func (m *Manager) Uninstall() error {
	switch runtime.GOOS {
	case "darwin":
		return m.uninstallLaunchd()
	case "linux":
		return m.uninstallSystemd()
	case "windows":
		return m.uninstallWindowsService()
	default:
		return fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
}

// Start starts the Forge service.
func (m *Manager) Start() error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("launchctl", "load", m.launchdPlistPath()).Run()
	case "linux":
		return exec.Command("systemctl", "--user", "start", "forge").Run()
	case "windows":
		return exec.Command("sc", "start", "forge").Run()
	default:
		return fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
}

// Stop stops the Forge service.
func (m *Manager) Stop() error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("launchctl", "unload", m.launchdPlistPath()).Run()
	case "linux":
		return exec.Command("systemctl", "--user", "stop", "forge").Run()
	case "windows":
		return exec.Command("sc", "stop", "forge").Run()
	default:
		return fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
}

// --- macOS launchd ---

func (m *Manager) installLaunchd() error {
	plistDir := filepath.Join(m.HomeDir, "Library", "LaunchAgents")
	if err := os.MkdirAll(plistDir, 0o700); err != nil {
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
	if err := os.WriteFile(plistPath, []byte(plist), 0o600); err != nil {
		return err
	}

	fmt.Printf("  Installed launchd agent: %s\n", plistPath)
	return nil
}

func (m *Manager) uninstallLaunchd() error {
	plistPath := m.launchdPlistPath()
	_ = exec.Command("launchctl", "unload", plistPath).Run()
	return os.Remove(plistPath)
}

func (m *Manager) launchdPlistPath() string {
	return filepath.Join(m.HomeDir, "Library", "LaunchAgents", "com.forge.daemon.plist")
}

// --- Linux systemd ---

func (m *Manager) installSystemd() error {
	unitDir := filepath.Join(m.HomeDir, ".config", "systemd", "user")
	if err := os.MkdirAll(unitDir, 0o700); err != nil {
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
	if err := os.WriteFile(unitPath, []byte(unit), 0o600); err != nil {
		return err
	}

	fmt.Printf("  Installed systemd service: %s\n", unitPath)
	return exec.Command("systemctl", "--user", "daemon-reload").Run()
}

func (m *Manager) uninstallSystemd() error {
	_ = exec.Command("systemctl", "--user", "stop", "forge").Run()
	_ = exec.Command("systemctl", "--user", "disable", "forge").Run()
	unitPath := filepath.Join(m.HomeDir, ".config", "systemd", "user", "forge.service")
	return os.Remove(unitPath)
}

// --- Windows Service ---

func (m *Manager) installWindowsService() error {
	// Use sc.exe to create service
	cmd := exec.Command("sc", "create", "forge",
		"binPath=", fmt.Sprintf(`"%s" daemon`, m.BinaryPath),
		"start=", "auto",
		"DisplayName=", "Forge Memory Forger",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		// Fallback: create a scheduled task
		return m.installWindowsScheduledTask()
	}
	fmt.Println("  Installed Windows service: forge")
	return nil
}

func (m *Manager) uninstallWindowsService() error {
	_ = exec.Command("sc", "stop", "forge").Run()
	return exec.Command("sc", "delete", "forge").Run()
}

func (m *Manager) installWindowsScheduledTask() error {
	// Fallback: use schtasks for Windows
	cmd := exec.Command("schtasks", "/create",
		"/tn", "Forge Daemon",
		"/tr", fmt.Sprintf(`"%s" daemon`, m.BinaryPath),
		"/sc", "onlogon",
		"/rl", "limited",
		"/f",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to install Windows service: %w", err)
	}
	fmt.Println("  Installed Windows scheduled task: Forge Daemon")
	return nil
}

// --- Helpers ---

// IsServiceInstalled checks if the service is installed.
func (m *Manager) IsServiceInstalled() bool {
	switch runtime.GOOS {
	case "darwin":
		_, err := os.Stat(m.launchdPlistPath())
		return err == nil
	case "linux":
		unitPath := filepath.Join(m.HomeDir, ".config", "systemd", "user", "forge.service")
		_, err := os.Stat(unitPath)
		return err == nil
	case "windows":
		out, _ := exec.Command("sc", "query", "forge").Output()
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

	switch runtime.GOOS {
	case "darwin":
		out, _ := exec.Command("launchctl", "list", "com.forge.daemon").Output()
		if strings.Contains(string(out), "com.forge.daemon") {
			return "installed"
		}
	case "linux":
		out, _ := exec.Command("systemctl", "--user", "is-active", "forge").Output()
		return strings.TrimSpace(string(out))
	case "windows":
		out, _ := exec.Command("sc", "query", "forge").Output()
		if strings.Contains(string(out), "RUNNING") {
			return "running"
		}
		return "installed"
	}
	return "unknown"
}
