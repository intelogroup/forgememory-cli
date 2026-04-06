package db

import (
	"path/filepath"
	"testing"
	"time"
)

func TestTouchExternalContextSummariesUpdatesLastUsedAt(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer database.Close()

	summary := &ExternalContextSummary{
		ProjectID:  "project-alpha",
		Source:     "context7",
		Query:      "rust error e0599",
		Title:      "Rust docs",
		Narrative:  "Import trait before method call.",
		TrustScore: 0.9,
		ExpiresAt:  time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
	}
	if err := database.UpsertExternalContextSummary(summary); err != nil {
		t.Fatalf("UpsertExternalContextSummary failed: %v", err)
	}

	if err := database.TouchExternalContextSummaries([]string{summary.ID}, "2026-04-05T12:00:00Z"); err != nil {
		t.Fatalf("TouchExternalContextSummaries failed: %v", err)
	}

	got, err := database.SearchExternalContextSummariesByProject("project-alpha", "context7", "", "", 5)
	if err != nil {
		t.Fatalf("SearchExternalContextSummariesByProject failed: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0].LastUsedAt != "2026-04-05T12:00:00Z" {
		t.Fatalf("LastUsedAt = %q, want %q", got[0].LastUsedAt, "2026-04-05T12:00:00Z")
	}
}

func TestFreshExternalContextSummariesByProjectSortsByLastUsedAt(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer database.Close()

	now := time.Now().UTC()
	old := &ExternalContextSummary{
		ProjectID:  "project-alpha",
		Source:     "context7",
		Query:      "rust old",
		Title:      "Old summary",
		Narrative:  "Old narrative",
		TrustScore: 0.9,
		ExpiresAt:  now.Add(6 * time.Hour).Format(time.RFC3339),
	}
	newerByTS := &ExternalContextSummary{
		ProjectID:  "project-alpha",
		Source:     "context7",
		Query:      "rust newer",
		Title:      "Newer summary",
		Narrative:  "Newer narrative",
		TrustScore: 0.9,
		TS:         now.Add(2 * time.Hour).Format(time.RFC3339),
		ExpiresAt:  now.Add(6 * time.Hour).Format(time.RFC3339),
	}
	if err := database.UpsertExternalContextSummary(old); err != nil {
		t.Fatalf("upsert old failed: %v", err)
	}
	if err := database.UpsertExternalContextSummary(newerByTS); err != nil {
		t.Fatalf("upsert newer failed: %v", err)
	}

	if err := database.TouchExternalContextSummaries([]string{old.ID}, now.Add(3*time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatalf("TouchExternalContextSummaries failed: %v", err)
	}

	got, err := database.FreshExternalContextSummariesByProject("project-alpha", 5)
	if err != nil {
		t.Fatalf("FreshExternalContextSummariesByProject failed: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	if got[0].ID != old.ID {
		t.Fatalf("expected recently used summary first; got %q want %q", got[0].ID, old.ID)
	}
}
