package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/forge/forge/internal/agent"
)

func repairServiceIfNeeded() error {
	if runtime.GOOS == "windows" {
		return nil
	}
	mgr, err := serviceNew()
	if err != nil {
		return err
	}
	storedPath, err := mgr.ReadInstalledBinaryPath()
	if err != nil {
		return fmt.Errorf("read installed binary path: %w", err)
	}
	if storedPath == "" || storedPath == mgr.BinaryPath {
		return nil
	}
	_ = serviceUninstall(mgr)
	if err := serviceInstall(mgr); err != nil {
		return fmt.Errorf("reinstall service: %w", err)
	}
	_ = mgr.Stop()
	_ = mgr.Start()
	fmt.Printf("  Repaired service: updated binary path to %s\n", mgr.BinaryPath)
	return nil
}

func runStart(args []string) {
	fmt.Println("Starting Forge daemon...")

	home, _ := os.UserHomeDir()
	checkAndRepairIntegrationPaths(home)
	warnEnvVsConfig()

	if err := repairServiceIfNeeded(); err != nil {
		fmt.Fprintf(os.Stderr, "  Warning: could not check service registration: %v\n", err)
	}

	result, err := ensureDaemonRunning(true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error starting daemon: %v\n", err)
		fmt.Fprintln(os.Stderr, "\n  Running diagnostics...")
		runDoctorInline()
		fmt.Fprintln(os.Stderr, "\n  Run 'forge doctor --repair' to auto-fix stale state.")
		os.Exit(1)
	}
	for _, item := range result.cleanup {
		fmt.Printf("  Cleaning stale %s...\n", item)
	}
	if !result.started {
		fmt.Println("  Daemon already running.")
		return
	}
	fmt.Println("  Daemon started.")
}

func newDaemonCommand(logPath string, skipParentMonitor bool) (*exec.Cmd, func()) {
	cmd := exec.Command(agent.ForgePath(), "daemon")
	cmd.Stdin = nil
	cmd.Env = filteredEnv("FORGE_NO_EXIT_ON_PARENT_EXIT")
	if skipParentMonitor {
		cmd.Env = append(cmd.Env, "FORGE_NO_EXIT_ON_PARENT_EXIT=1")
	}

	closeLog := func() {}
	if lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600); err == nil {
		cmd.Stdout = lf
		cmd.Stderr = lf
		closeLog = func() { lf.Close() }
	}
	return cmd, closeLog
}

func filteredEnv(keys ...string) []string {
	out := os.Environ()
	skip := map[string]bool{}
	for _, k := range keys {
		skip[k] = true
	}
	filtered := make([]string, 0, len(out))
	for _, e := range out {
		skipIt := false
		for k := range skip {
			if strings.HasPrefix(e, k+"=") {
				skipIt = true
				break
			}
		}
		if !skipIt {
			filtered = append(filtered, e)
		}
	}
	return filtered
}
