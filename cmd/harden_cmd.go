package main

import (
	"fmt"
	"os"

	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/security"
)

func runHarden(args []string) {
	if len(args) < 1 {
		printHardenUsage()
		os.Exit(1)
	}

	subcommand := args[0]
	subArgs := args[1:]

	switch subcommand {
	case "rotate-key":
		runHardenRotateKey()
	case "revoke":
		runHardenRevoke(subArgs)
	default:
		fmt.Fprintf(os.Stderr, "Unknown harden subcommand: %s\n", subcommand)
		printHardenUsage()
		os.Exit(1)
	}
}

func printHardenUsage() {
	fmt.Print(`forge harden — key lifecycle and principle revocation

Usage:
  forge harden <subcommand> [args]

Subcommands:
  rotate-key       Generate a new signing key and re-sign every stored principle
  revoke <id>      Mark a principle revoked; excluded from all reads even with a valid signature
`)
}

func runHardenRotateKey() {
	if security.Disabled() {
		fmt.Fprintln(os.Stderr, "Error: signing key feature is disabled (FORGE_DISABLE_KEY set) — nothing to rotate")
		os.Exit(1)
	}
	database, err := db.Open("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to open DB: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	oldKey, _, err := security.GetOrCreateKey()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to read current key before rotating: %v\n", err)
		os.Exit(1)
	}

	newKey, err := security.RotateKey()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to rotate key: %v\n", err)
		os.Exit(1)
	}

	resigned, quarantined, err := database.ResignAllPrinciples(oldKey, newKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: key rotated but re-signing failed: %v\n", err)
		fmt.Fprintln(os.Stderr, "Principles signed under the old key will fail verification on next read.")
		os.Exit(1)
	}

	fmt.Printf("Rotated signing key and re-signed %d principle(s).\n", resigned)
	if quarantined > 0 {
		fmt.Printf("Quarantined %d principle(s) that failed verification under the old key (revoked, not re-signed) — these were tampered before rotation and won't appear in `forge memory list` or agent context again.\n", quarantined)
	}
}

func runHardenRevoke(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: forge harden revoke <principle-id>")
		os.Exit(1)
	}
	id := args[0]

	database, err := db.Open("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to open DB: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	if err := database.RevokePrinciple(id); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to revoke principle: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Revoked principle %s — it will no longer be injected or returned by memory search.\n", id)
}
