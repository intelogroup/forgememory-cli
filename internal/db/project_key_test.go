package db

import "testing"

func TestProjectKey(t *testing.T) {
	a := ProjectKey("/repos/a/api")
	b := ProjectKey("/repos/b/api")
	if a == "" || b == "" {
		t.Fatal("ProjectKey returned empty for a real root")
	}
	if a == b {
		t.Fatalf("same basename produced the same key: %q", a)
	}
	if got := ProjectKey("/repos/a/api/"); got != a {
		t.Errorf("trailing slash changed the key: %q vs %q", got, a)
	}
	if got := ProjectKey("  "); got != "" {
		t.Errorf("blank root = %q, want empty", got)
	}
}

func TestProjectScopeSeparatesSameBasename(t *testing.T) {
	database := openPrinciplesDB(t)

	keyA := ProjectKey("/repos/a/api")
	keyB := ProjectKey("/repos/b/api")
	seed := []*Principle{
		{Title: "Zebra A", Narrative: "alpha", ProjectID: "api", ProjectKey: keyA, Type: "pattern", ImpactScore: 0.5},
		{Title: "Zebra B", Narrative: "beta", ProjectID: "api", ProjectKey: keyB, Type: "pattern", ImpactScore: 0.5},
		{Title: "Zebra legacy", Narrative: "legacy", ProjectID: "api", Type: "pattern", ImpactScore: 0.5},
		{Title: "Zebra global", Narrative: "global", ProjectID: "other", Scope: "global", Type: "pattern", ImpactScore: 0.5},
	}
	for _, p := range seed {
		if inserted, err := database.InsertPrinciple(p); err != nil || !inserted {
			t.Fatalf("InsertPrinciple(%s): inserted=%v err=%v", p.Title, inserted, err)
		}
		if p.ProjectKey != "" && p.ProjectKey != keyA && p.ProjectKey != keyB {
			t.Fatalf("project key not persisted: %q", p.ProjectKey)
		}
	}

	titles := func(ps []Principle) map[string]bool {
		out := map[string]bool{}
		for _, p := range ps {
			out[p.Title] = true
		}
		return out
	}

	gotA, err := database.RecentPrinciplesForProject("api", keyA, 10)
	if err != nil {
		t.Fatalf("RecentPrinciplesForProject(A): %v", err)
	}
	seen := titles(gotA)
	if !seen["Zebra A"] || !seen["Zebra legacy"] || seen["Zebra B"] {
		t.Errorf("project A rows = %v", seen)
	}

	gotB, err := database.RecentPrinciplesForProject("api", keyB, 10)
	if err != nil {
		t.Fatalf("RecentPrinciplesForProject(B): %v", err)
	}
	seen = titles(gotB)
	if !seen["Zebra B"] || !seen["Zebra legacy"] || seen["Zebra A"] {
		t.Errorf("project B rows = %v", seen)
	}

	legacy, err := database.RecentPrinciplesForProject("api", "", 10)
	if err != nil {
		t.Fatalf("RecentPrinciplesForProject(no key): %v", err)
	}
	if len(legacy) != 3 {
		t.Errorf("no-key lookup = %d rows, want 3 (basename fallback sees collided rows)", len(legacy))
	}

	visible, err := database.RecentPrinciplesVisibleTo("api", keyA, 10)
	if err != nil {
		t.Fatalf("RecentPrinciplesVisibleTo: %v", err)
	}
	seen = titles(visible)
	if !seen["Zebra A"] || !seen["Zebra legacy"] || !seen["Zebra global"] || seen["Zebra B"] {
		t.Errorf("visible rows = %v", seen)
	}

	found, err := database.SearchPrinciplesVisibleTo("api", keyA, []string{"zebra"}, 0, 0, 10)
	if err != nil {
		t.Fatalf("SearchPrinciplesVisibleTo: %v", err)
	}
	seen = titles(found)
	if !seen["Zebra A"] || seen["Zebra B"] || !seen["Zebra global"] {
		t.Errorf("search rows = %v", seen)
	}
}
