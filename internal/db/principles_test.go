package db

import (
	"path/filepath"
	"testing"
)

func openPrinciplesDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestFilterConcepts(t *testing.T) {
	cases := []struct {
		input []string
		want  []string
	}{
		{[]string{"security", "unknown", "pattern"}, []string{"security", "pattern"}},
		{[]string{"GOTCHA", " Performance "}, []string{"gotcha", "performance"}},
		{[]string{"not-a-concept"}, nil},
		{nil, nil},
	}
	for _, tc := range cases {
		got := FilterConcepts(tc.input)
		if len(got) != len(tc.want) {
			t.Errorf("FilterConcepts(%v) = %v, want %v", tc.input, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("FilterConcepts(%v)[%d] = %q, want %q", tc.input, i, got[i], tc.want[i])
			}
		}
	}
}

func TestBuildFTSQuery(t *testing.T) {
	cases := []struct {
		terms []string
		want  string
	}{
		{[]string{"rust", "error"}, `"rust" OR "error"`},
		{[]string{"a term"}, `"a term"`},
		{[]string{"", "foo"}, `"foo"`},
		{nil, ""},
	}
	for _, tc := range cases {
		got := buildFTSQuery(tc.terms)
		if got != tc.want {
			t.Errorf("buildFTSQuery(%v) = %q, want %q", tc.terms, got, tc.want)
		}
	}
}

func TestProjectIDSelectors(t *testing.T) {
	exact, unix, win := projectIDSelectors("/home/user/myproject")
	if exact != "/home/user/myproject" {
		t.Errorf("exact = %q", exact)
	}
	if unix != "%/myproject" {
		t.Errorf("unix = %q", unix)
	}
	if win != `%\myproject` {
		t.Errorf("win = %q", win)
	}
}

func TestEncodeDecodeStringSlice(t *testing.T) {
	input := []string{"a", "b", "c"}
	encoded := encodeStringSlice(input)
	decoded := decodeStringSlice(encoded)
	if len(decoded) != len(input) {
		t.Fatalf("len = %d, want %d", len(decoded), len(input))
	}
	for i, v := range decoded {
		if v != input[i] {
			t.Errorf("[%d] = %q, want %q", i, v, input[i])
		}
	}
	if encodeStringSlice(nil) != "" {
		t.Error("expected empty string for nil slice")
	}
	if decodeStringSlice("") != nil {
		t.Error("expected nil for empty string")
	}
}

func TestFingerprint(t *testing.T) {
	f1 := fingerprint("same title", "proj-a")
	f2 := fingerprint("same title", "proj-a")
	if f1 != f2 {
		t.Errorf("fingerprint not stable: %q vs %q", f1, f2)
	}
	f3 := fingerprint("different title", "proj-a")
	if f1 == f3 {
		t.Error("different titles should produce different fingerprints")
	}
}

func TestRecentPrinciplesByProjectAll(t *testing.T) {
	db := openPrinciplesDB(t)

	if _, err := db.InsertPrinciple(&Principle{
		Type:      "pattern",
		Title:     "alpha insight",
		Narrative: "narrative a",
		ProjectID: "/home/user/proj-alpha",
	}); err != nil {
		t.Fatalf("InsertPrinciple: %v", err)
	}
	if _, err := db.InsertPrinciple(&Principle{
		Type:      "pattern",
		Title:     "beta insight",
		Narrative: "narrative b",
		ProjectID: "/home/user/proj-beta",
	}); err != nil {
		t.Fatalf("InsertPrinciple: %v", err)
	}

	// All results for proj-alpha
	all, err := db.RecentPrinciplesByProjectAll("/home/user/proj-alpha", 10)
	if err != nil {
		t.Fatalf("RecentPrinciplesByProjectAll: %v", err)
	}
	if len(all) != 1 || all[0].Title != "alpha insight" {
		t.Errorf("unexpected results: %+v", all)
	}
}

func TestRecentPrinciplesByProjectAll_EmptyProjectReturnsAll(t *testing.T) {
	db := openPrinciplesDB(t)
	for i, title := range []string{"first", "second"} {
		if _, err := db.InsertPrinciple(&Principle{
			Type:      "pattern",
			Title:     title,
			Narrative: "narrative",
			ProjectID: "proj-" + string(rune('a'+i)),
		}); err != nil {
			t.Fatalf("InsertPrinciple: %v", err)
		}
	}
	all, err := db.RecentPrinciplesByProjectAll("", 10)
	if err != nil {
		t.Fatalf("RecentPrinciplesByProjectAll(empty): %v", err)
	}
	if len(all) < 2 {
		t.Errorf("expected >= 2 results, got %d", len(all))
	}
}

func TestActiveOnly(t *testing.T) {
	db := openPrinciplesDB(t)
	principles := []Principle{
		{Status: "active", Title: "keep-active"},
		{Status: "", Title: "keep-legacy"},
		{Status: "conflicting", Title: "skip-conflicting"},
	}
	got, err := db.activeOnly(principles, nil)
	if err != nil {
		t.Fatalf("activeOnly: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("activeOnly = %d, want 2", len(got))
	}
	for _, p := range got {
		if p.Status == "conflicting" {
			t.Errorf("conflicting principle leaked: %+v", p)
		}
	}
}

func TestActiveOnly_PropagatesError(t *testing.T) {
	db := openPrinciplesDB(t)
	testErr := &testError{"injected error"}
	_, err := db.activeOnly(nil, testErr)
	if err != testErr {
		t.Errorf("expected propagated error, got %v", err)
	}
}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }

func TestSessionEventsDB(t *testing.T) {
	db := openPrinciplesDB(t)

	for i := 0; i < 3; i++ {
		if err := db.InsertEvent(&Event{
			SessionID:  "sess-target",
			ProjectID:  "proj",
			SourceTool: "claude",
			EventType:  "PostToolUse",
			Payload:    "{}",
		}); err != nil {
			t.Fatalf("InsertEvent: %v", err)
		}
	}
	if err := db.InsertEvent(&Event{
		SessionID:  "other-sess",
		ProjectID:  "proj",
		SourceTool: "claude",
		EventType:  "PostToolUse",
		Payload:    "{}",
	}); err != nil {
		t.Fatalf("InsertEvent other: %v", err)
	}

	events, err := db.SessionEvents("sess-target", 10)
	if err != nil {
		t.Fatalf("SessionEvents: %v", err)
	}
	if len(events) != 3 {
		t.Errorf("len(events) = %d, want 3", len(events))
	}
}

func TestSearchEventsByProject(t *testing.T) {
	db := openPrinciplesDB(t)

	if err := db.InsertEvent(&Event{
		SessionID:  "s1",
		ProjectID:  "proj-search",
		SourceTool: "claude",
		EventType:  "PostToolUse",
		Payload:    "unique-token-xyz search target",
	}); err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}
	if err := db.InsertEvent(&Event{
		SessionID:  "s2",
		ProjectID:  "proj-other",
		SourceTool: "claude",
		EventType:  "PostToolUse",
		Payload:    "unique-token-xyz search target",
	}); err != nil {
		t.Fatalf("InsertEvent other: %v", err)
	}

	events, err := db.SearchEventsByProject("proj-search", "unique-token-xyz", 10)
	if err != nil {
		t.Fatalf("SearchEventsByProject: %v", err)
	}
	for _, e := range events {
		if e.ProjectID != "proj-search" {
			t.Errorf("unexpected project_id %q in results", e.ProjectID)
		}
	}
}

func TestSearchEventsByProject_EmptyFallsBackToGlobal(t *testing.T) {
	db := openPrinciplesDB(t)
	if err := db.InsertEvent(&Event{
		SessionID:  "s1",
		ProjectID:  "any-proj",
		SourceTool: "claude",
		EventType:  "PostToolUse",
		Payload:    "global-search-term-abc",
	}); err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}
	events, err := db.SearchEventsByProject("", "global-search-term-abc", 10)
	if err != nil {
		t.Fatalf("SearchEventsByProject(empty): %v", err)
	}
	if len(events) == 0 {
		t.Error("expected at least 1 result for global search")
	}
}

func TestProjectAgents(t *testing.T) {
	db := openPrinciplesDB(t)
	for _, tool := range []string{"claude", "gemini", "claude"} {
		if err := db.InsertEvent(&Event{
			SessionID:  "s",
			ProjectID:  "proj-agents",
			SourceTool: tool,
			EventType:  "PostToolUse",
			Payload:    "{}",
		}); err != nil {
			t.Fatalf("InsertEvent: %v", err)
		}
	}
	agents, err := db.ProjectAgents("proj-agents")
	if err != nil {
		t.Fatalf("ProjectAgents: %v", err)
	}
	if len(agents) != 2 {
		t.Errorf("agents = %v, want [claude gemini]", agents)
	}
}

func TestPrincipleScoreAndUsage(t *testing.T) {
	db := openPrinciplesDB(t)

	p := &Principle{
		ID:          "p-test-1",
		TS:          "2026-06-12T10:00:00Z",
		Type:        "bugfix",
		Title:       "Test Title",
		Narrative:   "Test Narrative",
		ImpactScore: 0.5,
		ProjectID:   "proj-test",
		Fingerprint: "fingerprint-1",
	}

	inserted, err := db.InsertPrinciple(p)
	if err != nil {
		t.Fatalf("InsertPrinciple: %v", err)
	}
	if !inserted {
		t.Fatal("expected principle to be inserted")
	}

	// Test UpdatePrincipleScore
	if err := db.UpdatePrincipleScore("p-test-1", 0.85); err != nil {
		t.Fatalf("UpdatePrincipleScore: %v", err)
	}

	// Test IncrementPrincipleUsage
	if err := db.IncrementPrincipleUsage("p-test-1"); err != nil {
		t.Fatalf("IncrementPrincipleUsage: %v", err)
	}

	fetched, err := db.GetPrincipleByID("p-test-1")
	if err != nil {
		t.Fatalf("GetPrincipleByID: %v", err)
	}
	if fetched == nil {
		t.Fatal("expected principle to be found")
	}

	if fetched.ImpactScore != 0.85 {
		t.Errorf("expected impact score 0.85, got %v", fetched.ImpactScore)
	}
	if fetched.UseCount != 1 {
		t.Errorf("expected use count 1, got %v", fetched.UseCount)
	}
	if fetched.LastUsedTS == "" {
		t.Error("expected last used ts to be set")
	}
}

