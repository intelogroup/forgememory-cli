package main

import (
	"fmt"
	"os"
)

func runStop(args []string) {
	fmt.Println("Stopping Forge daemon...")

	// Stop via the OS service manager first when Forge is installed as a
	// launchd/systemd/Windows service. Signaling the raw PID alone (below)
	// is not enough there: launchd's KeepAlive (even scoped to
	// SuccessfulExit=false) and systemd's Restart=on-failure both treat an
	// unexpected SIGTERM as a crash and can race to relaunch the daemon
	// behind this command's back, so `forge stop` appears to work while the
	// daemon comes right back seconds later (issue #51). launchctl
	// unload/systemctl stop tell the supervisor itself to stand down, so it
	// won't respawn what it just stopped.
	if mgr, err := serviceNew(); err == nil && serviceIsInstalled(mgr) {
		if err := serviceStop(mgr); err != nil {
			fmt.Fprintf(os.Stderr, "  Warning: could not stop service: %v\n", err)
		}
	}

	pid := readPID()

	// Kill the tracked PID directly too. This is not redundant with the
	// service stop above: it's the only path for a daemon started manually
	// (no service installed) or started outside the service's supervision,
	// and it's a safe no-op against a PID the service stop already reaped.
	if pid > 0 {
		if err := killProcess(pid); err != nil {
			fmt.Fprintf(os.Stderr, "  Warning: could not kill daemon (pid %d): %v\n", pid, err)
		}
	}

	// Clean up daemon state regardless of whether the process was alive.
	// This handles both normal shutdown and the stale state case where
	// the process crashed/force-killed but left behind addr/pid/lock files.
	cleanAddr()
	cleanPID()
	cleanLock()
	cleanSocket()
	fmt.Println("  Daemon stopped.")
}
