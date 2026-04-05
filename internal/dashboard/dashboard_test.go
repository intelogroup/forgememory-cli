package dashboard

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
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

	if err := database.InsertPrinciple(&db.Principle{
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
