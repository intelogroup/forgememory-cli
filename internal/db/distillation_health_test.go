package db

import (
	"path/filepath"
	"testing"
	"time"
)

func TestDistillationHealth_RecordSuccessAndFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	database, err := Open(path)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer database.Close()

	now := time.Now().UTC()
	next := now.Add(10 * time.Minute)
	if err := database.RecordDistillationSuccess(now, 2*time.Second, 12, 3, next); err != nil {
		t.Fatalf("RecordDistillationSuccess failed: %v", err)
	}

	h, err := database.GetDistillationHealth()
	if err != nil {
		t.Fatalf("GetDistillationHealth failed: %v", err)
	}
	if h.LastStatus != "success" {
		t.Fatalf("LastStatus = %q, want success", h.LastStatus)
	}
	if h.ConsecutiveFailures != 0 {
		t.Fatalf("ConsecutiveFailures = %d, want 0", h.ConsecutiveFailures)
	}
	if h.TotalSuccesses < 1 {
		t.Fatalf("TotalSuccesses = %d, want >= 1", h.TotalSuccesses)
	}
	if h.LastAttemptedEvents != 12 {
		t.Fatalf("LastAttemptedEvents = %d, want 12", h.LastAttemptedEvents)
	}

	failAt := now.Add(10 * time.Minute)
	next2 := failAt.Add(10 * time.Minute)
	if err := database.RecordDistillationFailure(failAt, 1500*time.Millisecond, 20, "rate limit", next2); err != nil {
		t.Fatalf("RecordDistillationFailure failed: %v", err)
	}
	h, err = database.GetDistillationHealth()
	if err != nil {
		t.Fatalf("GetDistillationHealth failed: %v", err)
	}
	if h.LastStatus != "failed" {
		t.Fatalf("LastStatus = %q, want failed", h.LastStatus)
	}
	if h.ConsecutiveFailures < 1 {
		t.Fatalf("ConsecutiveFailures = %d, want >= 1", h.ConsecutiveFailures)
	}
	if h.TotalFailures < 1 {
		t.Fatalf("TotalFailures = %d, want >= 1", h.TotalFailures)
	}
	if h.LastErrorMessage != "rate limit" {
		t.Fatalf("LastErrorMessage = %q, want rate limit", h.LastErrorMessage)
	}
}
