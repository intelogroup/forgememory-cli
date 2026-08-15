package main

import (
	"flag"
	"log"
	"os"
	"time"

	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/distill"
)

// runSynthesizeSession is an internal command spawned by the Stop/SessionEnd hook.
// It synthesizes a session summary and writes it to the DB.
func runSynthesizeSession(args []string) {
	fs := flag.NewFlagSet("synthesize-session", flag.ContinueOnError)
	sessionID := fs.String("session-id", "", "Session ID")
	projectID := fs.String("project-id", "", "Project ID")
	checkpointKind := fs.String("checkpoint-kind", "final", "Checkpoint kind: final|compact|clear")
	checkpointKey := fs.String("checkpoint-key", "", "Stable checkpoint key")
	cutoffTS := fs.String("cutoff-ts", "", "Optional timestamp cutoff")
	if err := fs.Parse(args); err != nil {
		log.Printf("synthesize-session: flag parse error: %v", err)
		os.Exit(0)
	}

	if *sessionID == "" {
		os.Exit(0)
	}

	database, err := db.Open("")
	if err != nil {
		log.Printf("synthesize-session: failed to open DB: %v", err)
		os.Exit(0)
	}
	defer database.Close()

	if *checkpointKey != "" {
		exists, err := database.HasSessionCheckpoint(*checkpointKey)
		if err == nil && exists {
			os.Exit(0)
		}
	}

	// head+tail split (not a plain 50-event cap) so the terminal Stop/SessionEnd
	// event — carrying the assistant's last_assistant_message — always survives
	// truncation on long sessions instead of being silently dropped.
	events, err := database.SessionEventsForCheckpoint(*sessionID, *cutoffTS, 20, 30)
	if err != nil {
		log.Printf("synthesize-session: failed to get session events: %v", err)
		os.Exit(0)
	}
	if len(events) < 3 {
		log.Printf("synthesize-session: not enough events (%d) for synthesis", len(events))
		os.Exit(0)
	}

	proj := *projectID
	if proj == "" {
		proj = events[0].ProjectID
	}

	runAt := time.Now().UTC()
	start := time.Now()
	d := distill.New(database, distill.LoadConfig())
	summary, err := d.SynthesizeCheckpoint(*sessionID, proj, *checkpointKind, *checkpointKey, events)
	if err != nil {
		log.Printf("synthesize-session: synthesis failed: %v", err)
		_ = database.RecordDistillationFailure(runAt, time.Since(start), len(events), err.Error(), runAt)
		os.Exit(1)
	}
	if summary == nil {
		_ = database.MarkSessionDistilled(*sessionID)
		os.Exit(0)
	}

	count, err := d.DistillCheckpointSummary(*summary, events)
	if err != nil {
		log.Printf("synthesize-session: principle distillation failed: %v", err)
		_ = database.RecordDistillationFailure(runAt, time.Since(start), len(events), err.Error(), runAt)
		// Mark events distilled even when principles fail — the summary was saved
	}
	_ = database.MarkSessionDistilled(*sessionID)
	_ = database.RecordDistillationSuccess(runAt, time.Since(start), len(events), count, runAt)

	log.Printf("synthesize-session: synthesized %s checkpoint for session %s with %d events and %d principle(s)", *checkpointKind, *sessionID, len(events), count)
	os.Exit(0)
}
