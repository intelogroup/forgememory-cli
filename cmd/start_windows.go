//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// createBreakawayFromJob detaches the child from the parent's Windows Job Object.
// PowerShell (and many terminals) place all child processes into a job object and
// kill them when the parent exits. CREATE_NEW_PROCESS_GROUP alone does not escape
// the job object — CREATE_BREAKAWAY_FROM_JOB is required.
const createBreakawayFromJob = 0x01000000

// startBackground launches the daemon as a fully detached process on Windows.
func startBackground(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | createBreakawayFromJob,
		HideWindow:    true,
	}
	return cmd.Start()
}
