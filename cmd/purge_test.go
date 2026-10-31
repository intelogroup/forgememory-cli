package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestParsePurgeArgs(t *testing.T) {
	if text, apply := parsePurgeArgs([]string{"two", "--apply", "words"}); text != "two words" || !apply {
		t.Fatalf("text=%q apply=%v", text, apply)
	}
	if text, apply := parsePurgeArgs(nil); text != "" || apply {
		t.Fatalf("text=%q apply=%v", text, apply)
	}
}

func TestForgetCommandEndToEnd(t *testing.T) {
	home := t.TempDir()
	run := func(args ...string) (string, error) {
		cmd := exec.Command(forgeBin, args...)
		cmd.Env = append(baseEnv(), "HOME="+home)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := run("save", "--type", "note", "--content", "project codename ZEBRA-7731 stays private"); err != nil {
		t.Fatalf("save: %v\n%s", err, out)
	}
	if out, err := run("forget", "abc"); err == nil {
		t.Fatalf("short text accepted: %s", out)
	}
	out, err := run("forget", "zebra-7731")
	if err != nil || !strings.Contains(out, "Dry run") || !strings.Contains(out, "events.payload: 1") {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	if strings.Contains(out, "stays private") {
		t.Fatalf("output leaked stored text: %s", out)
	}
	if out, err := run("forget", "zebra-7731", "--apply"); err != nil || strings.Contains(out, "Dry run") {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	if out, _ := run("search", "ZEBRA"); strings.Contains(out, "stays private") {
		t.Fatalf("still found: %s", out)
	}
	if out, err := run("forget", "zebra-7731"); err != nil || strings.Contains(out, "events.payload") {
		t.Fatalf("second dry run should find nothing: %v\n%s", err, out)
	}
}
