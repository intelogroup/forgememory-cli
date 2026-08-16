package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/forge/forge/internal/agent"
	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/distill"
	"github.com/forge/forge/internal/security"
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
		if lockPath := distillLockPath(); pathExists(lockPath) && isDistillLockStale(lockPath) {
			fmt.Println("  - Removing stale distill lock...")
			_ = os.Remove(lockPath)
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

	printKeychainStatus()

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

		// Run SQLite integrity check
		var integrity string
		if err := database.Conn().QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil {
			fmt.Printf("  [FAIL] Database integrity query: %v\n", err)
		} else if integrity != "ok" {
			fmt.Printf("  [FAIL] Database integrity check failed: %s\n", integrity)
		} else {
			fmt.Println("  [OK] Database integrity check: ok")
		}

		// Check database file size
		if info, err := os.Stat(database.Path); err == nil {
			fmt.Printf("  [OK] Database file size: %d bytes (%s)\n", info.Size(), database.Path)
		}

		printDistillationHealth(database, undistilled)

		database.Close()
	}

	printDistillLockStatus()

	// Check LLM Provider connection
	fmt.Println("  Checking LLM provider connectivity...")
	cfg := distill.LoadConfig()
	if cfg.Provider == "" {
		fmt.Println("  [FAIL] LLM Provider: no provider configured (run 'forge config' first)")
	} else {
		fmt.Printf("  [OK] LLM Provider: %s (model: %s)\n", cfg.Provider, cfg.Model)
		// Doctor is a diagnostic command and must never inherit an unbounded
		// distillation retry budget. A stopped local provider or offline host
		// should produce a useful failure quickly, not hold the CLI/test runner
		// for minutes while retry backoff sleeps.
		cfg.Timeout = 5 * time.Second
		cfg.OllamaTimeout = 5 * time.Second
		cfg.Retries = 0
		d := distill.New(nil, cfg)
		fmt.Print("    Testing connection (sending test token)... ")
		response, err := d.CallLLM("respond with only the word OK")
		if err != nil {
			fmt.Printf("\n    [FAIL] LLM Connection: %v\n", err)
		} else {
			fmt.Printf("Success! Response: %q\n", strings.TrimSpace(response))
			fmt.Println("    [OK] LLM Connection: verified")
		}
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
	if ref, stale := findStaleCodexMCPConfig(home); stale {
		fmt.Println("  [FAIL] Codex MCP: configured executable does not resolve to the active Forge binary")
		fmt.Printf("         Path: %s (%s)\n", ref.value, ref.path)
		fmt.Println("         Repair: forge sync-integrations -y")
	}

	// Check agents
	agents := agent.DetectAgents(home)
	fmt.Printf("  [OK] Agents detected: %d\n", len(agents))
	for _, a := range agents {
		fmt.Printf("    - %s\n", a)
	}

	// Check for source/package version skew. A "dev" version means this
	// binary was built directly from source (e.g. `go build ./cmd/`)
	// without going through the release process, so it can expose commands
	// or behavior newer than whatever is published to npm — a source tree
	// can show a command (e.g. `coach`) that an older installed npm binary
	// doesn't have yet (issue #43).
	if version == "dev" {
		fmt.Println("  [WARN] Build: this is an unreleased dev build (no version tag) — its command set may differ from the npm-installed forge on PATH.")
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
	if ref, stale := findStaleCodexMCPConfig(home); stale {
		fmt.Printf("  Codex MCP: configured executable does not resolve to the active Forge binary (%s: %s)\n", ref.path, ref.value)
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

// printKeychainStatus reports whether the HMAC signing key is protected by
// the OS keychain (Keychain/Credential Manager/Secret Service) or has fallen
// back to a plain file under ~/.forge. The fallback is functionally fine —
// principles still get signed — but it silently weakens what that signature
// protects against: a local attacker who can read forge.db can, in the
// fallback case, also read the key sitting right next to it. The daemon
// already logs this once (internal/db/principles.go), but a log line most
// users never read isn't a security warning — this makes it visible in the
// tool users actually run to check on Forge (issue #43).
// signingKeyStatusOnce guards the actual GetOrCreateKey() call. A blocked or
// absent OS keychain backend (e.g. no D-Bus Secret Service) can leave the
// underlying probe goroutine running past its own timeout — go-keyring's
// doc comment on GetOrCreateKey calls this cost "bounded by sync.Once",
// which only holds if every caller in a process shares one Once, not one
// per call. forge doctor is normally its own fresh process each run, so
// this doesn't change production behavior, but it matters a lot when many
// tests call runDoctor in the same test binary.
var (
	signingKeyStatusOnce   sync.Once
	signingKeyUsedFallback bool
	signingKeyErr          error
)

func printKeychainStatus() {
	if security.Disabled() {
		fmt.Println("[OFF] Signing key: disabled (FORGE_DISABLE_KEY set) — principles stored unsigned & unencrypted")
		return
	}
	signingKeyStatusOnce.Do(func() {
		_, signingKeyUsedFallback, signingKeyErr = security.GetOrCreateKey()
	})
	fmt.Print("  ")
	usedFallback, err := signingKeyUsedFallback, signingKeyErr
	switch {
	case err != nil:
		fmt.Printf("[FAIL] Signing key: unavailable — principles will be stored unsigned: %v\n", err)
	case usedFallback:
		fmt.Println("[WARN] Signing key: OS keychain unavailable — HMAC key stored in ~/.forge/forge.key instead")
		fmt.Println("         Signatures only protect against tampering that doesn't also read that file.")
		fmt.Println("         On headless Linux, install a Secret Service provider (gnome-keyring, kwallet, or keyctl) to restore keychain protection.")
	default:
		fmt.Println("[OK] Signing key: protected by OS keychain")
	}
}

// printDistillationHealth reports the distillation scheduler's health
// prominently in `forge doctor` output — including the wedged state
// (issue #33/#43): 3+ consecutive skips behind a stale/leaked distill lock
// write a distinct failure record, but `forge status`'s compact view only
// showed the bare LastStatus/count, not the message explaining why or what
// to do about it.
func printDistillationHealth(database *db.DB, undistilled int) {
	h, err := database.GetDistillationHealth()
	if err != nil {
		return
	}
	switch {
	case h.ConsecutiveFailures >= 3:
		fmt.Printf("  [FAIL] Distillation: wedged after %d consecutive failures — %s\n", h.ConsecutiveFailures, h.LastErrorMessage)
		fmt.Printf("         %d events undistilled. Repair: forge doctor --repair, or restart the daemon.\n", undistilled)
	case h.LastStatus == "failed":
		fmt.Printf("  [WARN] Distillation: last attempt failed — %s\n", h.LastErrorMessage)
	case h.LastStatus == "" || h.LastStatus == "pending":
		// No distillation has run yet — nothing to report.
	default:
		fmt.Printf("  [OK] Distillation: %s\n", h.LastStatus)
	}
}

// printDistillLockStatus flags a distill lock file left behind by a
// stale/leaked process. acquireDistillLock() already reclaims this
// automatically on the next distill attempt, so this is a visibility check,
// not a required repair step — but a stuck lock explains why events are
// piling up undistilled right now, so it's worth surfacing directly.
func printDistillLockStatus() {
	lockPath := distillLockPath()
	if !pathExists(lockPath) {
		return
	}
	if isDistillLockStale(lockPath) {
		fmt.Printf("  [FAIL] Distillation lock: stale, held by a dead/expired process (%s)\n", lockPath)
		fmt.Println("         Repair: forge doctor --repair, or restart the daemon.")
	} else {
		fmt.Printf("  [OK] Distillation lock: held by a live process (%s)\n", lockPath)
	}
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
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

// findStaleCodexMCPConfig checks whether Codex's native MCP registration
// (~/.codex/config.toml, managed by `codex mcp add`/`codex mcp get`) still
// points at the active Forge binary. Unlike findTransientIntegrationRefs
// (which flags known-ephemeral path substrings in Forge-managed files),
// config.toml is Codex's own file and can reference an install path that
// simply no longer exists on disk, so this compares against the current
// binary directly — the same check checkAndRepairIntegrationPaths uses to
// decide whether a repair is needed.
func findStaleCodexMCPConfig(home string) (transientIntegrationRef, bool) {
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	configPath := filepath.Join(codexHome, "config.toml")

	cmdPath, ok := codexMCPForgeCommand(configPath)
	if !ok {
		return transientIntegrationRef{}, false
	}

	currentPath := agent.ForgePath()
	currentReal := resolveRealPath(currentPath)
	currentHash := fileSHA256(currentPath)
	if isEquivalentBinary(cmdPath, currentPath, currentReal, currentHash) {
		return transientIntegrationRef{}, false
	}
	return transientIntegrationRef{path: configPath, value: truncate(cmdPath, 120)}, true
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
