//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
	"time"
)

// killProcess sends SIGTERM to the given PID, waits up to 3 seconds for
// graceful exit, then falls back to SIGKILL.
func killProcess(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process %d: %w", pid, err)
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return nil // process already gone
	}
	done := make(chan struct{})
	go func() {
		proc.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-time.After(3 * time.Second):
		_ = proc.Kill()
		return nil
	}
}

// isProcessAlive checks whether a process with the given PID exists.
func isProcessAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// On Unix, FindProcess succeeds even for zombie processes.
	// We use syscall.Signal(0) to check if the process is actually alive.
	err = proc.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	_, psErr := processIdentity(pid)
	return psErr == nil
}

// isParentAlive checks if the parent process is still alive.
func isParentAlive(parentPID int) bool {
	proc, err := os.FindProcess(parentPID)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil
}
