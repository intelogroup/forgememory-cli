//go:build !windows

package main

import (
	"os/exec"
	"testing"
)

func TestConfigureUnixBackground_DetachesUnixProcess(t *testing.T) {
	cmd := exec.Command("ignored")
	configureUnixBackground(cmd)

	if cmd.SysProcAttr == nil {
		t.Fatal("SysProcAttr should be configured on Unix background starts")
	}
	if !cmd.SysProcAttr.Setsid {
		t.Fatal("Unix background start should create a new session")
	}
}
