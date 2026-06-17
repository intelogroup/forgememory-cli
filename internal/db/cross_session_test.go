package db

import (
	"fmt"
	"path/filepath"
	"testing"
)

func TestInsertCrossSessionPattern_Defaults(t *testing.T) {
	database := openTestDB(t)
	p := &CrossSessionPattern{
		ProjectID:   "proj-a",
		Pattern:     "Always run tests before commit",
		PatternType: "workflow",
		Confidence:  0.8,
	}
	if err := database.InsertCrossSessionPattern(p); err != nil {
		t.Fatalf("InsertCrossSessionPattern: %v", err)
	}
	if p.ID == "" {
		t.Fatal("expected ID to be assigned")
	}
	if p.TS == "" {
		t.Fatal("expected TS to be assigned")
	}
}

func TestRecentCrossSessionPatternsByProject_PathVariants(t *testing.T) {
	database := openTestDB(t)
	unixPath := "/home/user/myproject"
	winPath := `C:\Users\dev\myproject`

	for _, p := range []CrossSessionPattern{
		{ProjectID: unixPath, Pattern: "unix pattern", PatternType: "workflow", Confidence: 0.7},
		{ProjectID: winPath, Pattern: "win pattern", PatternType: "workflow", Confidence: 0.6},
	} {
		if err := database.InsertCrossSessionPattern(&p); err != nil {
			t.Fatalf("InsertCrossSessionPattern: %v", err)
		}
	}

	// Query with unix-style path should match both via LIKE selectors.
	got, err := database.RecentCrossSessionPatternsByProject(unixPath, 10)
	if err != nil {
		t.Fatalf("RecentCrossSessionPatternsByProject: %v", err)
	}
	if len(got) < 2 {
		t.Fatalf("len(patterns) = %d, want at least 2 for path variant match", len(got))
	}
}

func TestUnSynthesizedPatterns_OnlyUnsynthesized(t *testing.T) {
	database := openTestDB(t)
	unsynth := CrossSessionPattern{ProjectID: "p1", Pattern: "a", PatternType: "workflow", Confidence: 0.5}
	synth := CrossSessionPattern{ProjectID: "p1", Pattern: "b", PatternType: "workflow", Confidence: 0.5, Synthesized: true}
	if err := database.InsertCrossSessionPattern(&unsynth); err != nil {
		t.Fatalf("insert unsynth: %v", err)
	}
	if err := database.InsertCrossSessionPattern(&synth); err != nil {
		t.Fatalf("insert synth: %v", err)
	}

	got, err := database.UnSynthesizedPatterns(10)
	if err != nil {
		t.Fatalf("UnSynthesizedPatterns: %v", err)
	}
	for _, p := range got {
		if p.Synthesized {
			t.Fatalf("pattern %s should not be synthesized", p.ID)
		}
	}
	found := false
	for _, p := range got {
		if p.ID == unsynth.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("expected unsynthesized pattern in results")
	}
}

func TestMarkCrossSessionSynthesized_Batch(t *testing.T) {
	database := openTestDB(t)
	var ids []string
	for i := 0; i < 3; i++ {
		p := CrossSessionPattern{ProjectID: "batch-proj", Pattern: "pat", PatternType: "workflow", Confidence: 0.5}
		if err := database.InsertCrossSessionPattern(&p); err != nil {
			t.Fatalf("InsertCrossSessionPattern: %v", err)
		}
		ids = append(ids, p.ID)
	}
	if err := database.MarkCrossSessionSynthesized(ids); err != nil {
		t.Fatalf("MarkCrossSessionSynthesized: %v", err)
	}
	got, err := database.UnSynthesizedPatterns(10)
	if err != nil {
		t.Fatalf("UnSynthesizedPatterns: %v", err)
	}
	for _, p := range got {
		for _, id := range ids {
			if p.ID == id {
				t.Fatalf("pattern %s should be marked synthesized", id)
			}
		}
	}
}

func TestProjectIDsWithEnoughSessions_MinThreshold(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	database, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	for i := 0; i < 4; i++ {
		s := &SessionSummary{
			SessionID: "s-low",
			ProjectID: "proj-low",
			Summary:   "x",
		}
		if err := database.InsertSessionSummary(s); err != nil {
			t.Fatalf("InsertSessionSummary low: %v", err)
		}
	}
	for i := 0; i < 6; i++ {
		s := &SessionSummary{
			SessionID: fmt.Sprintf("s-high-%d", i),
			ProjectID: "proj-high",
			Summary:   "x",
		}
		if err := database.InsertSessionSummary(s); err != nil {
			t.Fatalf("InsertSessionSummary high: %v", err)
		}
	}

	got, err := database.ProjectIDsWithEnoughSessions(5)
	if err != nil {
		t.Fatalf("ProjectIDsWithEnoughSessions: %v", err)
	}
	hasHigh := false
	hasLow := false
	for _, p := range got {
		if p == "proj-high" {
			hasHigh = true
		}
		if p == "proj-low" {
			hasLow = true
		}
	}
	if !hasHigh {
		t.Fatal("proj-high should meet min threshold")
	}
	if hasLow {
		t.Fatal("proj-low should be below min threshold")
	}
}
