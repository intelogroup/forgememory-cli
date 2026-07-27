package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/steering"
)

// runSteering reports how often the user redirected the agent mid-task
// (a new prompt before the agent's prior turn stopped) per session.
func runSteering(args []string) {
	fs := flag.NewFlagSet("steering", flag.ContinueOnError)
	path := fs.String("path", "", "repo path (default: current directory)")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	repoPath := *path
	if repoPath == "" {
		repoPath, _ = os.Getwd()
	}
	projectID := detectProjectIDForPath(repoPath)

	database, err := db.Open("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	ranges, err := database.SessionRangesByProject(projectID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if len(ranges) == 0 {
		fmt.Printf("No sessions found for %s.\n", projectID)
		return
	}

	var total steering.Stats
	fmt.Printf("%s: steering rate per session\n\n", projectID)
	for _, r := range ranges {
		events, err := database.SessionEventsUpTo(r.SessionID, "", 0)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		s := steering.Compute(events)
		total.Add(s)
		if s.TotalPrompts == 0 {
			continue
		}
		fmt.Printf("  %s: %d/%d prompts steered (%.0f%%)\n", r.SessionID, s.SteeredPrompts, s.TotalPrompts, s.Rate()*100)
	}
	fmt.Printf("\nOverall: %d/%d prompts steered (%.0f%%)\n", total.SteeredPrompts, total.TotalPrompts, total.Rate()*100)
}
