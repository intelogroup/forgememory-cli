package db

import (
	"fmt"
	"testing"
	"time"
)

func insertSessionEvent(t *testing.T, database *DB, sessionID, eventType string, ts time.Time) {
	t.Helper()
	e := &Event{
		TS:         ts.UTC().Format(time.RFC3339),
		SessionID:  sessionID,
		ProjectID:  "test-project",
		SourceTool: "claude",
		EventType:  eventType,
		ToolName:   "Bash",
		Payload:    `{"command":"echo"}`,
	}
	if err := database.InsertEvent(e); err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}
}

func seedSessionEvents(t *testing.T, database *DB, sessionID string, n int, withBoundary bool) {
	t.Helper()
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		insertSessionEvent(t, database, sessionID, "PostToolUse", base.Add(time.Duration(i)*time.Minute))
	}
	if withBoundary {
		insertSessionEvent(t, database, sessionID, "SessionEnd", base.Add(time.Duration(n)*time.Minute))
	}
}

func TestSessionEventsUpTo_LimitZero_ReturnsAll(t *testing.T) {
	database := openTestDB(t)
	const sessionID = "sess-all"
	seedSessionEvents(t, database, sessionID, 120, false)

	events, err := database.SessionEventsUpTo(sessionID, "", 0)
	if err != nil {
		t.Fatalf("SessionEventsUpTo: %v", err)
	}
	if len(events) != 120 {
		t.Fatalf("len(events) = %d, want 120", len(events))
	}
}

func TestSessionEventsUpTo_Limit50_Caps(t *testing.T) {
	database := openTestDB(t)
	const sessionID = "sess-cap"
	seedSessionEvents(t, database, sessionID, 120, false)

	events, err := database.SessionEventsUpTo(sessionID, "", 50)
	if err != nil {
		t.Fatalf("SessionEventsUpTo: %v", err)
	}
	if len(events) != 50 {
		t.Fatalf("len(events) = %d, want 50", len(events))
	}
}

func TestSessionEventsUpTo_CutoffTS(t *testing.T) {
	database := openTestDB(t)
	const sessionID = "sess-cutoff"
	base := time.Date(2026, 2, 1, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		insertSessionEvent(t, database, sessionID, "PostToolUse", base.Add(time.Duration(i)*time.Hour))
	}
	cutoff := base.Add(2 * time.Hour).UTC().Format(time.RFC3339)

	events, err := database.SessionEventsUpTo(sessionID, cutoff, 0)
	if err != nil {
		t.Fatalf("SessionEventsUpTo: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("len(events) = %d, want 3 (ts <= cutoff)", len(events))
	}
	if events[len(events)-1].TS > cutoff {
		t.Fatalf("last event ts %q after cutoff %q", events[len(events)-1].TS, cutoff)
	}
}

func TestSessionEventsUpTo_OrderChronological(t *testing.T) {
	database := openTestDB(t)
	const sessionID = "sess-order"
	base := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	for i := 4; i >= 0; i-- {
		insertSessionEvent(t, database, sessionID, "PostToolUse", base.Add(time.Duration(i)*time.Minute))
	}

	events, err := database.SessionEventsUpTo(sessionID, "", 0)
	if err != nil {
		t.Fatalf("SessionEventsUpTo: %v", err)
	}
	for i := 1; i < len(events); i++ {
		if events[i].TS < events[i-1].TS {
			t.Fatalf("events not in ASC order at %d: %q before %q", i, events[i].TS, events[i-1].TS)
		}
	}
}

func TestUndistilledCompletedSessions_ReturnsSessionWithStop(t *testing.T) {
	database := openTestDB(t)
	base := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	for _, boundary := range []string{"Stop", "SessionEnd", "AfterAgent"} {
		sid := "sess-" + boundary
		seedSessionEvents(t, database, sid, 3, false)
		insertSessionEvent(t, database, sid, boundary, base)
	}

	got, err := database.UndistilledCompletedSessions(10)
	if err != nil {
		t.Fatalf("UndistilledCompletedSessions: %v", err)
	}
	if len(got) < 3 {
		t.Fatalf("len(sessions) = %d, want at least 3", len(got))
	}
}

func TestUndistilledCompletedSessions_ExcludesUnknown(t *testing.T) {
	database := openTestDB(t)
	insertSessionEvent(t, database, "unknown", "SessionEnd", time.Now().UTC())
	for i := 0; i < 5; i++ {
		insertSessionEvent(t, database, "unknown", "PostToolUse", time.Now().UTC().Add(time.Duration(i)*time.Minute))
	}

	got, err := database.UndistilledCompletedSessions(10)
	if err != nil {
		t.Fatalf("UndistilledCompletedSessions: %v", err)
	}
	for _, sid := range got {
		if sid == "unknown" {
			t.Fatal("unknown session must not be returned")
		}
	}
}

func TestUndistilledCompletedSessions_ExcludesAlreadySummarized(t *testing.T) {
	database := openTestDB(t)
	const sessionID = "sess-done"
	seedSessionEvents(t, database, sessionID, 4, true)
	if err := database.InsertSessionSummary(&SessionSummary{
		SessionID: sessionID,
		ProjectID: "test-project",
		Summary:   "done",
	}); err != nil {
		t.Fatalf("InsertSessionSummary: %v", err)
	}

	got, err := database.UndistilledCompletedSessions(10)
	if err != nil {
		t.Fatalf("UndistilledCompletedSessions: %v", err)
	}
	for _, sid := range got {
		if sid == sessionID {
			t.Fatal("summarized session should be excluded")
		}
	}
}

func TestUndistilledCompletedSessions_IgnoresOrphanEvents(t *testing.T) {
	database := openTestDB(t)
	for i := 0; i < 20; i++ {
		insertSessionEvent(t, database, "orphan-no-boundary", "PostToolUse", time.Now().UTC().Add(time.Duration(i)*time.Second))
	}

	got, err := database.UndistilledCompletedSessions(10)
	if err != nil {
		t.Fatalf("UndistilledCompletedSessions: %v", err)
	}
	for _, sid := range got {
		if sid == "orphan-no-boundary" {
			t.Fatal("session without boundary event must not be returned")
		}
	}
}

func TestUndistilledCompletedSessions_RespectsLimit(t *testing.T) {
	database := openTestDB(t)
	for i := 0; i < 8; i++ {
		sid := fmt.Sprintf("sess-limit-%d", i)
		seedSessionEvents(t, database, sid, 2, true)
	}

	got, err := database.UndistilledCompletedSessions(3)
	if err != nil {
		t.Fatalf("UndistilledCompletedSessions: %v", err)
	}
	if len(got) > 3 {
		t.Fatalf("len(sessions) = %d, want at most 3", len(got))
	}
}

func TestMarkSessionDistilled_MarksAllSessionEvents(t *testing.T) {
	database := openTestDB(t)
	const sessionID = "sess-mark"
	seedSessionEvents(t, database, sessionID, 6, true)

	if err := database.MarkSessionDistilled(sessionID); err != nil {
		t.Fatalf("MarkSessionDistilled: %v", err)
	}

	events, err := database.SessionEvents(sessionID, 20)
	if err != nil {
		t.Fatalf("SessionEvents: %v", err)
	}
	for _, e := range events {
		if !e.Distilled {
			t.Fatalf("event %s not marked distilled", e.ID)
		}
	}
}
