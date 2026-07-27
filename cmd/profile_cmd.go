package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/distill"
	"github.com/forge/forge/internal/profile"
	"github.com/forge/forge/internal/steering"
	"github.com/forge/forge/internal/streams"
)

// runProfile scores a builder on 5 axes using local commit/stream/steering
// signals and session summaries already produced by forge — a single LLM
// call over locally-derived evidence, never raw payloads.
func runProfile(args []string) {
	fs := flag.NewFlagSet("profile", flag.ContinueOnError)
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

	sig := profile.Signals{ProjectID: projectID}

	commitSummaries, err := database.SessionCommitsByProject(projectID, 1000)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	for _, c := range commitSummaries {
		sig.TotalCommits += c.Commits
		sig.TotalInsertions += c.Insertions
		sig.TotalDeletions += c.Deletions
	}

	ranges, err := database.SessionRangesByProject(projectID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if len(ranges) == 0 {
		fmt.Printf("No sessions found for %s.\n", projectID)
		return
	}
	ws, err := streams.Cluster(ranges, streams.DefaultGap)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	sig.WorkStreams = len(ws)

	for _, r := range ranges {
		events, err := database.SessionEventsUpTo(r.SessionID, "", 0)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		st := steering.Compute(events)
		sig.TotalPrompts += st.TotalPrompts
		sig.SteeredPrompts += st.SteeredPrompts
	}

	summaries, err := database.GetRecentSessionSummariesByProject(projectID, 10)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	for _, s := range summaries {
		if s.Learnings != "" {
			sig.SessionNotes = append(sig.SessionNotes, s.Learnings)
		} else if s.Summary != "" {
			sig.SessionNotes = append(sig.SessionNotes, s.Summary)
		}
	}

	cfg := distill.LoadConfig()
	d := distill.New(database, cfg)
	resp, err := d.CallLLM(profile.BuildPrompt(sig))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error calling LLM (%v). Configure a provider: forge config --provider <name> --api-key <key>\n", err)
		os.Exit(1)
	}

	scores, err := profile.ParseScores(resp)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing model response: %v\nRaw response:\n%s\n", err, resp)
		os.Exit(1)
	}

	fmt.Printf("%s builder profile (%d commits, %d work streams, %d/%d prompts steered)\n\n",
		projectID, sig.TotalCommits, sig.WorkStreams, sig.SteeredPrompts, sig.TotalPrompts)
	printAxis := func(name string, a profile.Axis) {
		fmt.Printf("  %-17s %d/10  %s\n", name, a.Score, a.Why)
	}
	printAxis("Steering", scores.Steering)
	printAxis("Execution", scores.Execution)
	printAxis("Engineering", scores.Engineering)
	printAxis("Product instinct", scores.ProductInstinct)
	printAxis("Planning", scores.Planning)
}
