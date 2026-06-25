//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
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
		proc, findErr := os.FindProcess(pid)
		if findErr != nil {
			return nil
		}
		_ = proc.Kill()
	}

	for i := 0; i < 30; i++ {
		if _, err := processIdentity(pid); err != nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("process %d did not exit within 3s", pid)
}
