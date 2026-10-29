package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/forge/forge/internal/db"
)

func runMemory(args []string) {
	if len(args) < 1 {
		printMemoryUsage()
		os.Exit(1)
	}

	subcommand := args[0]
	subArgs := args[1:]

	database, err := db.Open("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to open DB: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	switch subcommand {
	case "list":
		runMemoryList(database, subArgs)
	case "delete", "remove":
		runMemoryDelete(database, subArgs)
	case "rate":
		runMemoryRate(database, subArgs)
	case "thumbs-up":
		runMemoryThumbsUp(database, subArgs)
	case "thumbs-down":
		runMemoryThumbsDown(database, subArgs)
	case "history", "log":
		runMemoryHistory(database, subArgs)
	default:
		fmt.Fprintf(os.Stderr, "Unknown memory subcommand: %s\n", subcommand)
		printMemoryUsage()
		os.Exit(1)
	}
}

func printMemoryUsage() {
	fmt.Print(`forge memory — manage distilled principles in memory

Usage:
  forge memory <subcommand> [args]

Subcommands:
  list [--project <id>] [--limit <N>]   List active principles
  delete <id>                           Permanently delete a principle
  rate <id> <score>                     Update the impact score (0.0 to 1.0) of a principle
  thumbs-up <id>                        Boost a principle's impact score by +0.2
  thumbs-down <id>                      Reduce score by -0.2 (deletes if < 0.2)
  history [<id>] [--limit N] [--json]   Show consolidation audit rows (all, or one principle)
`)
}

func runMemoryList(database *db.DB, args []string) {
	fs := flag.NewFlagSet("memory list", flag.ContinueOnError)
	projectFlag := fs.String("project", "", "Filter by project ID")
	limitFlag := fs.Int("limit", 20, "Limit the number of principles shown")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	var principles []db.Principle
	var err error
	if *projectFlag != "" {
		principles, err = database.RecentPrinciplesByProject(*projectFlag, *limitFlag)
	} else {
		principles, err = database.RecentActivePrinciples(*limitFlag)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if len(principles) == 0 {
		fmt.Println("No principles found.")
		return
	}

	fmt.Printf("%-36s %-12s %-6s %-20s %s\n", "ID", "TYPE", "SCORE", "PROJECT", "TITLE")
	fmt.Println(strings.Repeat("-", 100))
	for _, p := range principles {
		proj := p.ProjectID
		if len(proj) > 18 {
			proj = proj[:15] + "..."
		}
		title := p.Title
		if len(title) > 40 {
			title = title[:37] + "..."
		}
		fmt.Printf("%-36s %-12s %-6.2f %-20s %s\n", p.ID, p.Type, p.ImpactScore, proj, title)
		fmt.Printf("  Narrative: %s\n", p.Narrative)
		if p.LastUsedTS != "" {
			fmt.Printf("  Last Used: %s (used %d times)\n", p.LastUsedTS, p.UseCount)
		}
		fmt.Println()
	}
}

func runMemoryHistory(database *db.DB, args []string) {
	fs := flag.NewFlagSet("memory history", flag.ContinueOnError)
	limitFlag := fs.Int("limit", 50, "Limit the number of audit rows shown")
	jsonFlag := fs.Bool("json", false, "Emit JSON")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}

	targetID := fs.Arg(0)
	ops, err := database.MemoryOps(targetID, *limitFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if len(ops) == 0 {
		fmt.Println("No consolidation history recorded.")
		return
	}

	if *jsonFlag {
		data, _ := json.MarshalIndent(ops, "", "  ")
		fmt.Println(string(data))
		return
	}

	fmt.Printf("%-20s %-8s %-36s %s\n", "WHEN", "OP", "TARGET", "REASON")
	fmt.Println(strings.Repeat("-", 100))
	for _, op := range ops {
		reason := op.Reason
		if len(reason) > 40 {
			reason = reason[:37] + "..."
		}
		fmt.Printf("%-20s %-8s %-36s %s\n", op.TS, op.Op, op.TargetID, reason)
		if op.BatchID != "" {
			fmt.Printf("  batch=%s model=%s\n", op.BatchID, op.Model)
		}
	}
}

func runMemoryDelete(database *db.DB, args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: forge memory delete <id>")
		os.Exit(1)
	}
	id := args[0]
	err := database.DeletePrinciple(id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Principle %s deleted successfully.\n", id)
}

func runMemoryRate(database *db.DB, args []string) {
	if len(args) < 2 {
		fmt.Println("Usage: forge memory rate <id> <score>")
		os.Exit(1)
	}
	id := args[0]
	score, err := strconv.ParseFloat(args[1], 64)
	if err != nil || score < 0.0 || score > 1.0 {
		fmt.Fprintln(os.Stderr, "Error: score must be a number between 0.0 and 1.0")
		os.Exit(1)
	}

	err = database.UpdatePrincipleScore(id, score)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Principle %s score updated to %.2f.\n", id, score)
}

func runMemoryThumbsUp(database *db.DB, args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: forge memory thumbs-up <id>")
		os.Exit(1)
	}
	id := args[0]
	p, err := database.GetPrincipleByID(id)
	if err != nil || p == nil {
		fmt.Fprintf(os.Stderr, "Error: principle not found: %v\n", err)
		os.Exit(1)
	}

	newScore := p.ImpactScore + 0.2
	if newScore > 1.0 {
		newScore = 1.0
	}

	err = database.UpdatePrincipleScore(id, newScore)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Principle %s score boosted to %.2f.\n", id, newScore)
}

func runMemoryThumbsDown(database *db.DB, args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: forge memory thumbs-down <id>")
		os.Exit(1)
	}
	id := args[0]
	p, err := database.GetPrincipleByID(id)
	if err != nil || p == nil {
		fmt.Fprintf(os.Stderr, "Error: principle not found: %v\n", err)
		os.Exit(1)
	}

	newScore := p.ImpactScore - 0.2
	if newScore < 0.2 {
		err = database.DeletePrinciple(id)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Principle %s score fell below 0.2 and was auto-deleted.\n", id)
	} else {
		err = database.UpdatePrincipleScore(id, newScore)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Principle %s score reduced to %.2f.\n", id, newScore)
	}
}
