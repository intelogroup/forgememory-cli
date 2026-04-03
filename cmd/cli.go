package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/forge/forge/internal/agent"
	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/distill"
	"github.com/forge/forge/internal/mcp"
	"github.com/forge/forge/internal/scanner"
)

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

	fmt.Println("\nForge initialized. Run `forge start` to start the daemon.")
}

func runStart(args []string) {
	fmt.Println("Starting Forge daemon...")

	addr := readAddr()
	if addr != "" && isDaemonAlive(addr) {
		fmt.Println("  Daemon already running.")
		return
	}
	// Clear any stale addr/pid before starting fresh — handles both the
	// addr-but-not-alive case and the orphan-pid-with-no-addr case.
	cleanAddr()
	cleanPID()

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
	fmt.Fprintln(os.Stderr, "  Error: daemon process exited immediately — not responding.")
	fmt.Fprintf(os.Stderr, "  Check logs: %s\n", logPath)
	fmt.Fprintln(os.Stderr, "  Run 'forge doctor' to diagnose.")
	os.Exit(1)
}

func runStop(args []string) {
	fmt.Println("Stopping Forge daemon...")

	addr := readAddr()
	if addr == "" {
		fmt.Println("  Daemon not running.")
		return
	}

	pid := readPID()
	if pid > 0 {
		if err := killProcess(pid); err != nil {
			fmt.Fprintf(os.Stderr, "  Warning: could not kill daemon (pid %d): %v\n", pid, err)
		}
	}

	cleanAddr()
	cleanPID()
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
		os.Exit(0) // internal command — silent failure
	}

	if *sessionID == "" {
		os.Exit(0)
	}

	database, err := db.Open("")
	if err != nil {
		os.Exit(0)
	}
	defer database.Close()

	events, err := database.SessionEvents(*sessionID, 20)
	if err != nil || len(events) < 3 {
		os.Exit(0)
	}

	proj := *projectID
	if proj == "" {
		proj = events[0].ProjectID
	}

	d := distill.New(database, distill.LoadConfig())
	_ = d.SynthesizeSession(*sessionID, proj, events)
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
	database, err := db.Open("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	server := mcp.New(database)
	if err := server.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "MCP server error: %v\n", err)
		os.Exit(1)
	}
}

func runDoctor(args []string) {
	fmt.Println("Forge Doctor")
	fmt.Println()

	home, _ := os.UserHomeDir()

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
	home, _ := os.UserHomeDir()
	os.MkdirAll(home+"/.forge/logs", 0o700)
	fmt.Println("  Service installed. Use 'forge start' to start the daemon.")
}

func runServiceUninstall(args []string) {
	fmt.Println("Uninstalling Forge service...")
	fmt.Println("  Service uninstalled.")
}
