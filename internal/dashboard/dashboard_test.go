package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forge/forge/internal/db"
)

func TestHandleEvents_ShortEventID(t *testing.T) {
	tmp := t.TempDir()
	database, err := db.Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("db.Open failed: %v", err)
	}
	defer database.Close()

	if err := database.InsertEvent(&db.Event{
		ID:         "e",
		SessionID:  "s",
		ProjectID:  "p",
		SourceTool: "claude",
		EventType:  "PostToolUse",
		Payload:    "{}",
	}); err != nil {
		t.Fatalf("InsertEvent failed: %v", err)
	}

	s := New(database, 0)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/events", nil)
	s.handleEvents(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var out []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}
	if len(out) != 1 || out[0]["id"] != "e" {
		t.Fatalf("unexpected event response: %+v", out)
	}
}

func TestHandlePrinciples_ShortPrincipleID(t *testing.T) {
	tmp := t.TempDir()
	database, err := db.Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("db.Open failed: %v", err)
	}
	defer database.Close()

	if _, err := database.InsertPrinciple(&db.Principle{
		ID:        "p",
		TS:        "2026-01-01T00:00:00Z",
		Type:      "pattern",
		Title:     "title",
		Narrative: "narrative",
		ProjectID: "proj",
	}); err != nil {
		t.Fatalf("InsertPrinciple failed: %v", err)
	}

	s := New(database, 0)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/principles", nil)
	s.handlePrinciples(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var out []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}
	if len(out) != 1 || out[0]["id"] != "p" {
		t.Fatalf("unexpected principle response: %+v", out)
	}
}

func openDashboardTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func insertConflictingPair(t *testing.T, d *db.DB, titleA, titleB, project string) (db.Principle, db.Principle) {
	t.Helper()
	a := &db.Principle{Type: "pattern", Title: titleA, Narrative: titleA, ImpactScore: 0.8, ProjectID: project}
	b := &db.Principle{Type: "pattern", Title: titleB, Narrative: titleB, ImpactScore: 0.7, ProjectID: project}
	if _, err := d.InsertPrinciple(a); err != nil {
		t.Fatalf("InsertPrinciple a: %v", err)
	}
	if _, err := d.InsertPrinciple(b); err != nil {
		t.Fatalf("InsertPrinciple b: %v", err)
	}
	if err := d.MarkConflicting(a.ID, b.ID); err != nil {
		t.Fatalf("MarkConflicting: %v", err)
	}
	return *a, *b
}

func TestHandleConflicts_Empty(t *testing.T) {
	d := openDashboardTestDB(t)
	s := New(d, 0)
	rr := httptest.NewRecorder()
	s.handleConflicts(rr, httptest.NewRequest(http.MethodGet, "/api/conflicts", nil))
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var out []conflictPairJSON
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected empty pairs, got %d", len(out))
	}
}

func TestHandleConflicts_ReturnsPairs(t *testing.T) {
	d := openDashboardTestDB(t)
	a, b := insertConflictingPair(t, d, "Redis session cache use", "Avoid Redis session cache", "proj")

	s := New(d, 0)
	rr := httptest.NewRecorder()
	s.handleConflicts(rr, httptest.NewRequest(http.MethodGet, "/api/conflicts", nil))
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	var out []conflictPairJSON
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	// Should be exactly 1 pair (deduplicated from 2 conflicting rows).
	if len(out) != 1 {
		t.Fatalf("expected 1 pair, got %d", len(out))
	}
	ids := map[string]bool{out[0].A.ID: true, out[0].B.ID: true}
	if !ids[a.ID] || !ids[b.ID] {
		t.Errorf("pair IDs %v do not contain expected %q and %q", ids, a.ID, b.ID)
	}
}

func TestHandleResolveConflict_KeepsWinner(t *testing.T) {
	d := openDashboardTestDB(t)
	keep, del := insertConflictingPair(t, d, "Redis session cache good", "Redis session cache bad", "proj")

	s := New(d, 0)
	body := strings.NewReader(`{"keep_id":"` + keep.ID + `","delete_id":"` + del.ID + `"}`)
	rr := httptest.NewRecorder()
	s.handleResolveConflict(rr, httptest.NewRequest(http.MethodPost, "/api/conflicts/resolve", body))
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}

	winner, err := d.GetPrincipleByID(keep.ID)
	if err != nil || winner == nil {
		t.Fatalf("winner not found: %v", err)
	}
	if winner.Status != "active" {
		t.Errorf("winner.Status = %q, want active", winner.Status)
	}
	loser, err := d.GetPrincipleByID(del.ID)
	if err != nil {
		t.Fatalf("GetPrincipleByID(loser): %v", err)
	}
	if loser != nil {
		t.Error("loser should have been deleted")
	}
}

func TestHandleResolveConflict_InvalidInputs(t *testing.T) {
	d := openDashboardTestDB(t)
	s := New(d, 0)

	// GET should return 405.
	rr := httptest.NewRecorder()
	s.handleResolveConflict(rr, httptest.NewRequest(http.MethodGet, "/api/conflicts/resolve", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", rr.Code)
	}

	// POST with empty body should return 400.
	rr = httptest.NewRecorder()
	s.handleResolveConflict(rr, httptest.NewRequest(http.MethodPost, "/api/conflicts/resolve", strings.NewReader("{}")))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("empty body status = %d, want 400", rr.Code)
	}
}
