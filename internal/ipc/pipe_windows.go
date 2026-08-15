//go:build windows

package ipc

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Listen creates a TCP listener on localhost (Windows has no Unix sockets in stdlib).
func Listen() (net.Listener, string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", fmt.Errorf("listen tcp: %w", err)
	}
	addr := ln.Addr().String()
	return ln, addr, nil
}

// Send sends a JSON message to the daemon (used by hooks).
func Send(msg any) error {
	addr := pipeAddr()
	conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
	if err != nil {
		return err // silent failure — daemon is down
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(50 * time.Millisecond))
	if err := json.NewEncoder(conn).Encode(msg); err != nil {
		return err
	}
	return awaitAck(conn)
}

// SendRaw sends an already encoded JSON message to the daemon.
func SendRaw(payload []byte) error {
	conn, err := net.DialTimeout("tcp", pipeAddr(), 50*time.Millisecond)
	if err != nil {
		return err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(50 * time.Millisecond))
	if _, err = conn.Write(append(payload, '\n')); err != nil {
		return err
	}
	return awaitAck(conn)
}

// IsDaemonAlive checks if the daemon is currently responding to TCP connection.
func IsDaemonAlive() bool {
	conn, err := net.DialTimeout("tcp", pipeAddr(), 50*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// pipeAddr resolves the daemon TCP address.
// Priority: FORGE_PIPE_ADDR env var → forge.addr file → fallback.
func pipeAddr() string {
	if addr := os.Getenv("FORGE_PIPE_ADDR"); addr != "" {
		return addr
	}
	// Read from forge.addr file written by the daemon on startup.
	// Check HOME env var first so tests can override via t.Setenv.
	home := os.Getenv("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	addrFile := filepath.Join(home, ".forge", "forge.addr")
	if data, err := os.ReadFile(addrFile); err == nil {
		if addr := strings.TrimSpace(string(data)); addr != "" {
			return addr
		}
	}
	return "127.0.0.1:0"
}
