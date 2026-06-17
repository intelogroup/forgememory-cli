package db

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	if db.Path != path {
		t.Errorf("Path = %s, want %s", db.Path, path)
	}
}

func TestInsertEvent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	event := &Event{
		SessionID:  "test-session",
		ProjectID:  "test-project",
		SourceTool: "claude",
		EventType:  "PostToolUse",
		ToolName:   "Bash",
		Payload:    `{"command":"echo hello"}`,
	}

	if err := db.InsertEvent(event); err != nil {
		t.Fatalf("InsertEvent failed: %v", err)
	}

	if event.ID == "" {
		t.Error("InsertEvent should set ID")
	}

	total, undistilled, err := db.EventCount()
	if err != nil {
		t.Fatalf("EventCount failed: %v", err)
	}
	if total != 1 {
		t.Errorf("total = %d, want 1", total)
	}
	if undistilled != 1 {
		t.Errorf("undistilled = %d, want 1", undistilled)
	}

	t.Run("Payload Truncation", func(t *testing.T) {
		largePayload := strings.Repeat("a", 70000)
		eventLarge := &Event{
			SessionID:  "test-session",
			ProjectID:  "test-project",
			SourceTool: "claude",
			EventType:  "PostToolUse",
			Payload:    largePayload,
		}
		if err := db.InsertEvent(eventLarge); err != nil {
			t.Fatalf("InsertEvent failed for large payload: %v", err)
		}
		if len(eventLarge.Payload) != 65536 {
			t.Errorf("expected payload length 65536, got %d", len(eventLarge.Payload))
		}
		if !strings.HasSuffix(eventLarge.Payload, "...[TRUNCATED]") {
			t.Errorf("expected suffix ...[TRUNCATED], got %s", eventLarge.Payload[65520:])
		}
	})
}

func TestMarkDistilled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	event := &Event{
		SessionID:  "test-session",
		ProjectID:  "test-project",
		SourceTool: "claude",
		EventType:  "PostToolUse",
		Payload:    "test",
	}
	db.InsertEvent(event)

	if err := db.MarkDistilled([]string{event.ID}); err != nil {
		t.Fatalf("MarkDistilled failed: %v", err)
	}

	_, undistilled, _ := db.EventCount()
	if undistilled != 0 {
		t.Errorf("undistilled = %d, want 0", undistilled)
	}
}

func TestSearchEvents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	db.InsertEvent(&Event{
		SessionID:  "s1",
		ProjectID:  "p1",
		SourceTool: "claude",
		EventType:  "PostToolUse",
		Payload:    "windows daemon fix",
	})
	db.InsertEvent(&Event{
		SessionID:  "s1",
		ProjectID:  "p1",
		SourceTool: "claude",
		EventType:  "PostToolUse",
		Payload:    "linux kernel update",
	})

	results, err := db.SearchEvents("windows", 10)
	if err != nil {
		t.Fatalf("SearchEvents failed: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("len(results) = %d, want 1", len(results))
	}
}

func TestInsertPrinciple(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	p := &Principle{
		Type:        "bugfix",
		Title:       "Windows daemon fix",
		Narrative:   "Use CREATE_BREAKAWAY_FROM_JOB flag",
		ImpactScore: 0.8,
		ProjectID:   "forge",
	}
	if _, err := db.InsertPrinciple(p); err != nil {
		t.Fatalf("InsertPrinciple failed: %v", err)
	}

	principles, err := db.RecentPrinciples(10)
	if err != nil {
		t.Fatalf("RecentPrinciples failed: %v", err)
	}
	if len(principles) != 1 {
		t.Errorf("len(principles) = %d, want 1", len(principles))
	}
	if principles[0].Title != "Windows daemon fix" {
		t.Errorf("Title = %s, want 'Windows daemon fix'", principles[0].Title)
	}
}

func TestRecentPrinciplesByProject(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	for _, p := range []*Principle{
		{Type: "bugfix", Title: "Windows daemon fix", Narrative: "Use detached child process", ProjectID: "forgememory-cli"},
		{Type: "pattern", Title: "Legacy manual save", Narrative: "Stored under absolute repo path", ProjectID: "/tmp/repos/forgememory-cli"},
		{Type: "pattern", Title: "Other project lesson", Narrative: "Ignore me", ProjectID: "other-repo"},
	} {
		if _, err := db.InsertPrinciple(p); err != nil {
			t.Fatalf("InsertPrinciple failed: %v", err)
		}
	}

	principles, err := db.RecentPrinciplesByProject("forgememory-cli", 10)
	if err != nil {
		t.Fatalf("RecentPrinciplesByProject failed: %v", err)
	}
	if len(principles) != 2 {
		t.Fatalf("len(principles) = %d, want 2", len(principles))
	}
	for _, p := range principles {
		if p.ProjectID != "forgememory-cli" && p.ProjectID != "/tmp/repos/forgememory-cli" {
			t.Fatalf("unexpected ProjectID %q in result set", p.ProjectID)
		}
	}
}

func TestSearchPrinciplesCrossProject(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	principles := []*Principle{
		{
			Type:        "bugfix",
			Title:       "Rust E0599: import the trait before calling its methods",
			Narrative:   "When cargo reports E0599, the method exists on a trait not in scope. Add the use statement.",
			ImpactScore: 0.85,
			ProjectID:   "project-a",
			Concepts:    []string{"gotcha"},
		},
		{
			Type:        "pattern",
			Title:       "React hooks must not be called conditionally",
			Narrative:   "Calling hooks inside if statements violates the Rules of Hooks and causes invalid hook call errors.",
			ImpactScore: 0.75,
			ProjectID:   "project-b",
		},
		{
			Type:        "preference",
			Title:       "Always run prettier before committing",
			Narrative:   "Formatter discipline prevents noisy diffs.",
			ImpactScore: 0.4, // below threshold
			ProjectID:   "project-c",
		},
	}
	for _, p := range principles {
		if _, err := database.InsertPrinciple(p); err != nil {
			t.Fatalf("InsertPrinciple: %v", err)
		}
	}

	// Should find rust principle.
	results, err := database.SearchPrinciplesCrossProject([]string{"rust", "e0599"}, 0.5, 90, 5)
	if err != nil {
		t.Fatalf("SearchPrinciplesCrossProject: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if results[0].ProjectID != "project-a" {
		t.Fatalf("ProjectID = %q, want project-a", results[0].ProjectID)
	}

	// Should find react principle.
	results, err = database.SearchPrinciplesCrossProject([]string{"react", "hooks"}, 0.5, 90, 5)
	if err != nil {
		t.Fatalf("SearchPrinciplesCrossProject: %v", err)
	}
	if len(results) != 1 || results[0].ProjectID != "project-b" {
		t.Fatalf("unexpected results %#v", results)
	}

	// Low-impact principle should not be returned (minImpact=0.5).
	results, err = database.SearchPrinciplesCrossProject([]string{"prettier", "formatter"}, 0.5, 90, 5)
	if err != nil {
		t.Fatalf("SearchPrinciplesCrossProject: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("len(results) = %d, want 0 (impact too low)", len(results))
	}

	// Empty terms should return nothing.
	results, err = database.SearchPrinciplesCrossProject(nil, 0.5, 90, 5)
	if err != nil {
		t.Fatalf("SearchPrinciplesCrossProject nil terms: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("len(results) = %d, want 0 for nil terms", len(results))
	}
}

func TestGetRecentSessionSummariesByProject(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	for _, s := range []*SessionSummary{
		{SessionID: "s1", ProjectID: "forgememory-cli", Learnings: "Keep context project-scoped."},
		{SessionID: "s2", ProjectID: "other-repo", Learnings: "Ignore me."},
	} {
		if err := db.InsertSessionSummary(s); err != nil {
			t.Fatalf("InsertSessionSummary failed: %v", err)
		}
	}

	summaries, err := db.GetRecentSessionSummariesByProject("forgememory-cli", 10)
	if err != nil {
		t.Fatalf("GetRecentSessionSummariesByProject failed: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("len(summaries) = %d, want 1", len(summaries))
	}
	if summaries[0].ProjectID != "forgememory-cli" {
		t.Fatalf("ProjectID = %q, want forgememory-cli", summaries[0].ProjectID)
	}
}

func TestDefaultPath(t *testing.T) {
	path := defaultPath()
	if path == "" {
		t.Error("defaultPath should not be empty")
	}
	if !filepath.IsAbs(path) {
		t.Errorf("defaultPath should be absolute: %s", path)
	}
	if filepath.Ext(path) != ".db" {
		t.Errorf("defaultPath should end with .db: %s", path)
	}
}

func TestOpenCreatesDir(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "nested", "dir", "test.db")
	db, err := Open(nested)
	if err != nil {
		t.Fatalf("Open should create parent dirs: %v", err)
	}
	db.Close()

	if _, err := os.Stat(nested); os.IsNotExist(err) {
		t.Error("Database file should exist")
	}
}

func TestProjectTimeline_PrimaryAgentMultiForMixedSession(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	for _, e := range []*Event{
		{SessionID: "s-mixed", ProjectID: "proj", SourceTool: "claude", EventType: "PostToolUse", Payload: "a"},
		{SessionID: "s-mixed", ProjectID: "proj", SourceTool: "codex", EventType: "PostToolUse", Payload: "b"},
		{SessionID: "s-single", ProjectID: "proj", SourceTool: "gemini", EventType: "PostToolUse", Payload: "c"},
	} {
		if err := db.InsertEvent(e); err != nil {
			t.Fatalf("InsertEvent failed: %v", err)
		}
	}

	entries, err := db.ProjectTimeline("proj", 10)
	if err != nil {
		t.Fatalf("ProjectTimeline failed: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}

	agentsBySession := map[string]string{}
	for _, e := range entries {
		agentsBySession[e.SessionID] = e.PrimaryAgent
	}
	if agentsBySession["s-mixed"] != "multi" {
		t.Fatalf("PrimaryAgent for mixed session = %q, want multi", agentsBySession["s-mixed"])
	}
	if agentsBySession["s-single"] != "gemini" {
		t.Fatalf("PrimaryAgent for single session = %q, want gemini", agentsBySession["s-single"])
	}
}

func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func insertPrinciple(t *testing.T, d *DB, title, projectID string) Principle {
	t.Helper()
	p := &Principle{
		Type:        "pattern",
		Title:       title,
		Narrative:   title + " narrative",
		ImpactScore: 0.8,
		ProjectID:   projectID,
	}
	ok, err := d.InsertPrinciple(p)
	if err != nil {
		t.Fatalf("InsertPrinciple: %v", err)
	}
	if !ok {
		t.Fatalf("InsertPrinciple: expected inserted=true")
	}
	return *p
}

func TestInsertPrinciple_ReturnsInserted(t *testing.T) {
	d := openTestDB(t)
	p := &Principle{
		Type:        "pattern",
		Title:       "Redis cache session management pattern",
		Narrative:   "Use Redis for caching sessions.",
		ImpactScore: 0.8,
		ProjectID:   "proj",
	}
	ok, err := d.InsertPrinciple(p)
	if err != nil {
		t.Fatalf("first insert error: %v", err)
	}
	if !ok {
		t.Fatal("expected inserted=true on first insert")
	}

	// Second insert with same fingerprint should be ignored.
	p2 := &Principle{
		Type:        "pattern",
		Title:       "Redis cache session management pattern",
		Narrative:   "Different narrative, same title.",
		ImpactScore: 0.9,
		ProjectID:   "proj",
	}
	ok, err = d.InsertPrinciple(p2)
	if err != nil {
		t.Fatalf("second insert error: %v", err)
	}
	if ok {
		t.Fatal("expected inserted=false on duplicate fingerprint")
	}
}

func TestMarkConflicting(t *testing.T) {
	d := openTestDB(t)
	a := insertPrinciple(t, d, "Redis session caching strategy", "proj")
	b := insertPrinciple(t, d, "Avoid Redis for session storage", "proj")

	if err := d.MarkConflicting(a.ID, b.ID); err != nil {
		t.Fatalf("MarkConflicting: %v", err)
	}

	pa, err := d.GetPrincipleByID(a.ID)
	if err != nil || pa == nil {
		t.Fatalf("GetPrincipleByID(a): %v", err)
	}
	if pa.Status != "conflicting" {
		t.Errorf("a.Status = %q, want conflicting", pa.Status)
	}
	if pa.ConflictPeerID != b.ID {
		t.Errorf("a.ConflictPeerID = %q, want %q", pa.ConflictPeerID, b.ID)
	}

	pb, err := d.GetPrincipleByID(b.ID)
	if err != nil || pb == nil {
		t.Fatalf("GetPrincipleByID(b): %v", err)
	}
	if pb.Status != "conflicting" {
		t.Errorf("b.Status = %q, want conflicting", pb.Status)
	}
	if pb.ConflictPeerID != a.ID {
		t.Errorf("b.ConflictPeerID = %q, want %q", pb.ConflictPeerID, a.ID)
	}
}

func TestRecentPrinciplesByProject_FiltersConflicting(t *testing.T) {
	d := openTestDB(t)
	active := insertPrinciple(t, d, "Use PostgreSQL for relational data", "proj")
	conflicting := insertPrinciple(t, d, "Avoid PostgreSQL in high-write scenarios", "proj")

	if err := d.MarkConflicting(active.ID, conflicting.ID); err != nil {
		t.Fatalf("MarkConflicting: %v", err)
	}

	// Neither should be returned by the agent-facing query.
	results, err := d.RecentPrinciplesByProject("proj", 10)
	if err != nil {
		t.Fatalf("RecentPrinciplesByProject: %v", err)
	}
	for _, p := range results {
		if p.Status == "conflicting" {
			t.Errorf("conflicting principle %q leaked into RecentPrinciplesByProject", p.Title)
		}
	}
}

func TestSearchPrinciplesCrossProject_FiltersConflicting(t *testing.T) {
	d := openTestDB(t)
	a := insertPrinciple(t, d, "Rust error handling with anyhow", "proj-a")
	b := insertPrinciple(t, d, "Rust error handling prefer thiserror", "proj-a")

	if err := d.MarkConflicting(a.ID, b.ID); err != nil {
		t.Fatalf("MarkConflicting: %v", err)
	}

	results, err := d.SearchPrinciplesCrossProject([]string{"rust", "error", "handling"}, 0.0, 0, 10)
	if err != nil {
		t.Fatalf("SearchPrinciplesCrossProject: %v", err)
	}
	for _, p := range results {
		if p.Status == "conflicting" {
			t.Errorf("conflicting principle %q leaked into SearchPrinciplesCrossProject", p.Title)
		}
	}
}

func TestResolvePrincipleConflict(t *testing.T) {
	d := openTestDB(t)
	keep := insertPrinciple(t, d, "Redis session caching good", "proj")
	del := insertPrinciple(t, d, "Redis session caching bad", "proj")

	if err := d.MarkConflicting(keep.ID, del.ID); err != nil {
		t.Fatalf("MarkConflicting: %v", err)
	}
	if err := d.ResolvePrincipleConflict(keep.ID, del.ID); err != nil {
		t.Fatalf("ResolvePrincipleConflict: %v", err)
	}

	winner, err := d.GetPrincipleByID(keep.ID)
	if err != nil || winner == nil {
		t.Fatalf("winner not found: %v", err)
	}
	if winner.Status != "active" {
		t.Errorf("winner.Status = %q, want active", winner.Status)
	}
	if winner.ConflictPeerID != "" {
		t.Errorf("winner.ConflictPeerID = %q, want empty", winner.ConflictPeerID)
	}

	loser, err := d.GetPrincipleByID(del.ID)
	if err != nil {
		t.Fatalf("GetPrincipleByID(loser) error: %v", err)
	}
	if loser != nil {
		t.Error("loser should have been deleted")
	}
}

func TestClearConflictStatus(t *testing.T) {
	d := openTestDB(t)
	p := insertPrinciple(t, d, "Orphaned conflicting principle", "proj")

	// Simulate orphan: mark conflicting manually without a real peer.
	if err := d.MarkConflicting(p.ID, "nonexistent-peer-id"); err != nil {
		t.Fatalf("MarkConflicting: %v", err)
	}
	if err := d.ClearConflictStatus(p.ID); err != nil {
		t.Fatalf("ClearConflictStatus: %v", err)
	}

	got, err := d.GetPrincipleByID(p.ID)
	if err != nil || got == nil {
		t.Fatalf("GetPrincipleByID: %v", err)
	}
	if got.Status != "active" {
		t.Errorf("Status = %q, want active", got.Status)
	}
}

func TestConflictingPrinciples(t *testing.T) {
	d := openTestDB(t)
	a := insertPrinciple(t, d, "Redis session cache approach A", "proj")
	b := insertPrinciple(t, d, "Redis session cache approach B", "proj")
	insertPrinciple(t, d, "Unrelated principle about Docker", "proj")

	if err := d.MarkConflicting(a.ID, b.ID); err != nil {
		t.Fatalf("MarkConflicting: %v", err)
	}

	conflicts, err := d.ConflictingPrinciples(10)
	if err != nil {
		t.Fatalf("ConflictingPrinciples: %v", err)
	}
	if len(conflicts) != 2 {
		t.Errorf("len(conflicts) = %d, want 2", len(conflicts))
	}
	for _, c := range conflicts {
		if c.Status != "conflicting" {
			t.Errorf("principle %q has status %q, want conflicting", c.Title, c.Status)
		}
	}
}
