package evidence

import (
	"path/filepath"
	"testing"

	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/observations"
)

func TestSavePersistsAuditableSourcesAndProvenance(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	store := Store{DB: database, ExtractorVersion: "verification-v1"}
	got, err := store.Save("project-a", observations.ObservationDraft{
		Kind:       "verification_detected",
		SkillKey:   "verification.pre_ship",
		Confidence: 0.90,
		Severity:   "info",
		Summary:    "Relevant tests passed.",
		SupportingSources: []observations.SourceReference{
			{SourceType: "event", SourceID: "change-1", Excerpt: "internal/service.go"},
			{SourceType: "event", SourceID: "test-1", Excerpt: "go test ./internal/service"},
		},
		CounterSources: []observations.SourceReference{
			{SourceType: "failure_signature", SourceID: "failure-1", Excerpt: "prior intermittent failure"},
		},
	}, true)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got.ProjectID != "project-a" || got.SessionID != "" || got.ExtractorVersion != "verification-v1" {
		t.Fatalf("observation = %#v", got)
	}

	evidenceRows, err := database.ListObservationEvidence(got.ID)
	if err != nil {
		t.Fatalf("ListObservationEvidence: %v", err)
	}
	if len(evidenceRows) != 4 {
		t.Fatalf("evidence rows = %#v, want 4 rows", evidenceRows)
	}
	want := map[string]string{
		"event/change-1":              "supporting",
		"event/test-1":                "supporting",
		"failure_signature/failure-1": "counter",
		"ingestion/live":              "provenance",
	}
	for _, row := range evidenceRows {
		key := row.SourceType + "/" + row.SourceID
		if want[key] != row.Role {
			t.Errorf("evidence %s role = %q, want %q", key, row.Role, want[key])
		}
		delete(want, key)
	}
	if len(want) != 0 {
		t.Fatalf("missing evidence: %#v", want)
	}
}

func TestSaveDeduplicatesAnObservationByProjectSessionKindAndSourceSet(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	store := Store{DB: database, ExtractorVersion: "verification-v1", SessionID: "session-a"}
	draft := observations.ObservationDraft{
		Kind: "verification_detected", SkillKey: "verification.pre_ship", Confidence: 0.9, Severity: "info", Summary: "tests passed",
		SupportingSources: []observations.SourceReference{
			{SourceType: "event", SourceID: "test-1", Excerpt: "go test"},
			{SourceType: "event", SourceID: "change-1", Excerpt: "internal/service.go"},
		},
	}
	first, err := store.Save("project-a", draft, false)
	if err != nil {
		t.Fatalf("first Save: %v", err)
	}
	draft.SupportingSources[0], draft.SupportingSources[1] = draft.SupportingSources[1], draft.SupportingSources[0]
	second, err := store.Save("project-a", draft, false)
	if err != nil {
		t.Fatalf("second Save: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("observation IDs = %q and %q, want one deduplicated observation", first.ID, second.ID)
	}
	rows, err := database.ListObservations("project-a", "active", 10)
	if err != nil {
		t.Fatalf("ListObservations: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("observations = %#v, want exactly one", rows)
	}
	evidenceRows, err := database.ListObservationEvidence(first.ID)
	if err != nil {
		t.Fatalf("ListObservationEvidence: %v", err)
	}
	if len(evidenceRows) != 3 || evidenceRows[2].SourceID != "backfill" {
		t.Fatalf("evidence rows = %#v, want deduplicated sources and backfill provenance", evidenceRows)
	}
}
