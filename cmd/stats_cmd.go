package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/distill"
	"github.com/forge/forge/internal/funstats"
	"github.com/forge/forge/internal/profile"
	"github.com/forge/forge/internal/steering"
)

type statsResult struct {
	archetype   string
	peak        int
	top         []string
	concurrent  int
	steer       steering.Stats
	steeringTag string
	vibeScore   float64
	totalRanges int
}

// runStats prints cheap fun aggregates: peak working hour, top prompt
// keywords, agent-parallelism, and a rule-based archetype label.
func runStats(args []string) {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	path := fs.String("path", "", "repo path (default: current directory)")
	all := fs.Bool("all", false, "run across all known projects")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	database, err := db.Open("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	if *all {
		projects, err := knownProjects(database)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		if len(projects) == 0 {
			fmt.Println("No known projects found.")
			return
		}
		var scoredCount int
		var sumVibe float64
		for _, p := range projects {
			res, hasSessions, err := statsOne(database, p.ID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Skipping %s: %v\n", p.ID, err)
				continue
			}
			if !hasSessions {
				fmt.Printf("%s: no sessions found.\n\n", p.ID)
				continue
			}
			printStats(p.ID, res)
			fmt.Println()
			scoredCount++
			sumVibe += res.vibeScore
		}
		if scoredCount == 0 {
			return
		}
		fmt.Printf("Aggregate across %d projects — Engineer score: %.0f%%\n",
			scoredCount, sumVibe/float64(scoredCount))
		return
	}

	repoPath := *path
	if repoPath == "" {
		repoPath, _ = os.Getwd()
	}
	projectID := detectProjectIDForPath(repoPath)

	res, hasSessions, err := statsOne(database, projectID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if !hasSessions {
		fmt.Printf("No sessions found for %s.\n", projectID)
		return
	}
	printStats(projectID, res)
}

func statsOne(database *db.DB, projectID string) (statsResult, bool, error) {
	ranges, err := database.SessionRangesByProject(projectID)
	if err != nil {
		return statsResult{}, false, err
	}
	if len(ranges) == 0 {
		return statsResult{}, false, nil
	}

	var all []db.Event
	var steer steering.Stats
	var verifiedSessions int
	for _, r := range ranges {
		events, err := database.SessionEventsUpTo(r.SessionID, "", 0)
		if err != nil {
			return statsResult{}, false, err
		}
		all = append(all, events...)
		steer.Add(steering.Compute(events))
		if _, hasTestRun := profile.ClassifyEvents(events); hasTestRun {
			verifiedSessions++
		}
	}

	peak := funstats.PeakHour(all)
	top := funstats.TopKeywords(all, 5, distill.ExtractPromptText)
	concurrent := funstats.MaxConcurrentSessions(ranges)
	archetype := funstats.Archetype(peak, steer.Rate(), concurrent)

	sigs, err := database.FailureSignaturesByProject(projectID)
	if err != nil {
		return statsResult{}, false, err
	}
	errProfile := funstats.ComputeErrorProfile(sigs)
	verifiedRate := float64(verifiedSessions) / float64(len(ranges))
	vibeScore := funstats.VibeCoderScore(steer.Rate(), verifiedRate, errProfile)
	steeringTag := funstats.SteeringLabel(steer.Rate(), errProfile)

	return statsResult{
		archetype:   archetype,
		peak:        peak,
		top:         top,
		concurrent:  concurrent,
		steer:       steer,
		steeringTag: steeringTag,
		vibeScore:   vibeScore,
		totalRanges: len(ranges),
	}, true, nil
}

func printStats(projectID string, res statsResult) {
	fmt.Printf("%s fun stats\n\n", projectID)
	fmt.Printf("  Archetype:         %s\n", res.archetype)
	fmt.Printf("  Peak hour (UTC):   %02d:00\n", res.peak)
	fmt.Printf("  Top prompt words:  %v\n", res.top)
	fmt.Printf("  Max concurrent sessions: %d\n", res.concurrent)
	fmt.Printf("  Steering rate:     %.0f%% (%d/%d prompts) [%s]\n", res.steer.Rate()*100, res.steer.SteeredPrompts, res.steer.TotalPrompts, res.steeringTag)
	fmt.Printf("  Engineer score:    %.0f%% (vibe-coder: %.0f%%)\n", res.vibeScore, 100-res.vibeScore)
}
