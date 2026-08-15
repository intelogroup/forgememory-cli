package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/forge/forge/internal/artifacts"
	"github.com/forge/forge/internal/coach"
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
		if cfg.CoachMode != "" {
			os.Setenv("FORGE_COACH_MODE", cfg.CoachMode)
		}
		log.Printf("Loaded config: provider=%s", cfg.Provider)
	} else {
		log.Printf("No config found, using defaults (provider: ollama)")
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

	// Pre-fetch the signing/encryption key before the daemon signals it's
	// ready (writeAddr below) — see WarmSigningKey's doc comment for why
	// this can't just happen lazily on the first event.
	db.WarmSigningKey()

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
	//
	// Grace period: require 3 consecutive "parent dead" checks (15 s total)
	// before shutting down. This prevents a short-lived IDE restart or process
	// respawn from killing the daemon mid-distillation-cycle (issue: flapping).
	if parentPID > 1 && !skipParentMonitor {
		go func() {
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			deadStrikes := 0
			const maxDeadStrikes = 3
			for {
				select {
				case <-stop:
					return
				case <-ticker.C:
					if !isParentAlive(parentPID) {
						deadStrikes++
						if deadStrikes < maxDeadStrikes {
							log.Printf("Parent process (pid %d) not responding (%d/%d), waiting before shutdown", parentPID, deadStrikes, maxDeadStrikes)
							continue
						}
						log.Printf("Parent process (pid %d) gone for %d consecutive checks, shutting down", parentPID, deadStrikes)
						requestStop()
						_ = ln.Close()
						return
					}
					deadStrikes = 0 // reset on any alive check
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

	// Reclaim any orphaned manual-distill lock from a prior daemon run.
	// Manual distill is on-demand and never spans a daemon restart, so any
	// lock present at startup is stale by definition — clearing it prevents
	// the scheduler from wedging behind a lock held by a dead/leaked process
	// (issue #33).
	if _, err := os.Stat(distillLockPath()); err == nil {
		log.Printf("distill lock reclaimed at daemon startup: clearing orphaned lock")
		_ = os.Remove(distillLockPath())
	}

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
	projectRoot := extract("project_root")
	context := evidenceContext{
		TraceID:        extract("trace_id"),
		TaskID:         extract("task_id"),
		CWD:            extract("cwd"),
		GitBranch:      extract("git_branch"),
		GitCommit:      extract("git_commit"),
		TranscriptPath: extract("transcript_path"),
		ProjectRoot:    projectRoot,
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
	captureEvidenceArtifacts(database, event, context)
	if projectRoot != "" {
		if err := database.UpsertProject(event.ProjectID, projectRoot, event.ProjectID, event.SourceTool); err != nil {
			log.Printf("Upsert project error: %v", err)
		}
	}
	if err := detect.ProcessEvent(database, event); err != nil {
		log.Printf("Failure detection error: %v", err)
	}
}

type evidenceContext struct {
	TraceID        string
	TaskID         string
	CWD            string
	GitBranch      string
	GitCommit      string
	TranscriptPath string
	ProjectRoot    string
}

const maxEvidenceReadBytes = 32 << 20

func captureEvidenceArtifacts(database *db.DB, event *db.Event, context evidenceContext) {
	if database == nil || event == nil {
		return
	}
	traceID := context.TraceID
	if traceID == "" {
		traceID = event.SessionID
	}
	if traceID == "" || traceID == "unknown" {
		return
	}
	metadataBytes, _ := json.Marshal(map[string]string{
		"event_id":        event.ID,
		"session_id":      event.SessionID,
		"project_id":      event.ProjectID,
		"source_tool":     event.SourceTool,
		"event_type":      event.EventType,
		"tool_name":       event.ToolName,
		"cwd":             context.CWD,
		"git_branch":      context.GitBranch,
		"git_commit":      context.GitCommit,
		"transcript_path": context.TranscriptPath,
		"project_root":    context.ProjectRoot,
	})
	metadata := string(metadataBytes)
	store := artifacts.Store{DB: database, Root: filepath.Join(filepath.Dir(database.Path), "artifacts")}
	put := func(kind, mediaType string, content []byte) {
		if len(content) == 0 {
			return
		}
		if _, err := store.Put(traceID, context.TaskID, kind, mediaType, content, metadata); err != nil {
			log.Printf("evidence capture (%s): %v", kind, err)
		}
	}

	if isSessionEndEvent(event.EventType) && context.TranscriptPath != "" {
		if content, err := readEvidenceFile(context.TranscriptPath); err == nil {
			put("transcript", "application/jsonl", content)
		}
	}
	if event.ToolName == "Bash" {
		kind := "command-log"
		if isTestCommand(event.Payload) {
			kind = "test-report"
		}
		put(kind, "application/json", []byte(event.Payload))
	}
	if isWriteTool(event.ToolName) && context.CWD != "" {
		if diff := gitEvidenceDiff(context.CWD); len(diff) > 0 {
			put("diff", "text/x-diff", diff)
		}
	}
}

func readEvidenceFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxEvidenceReadBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) > maxEvidenceReadBytes {
		content = append(content[:maxEvidenceReadBytes], []byte("\n...[TRUNCATED]...")...)
	}
	return content, nil
}

func isTestCommand(payload string) bool {
	var raw struct {
		ToolInput struct {
			Command string `json:"command"`
		} `json:"tool_input"`
	}
	if json.Unmarshal([]byte(payload), &raw) == nil {
		command := strings.ToLower(raw.ToolInput.Command)
		for _, token := range []string{"go test", "cargo test", "npm test", "pnpm test", "yarn test", "pytest", "vitest", "jest"} {
			if strings.Contains(command, token) {
				return true
			}
		}
	}
	return false
}

func gitEvidenceDiff(cwd string) []byte {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	output := &evidenceLimitWriter{limit: maxEvidenceReadBytes + 1}
	command := exec.CommandContext(ctx, "git", "-C", cwd, "diff", "--binary", "--no-ext-diff", "HEAD", "--")
	command.Stdout = output
	if err := command.Run(); err != nil {
		return nil
	}
	content := output.buf.Bytes()
	if len(content) > maxEvidenceReadBytes {
		content = append(content[:maxEvidenceReadBytes], []byte("\n...[TRUNCATED]...\n")...)
	}
	return content
}

type evidenceLimitWriter struct {
	buf   bytes.Buffer
	limit int
}

func (w *evidenceLimitWriter) Write(p []byte) (int, error) {
	remaining := w.limit - w.buf.Len()
	if remaining > 0 {
		if len(p) > remaining {
			_, _ = w.buf.Write(p[:remaining])
		} else {
			_, _ = w.buf.Write(p)
		}
	}
	// Report the full input as consumed so the child process can never block
	// once the retention limit is reached.
	return len(p), nil
}

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

const crossSessionMinSessions = 5

// distillSessionBatch distills one batch of sessions and returns (processed, lastErr, hadWork).
// Extracted so the loop can call it both on tick and in burst-drain mode.
func distillSessionBatch(database *db.DB, interval time.Duration, lastCrossSessionRun *time.Time) (int, error, bool) {
	const batchLimit = 20

	// Check for completed sessions — avoids spinning on orphaned events.
	sessions, sErr := database.UndistilledCompletedSessions(batchLimit)
	if sErr != nil {
		log.Printf("Distillation: failed to find completed sessions: %v", sErr)
		return 0, sErr, false
	}
	if len(sessions) == 0 {
		return 0, nil, false
	}
	// Coaching is deterministic and provider-independent. Run it after this
	// batch returns, even if distillation is backoff-limited or the provider is
	// unavailable; it must never hold up hook event capture or distillation.
	defer processCompletedSessions(database, sessions)

	// Apply exponential backoff after consecutive failures so we don't
	// hammer a misconfigured provider every minute.
	if h, hErr := database.GetDistillationHealth(); hErr == nil && h.ConsecutiveFailures > 0 {
		backoff := distillBackoff(h.ConsecutiveFailures)
		if backoff > 0 {
			if last, parseErr := time.Parse(time.RFC3339, h.LastErrorAt); parseErr == nil && time.Since(last) < backoff {
				return 0, nil, false
			}
		}
	}

	// Skip if a manual `forge distill` is already running.
	lock, lockErr := acquireDistillLock()
	if lockErr != nil {
		log.Printf("Distillation skipped: could not acquire lock: %v", lockErr)
		noteDistillSkip()
		return 0, nil, false
	}
	if lock == nil {
		log.Printf("Distillation skipped: manual distill already in progress")
		noteDistillSkip()
		return 0, nil, false
	}

	var lastErr error
	for _, sessionID := range sessions {
		proj := ""
		checkpointKey := sessionID + ":daemon"

		// Check if already processed (fast path)
		exists, _ := database.HasSessionCheckpoint(checkpointKey)
		if exists {
			_ = database.MarkSessionDistilled(sessionID)
			continue
		}

		startDistill := time.Now()

		// Load session events
		events, evErr := database.SessionEventsUpTo(sessionID, "", 0)
		if evErr != nil {
			log.Printf("Distillation: failed to fetch events for session %s: %v", sessionID, evErr)
			lastErr = evErr
			_ = database.RecordDistillationFailure(startDistill, time.Since(startDistill), 0, evErr.Error(), time.Now().Add(interval))
			continue
		}

		if len(events) == 0 {
			_ = database.MarkSessionDistilled(sessionID)
			continue
		}

		for _, e := range events {
			if e.ProjectID != "" {
				proj = e.ProjectID
				break
			}
		}

		// Load config dynamically
		loopCfg := distill.LoadConfig()

		// Warn when provider/BaseURL look mismatched (e.g. openrouter key but
		// OpenAI endpoint) — surface misconfiguration before the first 404.
		warnProviderURLMismatch(loopCfg)

		// Skip distillation if provider is unconfigured
		if loopCfg.Provider == "" || loopCfg.APIKey == "" {
			err := errors.New("no inference provider configured")
			log.Printf("Distillation skipped: %v", err)
			lastErr = err
			_ = database.RecordDistillationFailure(startDistill, time.Since(startDistill), len(events), err.Error(), time.Now().Add(interval))
			continue
		}

		loopD := distill.New(database, loopCfg)

		summary, err := loopD.SynthesizeCheckpoint(sessionID, proj, "daemon", checkpointKey, events)
		if err != nil {
			log.Printf("Distillation: session %s SynthesizeCheckpoint failed: %v", sessionID, err)
			lastErr = err
			_ = database.RecordDistillationFailure(startDistill, time.Since(startDistill), len(events), err.Error(), time.Now().Add(interval))
			continue
		}

		distilledPrinciples := 0
		if summary != nil {
			dpCount, dpErr := loopD.DistillCheckpointSummary(*summary, events)
			if dpErr != nil {
				log.Printf("Distillation: session %s DistillCheckpointSummary failed: %v", sessionID, dpErr)
				lastErr = dpErr
				_ = database.RecordDistillationFailure(startDistill, time.Since(startDistill), len(events), dpErr.Error(), time.Now().Add(interval))
				continue
			}
			distilledPrinciples = dpCount
		}

		// Successfully distilled session!
		_ = database.RecordDistillationSuccess(startDistill, time.Since(startDistill), len(events), distilledPrinciples, time.Now().Add(interval))
		_ = database.MarkSessionDistilled(sessionID)
	}

	// Cross-session synthesis: run periodically (every 30 min).
	crossSessionCount := 0
	if time.Since(*lastCrossSessionRun) > 30*time.Minute {
		projects, pErr := database.ProjectIDsWithEnoughSessions(crossSessionMinSessions)
		if pErr == nil {
			for _, proj := range projects {
				loopCfg := distill.LoadConfig()
				loopD := distill.New(database, loopCfg)
				n, err := loopD.SynthesizeCrossSession(proj, crossSessionMinSessions)
				if err != nil {
					log.Printf("Cross-session synthesis for %s: %v", proj, err)
				} else {
					crossSessionCount += n
				}
			}
		}
		*lastCrossSessionRun = time.Now()
	}

	lock.Close()
	cleanDistillLock()
	noteDistillCycleRan()

	if lastErr != nil {
		log.Printf("Distillation error: %v", lastErr)
	} else {
		log.Printf("Distillation cycle complete: processed %d session(s)", len(sessions))
	}
	_ = crossSessionCount
	return len(sessions), lastErr, len(sessions) >= batchLimit
}

func processCompletedSessions(database *db.DB, sessionIDs []string) {
	for _, sessionID := range sessionIDs {
		if err := coach.ProcessCompletedSession(context.Background(), database, sessionID); err != nil {
			log.Printf("Coaching: process completed session %s: %v", sessionID, err)
		}
	}
}

// warnProviderURLMismatch logs a one-time warning when provider and BaseURL
// look inconsistent (e.g. provider=openrouter but BaseURL points at api.openai.com).
// This surfaces misconfiguration before the first failed LLM call.
var providerURLWarnOnce sync.Once

func warnProviderURLMismatch(cfg distill.Config) {
	provider := string(cfg.Provider)
	baseURL := cfg.BaseURL
	if provider == "" || baseURL == "" {
		return
	}
	type pair struct{ prov, mustContain string }
	checks := []pair{
		{"openrouter", "openrouter"},
		{"anthropic", "anthropic"},
		{"groq", "groq"},
		{"nvidia", "nvidia"},
	}
	for _, c := range checks {
		if provider == c.prov && !strings.Contains(baseURL, c.mustContain) {
			providerURLWarnOnce.Do(func() {
				log.Printf("WARNING: provider=%s but base_url=%q does not contain %q — possible misconfiguration; run `forge config --show`", provider, baseURL, c.mustContain)
			})
			return
		}
	}
}

func distillLoop(d *distill.Distiller, database *db.DB, interval time.Duration) {
	pollInterval := 60 * time.Second
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	lastCrossSessionRun := time.Time{}

	for range ticker.C {
		for {
			_, _, more := distillSessionBatch(database, interval, &lastCrossSessionRun)
			if !more {
				break
			}
			// Brief pause between burst iterations to keep the goroutine
			// cooperative and avoid hammering the DB under high backlog.
			time.Sleep(500 * time.Millisecond)
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
				// Suppress spammy log messages for unconfigured features
				if !strings.Contains(err.Error(), "no API key configured") {
					log.Printf("Retrieval error: %v", err)
				}
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
	Principles           int    `json:"principles"`
	Sessions             int    `json:"sessions"`
	CrossSessionPatterns int    `json:"cross_session_patterns"`
	Daemon               string `json:"daemon"`
	DaemonAddress        string `json:"daemon_address,omitempty"`
	LastDistilled        string `json:"last_distilled,omitempty"`
	Distillation         struct {
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
	crossPatterns, _ := database.CrossSessionPatternCount()
	addr := readAddr()
	cfg := distill.LoadConfig()

	report.Provider = string(cfg.Provider)
	report.Model = cfg.Model
	report.Database = database.Path
	report.Events.Total = total
	report.Events.Undistilled = undistilled
	report.Principles = principles
	report.Sessions = sessions
	report.CrossSessionPatterns = crossPatterns
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
	fmt.Printf("Cross-session patterns: %d\n", report.CrossSessionPatterns)
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
	// A bare "failed (N consecutive failures)" line doesn't say why or what
	// to do about it — surface the actual error (e.g. the wedged-behind-a-
	// stale-lock message) directly instead of requiring `forge health` or
	// `--detailed` to find it (issue #43).
	if report.Distillation.LastErrorMessage != "" && report.Distillation.ConsecutiveFailures > 0 {
		fmt.Printf("  -> %s\n", report.Distillation.LastErrorMessage)
		if report.Events.Undistilled > 0 {
			fmt.Printf("  -> %d events undistilled. Run 'forge doctor' to diagnose or 'forge doctor --repair' to fix.\n", report.Events.Undistilled)
		}
	}
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
	if !isProcessAlive(pid) {
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

// consecutiveDistillSkips counts scheduled distill cycles in a row that bailed
// without doing any work (usually blocked by a stale distill lock). >3 means
// the scheduler is wedged — health/status reports WEDGED instead of the
// database's stale LastStatus so the failure is visible (issue #33).
var (
	consecutiveDistillSkips int
	distillSkipsMu          sync.Mutex
)

func noteDistillSkip() {
	distillSkipsMu.Lock()
	consecutiveDistillSkips++
	skips := consecutiveDistillSkips
	distillSkipsMu.Unlock()
	// Surface wedge to the user: after 3 consecutive skips, write a failure
	// record to the DB so `forge status`/`health` show LastStatus=FAILURE with
	// a distinct "wedged" message instead of stale SUCCESS (issue #33).
	// Announced once per wedge episode (reset by noteDistillCycleRan).
	if skips == 3 {
		log.Printf("Distillation WEDGED: blocked by distill lock for %d consecutive cycles — run `forge doctor` or restart the daemon", skips)
		if database, err := db.Open(""); err == nil {
			_ = database.RecordDistillationFailure(time.Now(), 0, 0, "scheduler wedged: distill lock held by stale/leaked process for 3+ cycles", time.Now().Add(0))
			database.Close()
		}
	}
}

func noteDistillCycleRan() {
	distillSkipsMu.Lock()
	consecutiveDistillSkips = 0
	distillSkipsMu.Unlock()
}

func distillSkipCount() int {
	distillSkipsMu.Lock()
	defer distillSkipsMu.Unlock()
	return consecutiveDistillSkips
}

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

// distillLockTTL is the maximum age a distill lock may have before it is
// considered stale regardless of PID liveness. Protects against leaked
// `forge distill` processes (e.g. ephemeral npx temp-path installs per
// issue #29) whose PID may still appear alive while the owner is gone, and
// against PID reuse by an unrelated process. 10 min matches the default
// distill interval; any legitimate manual distill completes well within that.
const distillLockTTL = 10 * time.Minute

// isDistillLockStale reports whether the distill lock is reclaimable.
// Stale if: (a) lock age exceeds distillLockTTL, OR (b) PID is dead/garbage.
// TTL guard runs first so a long-held lock cannot wedge the scheduler
// even when its PID is technically alive (PID reuse, leaked zombie).
func isDistillLockStale(lockPath string) bool {
	if info, err := os.Stat(lockPath); err == nil && time.Since(info.ModTime()) > distillLockTTL {
		return true
	}
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
