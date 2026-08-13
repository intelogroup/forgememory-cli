package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/forge/forge/internal/db"
)

func TestDaemonProcessesCompletedVerificationWithoutProviderAndKeepsCapturingEvents(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("FORGE_PROVIDER", "")
	t.Setenv("FORGE_API_KEY", "")

	database, err := db.Open(filepath.Join(home, "daemon.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	base := time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC)
	insertDaemonCoachEvent(t, database, "change", base, "PostToolUse", "Edit", `{"file_path":"internal/payments/charge.go"}`)
	insertDaemonCoachEvent(t, database, "test", base.Add(time.Minute), "PostToolUse", "Bash", `{"tool_input":{"command":"go test ./internal/payments"},"tool_response":{"exit_code":0,"stdout":"PASS"}}`)
	insertDaemonCoachEvent(t, database, "stop", base.Add(2*time.Minute), "SessionEnd", "", `{}`)
	if _, err := database.Conn().Exec(`CREATE TRIGGER fail_daemon_processing_marker BEFORE INSERT ON observation_evidence
		WHEN NEW.source_type = 'processing'
		BEGIN
			SELECT RAISE(ABORT, 'coaching persistence failure');
		END`); err != nil {
		t.Fatalf("create coaching failure trigger: %v", err)
	}

	lastCrossSessionRun := time.Time{}
	_, batchErr, _ := distillSessionBatch(database, time.Minute, &lastCrossSessionRun)
	if batchErr == nil {
		t.Fatal("distillSessionBatch without a provider error = nil, want provider failure")
	}
	state, err := database.GetSkillState("verification.pre_ship", "project", "project-a")
	if err != nil {
		t.Fatalf("GetSkillState after coaching failure: %v", err)
	}
	if state != nil {
		t.Fatalf("coaching failure advanced state = %#v, want no state", state)
	}
	observations, err := database.ListAllObservations("project-a", "")
	if err != nil {
		t.Fatalf("ListAllObservations: %v", err)
	}
	if len(observations) != 1 || observations[0].SessionID != "completed-session" {
		t.Fatalf("completed-session observations = %#v, want one live observation", observations)
	}

	raw := map[string]json.RawMessage{
		"id":          json.RawMessage(`"later-event"`),
		"ts":          json.RawMessage(`"2026-08-13T13:00:00Z"`),
		"session_id":  json.RawMessage(`"next-session"`),
		"project_id":  json.RawMessage(`"project-a"`),
		"event_type":  json.RawMessage(`"PostToolUse"`),
		"tool_name":   json.RawMessage(`"Edit"`),
		"payload":     json.RawMessage(`"{}"`),
		"source_tool": json.RawMessage(`"claude"`),
	}
	handleEventMsg(raw, database)
	if events, err := database.SessionEventsUpTo("next-session", "", 0); err != nil || len(events) != 1 {
		t.Fatalf("event capture after coaching/provider failure = %#v, %v; want one event", events, err)
	}
}

func insertDaemonCoachEvent(t *testing.T, database *db.DB, id string, at time.Time, eventType, toolName, payload string) {
	t.Helper()
	if err := database.InsertEvent(&db.Event{ID: id, TS: at.Format(time.RFC3339), SessionID: "completed-session", ProjectID: "project-a", EventType: eventType, ToolName: toolName, Payload: payload}); err != nil {
		t.Fatalf("InsertEvent %s: %v", id, err)
	}
}
