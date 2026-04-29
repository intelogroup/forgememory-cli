package main

import (
	"fmt"
	"os"
)

func runStop(args []string) {
	fmt.Println("Stopping Forge daemon...")

	pid := readPID()

	// Try to kill the daemon process if it's running
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
