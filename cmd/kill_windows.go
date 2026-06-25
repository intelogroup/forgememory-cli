//go:build windows

package main

import (
	"fmt"
	"os"
	"syscall"
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

// isProcessAlive checks whether a process with the given PID exists.
func isProcessAlive(pid int) bool {
	h, err := syscall.OpenProcess(0x1000, false, uint32(pid)) // PROCESS_QUERY_LIMITED_INFORMATION
	if err != nil {
		return false
	}
	var code uint32
	err = syscall.GetExitCodeProcess(h, &code)
	syscall.CloseHandle(h)
	if err != nil || code != 259 {
		return false
	}
	_, psErr := processIdentity(pid)
	return psErr == nil
}

// isParentAlive checks if the parent process is still alive.
func isParentAlive(parentPID int) bool {
	h, err := syscall.OpenProcess(0x1000, false, uint32(parentPID)) // PROCESS_QUERY_LIMITED_INFORMATION
	if err != nil {
		return false
	}
	var code uint32
	err = syscall.GetExitCodeProcess(h, &code)
	syscall.CloseHandle(h)
	return err == nil && code == 259
}

