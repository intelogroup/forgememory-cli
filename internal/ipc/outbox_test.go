package ipc

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestEnqueueAndDrainOutbox(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	if err := Enqueue(map[string]string{"id": "first"}); err != nil {
		t.Fatalf("Enqueue first: %v", err)
	}
	if err := Enqueue(map[string]string{"id": "second"}); err != nil {
		t.Fatalf("Enqueue second: %v", err)
	}

	var got []string
	drained, err := DrainOutbox(func(payload []byte) error {
		var msg map[string]string
		if err := json.Unmarshal(payload, &msg); err != nil {
			return err
		}
		got = append(got, msg["id"])
		return nil
	})
	if err != nil {
		t.Fatalf("DrainOutbox: %v", err)
	}
	if drained != 2 {
		t.Fatalf("drained = %d, want 2", drained)
	}
	if len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("replay order = %#v, want [first second]", got)
	}

	entries, err := os.ReadDir(filepath.Join(home, ".forge", "outbox"))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("outbox has %d files after drain, want 0", len(entries))
	}
}

func TestDrainOutboxKeepsFailedAndRemainingMessages(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	if err := Enqueue(map[string]string{"id": "first"}); err != nil {
		t.Fatalf("Enqueue first: %v", err)
	}
	if err := Enqueue(map[string]string{"id": "second"}); err != nil {
		t.Fatalf("Enqueue second: %v", err)
	}

	drained, err := DrainOutbox(func([]byte) error { return errors.New("daemon unavailable") })
	if err == nil {
		t.Fatal("DrainOutbox unexpectedly succeeded")
	}
	if drained != 0 {
		t.Fatalf("drained = %d, want 0", drained)
	}

	entries, err := os.ReadDir(filepath.Join(home, ".forge", "outbox"))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("outbox has %d files after failed drain, want 2", len(entries))
	}
}
