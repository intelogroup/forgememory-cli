package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ForgeInstall describes a forge binary found on disk.
type ForgeInstall struct {
	Path    string // absolute path as found in PATH
	Version string // e.g. "0.4.20", "dev", or "unknown"
}

// findForgeInstalls scans every directory in PATH for binaries named "forge"
// or "forgememo", queries each one's version, and deduplicates by real path.
func findForgeInstalls() []ForgeInstall {
	dirs := filepath.SplitList(os.Getenv("PATH"))
	seen := make(map[string]struct{})
	var results []ForgeInstall

	add := func(candidate string) {
		real, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			real = candidate
		}
		if _, dup := seen[real]; dup {
			return
		}
		seen[real] = struct{}{}
		results = append(results, ForgeInstall{
			Path:    candidate,
			Version: queryForgeVersion(candidate),
		})
	}

	for _, dir := range dirs {
		for _, name := range []string{"forge", "forgememo"} {
			candidate := filepath.Join(dir, name)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				add(candidate)
			}
		}
	}
	return results
}

// queryForgeVersion runs `binaryPath --version` and extracts the version token.
// Expected output format: "forge 0.4.20". Returns "unknown" on any failure.
func queryForgeVersion(binaryPath string) string {
	out, err := exec.Command(binaryPath, "--version").Output()
	if err != nil {
		return "unknown"
	}
	line := strings.TrimSpace(string(out))
	parts := strings.Fields(line)
	if len(parts) >= 2 {
		return parts[len(parts)-1]
	}
	return line
}

// isManagedForgePath reports whether p is under a directory that forge itself
// manages, making it safe to auto-remove during doctor --repair.
// System-managed paths (e.g., /usr/local/bin, /opt/homebrew) are excluded.
func isManagedForgePath(p string) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	managed := []string{
		filepath.Join(home, ".forgememo", "bin"),
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, "go", "bin"),
		filepath.Join(home, "bin"),
		filepath.Join(home, ".nvm"),
	}
	for _, prefix := range managed {
		if strings.HasPrefix(p, prefix+string(filepath.Separator)) || p == prefix {
			return true
		}
	}
	return false
}
