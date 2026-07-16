//go:build windows

package ipc

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
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

	t.Setenv("FORGE_PIPE_ADDR", addr)
	testMsg := map[string]string{"key": "value"}
	if err := Send(testMsg); err != nil {
		t.Fatalf("Send failed: %v", err)
	}

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
	t.Setenv("FORGE_PIPE_ADDR", "127.0.0.1:1")
	testMsg := map[string]string{"key": "value"}
	err := Send(testMsg)
	if err == nil {
		t.Log("Send succeeded (unexpected — something is listening on port 1)")
	}
}

func TestPipeAddrEnvVar(t *testing.T) {
	t.Setenv("FORGE_PIPE_ADDR", "127.0.0.1:9999")
	if got := pipeAddr(); got != "127.0.0.1:9999" {
		t.Errorf("pipeAddr() = %q, want 127.0.0.1:9999", got)
	}
}

func TestPipeAddrAddrFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	addrFile := filepath.Join(home, ".forge", "forge.addr")
	if err := os.MkdirAll(filepath.Dir(addrFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(addrFile, []byte("127.0.0.1:7777"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := pipeAddr(); got != "127.0.0.1:7777" {
		t.Errorf("pipeAddr() = %q, want 127.0.0.1:7777", got)
	}
}

func TestPipeAddrAddrFileTrimsSpace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	addrFile := filepath.Join(home, ".forge", "forge.addr")
	if err := os.MkdirAll(filepath.Dir(addrFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(addrFile, []byte("  127.0.0.1:5555  \n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := pipeAddr(); got != "127.0.0.1:5555" {
		t.Errorf("pipeAddr() = %q, want 127.0.0.1:5555", got)
	}
}

func TestPipeAddrFallback(t *testing.T) {
	t.Setenv("FORGE_PIPE_ADDR", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	if got := pipeAddr(); got != "127.0.0.1:0" {
		t.Errorf("pipeAddr() = %q, want 127.0.0.1:0", got)
	}
}

func TestPipeAddrEnvVarOverridesFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	addrFile := filepath.Join(home, ".forge", "forge.addr")
	if err := os.MkdirAll(filepath.Dir(addrFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(addrFile, []byte("127.0.0.1:7777"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("FORGE_PIPE_ADDR", "127.0.0.1:9999")
	if got := pipeAddr(); got != "127.0.0.1:9999" {
		t.Errorf("pipeAddr() = %q, want 127.0.0.1:9999", got)
	}
}

func TestListenAcceptsConnections(t *testing.T) {
	ln, addr, err := Listen()
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	defer ln.Close()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	conn.Close()
}

func TestIsDaemonAlive(t *testing.T) {
	ln, addr, err := Listen()
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	defer ln.Close()

	t.Setenv("FORGE_PIPE_ADDR", addr)
	if !IsDaemonAlive() {
		t.Error("IsDaemonAlive should return true when daemon is listening")
	}
}

func TestIsDaemonAliveReturnsFalseWhenDown(t *testing.T) {
	t.Setenv("FORGE_PIPE_ADDR", "127.0.0.1:1")
	if IsDaemonAlive() {
		t.Error("IsDaemonAlive should return false when nothing is listening")
	}
}
