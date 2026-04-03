package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	agents := agent.DetectAgents(home)
	if len(agents) == 0 {
		fmt.Println("  No agents detected. Install Claude Code, Gemini CLI, or Codex first.")
	} else {
		fmt.Printf("  Detected agents: %v\n", agents)
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
	}

	// Prompt for provider setup
	fmt.Println("\n  Configure your AI provider:")
	fmt.Println("    forge config --provider anthropic --api-key YOUR_KEY")
	fmt.Println("  Or run 'forge config' for interactive setup.")
	fmt.Println("  Default provider is ollama (requires local Ollama running).")

	// Show MCP restart notice for Claude Code
	if contains(agents, "claude") {
		fmt.Println("\n  NOTE: Claude Code requires restart to pick up MCP changes.")
		fmt.Println("  Run 'forge start' to start the daemon, then restart Claude Code.")
	}

	fmt.Println("\nForge initialized. Run `forge start` to start the daemon.")
}

func runStart(args []string) {
	fmt.Println("Starting Forge daemon...")

	addr := readAddr()
	if addr != "" && isDaemonAlive(addr) {
		fmt.Println("  Daemon already running.")
		return
	}

	// Clear any stale daemon state before starting fresh — handles the
	// case where daemon crashed/force-killed and left behind lock/addr/pid/socket.
	// Only remove if the referenced process is not alive.
	if addr != "" && !isDaemonAlive(addr) {
		fmt.Println("  Cleaning stale daemon address...")
		cleanAddr()
	}
	if pid := readPID(); pid > 0 && !isProcessAlive(pid) {
		fmt.Println("  Cleaning stale daemon PID...")
		cleanPID()
	}
	if isStaleLock() {
		fmt.Println("  Cleaning stale daemon lock...")
		cleanLock()
	}
	// Clean up any stale socket file
	if isStaleSocket() {
		fmt.Println("  Cleaning stale daemon socket...")
		cleanSocket()
	}

	// Start daemon as background process, redirecting its output to a log file
	// so crashes are diagnosable without attaching a debugger.
	forgeDir := filepath.Join(forgeHome(), ".forge")
	_ = os.MkdirAll(forgeDir, 0o700) // ensure dir exists before opening log
	logPath := filepath.Join(forgeDir, "daemon.log")
	cmd := exec.Command(os.Args[0], "daemon")
	cmd.Stdin = nil
	if lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		cmd.Stdout = lf
		cmd.Stderr = lf
		defer lf.Close()
	}

	if err := startBackground(cmd); err != nil {
		fmt.Fprintf(os.Stderr, "Error starting daemon: %v\n", err)
		os.Exit(1)
	}

	// Wait up to 3 seconds for daemon to write its addr and start accepting.
	// On Windows, the process may be killed by the parent job object if not
	// properly detached — we catch that here before claiming success.
	for i := 0; i < 15; i++ {
		time.Sleep(200 * time.Millisecond)
		if a := readAddr(); a != "" && isDaemonAlive(a) {
			fmt.Println("  Daemon started.")
			return
		}
	}

	// Daemon failed to start — run inline diagnostics
	fmt.Fprintln(os.Stderr, "  Error: daemon process exited immediately — not responding.")
	fmt.Fprintln(os.Stderr, "\n  Running diagnostics...")
	runDoctorInline()
	fmt.Fprintln(os.Stderr, "\n  Run 'forge doctor --repair' to auto-fix stale state.")
	os.Exit(1)
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

	var ids []string
	for _, e := range events {
		ids = append(ids, e.ID)
	}
	if err := database.MarkDistilled(ids); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Distilled %d events.\n", len(ids))
}

func runSearch(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: forge search <query>")
		os.Exit(1)
	}

	database, err := db.Open("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	query := args[0]
	events, err := database.SearchEvents(query, 10)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if len(events) == 0 {
		fmt.Println("No results.")
		return
	}

	for _, e := range events {
		fmt.Printf("[%s] %s (%s)\n", e.TS[:10], e.EventType, e.SourceTool)
		fmt.Printf("  %s\n", truncate(e.Payload, 200))
		fmt.Println()
	}
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
	// Check if daemon is running, start it if not
	addr := readAddr()
	if addr == "" || !isDaemonAlive(addr) {
		fmt.Println("Starting Forge daemon for MCP server...")

		// Clear any stale addr/pid before starting fresh
		cleanAddr()
		cleanPID()

		// Start daemon as background process
		forgeDir := filepath.Join(forgeHome(), ".forge")
		_ = os.MkdirAll(forgeDir, 0o700) // ensure dir exists before opening log
		logPath := filepath.Join(forgeDir, "daemon.log")
		cmd := exec.Command(os.Args[0], "daemon")
		cmd.Stdin = nil
		if lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
			cmd.Stdout = lf
			cmd.Stderr = lf
			defer lf.Close()
		}

		if err := startBackground(cmd); err != nil {
			fmt.Fprintf(os.Stderr, "Error starting daemon: %v\n", err)
			os.Exit(1)
		}

		// Wait up to 3 seconds for daemon to write its addr and start accepting.
		for i := 0; i < 15; i++ {
			time.Sleep(200 * time.Millisecond)
			if a := readAddr(); a != "" && isDaemonAlive(a) {
				fmt.Println("  Daemon started.")
				break
			}
			if i == 14 { // Last attempt
				fmt.Fprintln(os.Stderr, "  Error: daemon process exited immediately — not responding.")
				fmt.Fprintf(os.Stderr, "  Check logs: %s\n", logPath)
				fmt.Fprintln(os.Stderr, "  Run 'forge doctor' to diagnose.")
				os.Exit(1)
			}
		}
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

	home, _ := os.UserHomeDir()

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
	if addr == "" {
		fmt.Println("[FAIL] Daemon: not running")
	} else if isDaemonAlive(addr) {
		fmt.Printf("[OK] Daemon: running (%s)\n", addr)
	} else {
		fmt.Printf("[FAIL] Daemon: stale addr file (%s) — daemon not responding\n", addr)
	}
	logPath := filepath.Join(forgeHome(), ".forge", "daemon.log")
	if info, err := os.Stat(logPath); err == nil {
		fmt.Printf("  [OK] Daemon log: %s (%d bytes)\n", logPath, info.Size())
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
	home, _ := os.UserHomeDir()

	fmt.Println("  --- Diagnostics ---")

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
	if isStaleSocket() {
		fmt.Println("  Daemon: stale socket file")
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

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func detectProjectID() string {
	if out, err := execCommand("git", "rev-parse", "--show-toplevel"); err == nil {
		return strings.TrimSpace(out)
	}
	cwd, _ := os.Getwd()
	return cwd
}

func runServiceInstall(args []string) {
	fmt.Println("Installing Forge as system service...")

	mgr, err := service.New()
	if err != nil {
		fmt.Fprintf(os.Stderr, "  Error: %v\n", err)
		os.Exit(1)
	}

	if mgr.IsServiceInstalled() {
		fmt.Println("  Service already installed.")
		return
	}

	if err := mgr.Install(); err != nil {
		fmt.Fprintf(os.Stderr, "  Error installing service: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("  Service installed successfully.")
	fmt.Println("  Run 'forge service-start' to start the daemon as a service.")
}

func runServiceUninstall(args []string) {
	fmt.Println("Uninstalling Forge service...")

	mgr, err := service.New()
	if err != nil {
		fmt.Fprintf(os.Stderr, "  Error: %v\n", err)
		os.Exit(1)
	}

	if !mgr.IsServiceInstalled() {
		fmt.Println("  Service not installed.")
		return
	}

	if err := mgr.Uninstall(); err != nil {
		fmt.Fprintf(os.Stderr, "  Error uninstalling service: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("  Service uninstalled successfully.")
}

func runServiceStart(args []string) {
	fmt.Println("Starting Forge service...")

	mgr, err := service.New()
	if err != nil {
		fmt.Fprintf(os.Stderr, "  Error: %v\n", err)
		os.Exit(1)
	}

	if err := mgr.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "  Error starting service: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("  Service started.")
}

func runServiceStop(args []string) {
	fmt.Println("Stopping Forge service...")

	mgr, err := service.New()
	if err != nil {
		fmt.Fprintf(os.Stderr, "  Error: %v\n", err)
		os.Exit(1)
	}

	if err := mgr.Stop(); err != nil {
		fmt.Fprintf(os.Stderr, "  Error stopping service: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("  Service stopped.")
}

func runConfig(args []string) {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	showFlag := fs.Bool("show", false, "Show current config")
	providerFlag := fs.String("provider", "", "Provider: anthropic/openai/ollama")
	apiKeyFlag := fs.String("api-key", "", "API key for provider")
	modelFlag := fs.String("model", "", "Model name (optional, defaults vary by provider)")
	baseURLFlag := fs.String("base-url", "", "Base URL for API (optional)")
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
			fmt.Println("No config set. Run 'forge config --provider openai --api-key YOUR_KEY' to configure.")
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
		return
	}

	if *providerFlag == "" {
		fmt.Println("Usage: forge config [options]")
		fmt.Println("")
		fmt.Println("Options:")
		fmt.Println("  --show           Show current configuration")
		fmt.Println("  --provider       Provider: anthropic, openai, or ollama")
		fmt.Println("  --api-key        API key for the provider")
		fmt.Println("  --model          Model name (optional)")
		fmt.Println("  --base-url       Base URL for API (optional)")
		fmt.Println("")
		fmt.Println("Examples:")
		fmt.Println("  forge config --show")
		fmt.Println("  forge config --provider openai --api-key sk-...")
		fmt.Println("  forge config --provider anthropic --api-key sk-ant-...")
		fmt.Println("  forge config --provider ollama --model llama3.2")
		os.Exit(0)
	}

	validProviders := map[string]bool{"anthropic": true, "openai": true, "ollama": true}
	if !validProviders[*providerFlag] {
		fmt.Fprintf(os.Stderr, "Error: provider must be one of: anthropic, openai, ollama\n")
		os.Exit(1)
	}

	cfg := config.Config{
		Provider: *providerFlag,
		APIKey:   *apiKeyFlag,
		Model:    *modelFlag,
		BaseURL:  *baseURLFlag,
	}

	if err := config.Save(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Error saving config: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Configuration saved.\n")
	fmt.Printf("Provider: %s\n", *providerFlag)
	if *apiKeyFlag != "" {
		fmt.Println("API Key: " + maskKey(*apiKeyFlag))
	}
	fmt.Println("\nTo apply, either:")
	fmt.Println("  1. Run: export $(cat ~/.forge/config | xargs)  # in your shell")
	fmt.Println("  2. Or add to your shell profile (~/.zshrc, ~/.bashrc)")
}

func maskKey(key string) string {
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
		Provider: "forge",
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
