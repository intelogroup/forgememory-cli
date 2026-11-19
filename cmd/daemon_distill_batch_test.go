package main

import (
	"fmt"
	"testing"
	"time"

	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/distill"
)

func TestScheduledDistillationDrainsLongSessionInBoundedChunks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("FORGE_PROVIDER", "anthropic")
	t.Setenv("FORGE_API_KEY", "testing")
	database, err := db.Open("")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	const chunks = 2
	const chunkSize = 300
	const sessionID = "long-completed-session"
	ts := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	for i := 0; i <= chunks*chunkSize; i++ {
		eventType := "PostToolUse"
		if i == chunks*chunkSize {
			eventType = "SessionEnd"
		}
		event := &db.Event{
			ID: fmt.Sprintf("event-%04d", i), TS: ts, SessionID: sessionID,
			ProjectID: "example", EventType: eventType, Payload: `{}`,
		}
		if err := database.InsertEvent(event); err != nil {
			t.Fatal(err)
		}
	}

	originalSynthesize := synthesizeCheckpointForSession
	t.Cleanup(func() { synthesizeCheckpointForSession = originalSynthesize })
	seen := make(map[string]bool)
	keys := make(map[string]bool)
	synthesizeCheckpointForSession = func(_ *distill.Distiller, id, _ string, key string, events []db.Event) (*db.SessionSummary, error) {
		if id != sessionID || len(events) > chunkSize {
			return nil, fmt.Errorf("scheduler sent %d events for %s, max %d per request", len(events), id, chunkSize)
		}
		if keys[key] {
			t.Fatalf("reused checkpoint key across chunks: %s", key)
		}
		keys[key] = true
		for _, event := range events {
			if seen[event.ID] {
				t.Fatalf("event %s was sent twice", event.ID)
			}
			seen[event.ID] = true
		}
		return nil, database.InsertSessionSummary(&db.SessionSummary{
			SessionID: id, ProjectID: "example", CheckpointKey: key, Summary: "test checkpoint",
		})
	}

	lastCrossSessionRun := time.Now()
	for _, wantRemaining := range []int{301, 1, 0} {
		_, err, more := distillSessionBatch(database, time.Minute, &lastCrossSessionRun)
		if err != nil {
			t.Fatal(err)
		}
		if more != (wantRemaining > 0) {
			t.Fatalf("more = %v with %d expected remaining events", more, wantRemaining)
		}
		_, remaining, err := database.EventCount()
		if err != nil || remaining != wantRemaining {
			t.Fatalf("remaining = %d, %v; want %d", remaining, err, wantRemaining)
		}
	}
	if len(seen) != 601 || !seen["event-0600"] {
		t.Fatalf("processed %d events; terminal event processed = %v", len(seen), seen["event-0600"])
	}
}
