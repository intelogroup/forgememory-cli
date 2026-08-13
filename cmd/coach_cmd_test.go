package main

import (
	"os"
	"testing"

	"github.com/forge/forge/internal/coach"
	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/observations"
	"github.com/forge/forge/internal/skills"
)

func TestVerificationEvidenceSummary(t *testing.T) {
	positive := verificationEvidenceSummary(observations.ObservationDraft{Kind: "verification_detected", Confidence: 0.8}, "now")
	if positive.SuccessfulApplications != 1 || positive.FailedApplications != 0 {
		t.Fatalf("expected a successful application for verification_detected, got %+v", positive)
	}

	for _, kind := range []string{"code_change_without_relevant_test", "unresolved_failure_after_change", "repeated_regression"} {
		negative := verificationEvidenceSummary(observations.ObservationDraft{Kind: kind, Confidence: 0.8}, "now")
		if negative.FailedApplications != 1 || negative.SuccessfulApplications != 0 {
			t.Fatalf("expected a failed application for %q, got %+v", kind, negative)
		}
	}
}

// TestDetectAndQueueVerificationEndToEnd proves the same evidence -> skill
// state -> coach pipeline knowledge-gap queueing uses also closes the loop
// for the deterministic verification detector: two sessions with a code
// change and no relevant test each produce one failed application, and the
// second crosses the skill into suspected_gap and gets queued.
func TestDetectAndQueueVerificationEndToEnd(t *testing.T) {
	tmp, _ := os.CreateTemp(t.TempDir(), "forge-coach-*.db")
	tmp.Close()
	database, err := db.Open(tmp.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	const projectID = "proj1"
	insertWrite := func(sessionID, ts string) {
		t.Helper()
		if err := database.InsertEvent(&db.Event{
			ID:        sessionID + "-write",
			ProjectID: projectID,
			SessionID: sessionID,
			TS:        ts,
			EventType: "PostToolUse",
			ToolName:  "Edit",
			Payload:   `{"tool_input":{"file_path":"internal/foo/foo.go"}}`,
		}); err != nil {
			t.Fatal(err)
		}
	}

	insertWrite("sess-1", "2026-01-01T00:00:00Z")
	n1, err := detectAndQueueVerification(database, projectID, coach.ModeNormal)
	if err != nil {
		t.Fatal(err)
	}
	if n1 != 0 {
		t.Fatalf("expected 0 queued after first session's observation, got %d", n1)
	}

	insertWrite("sess-2", "2026-01-02T00:00:00Z")
	n2, err := detectAndQueueVerification(database, projectID, coach.ModeNormal)
	if err != nil {
		t.Fatal(err)
	}
	// QueueEligible queues one item per eligible observation, not per skill —
	// once the skill crosses suspected_gap both sessions' observations queue.
	if n2 != 2 {
		t.Fatalf("expected 2 queued once the second session crosses suspected_gap, got %d", n2)
	}

	state, err := database.GetSkillState("verification.pre_ship", skills.ScopeProject, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if state == nil || state.State != skills.StateSuspectedGap {
		t.Fatalf("expected suspected_gap state, got %+v", state)
	}
}
