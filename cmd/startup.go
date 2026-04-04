package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type daemonStartupResult struct {
	started bool
	logPath string
	cleanup []string
}

func ensureDaemonRunning(skipParentMonitor bool) (daemonStartupResult, error) {
	if addr := readAddr(); addr != "" && isDaemonAlive(addr) {
		return daemonStartupResult{started: false}, nil
	}

	startupLock, err := acquireStartupLock()
	if err != nil {
		if waitErr := waitForDaemonReady(3 * time.Second); waitErr == nil {
			return daemonStartupResult{started: false}, nil
		}
		return daemonStartupResult{}, err
	}
	defer startupLock.Close()
	defer cleanStartupLock()

	if addr := readAddr(); addr != "" && isDaemonAlive(addr) {
		return daemonStartupResult{started: false}, nil
	}

	cleanup := cleanStaleDaemonState()

	forgeDir := filepath.Join(forgeHome(), ".forge")
	_ = os.MkdirAll(forgeDir, 0o700)
	logPath := filepath.Join(forgeDir, "daemon.log")
	cmd, closeLog := newDaemonCommand(logPath, skipParentMonitor)
	defer closeLog()

	if err := startBackground(cmd); err != nil {
		return daemonStartupResult{logPath: logPath, cleanup: cleanup}, err
	}
	if err := waitForDaemonReady(3 * time.Second); err != nil {
		return daemonStartupResult{logPath: logPath, cleanup: cleanup}, err
	}

	return daemonStartupResult{
		started: true,
		logPath: logPath,
		cleanup: cleanup,
	}, nil
}

func cleanStaleDaemonState() []string {
	var actions []string

	if addr := readAddr(); addr != "" && !isDaemonAlive(addr) {
		cleanAddr()
		actions = append(actions, "daemon address")
	}
	if pid := readPID(); pid > 0 && !isProcessAlive(pid) {
		cleanPID()
		actions = append(actions, "daemon PID")
	}
	if isStaleLock() {
		cleanLock()
		actions = append(actions, "daemon lock")
	}
	if isStaleStartupLock() {
		cleanStartupLock()
		actions = append(actions, "startup lock")
	}
	if isStaleSocket() {
		cleanSocket()
		actions = append(actions, "daemon socket")
	}

	return actions
}

func waitForDaemonReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
		if addr := readAddr(); addr != "" && isDaemonAlive(addr) {
			return nil
		}
	}
	return fmt.Errorf("daemon process exited immediately — not responding")
}
