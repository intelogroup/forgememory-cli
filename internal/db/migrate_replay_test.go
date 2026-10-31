package db

import (
	"path/filepath"
	"testing"
)

// A schema bump replays migrate(). Rows written after scope arrived must keep
// their project scope, or the bump silently publishes them across projects.
func TestSchemaBumpKeepsProjectScope(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replay.db")
	database, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	p := &Principle{Title: "local lesson", Narrative: "n", ProjectID: "proj-a", Type: "pattern", ImpactScore: 0.5}
	if inserted, err := database.InsertPrinciple(p); err != nil || !inserted {
		t.Fatalf("InsertPrinciple: inserted=%v err=%v", inserted, err)
	}
	if _, err := database.conn.Exec(`PRAGMA user_version = 9`); err != nil {
		t.Fatalf("rewind user_version: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	database, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer database.Close()
	var scope string
	if err := database.conn.QueryRow(`SELECT scope FROM principles WHERE id = ?`, p.ID).Scan(&scope); err != nil {
		t.Fatalf("read scope: %v", err)
	}
	if scope != "project" {
		t.Fatalf("scope after replay = %q, want project", scope)
	}
}
