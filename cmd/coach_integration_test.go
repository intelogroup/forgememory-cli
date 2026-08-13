package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forge/forge/internal/coach"
	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/skills"
)

// TestCoachRolloutFixture crosses the deterministic rollout boundary: raw
// events and linked commits become observations, compact evidence updates the
// project skill state, an eligible item is queued, and a separate forge
// process reads it through the public CLI.
func TestCoachRolloutFixture(t *testing.T) {
	home := t.TempDir()
	database, err := db.Open(filepath.Join(home, ".forge", "forge.db"))
	if err != nil {
		t.Fatal(err)
	}

	projectID := coachTestProjectID(t)
	seedCoachRolloutFixture(t, database, projectID)
	queued, err := detectAndQueueVerification(database, projectID, coach.ModeNormal)
	if err != nil {
		database.Close()
		t.Fatalf("detector -> evidence -> state -> queue: %v", err)
	}
	if queued == 0 {
		database.Close()
		t.Fatal("expected repeated negative evidence to queue coaching")
	}

	observations, err := database.ListAllObservations(projectID, "active")
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	if len(observations) < 3 {
		database.Close()
		t.Fatalf("observations = %d, want passing evidence plus repeated negative evidence", len(observations))
	}

	state, err := database.GetSkillState("verification.pre_ship", skills.ScopeProject, projectID)
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	if state == nil || state.State != skills.StateSuspectedGap {
		database.Close()
		t.Fatalf("skill state = %#v, want suspected_gap", state)
	}

	items, err := database.ListCoachingItems(projectID, "queued", 0)
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	if len(items) == 0 {
		database.Close()
		t.Fatal("expected queued coaching item")
	}
	observationID := items[0].ObservationID
	evidence, err := database.ListObservationEvidence(observationID)
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	if len(evidence) < 2 {
		database.Close()
		t.Fatalf("evidence = %#v, want source links and provenance", evidence)
	}
	database.Close()

	stdout, stderr, code := runForge(t, home, "coach", "list", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("forge coach list --json: code=%d stderr=%q", code, stderr)
	}
	var list coachListOutput
	if err := json.Unmarshal([]byte(stdout), &list); err != nil {
		t.Fatalf("coach list JSON: %v\noutput=%q", err, stdout)
	}
	if list.ProjectID != projectID || len(list.Items) == 0 {
		t.Fatalf("coach list = %#v, want project %q with queued item", list, projectID)
	}
	if list.Items[0].State != skills.StateSuspectedGap {
		t.Fatalf("coach list item = %#v, want suspected_gap state", list.Items[0])
	}

	stdout, stderr, code = runForge(t, home, "coach", "explain", observationID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("forge coach explain --json: code=%d stderr=%q", code, stderr)
	}
	var explain coachExplainOutput
	if err := json.Unmarshal([]byte(stdout), &explain); err != nil {
		t.Fatalf("coach explain JSON: %v\noutput=%q", err, stdout)
	}
	if explain.Observation.ID != observationID || len(explain.SupportingEvidence) == 0 {
		t.Fatalf("coach explain = %#v, want observation and supporting evidence", explain)
	}

	// A representative pre-coach command still reads the same temporary DB.
	stdout, stderr, code = runForge(t, home, "status", "--json")
	if code != 0 || stderr != "" || !strings.Contains(stdout, "\"database\"") {
		t.Fatalf("forge status --json compatibility: code=%d stderr=%q stdout=%q", code, stderr, stdout)
	}
}

func seedCoachRolloutFixture(t *testing.T, database *db.DB, projectID string) {
	t.Helper()
	seedSession := func(sessionID, changeTS, passTS, failTS, endTS string, withPassingTest bool) {
		events := []db.Event{{ID: sessionID + "-change", ProjectID: projectID, SessionID: sessionID, TS: changeTS, EventType: "PostToolUse", ToolName: "Edit", Payload: `{"tool_input":{"file_path":"internal/payments/charge.go"}}`}}
		if withPassingTest {
			events = append(events, db.Event{ID: sessionID + "-pass", ProjectID: projectID, SessionID: sessionID, TS: passTS, EventType: "PostToolUse", ToolName: "Bash", Payload: `{"tool_input":{"command":"go test ./internal/payments"},"tool_response":{"exit_code":0,"stdout":"ok"}}`})
		}
		events = append(events,
			db.Event{ID: sessionID + "-fail", ProjectID: projectID, SessionID: sessionID, TS: failTS, EventType: "PostToolUse", ToolName: "Bash", Payload: `{"tool_input":{"command":"go test ./internal/payments"},"tool_response":{"exit_code":1,"stderr":"FAIL"}}`},
			db.Event{ID: sessionID + "-end", ProjectID: projectID, SessionID: sessionID, TS: endTS, EventType: "SessionEnd", Payload: `{}`},
		)
		for _, event := range events {
			if err := database.InsertEvent(&event); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := database.LinkCommitToSession(projectID, db.SessionCommit{ID: sessionID + "-commit", SHA: "sha-" + sessionID, Author: "fixture", CommitTS: changeTS, Subject: "change payment behavior", Files: 1, Insertions: 2, Deletions: 1}); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			if _, err := database.UpsertFailureSignature(&db.FailureSignature{ID: sessionID + "-failure", ProjectID: projectID, SessionID: sessionID, ToolName: "Bash", CommandFamily: "go test", Fingerprint: "failure-" + sessionID, ErrorKind: "test_failure", NormalizedMessage: "payment test failed", FirstSeenTS: failTS, LastSeenTS: failTS}); err != nil {
				t.Fatal(err)
			}
		}
	}

	seedSession("rollout-1", "2026-08-10T10:00:00Z", "2026-08-10T10:01:00Z", "2026-08-10T10:02:00Z", "2026-08-10T10:03:00Z", true)
	seedSession("rollout-2", "2026-08-11T10:00:00Z", "", "2026-08-11T10:02:00Z", "2026-08-11T10:03:00Z", false)
}
