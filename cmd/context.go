package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/distill"
)

func runContext(args []string) {
	relevant := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--relevant" {
			if i+1 < len(args) {
				i++
				relevant = args[i]
			}
		}
	}

	projectID := distill.DetectProjectID("")

	database, err := db.Open("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	agents, _ := database.ProjectAgents(projectID)
	summaries, _ := database.GetRecentSessionSummariesByProject(projectID, 5)
	total, undistilled, _ := database.EventCount()

	header := fmt.Sprintf("Project: %s", projectID)
	if projectID == "" {
		header = "All projects"
	}
	fmt.Println(header)
	if len(agents) > 0 {
		fmt.Printf("Agents: %s\n", strings.Join(agents, ", "))
	}
	fmt.Printf("Events: %d total, %d undistilled\n\n", total, undistilled)

	timeline, _ := database.ProjectTimeline(projectID, 10)
	agentBySession := map[string]string{}
	for _, e := range timeline {
		agentBySession[e.SessionID] = e.PrimaryAgent
	}

	if len(summaries) > 0 {
		fmt.Println("Recent sessions:")
		for _, s := range summaries {
			ts := s.TS
			if len(ts) >= 10 {
				ts = ts[:10]
			}
			req := s.Request
			if req == "" {
				req = s.Summary
			}
			agent := agentBySession[s.SessionID]
			if agent != "" {
				agent += " "
			}
			fmt.Printf("  [%s] %s- %s\n", ts, agent, req)
			if s.NextSteps != "" {
				fmt.Printf("    next: %s\n", s.NextSteps)
			}
			fmt.Println()
		}
	}

	if relevant != "" {
		relevantPrinciples, _ := distill.GetRelevantPrinciples(database, projectID, relevant, nil)
		if len(relevantPrinciples) > 0 {
			fmt.Printf("Relevant to \"%s\":\n\n", relevant)
			for _, p := range relevantPrinciples {
				fmt.Printf("  %s (%.1f)\n", p.Title, p.ImpactScore)
				fmt.Printf("  %s\n\n", p.Narrative)
			}
		} else {
			fmt.Println("No relevant principles found.")
		}
	} else {
		recentFiles := detectRecentFiles()
		if len(recentFiles) > 0 {
			autoPrinciples, _ := distill.GetRelevantPrinciples(database, projectID, strings.Join(recentFiles, " "), recentFiles)
			if len(autoPrinciples) > 0 {
				fmt.Println("Patterns in recent files:")
				for _, p := range autoPrinciples {
					matched := intersectStrings(recentFiles, p.FilesModified)
					fileStr := ""
					if len(matched) > 0 {
						fileStr = " (\u2192 " + strings.Join(matched, ", ") + ")"
					}
					fmt.Printf("  %s (%.1f)%s\n", p.Title, p.ImpactScore, fileStr)
					fmt.Printf("  %s\n\n", p.Narrative)
				}
			}
		}
	}

	if len(summaries) == 0 && relevant == "" && len(detectRecentFiles()) == 0 {
		fmt.Println("No memory yet. Keep working - Forge will capture your patterns.")
	}
}

func detectRecentFiles() []string {
	files := map[string]bool{}

	out, err := exec.Command("git", "status", "--porcelain").Output()
	if err != nil {
		return nil
	}
	for _, line := range strings.Split(string(out), "\n") {
		if len(line) <= 3 {
			continue
		}
		f := strings.TrimSpace(line[3:])
		if f != "" {
			files[f] = true
		}
	}

	if len(files) == 0 {
		out2, err2 := exec.Command("git", "diff", "--name-only", "HEAD~3..HEAD", "--").Output()
		if err2 == nil {
			for _, f := range strings.Fields(string(out2)) {
				files[f] = true
			}
		}
	}

	var out2 []string
	for f := range files {
		out2 = append(out2, f)
	}
	return out2
}

func intersectStrings(a, b []string) []string {
	setB := map[string]bool{}
	for _, s := range b {
		setB[s] = true
	}
	var out []string
	for _, s := range a {
		if setB[s] {
			out = append(out, s)
		}
	}
	return out
}
