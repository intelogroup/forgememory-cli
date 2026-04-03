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
// It tries CREATE_BREAKAWAY_FROM_JOB first to escape PowerShell/terminal job
// objects. If the parent job disallows breakaway (e.g. GitHub Actions runners),
// it falls back to CREATE_NEW_PROCESS_GROUP only.
func startBackground(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | createBreakawayFromJob,
		HideWindow:    true,
	}
	if err := cmd.Start(); err == nil {
		return nil
	}
	// Breakaway disallowed by parent job — rebuild and retry without it.
	retry := exec.Command(cmd.Path, cmd.Args[1:]...)
	retry.Stdin = cmd.Stdin
	retry.Stdout = cmd.Stdout
	retry.Stderr = cmd.Stderr
	retry.Env = cmd.Env
	retry.Dir = cmd.Dir
	retry.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
		HideWindow:    true,
	}
	return retry.Start()
}
