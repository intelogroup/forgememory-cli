package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/distill"
)

// runInjectCheck is called by agent hooks (SessionStart/BeforeAgent).
// It checks for relevant distilled principles and outputs them as context
// the agent can inject into the conversation.
// Exit 0 with no output = no principles to inject (silent success).
// Exit 0 with output = principles formatted for the calling agent.
func runInjectCheck(args []string) {
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

	// Build context text
	var sb strings.Builder
	sb.WriteString("\n## RELEVANT LESSONS FROM PREVIOUS WORK\n")
	for _, p := range principles {
		sb.WriteString(fmt.Sprintf("- %s: %s\n", p.Title, p.Narrative))
	}
	sb.WriteString("\n---\n")
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
