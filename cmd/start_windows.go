//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// startBackground launches the daemon as a fully detached process on Windows.
// Without CREATE_NEW_PROCESS_GROUP the PowerShell/CMD job object kills the child
// when the parent (forge start) exits, leaving a stale addr/pid file but no listener.
func startBackground(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
		HideWindow:    true,
	}
	return cmd.Start()
}
