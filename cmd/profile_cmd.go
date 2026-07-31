package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/distill"
	forgegit "github.com/forge/forge/internal/git"
	"github.com/forge/forge/internal/profile"
	"github.com/forge/forge/internal/steering"
	"github.com/forge/forge/internal/streams"
)

const maxProfilePromptSamples = 8

// runProfile scores a builder on 5 axes using local commit-hygiene, tool-use,
// verification, and steering signals plus raw prompt samples — a single LLM
// call over locally-derived evidence, never raw payloads beyond this digest.
func runProfile(args []string) {
	fs := flag.NewFlagSet("profile", flag.ContinueOnError)
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
		var sumSteering, sumExecution, sumEngineering, sumProduct, sumPlanning float64
		for _, p := range projects {
			sig, scores, hasSessions, err := profileOne(database, p.ID, p.GitRoot)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Skipping %s: %v\n", p.ID, err)
				continue
			}
			if !hasSessions {
				fmt.Printf("%s: no sessions found.\n\n", p.ID)
				continue
			}
			printProfile(p.ID, sig, scores)
			fmt.Println()
			scoredCount++
			sumSteering += float64(scores.Steering.Score)
			sumExecution += float64(scores.Execution.Score)
			sumEngineering += float64(scores.Engineering.Score)
			sumProduct += float64(scores.ProductInstinct.Score)
			sumPlanning += float64(scores.Planning.Score)
		}
		if scoredCount == 0 {
			return
		}
		n := float64(scoredCount)
		fmt.Printf("Aggregate across %d projects — Steering %.1f/10, Execution %.1f/10, Engineering %.1f/10, Product instinct %.1f/10, Planning %.1f/10\n",
			scoredCount, sumSteering/n, sumExecution/n, sumEngineering/n, sumProduct/n, sumPlanning/n)
		return
	}

	repoPath := *path
	if repoPath == "" {
		repoPath, _ = os.Getwd()
	}
	gitRoot := repoPath
	if out, err := exec.Command("git", "-C", repoPath, "rev-parse", "--show-toplevel").Output(); err == nil {
		gitRoot = strings.TrimSpace(string(out))
	}
	projectID := detectProjectIDForPath(gitRoot)

	sig, scores, hasSessions, err := profileOne(database, projectID, gitRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if !hasSessions {
		fmt.Printf("No sessions found for %s.\n", projectID)
		return
	}
	printProfile(projectID, sig, scores)
}

// profileOne computes signals and scores for a single project. gitRoot may
// be empty (project known only from session_summaries, no resolved path
// yet) — git-dependent signals are skipped in that case rather than erroring.
func profileOne(database *db.DB, projectID, gitRoot string) (profile.Signals, profile.Scores, bool, error) {
	sig := profile.Signals{ProjectID: projectID}

	commitSummaries, err := database.SessionCommitsByProject(projectID, 1000)
	if err != nil {
		return sig, profile.Scores{}, false, err
	}
	for _, c := range commitSummaries {
		sig.TotalCommits += c.Commits
		sig.TotalInsertions += c.Insertions
		sig.TotalDeletions += c.Deletions
	}

	if gitRoot != "" {
		if commits, err := forgegit.CommitsSince(gitRoot, 365*24*time.Hour); err == nil {
			pathsPerCommit := make([][]string, len(commits))
			for i, c := range commits {
				pathsPerCommit[i] = c.Paths
			}
			sig.AvgFilesPerCommit, sig.TestPairedCommitPct = profile.CommitHygiene(pathsPerCommit)
		}
	}

	ranges, err := database.SessionRangesByProject(projectID)
	if err != nil {
		return sig, profile.Scores{}, false, err
	}
	if len(ranges) == 0 {
		return sig, profile.Scores{}, false, nil
	}
	ws, err := streams.Cluster(ranges, streams.DefaultGap)
	if err != nil {
		return sig, profile.Scores{}, false, err
	}
	sig.WorkStreams = len(ws)
	sig.TotalSessions = len(ranges)

	toolCounts := map[string]int{}
	for _, r := range ranges {
		events, err := database.SessionEventsUpTo(r.SessionID, "", 0)
		if err != nil {
			return sig, profile.Scores{}, false, err
		}
		st := steering.Compute(events)
		sig.TotalPrompts += st.TotalPrompts
		sig.SteeredPrompts += st.SteeredPrompts

		sessionTools, hasTestRun := profile.ClassifyEvents(events)
		for name, n := range sessionTools {
			toolCounts[name] += n
		}
		if hasTestRun {
			sig.VerifiedSessions++
		}

		for _, e := range events {
			if e.EventType != "UserPromptSubmit" && e.EventType != "beforeSubmitPrompt" {
				continue
			}
			if text := distill.ExtractPromptText(e.Payload); text != "" {
				sig.PromptSamples = append(sig.PromptSamples, text)
			}
			break // first prompt of the session is enough signal per session
		}
	}
	sig.ToolCounts = toolCounts

	// Keep only the most recent samples — ranges are ordered oldest-first.
	if len(sig.PromptSamples) > maxProfilePromptSamples {
		sig.PromptSamples = sig.PromptSamples[len(sig.PromptSamples)-maxProfilePromptSamples:]
	}

	summaries, err := database.GetRecentSessionSummariesByProject(projectID, 10)
	if err != nil {
		return sig, profile.Scores{}, false, err
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
		return sig, profile.Scores{}, true, fmt.Errorf("calling LLM (%w). Configure a provider: forge config --provider <name> --api-key <key>", err)
	}

	scores, err := profile.ParseScores(resp)
	if err != nil {
		return sig, profile.Scores{}, true, fmt.Errorf("parsing model response: %w\nRaw response:\n%s", err, resp)
	}

	return sig, scores, true, nil
}

func printProfile(projectID string, sig profile.Signals, scores profile.Scores) {
	fmt.Printf("%s builder profile (%d commits, %.1f files/commit, %.0f%% test-paired, %d/%d sessions verified, %d/%d prompts steered)\n\n",
		projectID, sig.TotalCommits, sig.AvgFilesPerCommit, sig.TestPairedCommitPct,
		sig.VerifiedSessions, sig.TotalSessions, sig.SteeredPrompts, sig.TotalPrompts)
	printAxis := func(name string, a profile.Axis) {
		fmt.Printf("  %-17s %d/10  %s\n", name, a.Score, a.Why)
	}
	printAxis("Steering", scores.Steering)
	printAxis("Execution", scores.Execution)
	printAxis("Engineering", scores.Engineering)
	printAxis("Product instinct", scores.ProductInstinct)
	printAxis("Planning", scores.Planning)
}
