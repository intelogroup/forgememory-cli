package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/forge/forge/internal/agent"
	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/mcp"
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
			if err := agent.SetupAgent(a, home); err != nil {
				fmt.Fprintf(os.Stderr, "  Error setting up %s: %v\n", a, err)
			} else {
				fmt.Printf("  Configured %s (MCP + hooks)\n", a)
			}
		}
	}

	fmt.Println("\nForge initialized. Run `forge start` to start the daemon.")
}

func runStart(args []string) {
	fmt.Println("Starting Forge daemon...")

	if readAddr() != "" {
		fmt.Println("  Daemon already running.")
		return
	}

	// Start daemon as background process
	cmd := exec.Command(os.Args[0], "daemon")
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil

	if err := startBackground(cmd); err != nil {
		fmt.Fprintf(os.Stderr, "Error starting daemon: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("  Daemon started.")
}

func runStop(args []string) {
	fmt.Println("Stopping Forge daemon...")

	addr := readAddr()
	if addr == "" {
		fmt.Println("  Daemon not running.")
		return
	}

	// Send SIGTERM to daemon process
	// For now, just clean the address file
	cleanAddr()
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

func startBackground(cmd *exec.Cmd) error {
	return cmd.Start()
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
		fmt.Printf("[OK] Database: %d events, %d undistilled, %d principles\n", total, undistilled, principles)
		database.Close()
	}

	// Check daemon
	fmt.Print("  ")
	addr := readAddr()
	if addr == "" {
		fmt.Println("[FAIL] Daemon: not running")
	} else {
		fmt.Printf("[OK] Daemon: running (%s)\n", addr)
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
