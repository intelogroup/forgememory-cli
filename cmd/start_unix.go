//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

func configureUnixBackground(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true,
	}
}

func startBackground(cmd *exec.Cmd) error {
	configureUnixBackground(cmd)
	return cmd.Start()
}
