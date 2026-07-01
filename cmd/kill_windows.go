//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

// killProcess terminates the given PID and its entire process tree on Windows.
// Uses taskkill /T /F to kill child processes (e.g. daemon grandchildren
// spawned by forge start) that os.Process.Kill would leave orphaned.
// Validates PID identity before killing to avoid terminating an unrelated
// process tree if the PID was reused after a daemon crash.
func killProcess(pid int) error {
	identity, err := processIdentity(pid)
	if err != nil {
		return nil // process already gone
	}
	if !isForgeProcessIdentity(identity) {
		return nil // PID reused by a non-Forge process — treat as stale
	}

	cmd := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid))
	if err := cmd.Run(); err != nil {
		// Re-check identity: process may have exited and PID been reused since the
		// initial check above. Only fall back to proc.Kill if it's still our daemon.
		if id2, idErr := processIdentity(pid); idErr != nil || !isForgeProcessIdentity(id2) {
			return nil
		}
		proc, findErr := os.FindProcess(pid)
		if findErr != nil {
			return nil
		}
		if killErr := proc.Kill(); killErr != nil {
			return fmt.Errorf("taskkill failed: %w; fallback kill failed: %v", err, killErr)
		}
	}

	for i := 0; i < 30; i++ {
		id, err := processIdentity(pid)
		if err != nil || !isForgeProcessIdentity(id) {
			return nil // process gone or PID reused by something else
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("process %d did not exit within 3s", pid)
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

