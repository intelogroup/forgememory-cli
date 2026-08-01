package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/distill"
	"github.com/forge/forge/internal/promptdoctor"
	"github.com/forge/forge/internal/steering"
	"github.com/forge/forge/internal/streams"
)

// runPromptDoctor surfaces recurring prompt anti-patterns (rephrase loops,
// degenerate one-word follow-ups, vague verbs) from a user's own prompt
// history — one LLM call over locally-derived evidence, same idiom as
// runKnowledgeGap.
func runPromptDoctor(args []string) {
	fs := flag.NewFlagSet("prompt-doctor", flag.ContinueOnError)
	path := fs.String("path", "", "repo path (default: current directory)")
	all := fs.Bool("all", false, "run across all known projects")
	coach := fs.Bool("coach", false, "print ready-to-copy fixed prompts")
	jsonOut := fs.Bool("json", false, "machine-readable JSON output")
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
		for _, p := range projects {
			result, hasSessions, err := promptDoctorOne(database, p.ID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Skipping %s: %v\n", p.ID, err)
				continue
			}
			if !hasSessions {
				fmt.Printf("%s: no sessions found.\n\n", p.ID)
				continue
			}
			printPromptDoctor(p.ID, result, *coach, *jsonOut)
			fmt.Println()
		}
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

	result, hasSessions, err := promptDoctorOne(database, projectID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if !hasSessions {
		fmt.Printf("No sessions found for %s.\n", projectID)
		return
	}
	printPromptDoctor(projectID, result, *coach, *jsonOut)
}

// promptDoctorOne gathers Signals for a project and runs the LLM pass.
func promptDoctorOne(database *db.DB, projectID string) (promptdoctor.Result, bool, error) {
	ranges, err := database.SessionRangesByProject(projectID)
	if err != nil {
		return promptdoctor.Result{}, false, err
	}
	if len(ranges) == 0 {
		return promptdoctor.Result{}, false, nil
	}
	if _, err := streams.Cluster(ranges, streams.DefaultGap); err != nil {
		return promptdoctor.Result{}, false, err
	}

	var findings []promptdoctor.Finding
	var steer steering.Stats
	for _, r := range ranges {
		events, err := database.SessionEventsUpTo(r.SessionID, "", 0)
		if err != nil {
			return promptdoctor.Result{}, false, err
		}
		steer.Add(steering.Compute(events))

		var prompts []promptdoctor.Prompt
		for _, e := range events {
			if e.EventType != "UserPromptSubmit" && e.EventType != "beforeSubmitPrompt" {
				continue
			}
			text := distill.ExtractPromptText(e.Payload)
			if text == "" {
				continue
			}
			ts, err := time.Parse(time.RFC3339, e.TS)
			if err != nil {
				continue
			}
			prompts = append(prompts, promptdoctor.Prompt{Text: text, TS: ts})
		}
		for i, chain := range promptdoctor.GroupChains(r.SessionID, prompts) {
			chainID := fmt.Sprintf("%s-%d", r.SessionID, i)
			findings = append(findings, promptdoctor.DetectFindings(chainID, chain)...)
		}
	}

	if len(findings) == 0 {
		return promptdoctor.Result{}, true, nil
	}

	sig := promptdoctor.Signals{
		ProjectID:    projectID,
		Findings:     findings,
		SteeredRate:  steer.Rate(),
		TotalPrompts: steer.TotalPrompts,
	}

	cfg := distill.LoadConfig()
	d := distill.New(database, cfg)
	resp, err := d.CallLLM(promptdoctor.BuildPrompt(sig))
	if err != nil {
		return promptdoctor.Result{}, true, fmt.Errorf("calling LLM (%w). Configure a provider: forge config --provider <name> --api-key <key>", err)
	}

	result, err := promptdoctor.ParseResult(resp)
	if err != nil {
		return promptdoctor.Result{}, true, fmt.Errorf("parsing model response: %w\nRaw response:\n%s", err, resp)
	}
	return result, true, nil
}

func printPromptDoctor(projectID string, result promptdoctor.Result, coach, jsonOut bool) {
	if jsonOut {
		b, _ := json.MarshalIndent(result, "", "  ")
		fmt.Printf("%s:\n%s\n", projectID, b)
		return
	}

	fmt.Printf("%s prompt doctor\n\n", projectID)
	if len(result.Fixes) == 0 {
		fmt.Println("  No prompt anti-patterns found in local evidence.")
		return
	}
	for i, f := range result.Fixes {
		fmt.Printf("  %d. [%s/%s] missing: %s\n     original: %q\n", i+1, f.AntiPattern, f.Severity, f.MissingSCARFField, f.OriginalPrompt)
		if coach {
			fmt.Printf("     fixed:    %q\n", f.FixedPrompt)
		}
		fmt.Println()
	}
}
