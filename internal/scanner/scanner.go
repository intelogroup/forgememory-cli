package scanner

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/distill"
	"github.com/google/uuid"
)

// Run scans ~/Developer for git repos, collects recent commits, and inserts
// them as events for the normal distillation pipeline to process.
// It also runs a second pass to scan AI tool memories (Gemini, Codex).
// If dryRun is true, it prints repos found without writing anything.
func Run(database *db.DB, d *distill.Distiller, dryRun bool) error {
	home, _ := os.UserHomeDir()
	devDir := filepath.Join(home, "Developer")

	repos, err := findRepos(devDir)
	if err != nil {
		return fmt.Errorf("find repos: %w", err)
	}
	if len(repos) == 0 {
		fmt.Println("No git repos found in ~/Developer")
		return nil
	}

	if dryRun {
		fmt.Printf("Found %d repos in ~/Developer:\n", len(repos))
		for _, r := range repos {
			fmt.Printf("  %s\n", r)
		}
		return nil
	}

	inserted := 0
	for _, repo := range repos {
		commits, err := recentCommits(repo, 24*time.Hour)
		if err != nil || len(commits) == 0 {
			continue
		}

		repoName := filepath.Base(repo)
		payload, err := buildPayload(repoName, commits)
		if err != nil {
			continue
		}

		event := &db.Event{
			ID:         uuid.New().String(),
			TS:         time.Now().UTC().Format(time.RFC3339),
			SessionID:  "scanner",
			ProjectID:  repoName,
			SourceTool: "scanner",
			EventType:  "ScannerRun",
			Payload:    payload,
		}
		if err := database.InsertEvent(event); err != nil {
			continue
		}
		inserted++
	}

	if inserted == 0 {
		fmt.Println("No new commits found in the last 24 hours.")
	} else {
		fmt.Printf("Scanned %d repos, inserted %d event(s). Running distillation...\n", len(repos), inserted)
		count, err := d.DistillBatch(inserted + 5)
		if err != nil {
			return fmt.Errorf("distill: %w", err)
		}
		fmt.Printf("Distilled %d principle(s) from scanner events.\n", count)
	}

	// Second pass: scan AI tool memories
	scanner := &Scanner{DB: database}
	if err := scanner.scanAIMemories(); err != nil {
		fmt.Printf("AI memory scan error: %v\n", err)
	}

	return nil
}

// findRepos walks dir (depth 2) and returns paths that contain a .git directory.
func findRepos(dir string) ([]string, error) {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var repos []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		candidate := filepath.Join(dir, e.Name())
		if _, err := os.Stat(filepath.Join(candidate, ".git")); err == nil {
			repos = append(repos, candidate)
			continue
		}
		// One level deeper
		sub, _ := os.ReadDir(candidate)
		for _, se := range sub {
			if !se.IsDir() {
				continue
			}
			nested := filepath.Join(candidate, se.Name())
			if _, err := os.Stat(filepath.Join(nested, ".git")); err == nil {
				repos = append(repos, nested)
			}
		}
	}
	return repos, nil
}

// recentCommits returns one-line git log entries from the last `since` duration.
func recentCommits(repoPath string, since time.Duration) ([]string, error) {
	sinceStr := fmt.Sprintf("%.0f hours ago", since.Hours())
	cmd := exec.Command("git", "-C", repoPath, "log",
		"--since="+sinceStr, "--oneline", "--no-merges", "--max-count=50")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var commits []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			commits = append(commits, line)
		}
	}
	return commits, nil
}

func buildPayload(repoName string, commits []string) (string, error) {
	payload := map[string]any{
		"repo":    repoName,
		"commits": commits,
	}
	b, err := json.Marshal(payload)
	return string(b), err
}
