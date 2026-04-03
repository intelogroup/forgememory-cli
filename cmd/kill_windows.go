//go:build windows

package main

import (
	"fmt"
	"os"
	"time"
)

// killProcess terminates the given PID immediately (Windows has no SIGTERM).
func killProcess(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process %d: %w", pid, err)
	}

	// Try SIGKILL equivalent on Windows (will terminate immediately)
	if err := proc.Kill(); err != nil {
		return fmt.Errorf("kill process %d: %w", pid, err)
	}

	// Wait up to 3 seconds for process to exit
	for i := 0; i < 30; i++ {
		if _, err := os.FindProcess(pid); err != nil {
			return nil // Process is gone
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Force kill - try to find the process again
	proc2, err := os.FindProcess(pid)
	if err != nil {
		return nil // Process already gone
	}
	proc2.Kill()
	return nil
}
