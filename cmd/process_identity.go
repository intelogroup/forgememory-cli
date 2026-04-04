package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

func processIdentity(pid int) (string, error) {
	if pid <= 0 {
		return "", fmt.Errorf("invalid pid %d", pid)
	}

	switch runtime.GOOS {
	case "windows":
		out, err := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/FO", "CSV", "/NH").Output()
		if err != nil {
			return "", err
		}
		line := strings.TrimSpace(string(out))
		if line == "" || strings.HasPrefix(line, "INFO:") {
			return "", fmt.Errorf("pid %d not found", pid)
		}
		line = strings.TrimPrefix(line, "\"")
		if idx := strings.Index(line, "\","); idx >= 0 {
			return line[:idx], nil
		}
		return strings.Trim(line, "\""), nil
	default:
		out, err := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
		if err != nil {
			return "", err
		}
		identity := strings.TrimSpace(string(out))
		if identity == "" {
			return "", fmt.Errorf("pid %d not found", pid)
		}
		return identity, nil
	}
}

func normalizeProcessIdentity(identity string) string {
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return ""
	}
	fields := strings.Fields(identity)
	if len(fields) == 0 {
		return ""
	}
	return strings.ToLower(filepath.Base(strings.Trim(fields[0], `"`)))
}

func isForgeProcessIdentity(identity string) bool {
	base := normalizeProcessIdentity(identity)
	if base == "" {
		return false
	}
	if strings.HasPrefix(base, "forgememo") {
		return false
	}
	return base == "forge" || base == "forge.exe" || strings.HasPrefix(base, "forge-")
}
