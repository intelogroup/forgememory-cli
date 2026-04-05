package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/forge/forge/internal/agent"
	"github.com/forge/forge/internal/config"
	"github.com/forge/forge/internal/dashboard"
	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/distill"
	"github.com/forge/forge/internal/mcp"
	"github.com/forge/forge/internal/scanner"
	"github.com/forge/forge/internal/service"
)

var (
	serviceNew         = service.New
	serviceIsInstalled = func(m *service.Manager) bool { return m.IsServiceInstalled() }
	serviceInstall     = func(m *service.Manager) error { return m.Install() }
	serviceUninstall   = func(m *service.Manager) error { return m.Uninstall() }
	serviceStart       = func(m *service.Manager) error { return m.Start() }
	serviceStop        = func(m *service.Manager) error { return m.Stop() }
	exitWithCode       = os.Exit
)

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func contains(slice []string, item string) bool {
	for _, v := range slice {
		if v == item {
			return true
		}
	}
	return false
}

func runInit(args []string) {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	interactive := fs.Bool("interactive", false, "Interactive provider setup wizard")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	fmt.Println("Initializing Forge...")

	// Create database
	database, err := db.Open("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	database.Close()
	fmt.Printf("  Database created: %s\n", database.Path)

	// Detect agents
	home, _ := os.UserHomeDir()
	agents := syncIntegrations(home)
	if len(agents) == 0 {
		fmt.Println("  No agents detected. Install Claude Code, Gemini CLI, or Codex first.")
	} else {
		fmt.Printf("  Detected agents: %v\n", agents)
	}

	// Prompt for provider setup
	fmt.Println("\n  Configure your AI provider:")
	fmt.Println("    forge config --provider forgememo   # auto-cheapest, 1000 free runs")
	fmt.Println("    forge config --provider anthropic --api-key YOUR_KEY")
	fmt.Println("  Or run 'forge config' for interactive setup.")
	fmt.Println("  Default provider is forgememo (falls back to ollama if configured).")

	if *interactive {
		fmt.Println("\n  Running interactive setup...")
		runConfig([]string{"--interactive"})
	}

	// Show MCP restart notice for Claude Code
	if contains(agents, "claude") {
		fmt.Println("\n  NOTE: Claude Code requires restart to pick up MCP changes.")
		fmt.Println("  Run 'forge start' to start the daemon, then restart Claude Code.")
	}

	fmt.Println("\nForge initialized. Run `forge start` to start the daemon.")
}

func runSyncIntegrations(args []string) {
	fmt.Println("Refreshing Forge integrations...")
	home, _ := os.UserHomeDir()
	agents := syncIntegrations(home)
	if len(agents) == 0 {
		fmt.Println("  No agents detected.")
		return
	}
	fmt.Printf("  Refreshed agents: %v\n", agents)
}

func runStart(args []string) {
	fmt.Println("Starting Forge daemon...")

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
	if lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		cmd.Stdout = lf
		cmd.Stderr = lf
		closeLog = func() { _ = lf.Close() }
	}

	return cmd, closeLog
}

func filteredEnv(keys ...string) []string {
	blocked := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		blocked[key] = struct{}{}
	}

	var env []string
	for _, entry := range os.Environ() {
		key := entry
		if idx := strings.IndexByte(entry, '='); idx >= 0 {
			key = entry[:idx]
		}
		if _, skip := blocked[key]; skip {
			continue
		}
		env = append(env, entry)
	}
	return env
}

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

func runDistill(args []string) {
	database, err := db.Open("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	events, err := database.UndistilledEvents(50)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if len(events) == 0 {
		fmt.Println("No undistilled events.")
		return
	}

	fmt.Printf("Distilling %d events...\n", len(events))
	cfg := distill.LoadConfig()
	d := distill.New(database, cfg)
	count, err := d.DistillBatch(50)
	if err != nil {
		if shouldSkipDistillForMissingProvider(cfg, err) {
			fmt.Printf("Skipping distillation: %s\n", distill.UserMessage(err))
			fmt.Println("Events remain queued until you configure a provider or start Ollama.")
			return
		}
		fmt.Fprintf(os.Stderr, "Error: %s\n", distill.UserMessage(err))
		fmt.Fprintln(os.Stderr, "Suggestions:")
		for i, hint := range distill.DiagnosticHints(cfg, err) {
			fmt.Fprintf(os.Stderr, "  %d. %s\n", i+1, hint)
		}
		os.Exit(1)
	}
	if count == 0 {
		fmt.Println("Not enough undistilled events to extract principles yet (need 3+ related events).")
		return
	}
	fmt.Printf("Distilled %d principle(s).\n", count)
}

func shouldSkipDistillForMissingProvider(cfg distill.Config, err error) bool {
	if hasExplicitInferenceProviderConfig() {
		return false
	}
	if cfg.Provider != distill.ProviderOllama {
		if cfg.Provider != distill.ProviderForgememo {
			return false
		}
	}
	errText := strings.ToLower(err.Error())
	return errors.Is(err, distill.ErrNoProvider) ||
		errors.Is(err, distill.ErrProviderUnreachable) ||
		(cfg.Provider == distill.ProviderForgememo && cfg.APIKey == "" && errors.Is(err, distill.ErrProviderInvalid)) ||
		strings.Contains(errText, "connection refused") ||
		strings.Contains(errText, "actively refused")
}

func hasExplicitInferenceProviderConfig() bool {
	if os.Getenv("FORGE_PROVIDER") != "" || os.Getenv("FORGE_API_KEY") != "" {
		return true
	}
	cfg, err := config.Load()
	if err != nil {
		return false
	}
	return cfg.Provider != "" || cfg.APIKey != ""
}

func runSearch(args []string) {
	jsonOutput := false
	filtered := make([]string, 0, len(args))
	for _, a := range args {
		if a == "--json" {
			jsonOutput = true
			continue
		}
		filtered = append(filtered, a)
	}

	if len(filtered) < 1 {
		fmt.Println("Usage: forge search <query>")
		os.Exit(1)
	}

	database, err := db.Open("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	query := filtered[0]
	events, err := database.SearchEvents(query, 10)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if len(events) == 0 {
		if jsonOutput {
			payload := map[string]any{
				"schema_version": "1",
				"query":          query,
				"results":        []db.Event{},
			}
			b, _ := json.MarshalIndent(payload, "", "  ")
			fmt.Println(string(b))
			return
		}
		fmt.Println("No results.")
		return
	}

	if jsonOutput {
		payload := map[string]any{
			"schema_version": "1",
			"query":          query,
			"results":        events,
		}
		b, _ := json.MarshalIndent(payload, "", "  ")
		fmt.Println(string(b))
		return
	}

	for _, e := range events {
		fmt.Printf("[%s] %s (%s)\n", e.TS[:10], e.EventType, e.SourceTool)
		fmt.Printf("  %s\n", truncate(e.Payload, 200))
		fmt.Println()
	}
}

func runStatus(args []string) {
	jsonOutput := false
	for _, a := range args {
		if a == "--json" {
			jsonOutput = true
			break
		}
	}
	if jsonOutput {
		statusOutputJSON()
		return
	}
	statusOutput()
}

func runHelp(args []string) {
	if len(args) == 1 && args[0] == "mcp" {
		fmt.Println("forge help mcp")
		fmt.Println("  get_recent_context       Get distilled memories and recent summaries")
		fmt.Println("  search_memories <query>  Search raw memory events with full-text matching")
		fmt.Println("  get_principles           Get high-level patterns and decisions")
		fmt.Println("  get_session_summaries    Get synthesized summaries of recent sessions")
		fmt.Println("  get_project_timeline     Get cross-agent timeline for the current project")
		return
	}
	printUsage()
}

// runSave stores a memory directly, bypassing the daemon.
// If --principle is given, it inserts straight into the principles table (no LLM).
// Otherwise it inserts as an event and triggers immediate distillation.
func runSave(args []string) {
	fs := flag.NewFlagSet("save", flag.ContinueOnError)
	typeFlag := fs.String("type", "note", "Memory type: success|failure|plan|note")
	content := fs.String("content", "", "What to remember")
	principle := fs.String("principle", "", "Principle text (skips LLM distillation)")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	if *content == "" {
		fmt.Fprintln(os.Stderr, "Error: --content is required")
		fmt.Fprintln(os.Stderr, "Usage: forge save --type [success|failure|plan|note] --content TEXT [--principle TEXT]")
		os.Exit(1)
	}

	validTypes := map[string]bool{"success": true, "failure": true, "plan": true, "note": true}
	if !validTypes[*typeFlag] {
		fmt.Fprintf(os.Stderr, "Error: --type must be one of: success, failure, plan, note\n")
		os.Exit(1)
	}

	database, err := db.Open("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	projectID := detectProjectID()

	if *principle != "" {
		// Direct insert into principles table — no LLM needed.
		p := &db.Principle{
			Type:        *typeFlag,
			Title:       truncate(*content, 80),
			Narrative:   *principle,
			ImpactScore: 0.7,
			ProjectID:   projectID,
		}
		if err := database.InsertPrinciple(p); err != nil {
			fmt.Fprintf(os.Stderr, "Error saving principle: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Saved principle: %s\n", p.Title)
		return
	}

	// Insert as event, then distill immediately.
	event := &db.Event{
		SessionID:  "manual-save",
		ProjectID:  projectID,
		SourceTool: "manual",
		EventType:  "ManualSave",
		Payload:    fmt.Sprintf(`{"type":%q,"content":%q}`, *typeFlag, *content),
	}
	if err := database.InsertEvent(event); err != nil {
		fmt.Fprintf(os.Stderr, "Error saving event: %v\n", err)
		os.Exit(1)
	}

	d := distill.New(database, distill.LoadConfig())
	count, err := d.DistillBatch(50)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Saved but distillation failed: %v\n", err)
		fmt.Println("Memory saved (will be distilled by daemon).")
		return
	}
	if count > 0 {
		fmt.Printf("Memory saved and distilled into %d principle(s).\n", count)
	} else {
		fmt.Println("Memory saved (queued for distillation — need 3+ events).")
	}
}

// runSynthesizeSession is an internal command spawned by the Stop/SessionEnd hook.
// It synthesizes a session summary and writes it to the DB.
func runSynthesizeSession(args []string) {
	fs := flag.NewFlagSet("synthesize-session", flag.ContinueOnError)
	sessionID := fs.String("session-id", "", "Session ID")
	projectID := fs.String("project-id", "", "Project ID")
	if err := fs.Parse(args); err != nil {
		log.Printf("synthesize-session: flag parse error: %v", err)
		os.Exit(0)
	}

	if *sessionID == "" {
		os.Exit(0)
	}

	database, err := db.Open("")
	if err != nil {
		log.Printf("synthesize-session: failed to open DB: %v", err)
		os.Exit(0)
	}
	defer database.Close()

	events, err := database.SessionEvents(*sessionID, 20)
	if err != nil {
		log.Printf("synthesize-session: failed to get session events: %v", err)
		os.Exit(0)
	}
	if len(events) < 3 {
		log.Printf("synthesize-session: not enough events (%d) for synthesis", len(events))
		os.Exit(0)
	}

	proj := *projectID
	if proj == "" {
		proj = events[0].ProjectID
	}

	d := distill.New(database, distill.LoadConfig())
	if err := d.SynthesizeSession(*sessionID, proj, events); err != nil {
		log.Printf("synthesize-session: synthesis failed: %v", err)
		os.Exit(1)
	}

	log.Printf("synthesize-session: synthesized session %s with %d events", *sessionID, len(events))
	os.Exit(0)
}

// runScan mines recent git history across ~/Developer repos and inserts learnings.
func runScan(args []string) {
	fs := flag.NewFlagSet("scan", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "Print repos found without writing to DB")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	database, err := db.Open("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	d := distill.New(database, distill.LoadConfig())
	if err := scanner.Run(database, d, *dryRun); err != nil {
		fmt.Fprintf(os.Stderr, "Scan error: %v\n", err)
		os.Exit(1)
	}
}

func runMCP(args []string) {
	// Check if daemon is running, start it if not.
	if result, err := ensureDaemonRunning(false); err != nil {
		fmt.Fprintf(os.Stderr, "Error starting daemon: %v\n", err)
		if result.logPath != "" {
			fmt.Fprintf(os.Stderr, "  Check logs: %s\n", result.logPath)
		}
		fmt.Fprintln(os.Stderr, "  Run 'forge doctor' to diagnose.")
		os.Exit(1)
	} else if result.started {
		fmt.Println("Starting Forge daemon for MCP server...")
		for _, item := range result.cleanup {
			fmt.Printf("  Cleaning stale %s...\n", item)
		}
		fmt.Println("  Daemon started.")
	}

	database, err := db.Open("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	server := mcp.New(database)
	server.Quiet = true // Suppress startup banner for MCP stdio transport
	if err := server.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "MCP server error: %v\n", err)
		os.Exit(1)
	}
}

func syncIntegrations(home string) []string {
	agents := agent.DetectAgents(home)
	for _, a := range agents {
		skillPath, err := agent.SetupAgent(a, home)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  Error setting up %s: %v\n", a, err)
		} else if skillPath != "" {
			fmt.Printf("  Configured %s (skill: %s)\n", a, skillPath)
		} else {
			fmt.Printf("  Configured %s\n", a)
		}
	}
	return agents
}

func runDashboard(args []string) {
	fs := flag.NewFlagSet("dashboard", flag.ContinueOnError)
	port := fs.Int("port", 5555, "Port to serve dashboard on")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	fmt.Printf("Starting Forge Memory Dashboard on http://localhost:%d\n", *port)

	database, err := db.Open("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	srv := dashboard.New(database, *port)
	if err := srv.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Dashboard error: %v\n", err)
		os.Exit(1)
	}
}

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
		if !hasIssues {
			fmt.Println("  No stale state found.")
		}
		fmt.Println("  Repair complete.")
		fmt.Println()
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

	// Check binary
	fmt.Printf("  [OK] Binary: %s\n", agent.ForgePath())
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
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	probe := filepath.Join(dir, ".doctor-write-probe")
	if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil {
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

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func runServiceInstall(args []string) {
	fmt.Println("Installing Forge as system service...")

	mgr, err := serviceNew()
	if err != nil {
		fmt.Fprintf(os.Stderr, "  Error: %v\n", err)
		exitWithCode(1)
	}

	if serviceIsInstalled(mgr) {
		fmt.Println("  Service already installed.")
		return
	}

	if err := serviceInstall(mgr); err != nil {
		fmt.Fprintf(os.Stderr, "  Error installing service: %v\n", err)
		exitWithCode(1)
	}

	fmt.Println("  Service installed successfully.")
	fmt.Println("  Run 'forge service-start' to start the daemon as a service.")
}

func runServiceUninstall(args []string) {
	fmt.Println("Uninstalling Forge service...")

	mgr, err := serviceNew()
	if err != nil {
		fmt.Fprintf(os.Stderr, "  Error: %v\n", err)
		exitWithCode(1)
	}

	if !serviceIsInstalled(mgr) {
		fmt.Println("  Service not installed.")
		return
	}

	if err := serviceUninstall(mgr); err != nil {
		fmt.Fprintf(os.Stderr, "  Error uninstalling service: %v\n", err)
		exitWithCode(1)
	}

	fmt.Println("  Service uninstalled successfully.")
}

func runServiceStart(args []string) {
	fmt.Println("Starting Forge service...")

	mgr, err := serviceNew()
	if err != nil {
		fmt.Fprintf(os.Stderr, "  Error: %v\n", err)
		exitWithCode(1)
	}

	if err := serviceStart(mgr); err != nil {
		fmt.Fprintf(os.Stderr, "  Error starting service: %v\n", err)
		exitWithCode(1)
	}

	fmt.Println("  Service started.")
}

func runServiceStop(args []string) {
	fmt.Println("Stopping Forge service...")

	mgr, err := serviceNew()
	if err != nil {
		fmt.Fprintf(os.Stderr, "  Error: %v\n", err)
		exitWithCode(1)
	}

	if err := serviceStop(mgr); err != nil {
		fmt.Fprintf(os.Stderr, "  Error stopping service: %v\n", err)
		exitWithCode(1)
	}

	fmt.Println("  Service stopped.")
}

func runConfig(args []string) {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	showFlag := fs.Bool("show", false, "Show current config")
	providerFlag := fs.String("provider", "", "Provider: forgememo/anthropic/openai/ollama")
	apiKeyFlag := fs.String("api-key", "", "API key for provider")
	modelFlag := fs.String("model", "", "Model name (optional, defaults vary by provider)")
	baseURLFlag := fs.String("base-url", "", "Base URL for API (optional)")
	timeoutFlag := fs.String("timeout", "", "Inference timeout (e.g. 30s)")
	retriesFlag := fs.Int("retries", -1, "Retry attempts for transient failures")
	intervalFlag := fs.String("interval", "", "Distillation interval (e.g. 10m)")
	jsonFlag := fs.Bool("json", false, "Machine-readable JSON output")
	validateFlag := fs.Bool("validate", false, "Validate provider credentials/model access")
	interactiveFlag := fs.Bool("interactive", false, "Interactive setup")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	if *showFlag {
		cfg, err := config.Load()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
			os.Exit(1)
		}
		if cfg.Provider == "" {
			fmt.Println("No config set. Run 'forge config --provider forgememo' or configure another provider.")
			return
		}
		if *jsonFlag {
			out := map[string]any{
				"schema_version": "1",
				"provider":       cfg.Provider,
				"api_key_set":    cfg.APIKey != "",
				"api_key_masked": maskKey(cfg.APIKey),
				"model":          cfg.Model,
				"base_url":       cfg.BaseURL,
				"timeout":        cfg.Timeout,
				"retries":        cfg.Retries,
				"interval":       cfg.DistillInterval,
			}
			b, _ := json.MarshalIndent(out, "", "  ")
			fmt.Println(string(b))
			return
		}
		fmt.Printf("Provider:  %s\n", cfg.Provider)
		if cfg.APIKey != "" {
			fmt.Printf("API Key:    %s\n", maskKey(cfg.APIKey))
		}
		if cfg.Model != "" {
			fmt.Printf("Model:      %s\n", cfg.Model)
		}
		if cfg.BaseURL != "" {
			fmt.Printf("Base URL:   %s\n", cfg.BaseURL)
		}
		if cfg.Timeout != "" {
			fmt.Printf("Timeout:    %s\n", cfg.Timeout)
		}
		if cfg.Retries > 0 {
			fmt.Printf("Retries:    %d\n", cfg.Retries)
		}
		if cfg.DistillInterval != "" {
			fmt.Printf("Interval:   %s\n", cfg.DistillInterval)
		}
		return
	}

	interactive := *interactiveFlag || *providerFlag == ""
	if interactive {
		runConfigInteractive(providerFlag, apiKeyFlag, modelFlag, timeoutFlag, retriesFlag, intervalFlag)
	}

	if *providerFlag == "" {
		fmt.Println("Usage: forge config [options]")
		fmt.Println("")
		fmt.Println("Options:")
		fmt.Println("  --show           Show current configuration")
		fmt.Println("  --provider       Provider: forgememo, anthropic, openai, or ollama")
		fmt.Println("  --api-key        API key for the provider")
		fmt.Println("  --model          Model name (optional)")
		fmt.Println("  --base-url       Base URL for API (optional)")
		fmt.Println("  --timeout        Inference timeout (e.g. 30s)")
		fmt.Println("  --retries        Retry attempts for transient failures")
		fmt.Println("  --interval       Distillation interval (e.g. 10m)")
		fmt.Println("  --validate       Validate provider credentials/model access")
		fmt.Println("  --json           JSON output (with --show)")
		fmt.Println("  --interactive    Interactive setup")
		fmt.Println("")
		fmt.Println("Defaults by provider:")
		fmt.Println("  forgememo: claude-haiku-4-5-20251001")
		fmt.Println("  anthropic: claude-haiku-4-5-20251001")
		fmt.Println("  openai:    gpt-4o")
		fmt.Println("  ollama:    llama3:latest")
		fmt.Println("")
		fmt.Println("Examples:")
		fmt.Println("  forge config --show")
		fmt.Println("  forge config --provider forgememo")
		fmt.Println("  forge config --provider openai --api-key sk-...")
		fmt.Println("  forge config --provider anthropic --api-key sk-ant-...")
		fmt.Println("  forge config --provider ollama --model llama3:latest")
		fmt.Println("  forge config --provider anthropic --api-key sk-ant-... --validate")
		os.Exit(0)
	}

	validProviders := map[string]bool{"forgememo": true, "forge": true, "anthropic": true, "openai": true, "ollama": true}
	if !validProviders[*providerFlag] {
		fmt.Fprintf(os.Stderr, "Error: provider must be one of: forgememo, anthropic, openai, ollama\n")
		os.Exit(1)
	}
	if *providerFlag == "forge" {
		*providerFlag = "forgememo"
	}

	if *timeoutFlag != "" {
		if _, err := time.ParseDuration(*timeoutFlag); err != nil {
			fmt.Fprintf(os.Stderr, "Error: invalid --timeout value %q\n", *timeoutFlag)
			os.Exit(1)
		}
	}
	if *intervalFlag != "" {
		if _, err := time.ParseDuration(*intervalFlag); err != nil {
			fmt.Fprintf(os.Stderr, "Error: invalid --interval value %q\n", *intervalFlag)
			os.Exit(1)
		}
	}
	if *retriesFlag < -1 {
		fmt.Fprintln(os.Stderr, "Error: --retries must be >= 0")
		os.Exit(1)
	}

	existing, _ := config.Load()
	cfg := existing
	cfg.Provider = *providerFlag
	if *apiKeyFlag != "" {
		cfg.APIKey = *apiKeyFlag
	}
	if *modelFlag != "" {
		cfg.Model = *modelFlag
	} else if cfg.Model == "" || cfg.Provider != existing.Provider {
		cfg.Model = defaultModelForProvider(cfg.Provider)
	}
	if *baseURLFlag != "" {
		cfg.BaseURL = *baseURLFlag
	}
	if *timeoutFlag != "" {
		cfg.Timeout = *timeoutFlag
	}
	if *retriesFlag >= 0 {
		cfg.Retries = *retriesFlag
	}
	if *intervalFlag != "" {
		cfg.DistillInterval = *intervalFlag
	}
	if cfg.Retries == 0 {
		cfg.Retries = 3
	}

	if *validateFlag {
		fmt.Println("Testing connection...")
		if err := distill.ValidateConfig(distillConfigFromUserConfig(cfg)); err != nil {
			fmt.Fprintf(os.Stderr, "Validation failed: %s\n", distill.UserMessage(err))
			os.Exit(1)
		}
		fmt.Println("Validation successful.")
	}

	if err := config.Save(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Error saving config: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Configuration saved.\n")
	fmt.Printf("Provider: %s\n", cfg.Provider)
	fmt.Printf("Model: %s\n", cfg.Model)
	if cfg.APIKey != "" {
		fmt.Println("API Key: " + maskKey(cfg.APIKey))
	}
	if cfg.Timeout != "" {
		fmt.Printf("Timeout: %s\n", cfg.Timeout)
	}
	fmt.Printf("Retries: %d\n", cfg.Retries)
	fmt.Println("\nTo apply, either:")
	fmt.Println("  1. Run: export $(cat ~/.forge/config | xargs)  # in your shell")
	fmt.Println("  2. Or add to your shell profile (~/.zshrc, ~/.bashrc)")
}

func runConfigInteractive(providerFlag, apiKeyFlag, modelFlag, timeoutFlag *string, retriesFlag *int, intervalFlag *string) {
	reader := bufio.NewReader(os.Stdin)
	isTTY := false
	if fi, err := os.Stdin.Stat(); err == nil {
		isTTY = (fi.Mode() & os.ModeCharDevice) != 0
	}
	if !isTTY {
		return
	}

	if *providerFlag == "" {
		*providerFlag = promptChoice(reader, "Select provider", []string{"forgememo", "anthropic", "openai", "ollama"}, "forgememo")
	}
	if *apiKeyFlag == "" && (*providerFlag == "anthropic" || *providerFlag == "openai") {
		*apiKeyFlag = promptInput(reader, "API Key", "")
	}
	if *modelFlag == "" {
		*modelFlag = promptModel(reader, *providerFlag)
	}
	if *timeoutFlag == "" {
		*timeoutFlag = promptInput(reader, "Timeout", "30s")
	}
	if *retriesFlag < 0 {
		retryRaw := promptInput(reader, "Retries", "3")
		if retryVal, err := strconv.Atoi(retryRaw); err == nil {
			*retriesFlag = retryVal
		}
	}
	if *intervalFlag == "" {
		*intervalFlag = promptInput(reader, "Distillation interval", "10m")
	}
}

func promptInput(reader *bufio.Reader, label, defaultValue string) string {
	if defaultValue != "" {
		fmt.Printf("? %s [%s]: ", label, defaultValue)
	} else {
		fmt.Printf("? %s: ", label)
	}
	text, _ := reader.ReadString('\n')
	text = strings.TrimSpace(text)
	if text == "" {
		return defaultValue
	}
	return text
}

func promptChoice(reader *bufio.Reader, label string, options []string, defaultValue string) string {
	fmt.Printf("? %s:\n", label)
	for _, opt := range options {
		fmt.Printf("  - %s\n", opt)
	}
	return promptInput(reader, label, defaultValue)
}

func promptModel(reader *bufio.Reader, provider string) string {
	switch provider {
	case "anthropic":
		fmt.Println("? Select model:")
		fmt.Println("  - claude-haiku-4-5-20251001 (recommended: fastest, budget-friendly)")
		fmt.Println("  - claude-sonnet-4-6 (balanced)")
		fmt.Println("  - claude-opus-4-6 (most capable, slowest)")
		return promptInput(reader, "Model", "claude-haiku-4-5-20251001")
	case "openai":
		fmt.Println("? Select model:")
		fmt.Println("  - gpt-4o (default)")
		fmt.Println("  - gpt-4-turbo")
		fmt.Println("  - gpt-3.5-turbo (budget)")
		return promptInput(reader, "Model", "gpt-4o")
	case "ollama":
		return promptInput(reader, "Model", "llama3:latest")
	case "forgememo", "forge":
		return promptInput(reader, "Model", "claude-haiku-4-5-20251001")
	default:
		return defaultModelForProvider(provider)
	}
}

func defaultModelForProvider(provider string) string {
	switch provider {
	case "forgememo", "forge":
		return "claude-haiku-4-5-20251001"
	case "anthropic":
		return "claude-haiku-4-5-20251001"
	case "openai":
		return "gpt-4o"
	case "ollama":
		return "llama3:latest"
	default:
		return "claude-haiku-4-5-20251001"
	}
}

func distillConfigFromUserConfig(cfg config.Config) distill.Config {
	d := distill.LoadConfig()
	d.Provider = distill.Provider(cfg.Provider)
	if d.Provider == "forge" {
		d.Provider = distill.ProviderForgememo
	}
	d.APIKey = cfg.APIKey
	d.Model = cfg.Model
	if cfg.BaseURL != "" {
		d.BaseURL = cfg.BaseURL
	}
	if cfg.Timeout != "" {
		if timeout, err := time.ParseDuration(cfg.Timeout); err == nil {
			d.Timeout = timeout
		}
	}
	if cfg.Retries > 0 {
		d.Retries = cfg.Retries
	}
	return d
}

func maskKey(key string) string {
	if key == "" {
		return ""
	}
	if len(key) <= 8 {
		return "***"
	}
	return key[:4] + "****" + key[len(key)-4:]
}

func openBrowser(url string) {
	var args []string
	switch runtime.GOOS {
	case "darwin":
		args = []string{"open", url}
	case "windows":
		args = []string{"cmd", "/c", "start", url}
	default:
		args = []string{"xdg-open", url}
	}
	exec.Command(args[0], args[1:]...).Start()
}

func runLogin(args []string) {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	emailFlag := fs.String("email", "", "Email address")
	passwordFlag := fs.String("password", "", "Password")
	purchaseFlag := fs.Bool("purchase", false, "Purchase credits after login")
	signupFlag := fs.Bool("signup", false, "Open signup page")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	paymentURL := os.Getenv("FORGE_PAYMENT_URL")
	if paymentURL == "" {
		paymentURL = "http://localhost:3000"
	}

	if *signupFlag {
		fmt.Println("Opening Forge signup page...")
		openBrowser("https://forge.sh/signup")
		return
	}

	if *emailFlag == "" {
		fmt.Println("Usage: forge login [--email USER --password PASS] [--purchase] [--signup]")
		fmt.Println("")
		fmt.Println("Login to Forge to access your credits and API.")
		fmt.Println("")
		fmt.Println("Options:")
		fmt.Println("  --email     Email address")
		fmt.Println("  --password  Password")
		fmt.Println("  --purchase  Purchase credits after login")
		fmt.Println("  --signup    Open signup page in browser")
		fmt.Println("")
		fmt.Println("Don't have an account? Run: forge login --signup")
		os.Exit(0)
	}

	type loginReq struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	body, _ := json.Marshal(loginReq{Email: *emailFlag, Password: *passwordFlag})
	resp, err := http.Post(paymentURL+"/api/login", "application/json", bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	var result struct {
		Success bool   `json:"success"`
		Message string `json:"message,omitempty"`
		Data    struct {
			Token   string `json:"token"`
			APIKey  string `json:"api_key"`
			Credits int    `json:"credits"`
		} `json:"data,omitempty"`
	}
	json.NewDecoder(resp.Body).Decode(&result)

	if !result.Success {
		fmt.Fprintf(os.Stderr, "Error: %s\n", result.Message)
		os.Exit(1)
	}

	cfg := config.Config{
		Provider: "forgememo",
		APIKey:   result.Data.APIKey,
	}
	config.Save(cfg)

	fmt.Printf("Logged in as %s\n", *emailFlag)
	fmt.Printf("Credits: %d\n", result.Data.Credits)

	if *purchaseFlag || result.Data.Credits < 5 {
		checkoutResp, _ := http.NewRequest("POST", paymentURL+"/api/checkout", bytes.NewReader(nil))
		checkoutResp.Header.Set("Authorization", result.Data.Token)
		client := &http.Client{}
		res, _ := client.Do(checkoutResp)
		var checkout struct {
			Data struct {
				URL          string `json:"url"`
				CreditAmount int    `json:"credit_amount"`
				PriceCents   int    `json:"price_cents"`
			} `json:"data"`
		}
		json.NewDecoder(res.Body).Decode(&checkout)
		if checkout.Data.URL != "" {
			price := float64(checkout.Data.PriceCents) / 100
			fmt.Printf("\nOpening Stripe to purchase %d credits for $%.2f...\n", checkout.Data.CreditAmount, price)
			openBrowser(checkout.Data.URL)
			fmt.Println("After payment, credits will be added automatically.")
		}
	}
}

// These functions are imported from daemon.go via package main
// forgeHome, readAddr, isDaemonAlive, cleanAddr, cleanPID
