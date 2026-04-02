package db

import (
	"os"
	"path/filepath"
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
	if err := db.InsertPrinciple(p); err != nil {
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
