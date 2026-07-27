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
