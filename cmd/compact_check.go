package main

import (
	"encoding/json"
	"os"
	"time"

	"github.com/forge/forge/internal/db"
)

// runCompactCheck is an internal command used by the asyncRewake PostToolUse hook.
// It counts events for the current session and exits with code 2 at every 30th
// tool use, which wakes the Claude Code model and injects the rewakeMessage
// telling it to run /compact.
func runCompactCheck(_ []string) {
	var input struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil || input.SessionID == "" {
		os.Exit(0)
	}

	// Wait briefly so the async forge hook has time to write the event first.
	time.Sleep(500 * time.Millisecond)

	database, err := db.Open("")
	if err != nil {
		os.Exit(0)
	}
	defer database.Close()

	count, err := database.SessionEventCount(input.SessionID)
	if err != nil || count == 0 {
		os.Exit(0)
	}

	if count%30 == 0 {
		os.Exit(2)
	}
	os.Exit(0)
}
