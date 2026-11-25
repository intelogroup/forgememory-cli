package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/forge/forge/internal/db"
)

func writeClineFixture(t *testing.T) (root string) {
	t.Helper()
	root = t.TempDir()

	projDir := filepath.Join(root, "projA")
	if err := os.MkdirAll(projDir, 0o700); err != nil {
		t.Fatal(err)
	}

	older := time.Now().Add(-2 * time.Hour).UnixMilli()
	newer := time.Now().Add(-1 * time.Hour).UnixMilli()
	history := []clineHistoryEntry{
		{ID: "1001", ULID: "ULIDAAA", TS: older, Task: "fix the login bug", CWD: projDir, ModelID: "kimi/test"},
		{ID: "1002", ULID: "ULIDBBB", TS: newer, Task: "second task", CWD: filepath.Join(root, "gone-proj"), ModelID: "kimi/test"},
	}

	stateDir := filepath.Join(root, "data", "state")
	tasksDir := filepath.Join(root, "data", "tasks")
	for _, dir := range []string{stateDir, filepath.Join(tasksDir, "1001"), filepath.Join(tasksDir, "1002")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	historyData, _ := json.Marshal(history)
	if err := os.WriteFile(filepath.Join(stateDir, "taskHistory.json"), historyData, 0o600); err != nil {
		t.Fatal(err)
	}

	msgs1 := []clineUIMessage{
		{TS: older + 1_000, Type: "say", Say: "task", Text: "fix the login bug", ModelInfo: &clineModelInfo{ModelID: "kimi/test"}},
		{TS: older + 2_000, Type: "say", Say: "text", Text: "I'll inspect the auth flow."},
		{TS: older + 3_000, Type: "say", Say: "command", Text: "npm test"},
		{TS: older + 4_000, Type: "say", Say: "error_retry", Text: "stream error, retrying"},
		{TS: older + 5_000, Type: "say", Say: "task_progress", Text: "noise to skip"},
		{TS: older + 6_000, Type: "ask", Say: "tool", Text: "approval prompt to skip"},
		{TS: older + 7_000, Type: "say", Say: "completion_result", Text: "Done."},
	}
	msgs2 := []clineUIMessage{
		{TS: newer + 1_000, Type: "say", Say: "task", Text: "second task"},
	}
	for id, msgs := range map[string][]clineUIMessage{"1001": msgs1, "1002": msgs2} {
		data, _ := json.Marshal(msgs)
		if err := os.WriteFile(filepath.Join(tasksDir, id, "ui_messages.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	meta := clineTaskMetadata{FilesInContext: []clineFileRecord{{Path: "omnichat/src/lib/tokens.ts"}}}
	metaData, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(tasksDir, "1001", "task_metadata.json"), metaData, 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func withTestClineDB(t *testing.T) *db.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "import.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	prev := clineOpenDB
	// runClineImport closes whatever clineOpenDB returns, so hand out a
	// fresh connection to the same SQLite file on every call.
	clineOpenDB = func(string) (*db.DB, error) {
		return db.Open(dbPath)
	}
	t.Cleanup(func() { clineOpenDB = prev; _ = database.Close() })
	return database
}

func TestMapClineMessage(t *testing.T) {
	cases := []struct {
		msg       clineUIMessage
		eventType string
		toolName  string
		ok        bool
	}{
		{clineUIMessage{Type: "say", Say: "task"}, "UserPromptSubmit", "", true},
		{clineUIMessage{Type: "say", Say: "user_feedback"}, "UserPromptSubmit", "", true},
		{clineUIMessage{Type: "say", Say: "text"}, "ModelTurn", "", true},
		{clineUIMessage{Type: "say", Say: "reasoning"}, "ModelTurn", "", true},
		{clineUIMessage{Type: "say", Say: "completion_result"}, "ModelTurn", "", true},
		{clineUIMessage{Type: "say", Say: "command"}, "PostToolUse", "execute_command", true},
		{clineUIMessage{Type: "say", Say: "tool"}, "PostToolUse", "cline_tool", true},
		{clineUIMessage{Type: "say", Say: "error_retry"}, "PostToolUseFailure", "", true},
		{clineUIMessage{Type: "say", Say: "api_req_started"}, "", "", false},
		{clineUIMessage{Type: "say", Say: "task_progress"}, "", "", false},
		{clineUIMessage{Type: "ask", Say: "tool"}, "", "", false},
	}
	for _, tc := range cases {
		eventType, toolName, ok := mapClineMessage(tc.msg)
		if eventType != tc.eventType || toolName != tc.toolName || ok != tc.ok {
			t.Errorf("mapClineMessage(%v/%v) = %q,%q,%v want %q,%q,%v",
				tc.msg.Type, tc.msg.Say, eventType, toolName, ok, tc.eventType, tc.toolName, tc.ok)
		}
	}
}

func TestBuildClineEvents_DeterministicIdsAndSessionEnd(t *testing.T) {
	entry := clineHistoryEntry{ID: "1001", ULID: "ULIDAAA", TS: 1_770_000_000_000, Task: "hello", CWD: "/tmp/proj", ModelID: "m"}
	msgs := []clineUIMessage{
		{TS: 1_770_000_001_000, Type: "say", Say: "task", Text: "hello"},
		{TS: 1_770_000_002_000, Type: "say", Say: "text", Text: "working"},
		{TS: 1_770_000_003_000, Type: "say", Say: "command", Text: "ls -la"},
	}

	first := buildClineEvents(entry, msgs, "proj", "[]", "/tmp/x/ui_messages.json")
	second := buildClineEvents(entry, msgs, "proj", "[]", "/tmp/x/ui_messages.json")
	if len(first) != 4 || len(first) != len(second) {
		t.Fatalf("expected 4 stable events, got %d and %d", len(first), len(second))
	}
	for i := range first {
		if first[i].ID != second[i].ID {
			t.Errorf("event %d id not deterministic: %s vs %s", i, first[i].ID, second[i].ID)
		}
	}
	wantTypes := []string{"UserPromptSubmit", "ModelTurn", "PostToolUse", "SessionEnd"}
	for i, want := range wantTypes {
		if first[i].EventType != want {
			t.Errorf("event %d type = %q, want %q", i, first[i].EventType, want)
		}
	}
	if first[0].SessionID != "cline-ULIDAAA" || first[0].SourceTool != "cline" {
		t.Errorf("session/source = %q/%q, want cline-ULIDAAA/cline", first[0].SessionID, first[0].SourceTool)
	}
	if first[1].Model != "m" || first[0].CWD != "/tmp/proj" {
		t.Errorf("model/cwd not propagated: %q / %q", first[1].Model, first[0].CWD)
	}
}

func TestRunClineImport_ImportsAndIsIdempotent(t *testing.T) {
	root := writeClineFixture(t)
	database := withTestClineDB(t)

	runClineImport([]string{"--path", root})

	entries, err := loadClineHistory(filepath.Join(root, "data", "state", "taskHistory.json"))
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, entry := range entries {
		uiPath := filepath.Join(root, "data", "tasks", entry.ID, "ui_messages.json")
		msgs, err := loadClineUIMessages(uiPath)
		if err != nil {
			t.Fatalf("load messages for %s: %v", entry.ID, err)
		}
		filesJSON := loadClineFilesJSON(filepath.Join(root, "data", "tasks", entry.ID, "task_metadata.json"))
		events := buildClineEvents(entry, msgs, filepath.Base(entry.CWD), filesJSON, uiPath)
		for _, ev := range events {
			exists, err := database.HasEvent(ev.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !exists {
				t.Errorf("event %s (%s) for task %s missing after import", ev.ID[:12], ev.EventType, entry.ID)
			}
			total++
		}
	}
	if total == 0 {
		t.Fatal("expected at least one imported event")
	}

	// Second run must not duplicate anything: ids are identical, INSERT OR IGNORE.
	runClineImport([]string{"--path", root})
	for _, entry := range entries {
		uiPath := filepath.Join(root, "data", "tasks", entry.ID, "ui_messages.json")
		msgs, _ := loadClineUIMessages(uiPath)
		events := buildClineEvents(entry, msgs, filepath.Base(entry.CWD), "[]", uiPath)
		for _, ev := range events {
			exists, err := database.HasEvent(ev.ID)
			if err != nil || !exists {
				t.Fatalf("idempotency broken for %s (err=%v)", ev.EventType, err)
			}
		}
	}
}

func readClineHistoryForTest(t *testing.T, root, id string) clineHistoryEntry {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "data", "state", "taskHistory.json"))
	if err != nil {
		t.Fatal(err)
	}
	var entries []clineHistoryEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.ID == id {
			return entry
		}
	}
	t.Fatalf("task %s not found in fixture history", id)
	return clineHistoryEntry{}
}

func TestRunClineImport_DryRunDoesNotWrite(t *testing.T) {
	root := writeClineFixture(t)
	opened := false
	prev := clineOpenDB
	clineOpenDB = func(string) (*db.DB, error) {
		opened = true
		return db.Open(filepath.Join(t.TempDir(), "dry.db"))
	}
	t.Cleanup(func() { clineOpenDB = prev })

	runClineImport([]string{"--path", root, "--dry-run"})
	if opened {
		t.Error("dry run must not open the forge database")
	}
}

func TestRunClineImport_LimitOne(t *testing.T) {
	root := writeClineFixture(t)
	database := withTestClineDB(t)

	runClineImport([]string{"--path", root, "--limit", "1"})

	// Task 1001 is older and must be present; task 1002 must not be.
	for _, tc := range []struct {
		id   string
		want bool
	}{{"1001", true}, {"1002", false}} {
		uiPath := filepath.Join(root, "data", "tasks", tc.id, "ui_messages.json")
		msgs, err := loadClineUIMessages(uiPath)
		if err != nil {
			t.Fatal(err)
		}
		entry := readClineHistoryForTest(t, root, tc.id)
		events := buildClineEvents(entry, msgs, filepath.Base(entry.CWD), "[]", uiPath)
		for _, ev := range events {
			exists, err := database.HasEvent(ev.ID)
			if err != nil {
				t.Fatal(err)
			}
			if exists != tc.want {
				t.Errorf("task %s event %s exists=%v, want %v", tc.id, ev.EventType, exists, tc.want)
			}
		}
	}
}

func TestParseClineSince(t *testing.T) {
	if d, err := parseClineSince(""); err != nil || d != 0 {
		t.Errorf("empty = %v, %v; want 0, nil", d, err)
	}
	if d, err := parseClineSince("30d"); err != nil || d != 30*24*time.Hour {
		t.Errorf("30d = %v, %v; want %v, nil", d, err, 30*24*time.Hour)
	}
	if d, err := parseClineSince("90m"); err != nil || d != 90*time.Minute {
		t.Errorf("90m = %v, %v; want %v, nil", d, err, 90*time.Minute)
	}
	if _, err := parseClineSince("nope"); err == nil {
		t.Error("invalid duration should error")
	}
}
