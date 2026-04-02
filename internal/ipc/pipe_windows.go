//go:build windows

package ipc

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
)

const pipeName = `\\.\pipe\forge`

// Listen creates a Named Pipe listener on Windows.
// Uses a simple TCP listener on localhost as a cross-platform fallback,
// since Go's standard library doesn't have native Named Pipe support.
// The pipe name is exposed via FORGE_PIPE_PORT env var.
func Listen() (net.Listener, string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", fmt.Errorf("listen tcp: %w", err)
	}
	addr := ln.Addr().String()
	return ln, addr, nil
}

// Send sends a JSON message to the pipe (used by hooks).
func Send(msg any) error {
	addr := pipeAddr()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return err // silent failure — daemon is down
	}
	defer conn.Close()
	return json.NewEncoder(conn).Encode(msg)
}

func pipeAddr() string {
	if addr := os.Getenv("FORGE_PIPE_ADDR"); addr != "" {
		return addr
	}
	return "127.0.0.1:0"
}
