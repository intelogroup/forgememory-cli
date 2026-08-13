package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forge/forge/internal/coach"
	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/observations"
	"github.com/forge/forge/internal/skills"
)

type coachExplainOutput struct {
	ProjectID          string                   `json:"project_id"`
	Observation        db.Observation           `json:"observation"`
	Item               coachListEntry           `json:"item"`
	SupportingEvidence []db.ObservationEvidence `json:"supporting_evidence"`
	CounterEvidence    []db.ObservationEvidence `json:"counter_evidence"`
	NeedsMoreEvidence  bool                     `json:"needs_more_evidence"`
}

type coachStatusOutput struct {
	ProjectID string            `json:"project_id"`
	Counts    coachStatusCounts `json:"counts"`
}

type coachStatusCounts struct {
	Total     int `json:"total"`
	Queued    int `json:"queued"`
	Surfaced  int `json:"surfaced"`
	Accepted  int `json:"accepted"`
	Deferred  int `json:"deferred"`
	Dismissed int `json:"dismissed"`
}

type coachListOutput struct {
	ProjectID string           `json:"project_id"`
	Items     []coachListEntry `json:"items"`
}

type coachListEntry struct {
	ID                string  `json:"id"`
	ObservationID     string  `json:"observation_id"`
	SkillKey          string  `json:"skill_key"`
	Status            string  `json:"status"`
	Confidence        float64 `json:"confidence"`
	State             string  `json:"state"`
	EvidenceCount     int     `json:"evidence_count"`
	NeedsMoreEvidence bool    `json:"needs_more_evidence"`
}

func TestCoachStatusJSONEmpty(t *testing.T) {
	home := t.TempDir()
	projectID := coachTestProjectID(t)

	stdout, stderr, code := runForge(t, home, "coach", "status", "--json")
	if code != 0 {
		t.Fatalf("forge coach status --json exited %d: stderr=%q", code, stderr)
	}
	if stderr != "" {
		t.Fatalf("forge coach status --json wrote stderr: %q", stderr)
	}

	var got coachStatusOutput
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("forge coach status --json must emit JSON only: %v\noutput: %q", err, stdout)
	}
	if got.ProjectID != projectID {
		t.Fatalf("project_id = %q, want %q", got.ProjectID, projectID)
	}
	if got.Counts != (coachStatusCounts{}) {
		t.Fatalf("counts = %+v, want empty counts", got.Counts)
	}
}

func TestCoachListJSONEmpty(t *testing.T) {
	home := t.TempDir()

	stdout, stderr, code := runForge(t, home, "coach", "list", "--json")
	if code != 0 {
		t.Fatalf("forge coach list --json exited %d: stderr=%q", code, stderr)
	}
	var got coachListOutput
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("forge coach list --json must emit JSON only: %v\noutput: %q", err, stdout)
	}
	if got.ProjectID != coachTestProjectID(t) {
		t.Fatalf("project_id = %q, want current project", got.ProjectID)
	}
	if got.Items == nil || len(got.Items) != 0 {
		t.Fatalf("items = %#v, want empty array", got.Items)
	}
}

func TestCoachReviewJSONEmpty(t *testing.T) {
	home := t.TempDir()

	stdout, stderr, code := runForge(t, home, "coach", "review", "--json")
	if code != 0 {
		t.Fatalf("forge coach review --json exited %d: stderr=%q", code, stderr)
	}
	var got coachListOutput
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("forge coach review --json must emit JSON only: %v\noutput: %q", err, stdout)
	}
	if got.Items == nil || len(got.Items) != 0 {
		t.Fatalf("items = %#v, want empty array", got.Items)
	}
}

func TestCoachSubcommandsExposeEvidenceAndDelegateLifecycle(t *testing.T) {
	home := t.TempDir()
	projectID := coachTestProjectID(t)
	observationID := "coach-observation"
	seedCoachCLIItem(t, home, projectID, observationID)

	stdout, stderr, code := runForge(t, home, "coach", "explain", observationID, "--json")
	if code != 0 {
		t.Fatalf("forge coach explain exited %d: stderr=%q", code, stderr)
	}
	var explained coachExplainOutput
	if err := json.Unmarshal([]byte(stdout), &explained); err != nil {
		t.Fatalf("explain --json must emit JSON only: %v\noutput: %q", err, stdout)
	}
	if explained.Observation.ID != observationID || explained.Item.Confidence != 0.91 {
		t.Fatalf("explain = %#v, want observation and confidence", explained)
	}
	if len(explained.SupportingEvidence) != 1 || len(explained.CounterEvidence) != 1 {
		t.Fatalf("explain evidence = supporting %#v counter %#v, want both", explained.SupportingEvidence, explained.CounterEvidence)
	}
	if explained.NeedsMoreEvidence {
		t.Fatal("suspected gap must not claim it needs more evidence")
	}

	_, stderr, code = runForge(t, home, "coach", "defer", observationID)
	if code != 0 || stderr != "" {
		t.Fatalf("forge coach defer failed: code=%d stderr=%q", code, stderr)
	}
	stdout, stderr, code = runForge(t, home, "coach", "review", "--json")
	if code != 0 {
		t.Fatalf("forge coach review exited %d: stderr=%q", code, stderr)
	}
	var review coachListOutput
	if err := json.Unmarshal([]byte(stdout), &review); err != nil {
		t.Fatalf("review --json must emit JSON only: %v\noutput: %q", err, stdout)
	}
	if len(review.Items) != 1 || review.Items[0].Status != "deferred" {
		t.Fatalf("review items = %#v, want deferred item", review.Items)
	}

	_, stderr, code = runForge(t, home, "coach", "accept", observationID)
	if code != 0 || stderr != "" {
		t.Fatalf("forge coach accept failed: code=%d stderr=%q", code, stderr)
	}
	_, stderr, code = runForge(t, home, "coach", "accept", observationID)
	if code == 0 || !strings.Contains(stderr, "already accepted") {
		t.Fatalf("second accept = code %d stderr %q, want lifecycle error", code, stderr)
	}
}

func TestCoachDismissValidatesObservationAndReason(t *testing.T) {
	home := t.TempDir()
	projectID := coachTestProjectID(t)
	seedCoachCLIItem(t, home, projectID, "dismiss-observation")

	_, stderr, code := runForge(t, home, "coach", "dismiss", "missing", "--reason", "not_relevant")
	if code == 0 || !strings.Contains(stderr, "was not found") {
		t.Fatalf("unknown observation = code %d stderr %q, want not-found error", code, stderr)
	}
	_, stderr, code = runForge(t, home, "coach", "dismiss", "dismiss-observation", "--reason", "made_up")
	if code == 0 || !strings.Contains(stderr, "invalid dismissal reason") {
		t.Fatalf("invalid reason = code %d stderr %q, want validation error", code, stderr)
	}
	_, stderr, code = runForge(t, home, "coach", "dismiss", "dismiss-observation", "--reason", "not_relevant")
	if code != 0 || stderr != "" {
		t.Fatalf("dismiss = code %d stderr %q", code, stderr)
	}
}

func TestCoachRejectsUnknownSubcommand(t *testing.T) {
	_, stderr, code := runForge(t, t.TempDir(), "coach", "stauts")
	if code == 0 || !strings.Contains(stderr, "Usage: forge coach") {
		t.Fatalf("unknown subcommand = code %d stderr %q, want usage error", code, stderr)
	}
}

func TestCoachReviewIncludesObservationsNeedingMoreEvidence(t *testing.T) {
	home := t.TempDir()
	projectID := coachTestProjectID(t)
	database, err := db.Open(filepath.Join(home, ".forge", "forge.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.InsertObservation(&db.Observation{
		ID: "needs-evidence", CreatedAt: "2026-08-13T12:00:00Z", ProjectID: projectID,
		SessionID: "review-session", Kind: "code_change_without_relevant_test",
		SkillKey: "verification.pre_ship", Confidence: 0.42, Severity: "info",
		Status: "active", Summary: "Needs another observation.",
	}); err != nil {
		database.Close()
		t.Fatal(err)
	}
	database.Close()

	stdout, stderr, code := runForge(t, home, "coach", "review", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("coach review failed: code=%d stderr=%q", code, stderr)
	}
	var review coachListOutput
	if err := json.Unmarshal([]byte(stdout), &review); err != nil {
		t.Fatalf("review --json must emit JSON only: %v\noutput: %q", err, stdout)
	}
	if len(review.Items) != 1 || review.Items[0].ObservationID != "needs-evidence" || !review.Items[0].NeedsMoreEvidence {
		t.Fatalf("review items = %#v, want needs-evidence observation", review.Items)
	}
}

func seedCoachCLIItem(t *testing.T, home, projectID, observationID string) {
	t.Helper()
	database, err := db.Open(filepath.Join(home, ".forge", "forge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.InsertObservation(&db.Observation{ID: observationID, CreatedAt: "2026-08-13T12:00:00Z", ProjectID: projectID, SessionID: "coach-session", Kind: "code_change_without_relevant_test", SkillKey: "verification.pre_ship", Confidence: 0.91, Severity: "warning", Status: "active", Summary: "A code change lacks a relevant test."}); err != nil {
		t.Fatal(err)
	}
	for _, evidence := range []db.ObservationEvidence{
		{ObservationID: observationID, SourceType: "event", SourceID: "edit", Role: "supporting", Excerpt: "edited internal/coach.go"},
		{ObservationID: observationID, SourceType: "event", SourceID: "unrelated-test", Role: "counter", Excerpt: "go test ./internal/auth"},
	} {
		if err := database.AddObservationEvidence(&evidence); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.UpsertSkillState(&db.SkillState{SkillKey: "verification.pre_ship", ScopeType: skills.ScopeProject, ScopeID: projectID, State: skills.StateSuspectedGap, Confidence: 0.91, EvidenceCount: 2, FailedApplications: 2}); err != nil {
		t.Fatal(err)
	}
	if err := database.InsertCoachingItem(&db.CoachingItem{ID: "coach-item-" + observationID, ObservationID: observationID, ProjectID: projectID, SkillKey: "verification.pre_ship", Status: "queued", DeliveryMode: "normal", Question: "What behavior should the test prove?", NextAction: "Add a narrow test.", Lesson: "A code change lacks a relevant test.", CreatedAt: "2026-08-13T12:00:00Z"}); err != nil {
		t.Fatal(err)
	}
}

func coachTestProjectID(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return detectProjectIDForPath(filepath.Dir(wd))
}

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
