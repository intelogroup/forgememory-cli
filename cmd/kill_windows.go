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
func killProcess(pid int) error {
	// taskkill /T /F /PID kills the process tree. This is critical because
	// forge start spawns a detached daemon grandchild — Process.Kill only
	// kills the direct process, leaving the grandchild orphaned.
	cmd := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid))
	if err := cmd.Run(); err != nil {
		// taskkill fails if process already exited — fall back to direct kill.
		proc, findErr := os.FindProcess(pid)
		if findErr != nil {
			return nil
		}
		_ = proc.Kill()
	}

	// Wait up to 3 seconds for process to fully exit.
	for i := 0; i < 30; i++ {
		if _, err := processIdentity(pid); err != nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("process %d did not exit within 3s", pid)
}
