package db

import "testing"

func TestScopeVisibility(t *testing.T) {
	database := openPrinciplesDB(t)

	own := &Principle{Title: "own lesson", Narrative: "n", ProjectID: "proj-a", Type: "pattern", ImpactScore: 0.5}
	global := &Principle{Title: "global lesson", Narrative: "n", ProjectID: "proj-b", Type: "pattern", ImpactScore: 0.6, Scope: "global"}
	foreign := &Principle{Title: "foreign lesson", Narrative: "n", ProjectID: "proj-b", Type: "pattern", ImpactScore: 0.7}
	for _, p := range []*Principle{own, global, foreign} {
		if inserted, err := database.InsertPrinciple(p); err != nil || !inserted {
			t.Fatalf("InsertPrinciple(%s): inserted=%v err=%v", p.Title, inserted, err)
		}
	}
	if own.Scope != "project" {
		t.Errorf("default scope = %q, want project", own.Scope)
	}

	visible, err := database.RecentPrinciplesVisibleTo("proj-a", 10)
	if err != nil {
		t.Fatalf("RecentPrinciplesVisibleTo: %v", err)
	}
	seen := map[string]bool{}
	for _, p := range visible {
		seen[p.Title] = true
	}
	if !seen["own lesson"] || !seen["global lesson"] {
		t.Errorf("expected own + global, got %v", seen)
	}
	if seen["foreign lesson"] {
		t.Error("project-scoped row from another project leaked into visibility")
	}

	globals, err := database.RecentGlobalPrinciples(10)
	if err != nil {
		t.Fatalf("RecentGlobalPrinciples: %v", err)
	}
	if len(globals) != 1 || globals[0].Title != "global lesson" {
		t.Errorf("RecentGlobalPrinciples = %v, want only the global row", globals)
	}
}

func TestSearchPrinciplesVisibleToHidesOtherProjectLocal(t *testing.T) {
	database := openPrinciplesDB(t)

	local := &Principle{
		Title: "Zebracorn caching layer", Narrative: "Cache Zebracorn responses in process.",
		ProjectID: "proj-b", Type: "performance", ImpactScore: 0.8,
	}
	global := &Principle{
		Title: "Zebracorn global cache", Narrative: "Cache Zebracorn responses at the edge.",
		ProjectID: "proj-c", Type: "performance", ImpactScore: 0.8, Scope: "global",
	}
	for _, p := range []*Principle{local, global} {
		if inserted, err := database.InsertPrinciple(p); err != nil || !inserted {
			t.Fatalf("InsertPrinciple(%s): inserted=%v err=%v", p.Title, inserted, err)
		}
	}

	fromA, err := database.SearchPrinciplesVisibleTo("proj-a", []string{"zebracorn"}, 0, 0, 10)
	if err != nil {
		t.Fatalf("SearchPrinciplesVisibleTo(proj-a): %v", err)
	}
	if len(fromA) != 1 || fromA[0].Title != "Zebracorn global cache" {
		t.Errorf("proj-a results = %v, want only the global row", fromA)
	}

	fromB, err := database.SearchPrinciplesVisibleTo("proj-b", []string{"zebracorn"}, 0, 0, 10)
	if err != nil {
		t.Fatalf("SearchPrinciplesVisibleTo(proj-b): %v", err)
	}
	if len(fromB) != 2 {
		t.Errorf("proj-b results = %d, want own + global", len(fromB))
	}

	anonymous, err := database.SearchPrinciplesVisibleTo("", []string{"zebracorn"}, 0, 0, 10)
	if err != nil {
		t.Fatalf("SearchPrinciplesVisibleTo(\"\"): %v", err)
	}
	if len(anonymous) != 1 || anonymous[0].Title != "Zebracorn global cache" {
		t.Errorf("anonymous results = %v, want global only", anonymous)
	}
}
