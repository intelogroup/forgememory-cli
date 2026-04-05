package mcp

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/forge/forge/internal/db"
)

func TestGetProjectTimeline_ShortSessionIDDoesNotPanic(t *testing.T) {
	tmp := t.TempDir()
	database, err := db.Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("db.Open failed: %v", err)
	}
	defer database.Close()

	if err := database.InsertEvent(&db.Event{
		ID:         "e1",
		SessionID:  "s",
		ProjectID:  detectProject(),
		SourceTool: "claude",
		EventType:  "PostToolUse",
		Payload:    "{}",
	}); err != nil {
		t.Fatalf("InsertEvent failed: %v", err)
	}

	s := New(database)
	result := s.getProjectTimeline(map[string]any{"limit": 10.0})
	if result.IsError {
		t.Fatalf("getProjectTimeline returned error: %+v", result)
	}
	if len(result.Content) == 0 || !strings.Contains(result.Content[0].Text, "`s`") {
		t.Fatalf("expected short session id in output, got: %q", result.Content[0].Text)
	}
}

func TestGetDistillationHealth_ReturnsJSON(t *testing.T) {
	tmp := t.TempDir()
	database, err := db.Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("db.Open failed: %v", err)
	}
	defer database.Close()
	now := time.Now().UTC()
	if err := database.RecordDistillationSuccess(now, 500*time.Millisecond, 5, 1, now.Add(10*time.Minute)); err != nil {
		t.Fatalf("RecordDistillationSuccess failed: %v", err)
	}

	s := New(database)
	result := s.getDistillationHealth(nil)
	if result.IsError {
		t.Fatalf("getDistillationHealth returned error: %+v", result)
	}
	if len(result.Content) == 0 || !strings.Contains(result.Content[0].Text, `"status": "success"`) {
		t.Fatalf("unexpected health payload: %q", result.Content[0].Text)
	}
}

func TestGetAlerts_ReturnsFailureAlert(t *testing.T) {
	tmp := t.TempDir()
	database, err := db.Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("db.Open failed: %v", err)
	}
	defer database.Close()
	now := time.Now().UTC()
	if err := database.RecordDistillationFailure(now, 500*time.Millisecond, 20, "bad key", now.Add(10*time.Minute)); err != nil {
		t.Fatalf("RecordDistillationFailure failed: %v", err)
	}

	s := New(database)
	result := s.getAlerts(nil)
	if result.IsError {
		t.Fatalf("getAlerts returned error: %+v", result)
	}
	if len(result.Content) == 0 || !strings.Contains(result.Content[0].Text, "distillation_failed") {
		t.Fatalf("expected distillation_failed alert, got: %q", result.Content[0].Text)
	}
}

func TestGetPrinciples_ShortTimestampDoesNotPanic(t *testing.T) {
	tmp := t.TempDir()
	database, err := db.Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("db.Open failed: %v", err)
	}
	defer database.Close()

	if err := database.InsertPrinciple(&db.Principle{
		ID:        "p1",
		TS:        "x",
		Type:      "pattern",
		Title:     "title",
		Narrative: "narrative",
		ProjectID: "proj",
	}); err != nil {
		t.Fatalf("InsertPrinciple failed: %v", err)
	}

	s := New(database)
	result := s.getPrinciples(map[string]any{"limit": 10.0})
	if result.IsError {
		t.Fatalf("getPrinciples returned error: %+v", result)
	}
	if len(result.Content) == 0 || !strings.Contains(result.Content[0].Text, "**Date**: x") {
		t.Fatalf("expected short date in output, got: %q", result.Content[0].Text)
	}
}
