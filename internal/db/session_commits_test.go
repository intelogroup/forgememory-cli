package db

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestLinkCommitToSession(t *testing.T) {
	dir := t.TempDir()
	d, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	mustInsertEvent(t, d, "sess-a", "proj", base)
	mustInsertEvent(t, d, "sess-a", "proj", base.Add(5*time.Minute))

	tests := []struct {
		name      string
		commitTS  time.Time
		wantEmpty bool
	}{
		{"inside session span", base.Add(2 * time.Minute), false},
		{"just past last event, within pad", base.Add(20 * time.Minute), false},
		{"far outside session + pad", base.Add(2 * time.Hour), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sc := SessionCommit{
				ID:       uuid.New().String(),
				SHA:      uuid.New().String()[:8],
				CommitTS: tt.commitTS.Format(time.RFC3339),
				Subject:  "test commit",
			}
			sessionID, err := d.LinkCommitToSession("proj", sc)
			if err != nil {
				t.Fatalf("LinkCommitToSession: %v", err)
			}
			if tt.wantEmpty && sessionID != "" {
				t.Errorf("expected no session match, got %q", sessionID)
			}
			if !tt.wantEmpty && sessionID != "sess-a" {
				t.Errorf("expected sess-a, got %q", sessionID)
			}
		})
	}
}

// TestLinkCommitToSession_IgnoresUnknownBucket guards against the regression
// found when dry-running against real repos: the "unknown" session (forgememo's
// catch-all for events with no captured session boundary) spans the entire
// project history, so it overlaps every commit and previously always won the
// old "latest hi" tiebreak — swallowing every real, more specific session.
func TestLinkCommitToSession_IgnoresUnknownBucket(t *testing.T) {
	dir := t.TempDir()
	d, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	// "unknown" spans the whole project history.
	mustInsertEvent(t, d, "unknown", "proj", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	mustInsertEvent(t, d, "unknown", "proj", time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC))

	// A real, narrow session sits inside that span.
	real := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	mustInsertEvent(t, d, "sess-real", "proj", real)
	mustInsertEvent(t, d, "sess-real", "proj", real.Add(5*time.Minute))

	sc := SessionCommit{ID: uuid.New().String(), SHA: "regressiontest", CommitTS: real.Add(2 * time.Minute).Format(time.RFC3339)}
	sessionID, err := d.LinkCommitToSession("proj", sc)
	if err != nil {
		t.Fatal(err)
	}
	if sessionID != "sess-real" {
		t.Errorf("expected commit inside sess-real's narrow window to match sess-real, got %q", sessionID)
	}
}

func mustInsertEvent(t *testing.T, d *DB, sessionID, projectID string, ts time.Time) {
	t.Helper()
	e := &Event{
		ID:         uuid.New().String(),
		TS:         ts.UTC().Format(time.RFC3339),
		SessionID:  sessionID,
		ProjectID:  projectID,
		SourceTool: "claude",
		EventType:  "tool_use",
		Payload:    "{}",
	}
	if err := d.InsertEvent(e); err != nil {
		t.Fatalf("insert event: %v", err)
	}
}
