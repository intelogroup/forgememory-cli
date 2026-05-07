package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/forge/forge/internal/config"
	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/detect"
	"github.com/forge/forge/internal/distill"
	"github.com/forge/forge/internal/ipc"
	"github.com/forge/forge/internal/retrieve"
)

// runDaemon is the entrypoint for `forge daemon`.
func runDaemon(args []string) {
	// Load config at startup so daemon has the correct provider settings
	// independent of shell environment.
	cfg, err := config.Load()
	if err == nil {
		// Apply config to environment for distill to pick up
		if cfg.Provider != "" {
			os.Setenv("FORGE_PROVIDER", cfg.Provider)
		}
		if cfg.APIKey != "" {
			os.Setenv("FORGE_API_KEY", cfg.APIKey)
		}
		if cfg.Model != "" {
			os.Setenv("FORGE_MODEL", cfg.Model)
		}
		if cfg.BaseURL != "" {
			os.Setenv("FORGE_BASE_URL", cfg.BaseURL)
			os.Setenv("FORGE_API_URL", cfg.BaseURL) // legacy compatibility
		}
		if cfg.Timeout != "" {
			os.Setenv("FORGE_TIMEOUT", cfg.Timeout)
		}
		if cfg.Retries > 0 {
			os.Setenv("FORGE_RETRIES", strconv.Itoa(cfg.Retries))
		}
		if cfg.DistillInterval != "" {
			os.Setenv("FORGE_DISTILL_INTERVAL", cfg.DistillInterval)
		}
		if cfg.OllamaTimeout != "" {
			os.Setenv("FORGE_OLLAMA_TIMEOUT", cfg.OllamaTimeout)
		}
		if cfg.OllamaStartupWait != "" {
			os.Setenv("FORGE_OLLAMA_STARTUP_WAIT", cfg.OllamaStartupWait)
		}
		log.Printf("Loaded config: provider=%s", cfg.Provider)
	} else {
		log.Printf("No config found, using defaults (provider: forgememo)")
	}

	// Acquire exclusive lock to prevent multiple daemons
	lockFile, err := acquireDaemonLock()
	if err != nil {
		log.Fatalf("Failed to acquire daemon lock: %v", err)
	}
	if lockFile != nil {
		defer lockFile.Close()
		defer os.Remove(lockFile.Name())
	}

	// Open database
	database, err := db.Open("")
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer database.Close()

	// Get the parent PID before we daemonize (if started via forge start/mcp)
	// The daemon should exit when its parent dies to avoid orphaned processes.
	// Skip parent monitoring if FORGE_NO_EXIT_ON_PARENT_EXIT is set - this is
	// set by `forge start` so the daemon doesn't exit when the CLI exits.
	parentPID := os.Getppid()
	skipParentMonitor := os.Getenv("FORGE_NO_EXIT_ON_PARENT_EXIT") == "1"

	// Listen for hook events
	ln, addr, err := ipc.Listen()
	if err != nil {
		log.Fatalf("Failed to create IPC listener: %v", err)
	}
	defer ln.Close()

	// Write pipe address and PID for hooks and stop command
	writeAddr(addr)
	writePID(os.Getpid())

	log.Printf("Forge daemon listening on %s (pid %d)", addr, os.Getpid())
	log.Printf("Database: %s", database.Path)

	// Graceful shutdown
	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(shutdown)

	stop := make(chan struct{})
	var stopOnce sync.Once
	requestStop := func() {
		stopOnce.Do(func() {
			close(stop)
		})
	}

	go func() {
		<-shutdown
		requestStop()
	}()

	// Parent process monitor - exit when parent dies to avoid orphaned daemons.
	// Only monitor if parent is not init (PID 1) - this means we were started
	// by a specific process (like forge mcp) rather than as a system service.
	if parentPID > 1 && !skipParentMonitor {
		go func() {
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-stop:
					return
				case <-ticker.C:
					// Check if parent is still alive
					proc, err := os.FindProcess(parentPID)
					if err != nil {
						// Parent process gone, exit
						log.Printf("Parent process (pid %d) gone, shutting down", parentPID)
						requestStop()
						_ = ln.Close()
						return
					}
					// On Unix, we need to signal(0) to check if process exists
					if runtime.GOOS != "windows" {
						err := proc.Signal(syscall.Signal(0))
						if err != nil {
							log.Printf("Parent process (pid %d) gone, shutting down", parentPID)
							requestStop()
							_ = ln.Close()
							return
						}
					}
				}
			}
		}()
	}

	// Accept connections
	var wg sync.WaitGroup
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				if errors.Is(err, net.ErrClosed) || strings.Contains(err.Error(), "use of closed network connection") {
					return
				}
				select {
				case <-stop:
					return
				default:
					log.Printf("Accept error: %v", err)
					continue
				}
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				handleConn(conn, database)
			}()
		}
	}()

	retrievalWake := make(chan struct{}, 1)
	retrieve.SetImmediateWakeChannel(retrievalWake)
	defer retrieve.SetImmediateWakeChannel(nil)
	distillCfg := distill.LoadConfig()
	go distillLoop(distill.New(database, distillCfg), database, distillCfg.DistillInterval)
	go retrievalLoop(retrieve.NewWorker(database), retrievalWake, stop)

	<-stop
	log.Println("Shutting down...")
	_ = ln.Close()
	wg.Wait()
	cleanAddr()
	cleanPID()
	cleanLock()
	log.Println("Forge daemon stopped.")
}

func handleConn(conn net.Conn, database *db.DB) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	decoder := json.NewDecoder(conn)

	// Decode into a generic map first to inspect the type field.
	var raw map[string]json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return
	}

	// Determine message type (default: event).
	msgType := "event"
	if t, ok := raw["type"]; ok {
		var s string
		if err := json.Unmarshal(t, &s); err == nil && s != "" {
			msgType = s
		}
	}

	switch msgType {
	case "event":
		handleEventMsg(raw, database)
	// "query" type reserved for future bidirectional IPC
	default:
		handleEventMsg(raw, database)
	}
}

func handleEventMsg(raw map[string]json.RawMessage, database *db.DB) {
	extract := func(key string) string {
		v, ok := raw[key]
		if !ok {
			return ""
		}
		var s string
		json.Unmarshal(v, &s)
		return s
	}

	event := &db.Event{
		ID:         extract("id"),
		TS:         extract("ts"),
		SessionID:  extract("session_id"),
		ProjectID:  extract("project_id"),
		SourceTool: extract("source_tool"),
		EventType:  extract("event_type"),
		ToolName:   extract("tool_name"),
		Payload:    extract("payload"),
	}

	if event.SessionID == "" {
		event.SessionID = "unknown"
	}
	if event.ProjectID == "" {
		event.ProjectID = "unknown"
	}

	if err := database.InsertEvent(event); err != nil {
		log.Printf("Insert event error: %v", err)
		return
	}
	if err := detect.ProcessEvent(database, event); err != nil {
		log.Printf("Failure detection error: %v", err)
	}
}

const distillEventThreshold = 300

// distillBackoff returns the minimum interval that must elapse since the last
// failure before another distill attempt is allowed, given consecutiveFailures.
// Exponential: 1m, 2m, 4m, 8m, 16m, capped at 30m. The first failure does not
// add backoff (returns 0) — recovery from transient errors should be fast.
func distillBackoff(consecutiveFailures int) time.Duration {
	if consecutiveFailures <= 1 {
		return 0
	}
	exp := math.Pow(2, float64(consecutiveFailures-2))
	if exp > 30 {
		exp = 30
	}
	return time.Duration(exp) * time.Minute
}

func distillLoop(d *distill.Distiller, database *db.DB, interval time.Duration) {
	pollInterval := 60 * time.Second
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for range ticker.C {
		_, undistilled, _ := database.EventCount()
		if undistilled < distillEventThreshold {
			continue
		}

		// Apply exponential backoff after consecutive failures so we don't
		// hammer a misconfigured provider every minute. Without this, a stale
		// API key produces thousands of consecutive failures before anyone
		// notices.
		if h, hErr := database.GetDistillationHealth(); hErr == nil && h.ConsecutiveFailures > 0 {
			backoff := distillBackoff(h.ConsecutiveFailures)
			if backoff > 0 {
				if last, parseErr := time.Parse(time.RFC3339, h.LastErrorAt); parseErr == nil && time.Since(last) < backoff {
					continue
				}
			}
		}

		// Skip if a manual `forge distill` is already running.
		lock, lockErr := acquireDistillLock()
		if lockErr != nil {
			log.Printf("Distillation skipped: could not acquire lock: %v", lockErr)
			continue
		}
		if lock == nil {
			log.Printf("Distillation skipped: manual distill already in progress")
			continue
		}

		runAt := time.Now().UTC()
		start := time.Now()
		count, err := d.DistillBatch(300)
		lock.Close()
		cleanDistillLock()

		next := runAt.Add(pollInterval)
		if err != nil {
			// Annotate the error with the new failure count + backoff so
			// `forge status` and the get_alerts MCP tool show what's happening.
			h, _ := database.GetDistillationHealth()
			nextFailures := h.ConsecutiveFailures + 1
			backoff := distillBackoff(nextFailures)
			if backoff > 0 {
				next = runAt.Add(backoff)
			}
			msg := err.Error()
			if nextFailures >= 3 {
				msg = fmt.Sprintf("%s (%d consecutive failures, next retry in %s)", msg, nextFailures, backoff)
				log.Printf("Distillation error (CRITICAL — %d consecutive): %v", nextFailures, err)
			} else {
				log.Printf("Distillation error: %v", err)
			}
			if recErr := database.RecordDistillationFailure(runAt, time.Since(start), undistilled, msg, next); recErr != nil {
				log.Printf("Distillation health record error: %v", recErr)
			}
		} else if count > 0 {
			log.Printf("Distilled %d principles from %d events", count, undistilled)
			if recErr := database.RecordDistillationSuccess(runAt, time.Since(start), undistilled, count, next); recErr != nil {
				log.Printf("Distillation health record error: %v", recErr)
			}
		} else {
			if recErr := database.RecordDistillationSuccess(runAt, time.Since(start), undistilled, 0, next); recErr != nil {
				log.Printf("Distillation health record error: %v", recErr)
			}
		}
	}
}

func retrievalLoop(worker *retrieve.Worker, wake <-chan struct{}, stop <-chan struct{}) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	runBatch := func() {
		for i := 0; i < 3; i++ {
			count, err := worker.RunOnce()
			if err != nil {
				log.Printf("Retrieval error: %v", err)
				break
			}
			if count == 0 {
				break
			}
			log.Printf("Processed %d retrieval job", count)
		}
	}
	runBatch()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			runBatch()
		case <-wake:
			runBatch()
		}
	}
}

// forgeHome returns the home directory for forge data files.
// Checks the HOME env var first so that tests can override it via t.Setenv —
// needed on Windows where os.UserHomeDir() ignores HOME and reads USERPROFILE.
func forgeHome() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	home, _ := os.UserHomeDir()
	return home
}

func writeAddr(addr string) {
	home := forgeHome()
	dir := filepath.Join(home, ".forge")
	_ = os.MkdirAll(dir, 0o700)
	finalPath := filepath.Join(dir, "forge.addr")
	tmp, err := os.CreateTemp(dir, "forge.addr.tmp.*")
	if err != nil {
		_ = os.WriteFile(finalPath, []byte(addr), 0o600)
		os.Setenv("FORGE_PIPE_ADDR", addr)
		return
	}
	if _, werr := tmp.WriteString(addr); werr != nil {
		tmp.Close()
		_ = os.Remove(tmp.Name())
		_ = os.WriteFile(finalPath, []byte(addr), 0o600)
		os.Setenv("FORGE_PIPE_ADDR", addr)
		return
	}
	_ = tmp.Chmod(0o600)
	tmp.Close()
	_ = os.Rename(tmp.Name(), finalPath)
	os.Setenv("FORGE_PIPE_ADDR", addr)
}

func cleanAddr() {
	_ = os.Remove(filepath.Join(forgeHome(), ".forge", "forge.addr"))
}

func readAddr() string {
	data, err := os.ReadFile(filepath.Join(forgeHome(), ".forge", "forge.addr"))
	if err != nil {
		return ""
	}
	return string(data)
}

func writePID(pid int) {
	home := forgeHome()
	dir := filepath.Join(home, ".forge")
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, "forge.pid"), []byte(fmt.Sprintf("%d", pid)), 0o600)
}

func cleanPID() {
	_ = os.Remove(filepath.Join(forgeHome(), ".forge", "forge.pid"))
}

func readPID() int {
	data, err := os.ReadFile(filepath.Join(forgeHome(), ".forge", "forge.pid"))
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return pid
}

func daemonLockPath() string {
	return filepath.Join(forgeHome(), ".forge", "forge.lock")
}

func startupLockPath() string {
	return filepath.Join(forgeHome(), ".forge", "forge.start.lock")
}

func readLockPID(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	line := strings.TrimSpace(string(data))
	if idx := strings.IndexByte(line, '\n'); idx >= 0 {
		line = line[:idx]
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(line))
	return pid
}

func lockOwnerIdentity(lockPath string) (int, string, bool) {
	pid := readLockPID(lockPath)
	if pid <= 0 {
		return pid, "", false
	}
	identity, err := processIdentity(pid)
	if err != nil {
		return pid, "", false
	}
	return pid, identity, !isForgeProcessIdentity(identity)
}

// isDaemonAlive checks whether the daemon behind addr is actually responding.
// Returns false for empty addr or if the socket/port can't be dialed.
func isDaemonAlive(addr string) bool {
	if addr == "" {
		return false
	}
	network := "unix"
	if strings.Contains(addr, ":") {
		network = "tcp"
	}
	conn, err := net.DialTimeout(network, addr, 500*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// isProcessAlive checks whether a process with the given PID exists.
func isProcessAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// On Unix, FindProcess succeeds even for zombie processes.
	// We use syscall.Signal(0) to check if the process is actually alive.
	if runtime.GOOS != "windows" {
		err := proc.Signal(syscall.Signal(0))
		if err == nil {
			return true
		}
		_, psErr := processIdentity(pid)
		return psErr == nil
	}
	// On Windows, FindProcess returns error if process doesn't exist.
	_, psErr := processIdentity(pid)
	return psErr == nil
}

// isStaleLock checks whether the lock file exists but points to a dead process.
func isStaleLock() bool {
	lockPath := daemonLockPath()
	_, err := os.Stat(lockPath)
	if err != nil {
		return false
	}
	pid := readLockPID(lockPath)
	return pid <= 0 || !isProcessAlive(pid)
}

func isStaleStartupLock() bool {
	lockPath := startupLockPath()
	_, err := os.Stat(lockPath)
	if err != nil {
		return false
	}
	return isPIDLockStale(lockPath)
}

// Status output
type statusReport struct {
	SchemaVersion string `json:"schema_version"`
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	Database      string `json:"database"`
	Events        struct {
		Total       int `json:"total"`
		Undistilled int `json:"undistilled"`
	} `json:"events"`
	Principles    int    `json:"principles"`
	Sessions      int    `json:"sessions"`
	Daemon        string `json:"daemon"`
	DaemonAddress string `json:"daemon_address,omitempty"`
	LastDistilled string `json:"last_distilled,omitempty"`
	Distillation  struct {
		LastRunAt            string `json:"last_run_at,omitempty"`
		LastSuccessAt        string `json:"last_success_at,omitempty"`
		LastErrorAt          string `json:"last_error_at,omitempty"`
		LastErrorMessage     string `json:"last_error_message,omitempty"`
		LastStatus           string `json:"last_status"`
		ConsecutiveFailures  int    `json:"consecutive_failures"`
		TotalSuccesses       int    `json:"total_successes"`
		TotalFailures        int    `json:"total_failures"`
		NextScheduledAt      string `json:"next_scheduled_at,omitempty"`
		LastAttemptedEvents  int    `json:"last_attempted_events"`
		LastDistilledResults int    `json:"last_distilled_results"`
	} `json:"distillation"`
}

func loadLastDistilled(database *db.DB) string {
	var ts string
	if err := database.Conn().QueryRow("SELECT ts FROM principles ORDER BY ts DESC LIMIT 1").Scan(&ts); err != nil {
		return ""
	}
	return ts
}

func collectStatusReport() (statusReport, error) {
	report := statusReport{SchemaVersion: "1"}
	database, err := db.Open("")
	if err != nil {
		return report, err
	}
	defer database.Close()

	total, undistilled, _ := database.EventCount()
	principles, _ := database.PrincipleCount()
	sessions, _ := database.SessionSummaryCount()
	addr := readAddr()
	cfg := distill.LoadConfig()

	report.Provider = string(cfg.Provider)
	report.Model = cfg.Model
	report.Database = database.Path
	report.Events.Total = total
	report.Events.Undistilled = undistilled
	report.Principles = principles
	report.Sessions = sessions
	report.LastDistilled = loadLastDistilled(database)
	if h, hErr := database.GetDistillationHealth(); hErr == nil {
		report.Distillation.LastRunAt = h.LastRunAt
		report.Distillation.LastSuccessAt = h.LastSuccessAt
		report.Distillation.LastErrorAt = h.LastErrorAt
		report.Distillation.LastErrorMessage = h.LastErrorMessage
		report.Distillation.LastStatus = h.LastStatus
		report.Distillation.ConsecutiveFailures = h.ConsecutiveFailures
		report.Distillation.TotalSuccesses = h.TotalSuccesses
		report.Distillation.TotalFailures = h.TotalFailures
		report.Distillation.NextScheduledAt = h.NextScheduledAt
		report.Distillation.LastAttemptedEvents = h.LastAttemptedEvents
		report.Distillation.LastDistilledResults = h.LastDistilledPrinciples
	}
	if addr != "" && isDaemonAlive(addr) {
		report.Daemon = "running"
		report.DaemonAddress = addr
	} else if addr != "" {
		report.Daemon = "stale"
		report.DaemonAddress = addr
	} else {
		report.Daemon = "not running"
	}
	return report, nil
}

func formatRelativeTime(ts string) string {
	if ts == "" {
		return "never"
	}
	parsed, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ts
	}
	delta := time.Since(parsed)
	if delta < time.Minute {
		return "just now"
	}
	if delta < time.Hour {
		return fmt.Sprintf("%d mins ago", int(delta.Minutes()))
	}
	if delta < 24*time.Hour {
		return fmt.Sprintf("%d hours ago", int(delta.Hours()))
	}
	return fmt.Sprintf("%d days ago", int(delta.Hours()/24))
}

func statusOutput() {
	report, err := collectStatusReport()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	fmt.Printf("Provider:   %s\n", report.Provider)
	fmt.Printf("Model:      %s\n", report.Model)
	fmt.Printf("Database:   %s\n", report.Database)
	fmt.Printf("Events:     %d (%d undistilled)\n", report.Events.Total, report.Events.Undistilled)
	fmt.Printf("Principles: %d\n", report.Principles)
	fmt.Printf("Sessions:   %d\n", report.Sessions)
	if report.DaemonAddress != "" {
		fmt.Printf("Daemon:     %s (%s)\n", report.Daemon, report.DaemonAddress)
	} else {
		fmt.Printf("Daemon:     %s\n", report.Daemon)
	}
	fmt.Printf("Last distilled: %s\n", formatRelativeTime(report.LastDistilled))
	fmt.Printf("Distillation: %s", report.Distillation.LastStatus)
	if report.Distillation.ConsecutiveFailures > 0 {
		fmt.Printf(" (%d consecutive failures)", report.Distillation.ConsecutiveFailures)
	}
	fmt.Println()
}

func statusOutputJSON() {
	report, err := collectStatusReport()
	if err != nil {
		fmt.Printf("{\"error\":%q}\n", err.Error())
		return
	}
	out, _ := json.MarshalIndent(report, "", "  ")
	fmt.Println(string(out))
}

// acquireDaemonLock creates an exclusive lock file to ensure only one daemon runs.
// Returns nil, nil if no existing lock, or an error if lock cannot be acquired.
func acquireDaemonLock() (*os.File, error) {
	return acquirePIDLock(daemonLockPath(), "daemon already running or stale lock exists")
}

func acquireStartupLock() (*os.File, error) {
	return acquirePIDLock(startupLockPath(), "daemon startup already in progress")
}

func acquirePIDLock(lockPath, alreadyRunningMessage string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, fmt.Errorf("create lock dir: %w", err)
	}

	for attempt := 0; attempt < 2; attempt++ {
		lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			// Write PID to lock file for debugging and liveness checks.
			if _, err := fmt.Fprintf(lockFile, "%d", os.Getpid()); err != nil {
				lockFile.Close()
				_ = os.Remove(lockPath)
				return nil, fmt.Errorf("write lock pid: %w", err)
			}
			lockFile.Close()

			// Re-open for holding the lock.
			lockFile, err = os.OpenFile(lockPath, os.O_WRONLY, 0o600)
			if err != nil {
				return nil, fmt.Errorf("reopen lock: %w", err)
			}
			return lockFile, nil
		}
		if os.IsExist(err) && isPIDLockStale(lockPath) {
			_ = os.Remove(lockPath)
			continue
		}
		if os.IsExist(err) {
			return nil, errors.New(alreadyRunningMessage)
		}
		return nil, fmt.Errorf("acquire lock: %w", err)
	}
	return nil, errors.New(alreadyRunningMessage)
}

func isPIDLockStale(lockPath string) bool {
	pid := readLockPID(lockPath)
	if pid <= 0 || !isProcessAlive(pid) {
		return true
	}
	_, _, foreign := lockOwnerIdentity(lockPath)
	return foreign
}

// cleanLock removes the daemon lock file.
func cleanLock() {
	_ = os.Remove(daemonLockPath())
}

func cleanStartupLock() {
	_ = os.Remove(startupLockPath())
}

func distillLockPath() string {
	return filepath.Join(forgeHome(), ".forge", "forge.distill.lock")
}

// distillInProcess is an in-process guard that prevents concurrent goroutines
// within the same process from racing on the filesystem lock. The filesystem
// lock (forge.distill.lock) handles cross-process exclusion.
var distillInProcess sync.Mutex

// acquireDistillLock acquires an exclusive distillation lock. Returns (lock,
// nil) on success, (nil, nil) when another distill is already running (caller
// should exit gracefully), or (nil, err) on unexpected failure.
//
// Unlike the daemon lock, staleness is determined solely by PID liveness —
// no process-identity check — so any alive process holding the lock blocks.
func acquireDistillLock() (*os.File, error) {
	// Fast path: reject concurrent goroutines in this process immediately,
	// before touching the filesystem. This eliminates the TOCTOU window
	// between O_EXCL file creation and PID write that would let a second
	// goroutine misread an empty lock file as stale and steal it.
	if !distillInProcess.TryLock() {
		return nil, nil
	}

	lockPath := distillLockPath()
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		distillInProcess.Unlock()
		return nil, fmt.Errorf("create distill lock dir: %w", err)
	}

	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			if _, werr := fmt.Fprintf(f, "%d", os.Getpid()); werr != nil {
				f.Close()
				_ = os.Remove(lockPath)
				distillInProcess.Unlock()
				return nil, fmt.Errorf("write distill lock pid: %w", werr)
			}
			// Keep f open — the open fd is the lock. Close+reopen creates a
			// window where another goroutine can O_EXCL-create the same file.
			return f, nil
		}
		if os.IsExist(err) {
			// Check staleness by PID liveness only — no identity check.
			if isDistillLockStale(lockPath) {
				_ = os.Remove(lockPath)
				continue // retry
			}
			distillInProcess.Unlock()
			return nil, nil // live holder — signal blocked
		}
		distillInProcess.Unlock()
		return nil, fmt.Errorf("acquire distill lock: %w", err)
	}
	distillInProcess.Unlock()
	return nil, nil // could not acquire after retries — treat as blocked
}

// isDistillLockStale reports whether the distill lock is held by a dead process.
// Unlike the daemon lock, we do not check process identity — any alive PID
// means the lock is legitimately held.
func isDistillLockStale(lockPath string) bool {
	pid := readLockPID(lockPath)
	if pid <= 0 {
		// Distinguish two cases:
		//   size=0, very recent → file created by O_EXCL but PID not yet written
		//                         (the create→Fprintf window); not stale.
		//   size>0, pid=0       → garbage/corrupted content → stale.
		if info, err := os.Stat(lockPath); err == nil && info.Size() == 0 && time.Since(info.ModTime()) < time.Second {
			return false
		}
		return true
	}
	return !isProcessAlive(pid)
}

func cleanDistillLock() {
	_ = os.Remove(distillLockPath())
	distillInProcess.Unlock()
}

// cleanSocket removes the daemon socket file.
func cleanSocket() {
	socketPath := filepath.Join(forgeHome(), ".forge", "forge.sock")
	_ = os.Remove(socketPath)
}

// isStaleSocket checks whether the socket file exists.
func isStaleSocket() bool {
	socketPath := filepath.Join(forgeHome(), ".forge", "forge.sock")
	_, err := os.Stat(socketPath)
	return err == nil
}
