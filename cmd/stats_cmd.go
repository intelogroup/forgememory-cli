package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/distill"
	"github.com/forge/forge/internal/funstats"
	"github.com/forge/forge/internal/steering"
)

// runStats prints cheap fun aggregates: peak working hour, top prompt
// keywords, agent-parallelism, and a rule-based archetype label.
func runStats(args []string) {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
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

	var all []db.Event
	var steer steering.Stats
	for _, r := range ranges {
		events, err := database.SessionEventsUpTo(r.SessionID, "", 0)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		all = append(all, events...)
		steer.Add(steering.Compute(events))
	}

	peak := funstats.PeakHour(all)
	top := funstats.TopKeywords(all, 5, distill.ExtractPromptText)
	concurrent := funstats.MaxConcurrentSessions(ranges)
	archetype := funstats.Archetype(peak, steer.Rate(), concurrent)

	fmt.Printf("%s fun stats\n\n", projectID)
	fmt.Printf("  Archetype:         %s\n", archetype)
	fmt.Printf("  Peak hour (UTC):   %02d:00\n", peak)
	fmt.Printf("  Top prompt words:  %v\n", top)
	fmt.Printf("  Max concurrent sessions: %d\n", concurrent)
	fmt.Printf("  Steering rate:     %.0f%% (%d/%d prompts)\n", steer.Rate()*100, steer.SteeredPrompts, steer.TotalPrompts)
}
