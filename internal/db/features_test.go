package db

import (
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
)

func TestEventMetadataCorrect(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	event := &Event{
		SessionID:  "sess-abc-123",
		ProjectID:  "forge",
		SourceTool: "claude",
		EventType:  "PostToolUse",
		ToolName:   "Edit",
		Payload:    `{"file_path":"src/daemon.py","old_string":"serve_forever()"}`,
	}
	if err := db.InsertEvent(event); err != nil {
		t.Fatal(err)
	}

	// Verify all metadata fields are saved
	events, err := db.RecentEvents(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	e := events[0]
	if e.ID == "" {
		t.Error("ID should not be empty")
	}
	if e.TS == "" {
		t.Error("TS should not be empty")
	}
	if e.SessionID != "sess-abc-123" {
		t.Errorf("SessionID = %s, want sess-abc-123", e.SessionID)
	}
	if e.ProjectID != "forge" {
		t.Errorf("ProjectID = %s, want forge", e.ProjectID)
	}
	if e.SourceTool != "claude" {
		t.Errorf("SourceTool = %s, want claude", e.SourceTool)
	}
	if e.EventType != "PostToolUse" {
		t.Errorf("EventType = %s, want PostToolUse", e.EventType)
	}
	if e.ToolName != "Edit" {
		t.Errorf("ToolName = %s, want Edit", e.ToolName)
	}
	if e.Distilled {
		t.Error("new event should not be distilled")
	}

	// Verify payload is valid JSON
	var payload map[string]any
	if err := json.Unmarshal([]byte(e.Payload), &payload); err != nil {
		t.Errorf("Payload is not valid JSON: %v", err)
	}
	if payload["file_path"] != "src/daemon.py" {
		t.Errorf("Payload file_path = %v, want src/daemon.py", payload["file_path"])
	}
}

func TestFTS5SearchOptimized(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Insert diverse events
	events := []Event{
		{SessionID: "s1", ProjectID: "p1", SourceTool: "claude", EventType: "PostToolUse", Payload: "windows daemon fix with CREATE_BREAKAWAY_FROM_JOB"},
		{SessionID: "s1", ProjectID: "p1", SourceTool: "claude", EventType: "PostToolUse", Payload: "linux kernel module update"},
		{SessionID: "s1", ProjectID: "p1", SourceTool: "gemini", EventType: "Stop", Payload: "macos build failure in CI pipeline"},
		{SessionID: "s2", ProjectID: "p1", SourceTool: "claude", EventType: "PostToolUse", Payload: "windows named pipe IPC protocol"},
		{SessionID: "s2", ProjectID: "p1", SourceTool: "codex", EventType: "Stop", Payload: "refactor daemon polling loop"},
	}
	for i := range events {
		db.InsertEvent(&events[i])
	}

	// Test FTS5 exact match
	results, err := db.SearchEvents("daemon", 10)
	if err != nil {
		t.Fatalf("FTS5 search failed: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("search 'daemon' returned %d results, want 2", len(results))
	}

	// Test FTS5 multi-word
	results, err = db.SearchEvents("windows", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Errorf("search 'windows' returned %d results, want 2", len(results))
	}

	// Test FTS5 no match
	results, err = db.SearchEvents("nonexistent_term_xyz", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("search 'nonexistent_term_xyz' returned %d results, want 0", len(results))
	}

	// Test FTS5 with limit
	results, err = db.SearchEvents("claude", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) > 2 {
		t.Errorf("search with limit=2 returned %d results", len(results))
	}
}

func TestWALConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Simulate concurrent hook writes from multiple agents
	var wg sync.WaitGroup
	errors := make(chan error, 20)

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < 4; j++ {
				event := &Event{
					SessionID:  "concurrent-test",
					ProjectID:  "forge",
					SourceTool: "claude",
					EventType:  "PostToolUse",
					ToolName:   "Bash",
					Payload:    `{"command":"test"}`,
				}
				if err := db.InsertEvent(event); err != nil {
					errors <- err
				}
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	// Check for errors
	for err := range errors {
		t.Errorf("Concurrent write error: %v", err)
	}

	// Verify all 20 events were written
	total, undistilled, err := db.EventCount()
	if err != nil {
		t.Fatal(err)
	}
	if total != 20 {
		t.Errorf("total = %d, want 20 (WAL concurrent writes)", total)
	}
	if undistilled != 20 {
		t.Errorf("undistilled = %d, want 20", undistilled)
	}
}

func TestHookToDaemonQueue(t *testing.T) {
	// Test the IPC message queue: hook → daemon → SQLite
	// This simulates the real flow: hook sends JSON, daemon receives and stores

	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Simulate daemon receiving messages from hooks (queue processing)
	messages := []Event{
		{SessionID: "q1", ProjectID: "p1", SourceTool: "claude", EventType: "PostToolUse", ToolName: "Bash", Payload: `{"cmd":"ls"}`},
		{SessionID: "q1", ProjectID: "p1", SourceTool: "claude", EventType: "PostToolUse", ToolName: "Edit", Payload: `{"file":"main.go"}`},
		{SessionID: "q1", ProjectID: "p1", SourceTool: "claude", EventType: "Stop", Payload: `{}`},
		{SessionID: "q1", ProjectID: "p1", SourceTool: "claude", EventType: "SessionEnd", Payload: `{}`},
	}

	// Insert in order (simulating queue FIFO)
	for i, msg := range messages {
		if err := db.InsertEvent(&messages[i]); err != nil {
			t.Fatalf("queue insert %d failed: %v", i, err)
		}
		_ = msg
	}

	// Verify order is preserved (by ts)
	events, err := db.RecentEvents(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 {
		t.Fatalf("expected 4 events, got %d", len(events))
	}

	// RecentEvents returns DESC order, so reverse check
	if events[0].EventType != "SessionEnd" {
		t.Errorf("first recent should be SessionEnd, got %s", events[0].EventType)
	}
	if events[3].EventType != "PostToolUse" {
		t.Errorf("last recent should be PostToolUse, got %s", events[3].EventType)
	}

	// Verify undistilled count matches
	_, undistilled, _ := db.EventCount()
	if undistilled != 4 {
		t.Errorf("undistilled = %d, want 4 (all events still raw)", undistilled)
	}
}

func TestDistillationProducesPrinciples(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Insert events that would be distilled
	for i := 0; i < 10; i++ {
		db.InsertEvent(&Event{
			SessionID:  "distill-test",
			ProjectID:  "forge",
			SourceTool: "claude",
			EventType:  "PostToolUse",
			ToolName:   "Edit",
			Payload:    `{"file":"daemon.py","change":"fix polling loop"}`,
		})
	}

	// Get undistilled
	events, err := db.UndistilledEvents(50)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 10 {
		t.Fatalf("expected 10 undistilled, got %d", len(events))
	}

	// Create a principle from events
	principle := &Principle{
		Type:        "bugfix",
		Title:       "Windows daemon polling loop fix",
		Narrative:   "Replaced blocking serve_forever() with polling loop that checks shutdown flag",
		ImpactScore: 0.9,
		ProjectID:   "forge",
	}
	if _, err := db.InsertPrinciple(principle); err != nil {
		t.Fatal(err)
	}

	// Mark events as distilled
	var ids []string
	for _, e := range events {
		ids = append(ids, e.ID)
	}
	db.MarkDistilled(ids)

	// Verify state
	total, undistilled, _ := db.EventCount()
	principles, _ := db.PrincipleCount()

	if total != 10 {
		t.Errorf("total = %d, want 10", total)
	}
	if undistilled != 0 {
		t.Errorf("undistilled = %d, want 0 (all distilled)", undistilled)
	}
	if principles != 1 {
		t.Errorf("principles = %d, want 1", principles)
	}
}

// TestInsertPrincipleDuplicateIgnored verifies that inserting a principle with
// the same title+project produces only one row (fingerprint-based dedup).
func TestInsertPrincipleDuplicateIgnored(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	p := &Principle{
		Type: "pattern", Title: "Always use connection pooling",
		Narrative: "Reuse DB connections to reduce overhead.",
		ImpactScore: 0.8, ProjectID: "proj",
	}

	inserted1, err := db.InsertPrinciple(p)
	if err != nil {
		t.Fatalf("first InsertPrinciple: %v", err)
	}
	if !inserted1 {
		t.Error("first insert should return inserted=true")
	}

	// Same title + project but different narrative and ID — should be ignored.
	dup := &Principle{
		Type: "pattern", Title: "Always use connection pooling",
		Narrative: "Different explanation but same title.",
		ImpactScore: 0.5, ProjectID: "proj",
	}
	inserted2, err := db.InsertPrinciple(dup)
	if err != nil {
		t.Fatalf("second InsertPrinciple: %v", err)
	}
	if inserted2 {
		t.Error("duplicate insert should return inserted=false")
	}

	count, err := db.PrincipleCount()
	if err != nil {
		t.Fatalf("PrincipleCount: %v", err)
	}
	if count != 1 {
		t.Errorf("principle count = %d, want 1 (duplicate ignored)", count)
	}
}

// TestUndistilledEventsExcludesDistilled verifies that UndistilledEvents never
// returns events that have already been marked as distilled.
func TestUndistilledEventsExcludesDistilled(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Insert 6 events.
	var ids []string
	for i := 0; i < 6; i++ {
		e := &Event{
			SessionID: "s1", ProjectID: "proj", SourceTool: "claude",
			EventType: "PostToolUse", Payload: "event payload",
		}
		db.InsertEvent(e)
		ids = append(ids, e.ID)
	}

	// Mark first 3 as distilled.
	if err := db.MarkDistilled(ids[:3]); err != nil {
		t.Fatalf("MarkDistilled: %v", err)
	}

	undistilled, err := db.UndistilledEvents(50)
	if err != nil {
		t.Fatalf("UndistilledEvents: %v", err)
	}
	if len(undistilled) != 3 {
		t.Errorf("undistilled count = %d, want 3", len(undistilled))
	}

	// Verify none of the returned events are the ones we marked.
	marked := map[string]bool{ids[0]: true, ids[1]: true, ids[2]: true}
	for _, e := range undistilled {
		if marked[e.ID] {
			t.Errorf("event %s was marked distilled but still returned by UndistilledEvents", e.ID)
		}
	}
}

func TestWALModeEnabled(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Check WAL mode is enabled
	var journalMode string
	err = db.Conn().QueryRow("PRAGMA journal_mode").Scan(&journalMode)
	if err != nil {
		t.Fatal(err)
	}
	if journalMode != "wal" {
		t.Errorf("journal_mode = %s, want 'wal'", journalMode)
	}
}
