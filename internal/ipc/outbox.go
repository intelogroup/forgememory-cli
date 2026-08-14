package ipc

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Enqueue persists one IPC message locally so it can be replayed if the daemon
// is unavailable. Each message is written to a temporary file and atomically
// renamed into the outbox, so readers never see a partial JSON document.
func Enqueue(msg any) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal outbox message: %w", err)
	}

	dir, err := outboxDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create outbox: %w", err)
	}

	name := fmt.Sprintf("%d-%s.json", time.Now().UTC().UnixNano(), uuid.NewString())
	tmp, err := os.CreateTemp(dir, ".outbox-*.tmp")
	if err != nil {
		return fmt.Errorf("create outbox temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("protect outbox temp file: %w", err)
	}
	if _, err := tmp.Write(payload); err != nil {
		tmp.Close()
		return fmt.Errorf("write outbox message: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close outbox message: %w", err)
	}
	if err := os.Rename(tmpName, filepath.Join(dir, name)); err != nil {
		return fmt.Errorf("commit outbox message: %w", err)
	}
	return nil
}

// DrainOutbox replays queued messages in creation order. A failed send leaves
// the current and remaining messages untouched for a later retry.
func DrainOutbox(send func([]byte) error) (int, error) {
	dir, err := outboxDir()
	if err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read outbox: %w", err)
	}

	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)

	drained := 0
	for _, name := range names {
		path := filepath.Join(dir, name)
		payload, err := os.ReadFile(path)
		if err != nil {
			return drained, fmt.Errorf("read outbox message %s: %w", name, err)
		}
		if err := send(payload); err != nil {
			return drained, fmt.Errorf("replay outbox message %s: %w", name, err)
		}
		if err := os.Remove(path); err != nil {
			return drained, fmt.Errorf("remove outbox message %s: %w", name, err)
		}
		drained++
	}
	return drained, nil
}

func outboxDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home for outbox: %w", err)
	}
	return filepath.Join(home, ".forge", "outbox"), nil
}
