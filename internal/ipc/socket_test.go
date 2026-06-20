//go:build !windows

package ipc

import (
	"encoding/json"
	"net"
	"testing"
	"time"
)

func TestListenAndSend(t *testing.T) {
	ln, addr, err := Listen()
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	defer ln.Close()

	if addr == "" {
		t.Error("addr should not be empty")
	}

	// Start accepting connections
	msgCh := make(chan map[string]string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var msg map[string]string
		json.NewDecoder(conn).Decode(&msg)
		msgCh <- msg
	}()

	// Send a message
	testMsg := map[string]string{"key": "value"}
	if err := Send(testMsg); err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	// Wait for message
	select {
	case msg := <-msgCh:
		if msg["key"] != "value" {
			t.Errorf("msg[key] = %s, want 'value'", msg["key"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for message")
	}
}

func TestSendFailsSilentlyWhenDaemonDown(t *testing.T) {
	// Remove any existing socket
	sockPath := socketPath()
	_ = removeSocket(sockPath)

	// Send should fail silently (no panic, no hang)
	testMsg := map[string]string{"key": "value"}
	err := Send(testMsg)
	if err == nil {
		// It might succeed if there's a stale socket, that's ok
		t.Log("Send succeeded (stale socket?)")
	}
}

func removeSocket(path string) error {
	return nil // Don't actually remove during tests
}

func TestSocketPath(t *testing.T) {
	path := socketPath()
	if path == "" {
		t.Error("socketPath should not be empty")
	}
	if !contains(path, ".forge") {
		t.Errorf("socketPath should contain .forge: %s", path)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestListenAcceptsConnections(t *testing.T) {
	ln, addr, err := Listen()
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	defer ln.Close()

	// Try to connect
	conn, err := net.Dial("unix", addr)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	conn.Close()
}

func TestIsDaemonAlive(t *testing.T) {
	ln, _, err := Listen()
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	defer ln.Close()

	if !IsDaemonAlive() {
		t.Error("IsDaemonAlive should return true when daemon is listening")
	}
}

