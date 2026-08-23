package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/distill"
)

// runInjectCheck handles two modes:
// 1. Agent hook mode (default) — called by SessionStart/BeforeAgent hooks
// 2. Pre-push check mode (--pre-push) — called by git pre-push hook
func runInjectCheck(args []string) {
	for _, a := range args {
		if a == "--pre-push" {
			os.Exit(runPrePushCheck())
		}
	}

	sourceAgent := os.Getenv("FORGE_SOURCE_TOOL")

	// Respect FORGE_INJECT_PRINCIPLES env var
	if os.Getenv("FORGE_INJECT_PRINCIPLES") == "false" {
		os.Exit(0)
	}

	// Detect project
	projectID := distill.DetectProjectID("")
	if projectID == "" {
		os.Exit(0)
	}

	// Open DB and get principles (read-only mode, bypassing migrations)
	database, err := db.OpenReadOnly("")
	if err != nil {
		os.Exit(0)
	}
	defer database.Close()

	// Read query hint from environment variable
	queryHint := os.Getenv("FORGE_INJECT_QUERY_HINT")

	// Read active files from environment variable (comma separated or JSON array)
	var activeFiles []string
	if filesEnv := os.Getenv("FORGE_INJECT_ACTIVE_FILES"); filesEnv != "" {
		if strings.HasPrefix(filesEnv, "[") {
			_ = json.Unmarshal([]byte(filesEnv), &activeFiles)
		} else {
			for _, part := range strings.Split(filesEnv, ",") {
				if trimmed := strings.TrimSpace(part); trimmed != "" {
					activeFiles = append(activeFiles, trimmed)
				}
			}
		}
	}

	principles, err := distill.GetRelevantPrinciples(database, projectID, queryHint, activeFiles)
	if err != nil || len(principles) == 0 {
		os.Exit(0)
	}

	// Build hierarchical overview text (~20 tokens) instead of full narrative dump.
	var sb strings.Builder
	sb.WriteString("\n## FORGE MEMORY PREVIEW\n")
	sb.WriteString(fmt.Sprintf("Forge: %d distilled memories exist for project '%s'.\n", len(principles), projectID))
	sb.WriteString("Use the `search_memories` MCP tool to retrieve detailed architectural guidelines, bugfixes, and past patterns.\n")
	sb.WriteString("---\n")
	context := sb.String()

	switch sourceAgent {
	case "gemini":
		// Gemini CLI: JSON with additionalContext for SessionStart/BeforeAgent hooks
		out := map[string]any{
			"hookSpecificOutput": map[string]any{
				"additionalContext": context,
			},
		}
		b, _ := json.Marshal(out)
		fmt.Println(string(b))
	default:
		// Claude Code / others: plain text to stdout (SessionStart adds to context)
		fmt.Print(context)
	}

	os.Exit(0)
}

// runPrePushCheck is a git pre-push hook. It checks changed files against
// stored principles and blocks the push if any principle references modified
// files. Exit 0 = clean, Exit 1 = conflicts found.
func runPrePushCheck() int {
	projectID := distill.DetectProjectID("")
	if projectID == "" {
		return 0
	}

	database, err := db.OpenReadOnly("")
	if err != nil {
		return 0
	}
	defer database.Close()

	pushFiles := getPushFiles()
	if len(pushFiles) == 0 {
		return 0
	}

	principles, err := distill.GetRelevantPrinciples(database, projectID, strings.Join(pushFiles, " "), pushFiles)
	if err != nil || len(principles) == 0 {
		return 0
	}

	var hits []struct {
		p     db.Principle
		files []string
	}
	for _, p := range principles {
		matched := intersectStrings(pushFiles, p.FilesModified)
		if len(matched) > 0 && p.ImpactScore >= 0.5 {
			hits = append(hits, struct {
				p     db.Principle
				files []string
			}{p, matched})
		}
	}

	if len(hits) == 0 {
		return 0
	}

	fmt.Printf("\u2717 Push blocked by %d principle conflict(s):\n\n", len(hits))
	for _, h := range hits {
		ts := ""
		if len(h.p.TS) >= 10 {
			ts = h.p.TS[:10]
		}
		fmt.Printf("  \"%s\" (%.1f) — %s\n", h.p.Title, h.p.ImpactScore, ts)
		fmt.Printf("    \u2192 files changed: %s\n", strings.Join(h.files, ", "))
		fmt.Printf("    %s\n\n", h.p.Narrative)
	}
	fmt.Println("Override with: git push --no-verify")
	return 1
}

// getPushFiles returns files changed in working tree + staged area.
// Falls back to last 3 commits if working tree is clean.
func getPushFiles() []string {
	files := map[string]bool{}

	out, err := exec.Command("git", "diff", "--cached", "--name-only", "--").Output()
	if err == nil {
		for _, f := range strings.Fields(string(out)) {
			files[f] = true
		}
	}
	out, err = exec.Command("git", "diff", "--name-only", "--").Output()
	if err == nil {
		for _, f := range strings.Fields(string(out)) {
			files[f] = true
		}
	}
	// Untracked files that would be pushed if staged
	out, err = exec.Command("git", "ls-files", "--others", "--exclude-standard").Output()
	if err == nil {
		for _, f := range strings.Fields(string(out)) {
			files[f] = true
		}
	}

	if len(files) == 0 {
		out, err = exec.Command("git", "diff", "--name-only", "HEAD~3..HEAD", "--").Output()
		if err == nil {
			for _, f := range strings.Fields(string(out)) {
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
