//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

const (
	// detachedProcess creates a process with no inherited console attachment.
	// Without this, a hidden child can still be tied to the parent's console
	// lifecycle and die as soon as the wrapper process exits.
	detachedProcess = 0x00000008
	// createBreakawayFromJob escapes the parent's Windows Job Object so the
	// daemon is not killed when PowerShell/terminal job objects are torn down.
	createBreakawayFromJob = 0x01000000
	// createNoWindow creates the process without attaching it to any console.
	// This is the key flag: without it the daemon inherits the parent's console
	// and is killed by CTRL_CLOSE_EVENT when the terminal window is closed.
	createNoWindow = 0x08000000
)

// startBackground launches the daemon as a fully detached process on Windows.
// Uses DETACHED_PROCESS + CREATE_NO_WINDOW so the daemon has no console
// attachment and survives terminal close. Tries CREATE_BREAKAWAY_FROM_JOB
// first; falls back without it when the parent job disallows breakaway
// (e.g. GitHub Actions runners).
func startBackground(cmd *exec.Cmd) error {
	const base = detachedProcess | syscall.CREATE_NEW_PROCESS_GROUP | createNoWindow
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: base | createBreakawayFromJob,
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
		CreationFlags: base,
		HideWindow:    true,
	}
	return retry.Start()
}
