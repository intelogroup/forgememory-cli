package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/forge/forge/internal/db"
)

func TestRunCompactCheck_NoSessionID(t *testing.T) {
	var input struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(strings.NewReader(`{}`)).Decode(&input); err != nil {
		t.Fatal(err)
	}
	if input.SessionID != "" {
		t.Fatal("expected empty session_id")
	}
}

func TestSessionEventCount_Zero(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	database, err := db.Open("")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	count, err := database.SessionEventCount("nonexistent-session")
	if err != nil {
		t.Fatalf("SessionEventCount: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0, got %d", count)
	}
}

func TestSessionEventCount_ThirtyTriggersCompact(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	database, err := db.Open("")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	sessionID := "test-session-abc"
	for i := 0; i < 30; i++ {
		ev := &db.Event{
			SessionID:  sessionID,
			ProjectID:  "test-project",
			SourceTool: "claude",
			EventType:  "PostToolUse",
			Payload:    `{"test":true}`,
		}
		if err := database.InsertEvent(ev); err != nil {
			t.Fatalf("InsertEvent %d: %v", i, err)
		}
	}

	count, err := database.SessionEventCount(sessionID)
	if err != nil {
		t.Fatalf("SessionEventCount: %v", err)
	}
	if count != 30 {
		t.Fatalf("expected 30, got %d", count)
	}
	if count%30 != 0 {
		t.Fatal("expected count%30 == 0 to trigger compact")
	}
}

func TestSessionEventCount_TwentyNineNoTrigger(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	database, err := db.Open("")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	sessionID := "test-session-29"
	for i := 0; i < 29; i++ {
		ev := &db.Event{
			SessionID:  sessionID,
			ProjectID:  "proj",
			SourceTool: "claude",
			EventType:  "PostToolUse",
			Payload:    `{}`,
		}
		if err := database.InsertEvent(ev); err != nil {
			t.Fatalf("InsertEvent %d: %v", i, err)
		}
	}

	count, err := database.SessionEventCount(sessionID)
	if err != nil {
		t.Fatalf("SessionEventCount: %v", err)
	}
	if count != 29 {
		t.Fatalf("expected 29, got %d", count)
	}
	if count%30 == 0 {
		t.Fatal("29 should not trigger compact")
	}
}

func TestSessionEventCount_OnlyCountsMatchingSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	database, err := db.Open("")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	for _, sid := range []string{"session-a", "session-b"} {
		ev := &db.Event{
			SessionID:  sid,
			ProjectID:  "proj",
			SourceTool: "claude",
			EventType:  "PostToolUse",
			Payload:    `{}`,
		}
		if err := database.InsertEvent(ev); err != nil {
			t.Fatalf("InsertEvent: %v", err)
		}
	}

	count, err := database.SessionEventCount("session-a")
	if err != nil {
		t.Fatalf("SessionEventCount: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1, got %d", count)
	}
}
