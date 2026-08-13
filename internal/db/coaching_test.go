package db

import (
	"fmt"
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

func TestCompleteCoachingListsBypassPublicPageLimit(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer database.Close()

	for i := 0; i < 101; i++ {
		id := fmt.Sprintf("observation-%03d", i)
		if err := database.InsertObservation(&Observation{ID: id, CreatedAt: "2026-08-13T12:00:00Z", ProjectID: "project-a", Kind: "verification_detected", SkillKey: "verification.pre_ship", Confidence: 0.9, Severity: "info", Status: "active", Summary: "tests passed"}); err != nil {
			t.Fatalf("InsertObservation %q: %v", id, err)
		}
		if err := database.InsertCoachingItem(&CoachingItem{ID: fmt.Sprintf("coaching-%03d", i), ObservationID: id, ProjectID: "project-a", SkillKey: "verification.pre_ship", Status: "queued", DeliveryMode: "normal", CreatedAt: "2026-08-13T12:00:00Z"}); err != nil {
			t.Fatalf("InsertCoachingItem %q: %v", id, err)
		}
	}

	observations, err := database.ListObservations("project-a", "active", 0)
	if err != nil {
		t.Fatalf("ListObservations: %v", err)
	}
	if len(observations) != 100 {
		t.Fatalf("ListObservations returned %d records, want bounded 100", len(observations))
	}
	allObservations, err := database.ListAllObservations("project-a", "active")
	if err != nil {
		t.Fatalf("ListAllObservations: %v", err)
	}
	if len(allObservations) != 101 {
		t.Fatalf("ListAllObservations returned %d records, want 101", len(allObservations))
	}

	items, err := database.ListCoachingItems("project-a", "queued", 0)
	if err != nil {
		t.Fatalf("ListCoachingItems: %v", err)
	}
	if len(items) != 100 {
		t.Fatalf("ListCoachingItems returned %d records, want bounded 100", len(items))
	}
	allItems, err := database.ListAllCoachingItems("project-a", "queued")
	if err != nil {
		t.Fatalf("ListAllCoachingItems: %v", err)
	}
	if len(allItems) != 101 {
		t.Fatalf("ListAllCoachingItems returned %d records, want 101", len(allItems))
	}
}

func TestCoachingRepositories(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer database.Close()

	observation := &Observation{ID: "observation-a", CreatedAt: "2026-08-13T12:00:00Z", ProjectID: "project-a", SessionID: "session-a", Kind: "verification_detected", SkillKey: "verification.pre_ship", Confidence: 0.9, Severity: "info", Status: "active", Summary: "tests passed", ExtractorVersion: "detector-v1"}
	if err := database.InsertObservation(observation); err != nil {
		t.Fatalf("InsertObservation: %v", err)
	}
	if err := database.InsertObservation(observation); err != nil {
		t.Fatalf("idempotent InsertObservation: %v", err)
	}
	got, err := database.ListObservations("project-a", "active", 10)
	if err != nil {
		t.Fatalf("ListObservations: %v", err)
	}
	if len(got) != 1 || got[0] != *observation {
		t.Fatalf("observation round trip = %#v, want %#v", got, observation)
	}

	evidence := &ObservationEvidence{ObservationID: observation.ID, SourceType: "event", SourceID: "event-1", Role: "supporting", Excerpt: "go test ./..."}
	if err := database.AddObservationEvidence(evidence); err != nil {
		t.Fatalf("AddObservationEvidence: %v", err)
	}
	if err := database.AddObservationEvidence(&ObservationEvidence{ObservationID: observation.ID, SourceType: "event", SourceID: "event-1", Role: "supporting"}); err != nil {
		t.Fatalf("idempotent AddObservationEvidence: %v", err)
	}
	evidenceRows, err := database.ListObservationEvidence(observation.ID)
	if err != nil {
		t.Fatalf("ListObservationEvidence: %v", err)
	}
	if len(evidenceRows) != 1 || evidenceRows[0] != *evidence {
		t.Fatalf("evidence round trip = %#v, want %#v", evidenceRows, evidence)
	}

	other := &Observation{ID: "observation-b", CreatedAt: "2026-08-13T12:01:00Z", ProjectID: "project-b", SessionID: "session-b", Kind: "unresolved_failure_after_change", SkillKey: "verification.pre_ship", Confidence: 0.4, Severity: "warning", Status: "active", Summary: "failure", ExtractorVersion: "detector-v2"}
	if err := database.InsertObservation(other); err != nil {
		t.Fatalf("InsertObservation other: %v", err)
	}
	otherStatus := &Observation{ID: "observation-c", CreatedAt: "2026-08-13T12:02:00Z", ProjectID: "project-a", SessionID: "session-a", Kind: "repeated_regression", SkillKey: "verification.pre_ship", Confidence: 0.7, Severity: "error", Status: "open", Summary: "regression", ExtractorVersion: "detector-v3"}
	if err := database.InsertObservation(otherStatus); err != nil {
		t.Fatalf("InsertObservation otherStatus: %v", err)
	}
	got, err = database.ListObservations("project-a", "open", 10)
	if err != nil {
		t.Fatalf("ListObservations: %v", err)
	}
	if len(got) != 1 || got[0] != *otherStatus {
		t.Fatalf("filtered observations = %#v", got)
	}
	got, err = database.ListObservations("project-b", "active", 10)
	if err != nil || len(got) != 1 || got[0] != *other {
		t.Fatalf("project-filtered observations = %#v, err=%v", got, err)
	}

	state := &SkillState{SkillKey: "verification.pre_ship", ScopeType: "project", ScopeID: "project-a", State: "unobserved", Confidence: 0.2, EvidenceCount: 1, SuccessfulApplications: 2, FailedApplications: 3, LastObservedAt: "2026-08-13T12:03:00Z", UpdatedAt: "2026-08-13T12:04:00Z"}
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
	state.SuccessfulApplications, state.FailedApplications, state.LastObservedAt, state.UpdatedAt = 4, 5, "2026-08-13T12:05:00Z", "2026-08-13T12:06:00Z"
	if err := database.UpsertSkillState(state); err != nil {
		t.Fatalf("third UpsertSkillState: %v", err)
	}
	gotState, err = database.GetSkillState(state.SkillKey, state.ScopeType, state.ScopeID)
	if err != nil || gotState == nil || *gotState != *state {
		t.Fatalf("state round trip = %#v, want %#v, err=%v", gotState, state, err)
	}

	item := &CoachingItem{ID: "coaching-a", ObservationID: observation.ID, SkillKey: "verification.pre_ship", ProjectID: "project-a", Status: "queued", DeliveryMode: "prompt", Question: "Did you verify this change?", NextAction: "Run the relevant tests", Lesson: "Verify before shipping", CreatedAt: "2026-08-13T12:07:00Z", SurfacedAt: "", ResolvedAt: "", Resolution: ""}
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
	if items[0] != *item {
		t.Fatalf("coaching item round trip = %#v, want %#v", items[0], *item)
	}
	otherItem := &CoachingItem{ID: "coaching-b", ObservationID: other.ID, SkillKey: "verification.pre_ship", ProjectID: "project-b", Status: "queued", DeliveryMode: "session", Question: "Was the failure resolved?", NextAction: "Review the failure", Lesson: "Resolve before shipping", CreatedAt: "2026-08-13T12:08:00Z"}
	if err := database.InsertCoachingItem(otherItem); err != nil {
		t.Fatalf("InsertCoachingItem other: %v", err)
	}
	item.Status, item.SurfacedAt, item.ResolvedAt, item.Resolution = "verified", "2026-08-13T12:00:00Z", "2026-08-13T12:01:00Z", "tests passed"
	if err := database.UpdateCoachingItem(item); err != nil {
		t.Fatalf("UpdateCoachingItem: %v", err)
	}
	items, err = database.ListCoachingItems("project-a", "verified", 10)
	if err != nil {
		t.Fatalf("ListCoachingItems after update: %v", err)
	}
	if len(items) != 1 || items[0] != *item {
		t.Fatalf("updated items = %#v", items)
	}
	items, err = database.ListCoachingItems("project-b", "queued", 10)
	if err != nil || len(items) != 1 || items[0] != *otherItem {
		t.Fatalf("project-filtered coaching items = %#v, err=%v", items, err)
	}
}
