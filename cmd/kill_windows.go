//go:build windows

package main

import (
	"fmt"
	"os"
)

// killProcess terminates the given PID immediately (Windows has no SIGTERM).
func killProcess(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process %d: %w", pid, err)
	}
	return proc.Kill()
}
