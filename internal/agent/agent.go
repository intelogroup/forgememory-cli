package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// ForgePath returns the stable forge binary path to use for persistent
// integrations and daemon respawn. It intentionally refuses to reuse the
// current process image when that image is a temp/test artifact.
func ForgePath() string {
	if exe, err := os.Executable(); err == nil && IsStableExecutablePath(exe) {
		return exe
	}
	if path, err := exec.LookPath("forge"); err == nil {
		if abs, absErr := filepath.Abs(path); absErr == nil {
			return abs
		}
		return path
	}
	return "forge"
}

// IsStableExecutablePath reports whether path is suitable for persistent
// registration and daemon respawn.
func IsStableExecutablePath(path string) bool {
	base := filepath.Base(path)
	if base == "agent.test" {
		return false
	}
	normalized := strings.ToLower(strings.ReplaceAll(path, "\\", "/"))
	return !strings.Contains(normalized, "/go-build") &&
		!strings.Contains(normalized, "/forge-bin-")
}

func stableExecutablePath(path string) bool {
	return IsStableExecutablePath(path)
}

// DetectAgents returns which AI agents are installed.
func DetectAgents(home string) []string {
	var agents []string
	for _, adapter := range registeredAdapters() {
		if adapter.Detect(home) {
			agents = append(agents, adapter.Name())
		}
	}
	return agents
}

// hasCodex checks if Codex is installed by probing its version command.
func hasCodex() bool {
	cmd := exec.Command("codex", "--version")
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// SetupAgent configures an agent to work with Forge and returns the path of
// the installed skill file (empty string for agents that don't write one).
func SetupAgent(agent string, home string) (string, error) {
	for _, adapter := range registeredAdapters() {
		if adapter.Name() == agent {
			return adapter.Setup(home)
		}
	}
	return "", fmt.Errorf("unknown agent: %s", agent)
}

// OS detection helpers

func IsWindows() bool {
	return runtime.GOOS == "windows"
}

func IsMacOS() bool {
	return runtime.GOOS == "darwin"
}

func IsLinux() bool {
	return runtime.GOOS == "linux"
}
