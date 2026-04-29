package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/distill"
	"github.com/forge/forge/internal/scanner"
)

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
