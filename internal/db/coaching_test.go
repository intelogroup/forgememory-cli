package db

import (
	"path/filepath"
	"testing"
)

func TestCoachingSchemaAndSeed(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer database.Close()

	for _, table := range []string{
		"skill_definitions",
		"observations",
		"observation_evidence",
		"skill_states",
		"coaching_items",
	} {
		var count int
		if err := database.Conn().QueryRow(
			"SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", table,
		).Scan(&count); err != nil {
			t.Fatalf("check %s: %v", table, err)
		}
		if count != 1 {
			t.Errorf("table %s count = %d, want 1", table, count)
		}
	}

	var seedCount int
	if err := database.Conn().QueryRow(
		"SELECT count(*) FROM skill_definitions WHERE key='verification.pre_ship'",
	).Scan(&seedCount); err != nil {
		t.Fatalf("check seed: %v", err)
	}
	if seedCount != 1 {
		t.Fatalf("verification.pre_ship seed count = %d, want 1", seedCount)
	}
}

func TestCoachingRepositories(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer database.Close()

	observation := &Observation{ProjectID: "project-a", SessionID: "session-a", Kind: "verification_detected", SkillKey: "verification.pre_ship", Confidence: 0.9, Severity: "info", Status: "active", Summary: "tests passed"}
	if err := database.InsertObservation(observation); err != nil {
		t.Fatalf("InsertObservation: %v", err)
	}
	if err := database.InsertObservation(observation); err != nil {
		t.Fatalf("idempotent InsertObservation: %v", err)
	}
	if observation.ID == "" || observation.CreatedAt == "" {
		t.Fatalf("repository did not assign ID/timestamp: %#v", observation)
	}

	if err := database.AddObservationEvidence(&ObservationEvidence{ObservationID: observation.ID, SourceType: "event", SourceID: "event-1", Role: "supporting", Excerpt: "go test ./..."}); err != nil {
		t.Fatalf("AddObservationEvidence: %v", err)
	}
	if err := database.AddObservationEvidence(&ObservationEvidence{ObservationID: observation.ID, SourceType: "event", SourceID: "event-1", Role: "supporting"}); err != nil {
		t.Fatalf("idempotent AddObservationEvidence: %v", err)
	}
	var evidenceCount int
	if err := database.Conn().QueryRow("SELECT count(*) FROM observation_evidence WHERE observation_id=?", observation.ID).Scan(&evidenceCount); err != nil {
		t.Fatal(err)
	}
	if evidenceCount != 1 {
		t.Fatalf("evidence count = %d, want 1", evidenceCount)
	}

	other := &Observation{ProjectID: "project-a", Kind: "unresolved_failure_after_change", SkillKey: "verification.pre_ship", Status: "open", Summary: "failure"}
	if err := database.InsertObservation(other); err != nil {
		t.Fatalf("InsertObservation other: %v", err)
	}
	got, err := database.ListObservations("project-a", "active", 10)
	if err != nil {
		t.Fatalf("ListObservations: %v", err)
	}
	if len(got) != 1 || got[0].ID != observation.ID {
		t.Fatalf("filtered observations = %#v", got)
	}

	state := &SkillState{SkillKey: "verification.pre_ship", ScopeType: "project", ScopeID: "project-a", State: "unobserved", Confidence: 0.2, EvidenceCount: 1}
	if err := database.UpsertSkillState(state); err != nil {
		t.Fatalf("UpsertSkillState: %v", err)
	}
	state.State, state.Confidence, state.EvidenceCount = "suspected_gap", 0.8, 3
	if err := database.UpsertSkillState(state); err != nil {
		t.Fatalf("second UpsertSkillState: %v", err)
	}
	gotState, err := database.GetSkillState(state.SkillKey, state.ScopeType, state.ScopeID)
	if err != nil {
		t.Fatalf("GetSkillState: %v", err)
	}
	if gotState == nil || gotState.State != "suspected_gap" || gotState.EvidenceCount != 3 {
		t.Fatalf("state = %#v", gotState)
	}

	item := &CoachingItem{ObservationID: observation.ID, SkillKey: "verification.pre_ship", ProjectID: "project-a", Status: "queued", DeliveryMode: "prompt", Question: "Did you verify this change?", NextAction: "Run the relevant tests", Lesson: "Verify before shipping"}
	if err := database.InsertCoachingItem(item); err != nil {
		t.Fatalf("InsertCoachingItem: %v", err)
	}
	items, err := database.ListCoachingItems("project-a", "queued", 10)
	if err != nil {
		t.Fatalf("ListCoachingItems: %v", err)
	}
	if len(items) != 1 || items[0].ID != item.ID {
		t.Fatalf("queued items = %#v", items)
	}
	item.Status, item.SurfacedAt, item.ResolvedAt, item.Resolution = "verified", "2026-08-13T12:00:00Z", "2026-08-13T12:01:00Z", "tests passed"
	if err := database.UpdateCoachingItem(item); err != nil {
		t.Fatalf("UpdateCoachingItem: %v", err)
	}
	items, err = database.ListCoachingItems("project-a", "verified", 10)
	if err != nil {
		t.Fatalf("ListCoachingItems after update: %v", err)
	}
	if len(items) != 1 || items[0].Resolution != "tests passed" {
		t.Fatalf("updated items = %#v", items)
	}
}
