package main

import (
	"fmt"
	"os"

	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/mcp"
)

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
		fmt.Fprintln(os.Stderr, "Starting Forge daemon for MCP server...")
		for _, item := range result.cleanup {
			fmt.Fprintf(os.Stderr, "  Cleaning stale %s...\n", item)
		}
		fmt.Fprintln(os.Stderr, "  Daemon started.")
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
