//go:build !windows

package ipc

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

// Listen creates a Unix domain socket for IPC.
func Listen() (net.Listener, string, error) {
	sockPath := socketPath()
	_ = os.Remove(sockPath) // clean up stale socket
	if err := os.MkdirAll(filepath.Dir(sockPath), 0o700); err != nil {
		return nil, "", fmt.Errorf("create socket dir: %w", err)
	}
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, "", fmt.Errorf("listen unix: %w", err)
	}
	_ = os.Chmod(sockPath, 0o600)
	return ln, sockPath, nil
}

// Send sends a JSON message to the socket (used by hooks).
func Send(msg any) error {
	conn, err := net.DialTimeout("unix", socketPath(), 50*time.Millisecond)
	if err != nil {
		return err // silent failure — daemon is down
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(50 * time.Millisecond))
	return json.NewEncoder(conn).Encode(msg)
}

// IsDaemonAlive checks if the daemon is currently responding to Unix socket connection.
func IsDaemonAlive() bool {
	conn, err := net.DialTimeout("unix", socketPath(), 50*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func socketPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".forge", "forge.sock")
}
