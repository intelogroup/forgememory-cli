package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/forge/forge/internal/db"
	forgegit "github.com/forge/forge/internal/git"
)

// runCommits links recent git commits in a repo to the agent session that
// was active when each commit landed (matched by event-timestamp overlap),
// then prints a per-session breakdown of commits and lines changed.
func runCommits(args []string) {
	fs := flag.NewFlagSet("commits", flag.ContinueOnError)
	path := fs.String("path", "", "repo path (default: current directory)")
	since := fs.Duration("since", 30*24*time.Hour, "how far back to scan git history")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	repoPath := *path
	if repoPath == "" {
		repoPath, _ = os.Getwd()
	}
	out, err := exec.Command("git", "-C", repoPath, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s is not a git repo: %v\n", repoPath, err)
		os.Exit(1)
	}
	gitRoot := strings.TrimSpace(string(out))
	projectID := detectProjectIDForPath(gitRoot)

	commits, err := forgegit.CommitsSince(gitRoot, *since)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading git log: %v\n", err)
		os.Exit(1)
	}
	if len(commits) == 0 {
		fmt.Printf("No commits in %s in the last %s.\n", projectID, since.String())
		return
	}

	database, err := db.Open("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	linked, unlinked := 0, 0
	for _, c := range commits {
		sc := db.SessionCommit{
			SHA:        c.SHA,
			Author:     c.Author,
			CommitTS:   c.Date.UTC().Format(time.RFC3339),
			Subject:    c.Subject,
			Files:      c.Files,
			Insertions: c.Insertions,
			Deletions:  c.Deletions,
		}
		sessionID, err := database.LinkCommitToSession(projectID, sc)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: linking %s: %v\n", c.SHA[:7], err)
			continue
		}
		if sessionID == "" {
			unlinked++
			continue
		}
		linked++
	}

	summaries, err := database.SessionCommitsByProject(projectID, 50)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("%s: %d commits linked to sessions, %d with no matching session\n\n", projectID, linked, unlinked)
	fmt.Printf("%-38s %8s %10s %10s\n", "SESSION", "COMMITS", "+LINES", "-LINES")
	for _, s := range summaries {
		fmt.Printf("%-38s %8d %10d %10d\n", s.SessionID, s.Commits, s.Insertions, s.Deletions)
	}
}
