package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/forge/forge/internal/agent"
	"github.com/forge/forge/internal/db"
)

func runDoctor(args []string) {
	repair := len(args) > 0 && args[0] == "--repair"

	fmt.Println("Forge Doctor")
	fmt.Println()

	home := forgeHome()

	// Repair mode: clean up stale daemon state
	if repair {
		fmt.Println("Repairing stale daemon state...")
		addr := readAddr()
		pid := readPID()
		hasIssues := false

		if addr != "" && !isDaemonAlive(addr) {
			fmt.Println("  - Removing stale address file...")
			cleanAddr()
			hasIssues = true
		}
		if pid > 0 && !isProcessAlive(pid) {
			fmt.Println("  - Removing stale PID file...")
			cleanPID()
			hasIssues = true
		}
		if isStaleLock() {
			fmt.Println("  - Removing stale lock file...")
			cleanLock()
			hasIssues = true
		}
		if isStaleStartupLock() {
			fmt.Println("  - Removing stale startup lock file...")
			cleanStartupLock()
			hasIssues = true
		}
		if isStaleSocket() {
			fmt.Println("  - Removing stale socket file...")
			cleanSocket()
			hasIssues = true
		}

		// Remove stale forge binaries at managed paths.
		activeInstallPath := agent.ForgePath()
		activeReal, _ := filepath.EvalSymlinks(activeInstallPath)
		if activeReal == "" {
			activeReal = activeInstallPath
		}
		activeVersion := queryForgeVersion(activeInstallPath)
		for _, fi := range findForgeInstalls() {
			fiReal, _ := filepath.EvalSymlinks(fi.Path)
			if fiReal == "" {
				fiReal = fi.Path
			}
			if fiReal == activeReal {
				continue // never remove the active binary
			}
			if !isManagedForgePath(fi.Path) {
				fmt.Printf("  [SKIP] %s — not a managed path, remove manually if needed\n", fi.Path)
				continue
			}
			if fi.Version == activeVersion {
				fmt.Printf("  [SKIP] %s — same version as active (v%s)\n", fi.Path, fi.Version)
				continue
			}
			if err := os.Remove(fi.Path); err != nil {
				fmt.Printf("  [FAIL] Could not remove %s: %v\n", fi.Path, err)
			} else {
				fmt.Printf("  - Removed stale binary: %s (v%s)\n", fi.Path, fi.Version)
				hasIssues = true
			}
		}

		if !hasIssues {
			fmt.Println("  No stale state found.")
		}
		fmt.Println("  Repair complete.")
		fmt.Println()

		addr = readAddr()
		if addr == "" || !isDaemonAlive(addr) {
			result, err := ensureDaemonRunning(true)
			if err != nil {
				fmt.Printf("  Warning: could not restart daemon: %v\n", err)
			} else if result.started {
				fmt.Println("  Daemon restarted.")
			}
		}
	}

	// Check forge home writability
	fmt.Print("  ")
	if err := probeForgeDirWritable(); err != nil {
		fmt.Printf("[FAIL] Forge home: cannot write %s: %v\n", forgeDataDir(), err)
	} else {
		fmt.Printf("[OK] Forge home: writable (%s)\n", forgeDataDir())
	}

	// Check database
	fmt.Print("  ")
	database, err := db.Open("")
	if err != nil {
		fmt.Printf("[FAIL] Database: %v\n", err)
	} else {
		total, undistilled, _ := database.EventCount()
		principles, _ := database.PrincipleCount()
		sessions, _ := database.SessionSummaryCount()
		fmt.Printf("[OK] Database: %d events, %d undistilled, %d principles, %d sessions\n",
			total, undistilled, principles, sessions)
		database.Close()
	}

	// Check daemon
	fmt.Print("  ")
	addr := readAddr()
	pid := readPID()
	if addr == "" {
		fmt.Println("[FAIL] Daemon: not running")
	} else if isDaemonAlive(addr) {
		fmt.Printf("[OK] Daemon: running (%s)\n", addr)
	} else {
		fmt.Printf("[FAIL] Daemon: stale addr file (%s) — daemon not responding\n", addr)
	}
	if lockPID, identity, foreign := lockOwnerIdentity(daemonLockPath()); foreign {
		fmt.Printf("  [FAIL] Daemon lock: lock owned by non-Forge process (pid %d: %s)\n", lockPID, identity)
	}
	if isStaleSocket() && (pid <= 0 || !isProcessAlive(pid)) {
		fmt.Printf("  [FAIL] Daemon socket: socket exists but no daemon process (%s)\n", filepath.Join(forgeDataDir(), "forge.sock"))
	}
	logPath := filepath.Join(forgeHome(), ".forge", "daemon.log")
	if info, err := os.Stat(logPath); err == nil {
		fmt.Printf("  [OK] Daemon log: %s (%d bytes)\n", logPath, info.Size())
	}

	for _, ref := range findTransientIntegrationRefs(home) {
		fmt.Printf("  [FAIL] Integration: transient Forge path in %s (%s)\n", ref.path, ref.value)
	}

	// Check agents
	agents := agent.DetectAgents(home)
	fmt.Printf("  [OK] Agents detected: %d\n", len(agents))
	for _, a := range agents {
		fmt.Printf("    - %s\n", a)
	}

	// Check binary installations
	installs := findForgeInstalls()
	activePath := agent.ForgePath()
	if len(installs) <= 1 {
		fmt.Printf("  [OK] Binary: %s (v%s)\n", activePath, queryForgeVersion(activePath))
	} else {
		fmt.Printf("  [WARN] Multiple forge installations found (%d):\n", len(installs))
		for _, fi := range installs {
			marker := ""
			if fi.Path == activePath {
				marker = " <- active"
			}
			fmt.Printf("    %s  (v%s)%s\n", fi.Path, fi.Version, marker)
		}
		fmt.Println("  Run 'forge doctor --repair' to remove stale installations.")
	}
}

// runDoctorInline prints a compact diagnostic output for failed daemon starts.
// This is shown inline when the daemon fails to start, so users see what's wrong immediately.
func runDoctorInline() {
	home := forgeHome()

	fmt.Println("  --- Diagnostics ---")

	if err := probeForgeDirWritable(); err != nil {
		fmt.Printf("  Forge home: [FAIL] cannot write %s: %v\n", forgeDataDir(), err)
	}

	// Check database
	database, err := db.Open("")
	if err != nil {
		fmt.Printf("  Database: [FAIL] %v\n", err)
	} else {
		total, _, _ := database.EventCount()
		fmt.Printf("  Database: [OK] %d events\n", total)
		database.Close()
	}

	// Check daemon state
	addr := readAddr()
	pid := readPID()
	if addr == "" && pid == 0 {
		fmt.Println("  Daemon: no state files (clean slate)")
	} else if addr != "" && !isDaemonAlive(addr) {
		fmt.Printf("  Daemon: stale address %s\n", addr)
	}
	if pid > 0 && !isProcessAlive(pid) {
		fmt.Printf("  Daemon: stale PID %d\n", pid)
	}
	if isStaleLock() {
		fmt.Println("  Daemon: stale lock file")
	}
	if lockPID, identity, foreign := lockOwnerIdentity(daemonLockPath()); foreign {
		fmt.Printf("  Daemon lock: owned by non-Forge process (pid %d: %s)\n", lockPID, identity)
	}
	if isStaleStartupLock() {
		fmt.Println("  Daemon: stale startup lock file")
	}
	if isStaleSocket() {
		fmt.Println("  Daemon: stale socket file")
	}
	if isStaleSocket() && (pid <= 0 || !isProcessAlive(pid)) {
		fmt.Println("  Daemon: socket exists but no daemon process")
	}

	// Check config
	configPath := filepath.Join(forgeHome(), ".forge", "config")
	if _, err := os.Stat(configPath); err == nil {
		fmt.Println("  Config: [OK] ~/.forge/config exists")
	} else {
		fmt.Println("  Config: [WARN] no config file (run 'forge config' to set up provider)")
	}

	// Check agents
	agents := agent.DetectAgents(home)
	if len(agents) == 0 {
		fmt.Println("  Agents: none detected")
	} else {
		fmt.Printf("  Agents: %v\n", agents)
	}

	for _, ref := range findTransientIntegrationRefs(home) {
		fmt.Printf("  Integration: transient Forge path in %s (%s)\n", ref.path, ref.value)
	}

	// Check log
	logPath := filepath.Join(forgeHome(), ".forge", "daemon.log")
	if info, err := os.Stat(logPath); err == nil {
		if info.Size() > 0 {
			logSize := int(info.Size())
			if logSize > 500 {
				logSize = 500
			}
			fmt.Printf("  Log: last %d bytes:\n", logSize)
			if data, err := os.ReadFile(logPath); err == nil {
				lines := strings.Split(string(data), "\n")
				start := max(0, len(lines)-5)
				for _, line := range lines[start:] {
					if line != "" {
						fmt.Printf("    | %s\n", truncate(line, 80))
					}
				}
			}
		}
	}
}

func forgeDataDir() string {
	return filepath.Join(forgeHome(), ".forge")
}

func probeForgeDirWritable() error {
	dir := forgeDataDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	probe := filepath.Join(dir, ".doctor-write-probe")
	if err := os.WriteFile(probe, []byte("ok"), 0600); err != nil {
		return err
	}
	return os.Remove(probe)
}

type transientIntegrationRef struct {
	path  string
	value string
}

func findTransientIntegrationRefs(home string) []transientIntegrationRef {
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}

	candidates := []string{
		filepath.Join(home, ".claude", "settings.json"),
		filepath.Join(home, ".gemini", "settings.json"),
		filepath.Join(codexHome, "skills", "forge.json"),
	}

	var refs []transientIntegrationRef
	for _, path := range candidates {
		value, ok := findTransientForgeReference(path)
		if !ok {
			continue
		}
		refs = append(refs, transientIntegrationRef{
			path:  path,
			value: truncate(value, 120),
		})
	}
	return refs
}

func findTransientForgeReference(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}

	var decoded any
	if err := json.Unmarshal(data, &decoded); err == nil {
		if ref := transientForgeString(decoded); ref != "" {
			return ref, true
		}
	}

	text := string(data)
	normalizedText := normalizePathLikeString(text)
	for _, marker := range []string{"/go-build", "/forge-bin-"} {
		if idx := strings.Index(normalizedText, marker); idx >= 0 {
			start := idx
			for start > 0 && text[start-1] != '"' && text[start-1] != '\n' {
				start--
			}
			end := idx
			for end < len(text) && text[end] != '"' && text[end] != '\n' {
				end++
			}
			return text[start:end], true
		}
	}

	return "", false
}

func transientForgeString(v any) string {
	switch x := v.(type) {
	case string:
		normalized := normalizePathLikeString(x)
		if strings.Contains(normalized, "/go-build") ||
			strings.Contains(normalized, "/forge-bin-") {
			return x
		}
	case map[string]any:
		for _, value := range x {
			if ref := transientForgeString(value); ref != "" {
				return ref
			}
		}
	case []any:
		for _, value := range x {
			if ref := transientForgeString(value); ref != "" {
				return ref
			}
		}
	}
	return ""
}

func normalizePathLikeString(s string) string {
	return strings.ToLower(strings.ReplaceAll(s, "\\", "/"))
}
