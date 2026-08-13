package coach

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/forge/forge/internal/db"
)

func TestBackfillUsesBoundedHighSignalSessionsAndRecordsHistoricalProvenance(t *testing.T) {
	database := openTestDB(t)
	base := time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC)

	seedBackfillChange(t, database, "commit-session", "project-a", base, "internal/payments/charge.go")
	seedBackfillBoundary(t, database, "commit-session", "project-a", base.Add(2*time.Minute))
	linkBackfillCommit(t, database, "project-a", "commit-session", "commit-a", base.Add(time.Minute))

	failureAt := base.Add(2 * time.Hour)
	seedBackfillChange(t, database, "failure-session", "project-a", failureAt, "internal/billing/invoice.go")
	seedBackfillTest(t, database, "failure-session", "project-a", failureAt.Add(time.Minute), 1)
	seedBackfillBoundary(t, database, "failure-session", "project-a", failureAt.Add(2*time.Minute))
	seedRepeatedFailure(t, database, "project-a", "failure-session", failureAt.Add(time.Minute))

	// This ordinary edit is not linked to a commit or repeated failure, so it
	// must not widen the historical replay set.
	seedBackfillChange(t, database, "ordinary-session", "project-a", base.Add(3*time.Hour), "internal/unused/ordinary.go")

	report, err := Backfill(context.Background(), database, "project-a", 1)
	if err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	if report.SessionsProcessed != 1 || report.ObservationsCreated != 1 {
		t.Fatalf("bounded report = %#v, want one processed session and observation", report)
	}

	observations, err := database.ListAllObservations("project-a", "")
	if err != nil {
		t.Fatalf("ListAllObservations: %v", err)
	}
	if len(observations) != 1 || observations[0].SessionID != "failure-session" {
		t.Fatalf("bounded observations = %#v, want only failure-session", observations)
	}
	assertBackfillProvenance(t, database, observations[0].ID)

	if _, err := Backfill(context.Background(), database, "project-a", 10); err != nil {
		t.Fatalf("Backfill full pass: %v", err)
	}
	stateBeforeRetry, err := database.GetSkillState("verification.pre_ship", "project", "project-a")
	if err != nil || stateBeforeRetry == nil {
		t.Fatalf("GetSkillState before retry = %#v, %v", stateBeforeRetry, err)
	}
	retry, err := Backfill(context.Background(), database, "project-a", 10)
	if err != nil {
		t.Fatalf("Backfill retry: %v", err)
	}
	if retry.ObservationsCreated != 0 {
		t.Fatalf("retry observations = %d, want 0", retry.ObservationsCreated)
	}
	stateAfterRetry, err := database.GetSkillState("verification.pre_ship", "project", "project-a")
	if err != nil || stateAfterRetry == nil {
		t.Fatalf("GetSkillState after retry = %#v, %v", stateAfterRetry, err)
	}
	if *stateAfterRetry != *stateBeforeRetry {
		t.Fatalf("retry changed skill state:\n before=%#v\n after =%#v", stateBeforeRetry, stateAfterRetry)
	}

	observations, err = database.ListAllObservations("project-a", "")
	if err != nil {
		t.Fatalf("ListAllObservations after retry: %v", err)
	}
	for _, observation := range observations {
		if observation.SessionID == "ordinary-session" {
			t.Fatalf("ordinary session was backfilled: %#v", observation)
		}
	}
}

func TestBackfillSkipsAmbiguousHistoricalSession(t *testing.T) {
	database := openTestDB(t)
	base := time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC)
	seedBackfillChange(t, database, "docs-session", "project-a", base, "README.md")
	seedBackfillBoundary(t, database, "docs-session", "project-a", base.Add(time.Minute))
	linkBackfillCommit(t, database, "project-a", "docs-session", "docs-commit", base.Add(30*time.Second))

	report, err := Backfill(context.Background(), database, "project-a", 10)
	if err != nil {
		t.Fatalf("Backfill: %v", err)
	}
	if report.SessionsSkipped != 1 || report.ObservationsCreated != 0 {
		t.Fatalf("ambiguous history report = %#v, want one skipped session and no observations", report)
	}
}

func TestProcessCompletedSessionUsesLiveProvenanceAndIsRetrySafe(t *testing.T) {
	database := openTestDB(t)
	base := time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC)
	seedBackfillChange(t, database, "completed-session", "project-a", base, "internal/payments/charge.go")
	seedBackfillTest(t, database, "completed-session", "project-a", base.Add(time.Minute), 0)
	seedBackfillBoundary(t, database, "completed-session", "project-a", base.Add(2*time.Minute))

	if err := ProcessCompletedSession(context.Background(), database, "completed-session"); err != nil {
		t.Fatalf("ProcessCompletedSession: %v", err)
	}
	observations, err := database.ListAllObservations("project-a", "")
	if err != nil {
		t.Fatalf("ListAllObservations: %v", err)
	}
	if len(observations) != 1 {
		t.Fatalf("observations = %#v, want one", observations)
	}
	assertLiveProvenance(t, database, observations[0].ID)
	stateBeforeRetry, err := database.GetSkillState("verification.pre_ship", "project", "project-a")
	if err != nil || stateBeforeRetry == nil {
		t.Fatalf("GetSkillState before retry = %#v, %v", stateBeforeRetry, err)
	}

	if err := ProcessCompletedSession(context.Background(), database, "completed-session"); err != nil {
		t.Fatalf("ProcessCompletedSession retry: %v", err)
	}
	stateAfterRetry, err := database.GetSkillState("verification.pre_ship", "project", "project-a")
	if err != nil || stateAfterRetry == nil {
		t.Fatalf("GetSkillState after retry = %#v, %v", stateAfterRetry, err)
	}
	if *stateAfterRetry != *stateBeforeRetry {
		t.Fatalf("retry changed skill state:\n before=%#v\n after =%#v", stateBeforeRetry, stateAfterRetry)
	}
}

func TestProcessCompletedSessionRollsBackStateWhenProcessingMarkerCannotPersist(t *testing.T) {
	database := openTestDB(t)
	base := time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC)
	seedBackfillChange(t, database, "interrupted-session", "project-a", base, "internal/payments/charge.go")
	seedBackfillTest(t, database, "interrupted-session", "project-a", base.Add(time.Minute), 0)
	seedBackfillBoundary(t, database, "interrupted-session", "project-a", base.Add(2*time.Minute))

	if _, err := database.Conn().Exec(`CREATE TRIGGER fail_processing_marker BEFORE INSERT ON observation_evidence
		WHEN NEW.source_type = 'processing'
		BEGIN
			SELECT RAISE(ABORT, 'interrupted processing marker write');
		END`); err != nil {
		t.Fatalf("create marker trigger: %v", err)
	}
	if err := ProcessCompletedSession(context.Background(), database, "interrupted-session"); err == nil {
		t.Fatal("ProcessCompletedSession marker failure = nil, want error")
	}

	state, err := database.GetSkillState("verification.pre_ship", "project", "project-a")
	if err != nil {
		t.Fatalf("GetSkillState after interrupted marker write: %v", err)
	}
	if state != nil {
		t.Fatalf("interrupted marker write advanced state = %#v, want no state", state)
	}

	if _, err := database.Conn().Exec(`DROP TRIGGER fail_processing_marker`); err != nil {
		t.Fatalf("drop marker trigger: %v", err)
	}
	if err := ProcessCompletedSession(context.Background(), database, "interrupted-session"); err != nil {
		t.Fatalf("ProcessCompletedSession retry: %v", err)
	}
	state, err = database.GetSkillState("verification.pre_ship", "project", "project-a")
	if err != nil || state == nil {
		t.Fatalf("GetSkillState after retry = %#v, %v", state, err)
	}
	if state.SuccessfulApplications != 1 {
		t.Fatalf("successful applications after retry = %d, want 1", state.SuccessfulApplications)
	}
}

func TestBackfillUsesDefaultAndMaximumLimits(t *testing.T) {
	database := openTestDB(t)
	base := time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC)
	for i := 0; i < maximumBackfillLimit+1; i++ {
		sessionID := "limit-session-" + strconv.Itoa(i)
		at := base.Add(time.Duration(i) * time.Hour)
		seedBackfillChange(t, database, sessionID, "project-a", at, "internal/payments/charge.go")
		seedBackfillBoundary(t, database, sessionID, "project-a", at.Add(time.Second))
		linkBackfillCommit(t, database, "project-a", sessionID, "limit-commit-"+strconv.Itoa(i), at)
	}

	defaultReport, err := Backfill(context.Background(), database, "project-a", 0)
	if err != nil {
		t.Fatalf("Backfill default limit: %v", err)
	}
	if defaultReport.SessionsSelected != defaultBackfillLimit || defaultReport.ObservationsCreated != defaultBackfillLimit {
		t.Fatalf("default report = %#v, want %d selected and created", defaultReport, defaultBackfillLimit)
	}

	maximumReport, err := Backfill(context.Background(), database, "project-a", maximumBackfillLimit+1)
	if err != nil {
		t.Fatalf("Backfill maximum limit: %v", err)
	}
	if maximumReport.SessionsSelected != maximumBackfillLimit || maximumReport.ObservationsCreated != maximumBackfillLimit-defaultBackfillLimit {
		t.Fatalf("maximum report = %#v, want %d selected and %d newly created", maximumReport, maximumBackfillLimit, maximumBackfillLimit-defaultBackfillLimit)
	}
}

func seedBackfillChange(t *testing.T, database *db.DB, sessionID, projectID string, at time.Time, path string) {
	t.Helper()
	if err := database.InsertEvent(&db.Event{ID: sessionID + "-change", TS: at.Format(time.RFC3339), SessionID: sessionID, ProjectID: projectID, EventType: "PostToolUse", ToolName: "Edit", Payload: `{"file_path":"` + path + `"}`}); err != nil {
		t.Fatalf("InsertEvent change: %v", err)
	}
}

func seedBackfillTest(t *testing.T, database *db.DB, sessionID, projectID string, at time.Time, exitCode int) {
	t.Helper()
	payload := `{"tool_input":{"command":"go test ./internal/payments"},"tool_response":{"exit_code":` + strconv.Itoa(exitCode) + `,"stdout":"PASS"}}`
	if exitCode != 0 {
		payload = `{"tool_input":{"command":"go test ./internal/billing"},"tool_response":{"exit_code":1,"stdout":"FAIL"}}`
	}
	if err := database.InsertEvent(&db.Event{ID: sessionID + "-test", TS: at.Format(time.RFC3339), SessionID: sessionID, ProjectID: projectID, EventType: "PostToolUse", ToolName: "Bash", Payload: payload}); err != nil {
		t.Fatalf("InsertEvent test: %v", err)
	}
}

func seedBackfillBoundary(t *testing.T, database *db.DB, sessionID, projectID string, at time.Time) {
	t.Helper()
	if err := database.InsertEvent(&db.Event{ID: sessionID + "-stop", TS: at.Format(time.RFC3339), SessionID: sessionID, ProjectID: projectID, EventType: "SessionEnd", Payload: `{}`}); err != nil {
		t.Fatalf("InsertEvent boundary: %v", err)
	}
}

func linkBackfillCommit(t *testing.T, database *db.DB, projectID, wantSession, sha string, at time.Time) {
	t.Helper()
	gotSession, err := database.LinkCommitToSession(projectID, db.SessionCommit{SHA: sha, CommitTS: at.Format(time.RFC3339), Subject: "test commit", Files: 1, Insertions: 1})
	if err != nil {
		t.Fatalf("LinkCommitToSession: %v", err)
	}
	if gotSession != wantSession {
		t.Fatalf("LinkCommitToSession = %q, want %q", gotSession, wantSession)
	}
}

func seedRepeatedFailure(t *testing.T, database *db.DB, projectID, sessionID string, at time.Time) {
	t.Helper()
	for i := 0; i < 2; i++ {
		if _, err := database.UpsertFailureSignature(&db.FailureSignature{ProjectID: projectID, SessionID: sessionID, ToolName: "Bash", CommandFamily: "go test", Fingerprint: "failure-" + sessionID, ErrorKind: "test_failure", NormalizedMessage: "billing test failed", FirstSeenTS: at.Format(time.RFC3339), LastSeenTS: at.Format(time.RFC3339)}); err != nil {
			t.Fatalf("UpsertFailureSignature: %v", err)
		}
	}
}

func assertBackfillProvenance(t *testing.T, database *db.DB, observationID string) {
	t.Helper()
	assertProvenance(t, database, observationID, "backfill")
}

func assertLiveProvenance(t *testing.T, database *db.DB, observationID string) {
	t.Helper()
	assertProvenance(t, database, observationID, "live")
}

func assertProvenance(t *testing.T, database *db.DB, observationID, want string) {
	t.Helper()
	evidence, err := database.ListObservationEvidence(observationID)
	if err != nil {
		t.Fatalf("ListObservationEvidence: %v", err)
	}
	for _, source := range evidence {
		if source.SourceType == "ingestion" && source.Role == "provenance" && source.SourceID == want {
			return
		}
	}
	t.Fatalf("provenance = %#v, want %q", evidence, want)
}
