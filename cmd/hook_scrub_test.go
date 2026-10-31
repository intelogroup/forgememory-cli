package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The outbox is written before the database, so a secret must not reach it either.
func TestHookScrubsSecretsBeforeTheOutbox(t *testing.T) {
	home := t.TempDir()
	for name, secret := range map[string]string{
		"prose":    "my staging database password is hunter2Fake0",
		"supabase": `here it is: sbp_` + strings.Repeat("0a1b2c3d", 5),
	} {
		payload, _ := json.Marshal(map[string]string{"session_id": "s-" + name, "hook_event_name": "PostToolUse", "tool_name": "Bash", "prompt": secret, "cwd": home})
		cmd := exec.Command(forgeBin, "hook")
		cmd.Env = append(baseEnv(), "HOME="+home)
		cmd.Stdin = bytes.NewReader(payload)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("hook %s: %v\n%s", name, err, out)
		}
	}
	files, err := filepath.Glob(filepath.Join(home, ".forge", "outbox", "*"))
	if err != nil || len(files) != 2 {
		t.Fatalf("outbox files = %v, err = %v; want 2, so the check below means something", files, err)
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte("hunter2Fake0")) || bytes.Contains(data, []byte("sbp_0a1b")) {
			t.Fatalf("secret reached the outbox: %s", data)
		}
	}
}
