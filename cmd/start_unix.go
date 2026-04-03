//go:build !windows

package main

import "os/exec"

func startBackground(cmd *exec.Cmd) error {
	return cmd.Start()
}
