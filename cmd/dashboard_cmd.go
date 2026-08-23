package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/forge/forge/internal/dashboard"
	"github.com/forge/forge/internal/db"
)

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
