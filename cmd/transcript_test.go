package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/forge/forge/internal/db"
	_ "modernc.org/sqlite"
)

func TestIngestTranscriptNormalizesTurnsAndDeduplicates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	content := "{" + `"type":"user","timestamp":"2026-08-14T12:00:00Z","message":{"role":"user","content":"fix it"}` + "}\n" +
		"{" + `"type":"assistant","model":"test-model","message":{"role":"assistant","content":"I will inspect the code"}` + "}\n" +
		"{" + `"type":"tool_result","is_error":true,"tool_name":"Bash","tool_response":{"exit_code":1}}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	database, err := db.Open(filepath.Join(t.TempDir(), "trace.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	parent := db.Event{ID: "end", TS: "2026-08-14T12:01:00Z", TraceID: "trace-1", SpanID: "span-end", TaskID: "task-1", SessionID: "session-1", ProjectID: "project-1", SourceTool: "claude", TranscriptPath: path}
	count, err := ingestTranscript(database, parent)
	if err != nil || count != 3 {
		t.Fatalf("ingestTranscript count=%d err=%v, want 3", count, err)
	}
	count, err = ingestTranscript(database, parent)
	if err != nil || count != 3 {
		t.Fatalf("retry ingestTranscript count=%d err=%v, want 3 idempotent inserts", count, err)
	}
	events, err := database.TraceEvents("trace-1", 20)
	if err != nil {
		t.Fatalf("TraceEvents: %v", err)
	}
	if len(events) != 3 || events[0].EventType != "TranscriptPrompt" || events[1].EventType != "ModelTurn" || events[2].EventType != "TranscriptToolResult" {
		t.Fatalf("normalized events = %#v", events)
	}
	if events[1].Model != "test-model" || events[2].Status != "error" || events[2].ParentSpanID == "" {
		t.Fatalf("normalized metadata = %#v", events)
	}
}

func TestTranscriptEventTypeRecognizesCodexResponseItem(t *testing.T) {
	if got := transcriptEventType(map[string]any{"type": "response_item", "payload": map[string]any{"type": "message", "role": "assistant"}}); got != "ModelTurn" {
		t.Fatalf("transcriptEventType() = %q, want ModelTurn", got)
	}
}

func TestResolveTranscriptPath(t *testing.T) {
	if got := resolveTranscriptPath(&db.Event{TranscriptPath: "/tmp/t.jsonl"}); got != "/tmp/t.jsonl" {
		t.Fatalf("resolveTranscriptPath() = %q, want explicit path", got)
	}
	if got := resolveTranscriptPath(&db.Event{SourceTool: "claude"}); got != "" {
		t.Fatalf("resolveTranscriptPath() = %q, want empty for claude without path", got)
	}
	if got := resolveTranscriptPath(&db.Event{SourceTool: "codex", SessionID: "unknown"}); got != "" {
		t.Fatalf("resolveTranscriptPath() = %q, want empty for unknown codex session", got)
	}
}

func TestFindCodexTranscriptMatchesSuffix(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sessID := "019ff278-e026-7272-855f-ff0adcdf5a36"
	day := filepath.Join(home, ".codex", "sessions", "2026", "08", "11")
	if err := os.MkdirAll(day, 0o700); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(day, "rollout-2026-08-11T16-17-15-"+sessID+".jsonl")
	if err := os.WriteFile(filename, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := findCodexTranscript(sessID, "2026-08-11T20:18:15.829Z"); got != filename {
		t.Fatalf("findCodexTranscript() = %q, want %q", got, filename)
	}
	// Bounded to the calendar day: a different day must not match.
	if got := findCodexTranscript(sessID, "2026-08-12T00:00:00.000Z"); got != "" {
		t.Fatalf("findCodexTranscript() = %q, want empty for different day", got)
	}
}

func TestTranscriptEventTypeCodexToolCalls(t *testing.T) {
	cases := []struct {
		raw  map[string]any
		want string
	}{
		{map[string]any{"type": "response_item", "payload": map[string]any{"type": "custom_tool_call", "name": "exec"}}, "TranscriptToolCall"},
		{map[string]any{"type": "response_item", "payload": map[string]any{"type": "custom_tool_call_output"}}, "TranscriptToolResult"},
		{map[string]any{"type": "function_call", "payload": map[string]any{"name": "exec"}}, "TranscriptToolCall"},
		{map[string]any{"type": "function_call_output"}, "TranscriptToolResult"},
	}
	for _, c := range cases {
		if got := transcriptEventType(c.raw); got != c.want {
			t.Fatalf("transcriptEventType(%v) = %q, want %q", c.raw, got, c.want)
		}
	}
}

func TestIngestTranscriptCodexToolCallExtractsName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"response_item","payload":{"type":"custom_tool_call","name":"exec","input":"ls"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(filepath.Join(t.TempDir(), "trace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	parent := db.Event{ID: "e", TS: "t", TraceID: "tr", SpanID: "s", SessionID: "s1", ProjectID: "p", SourceTool: "codex", TranscriptPath: path}
	count, err := ingestTranscript(database, parent)
	if err != nil || count != 1 {
		t.Fatalf("ingestTranscript count=%d err=%v, want 1", count, err)
	}
	events, err := database.TraceEvents("tr", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].EventType != "TranscriptToolCall" || events[0].ToolName != "exec" {
		t.Fatalf("events = %#v, want a TranscriptToolCall named exec", events)
	}
}

func TestAntigravityLines(t *testing.T) {
	user := antigravityLines(map[string]any{"source": "USER_EXPLICIT", "type": "USER_INPUT", "created_at": "2026-06-05T18:58:06Z", "content": "hello"})
	if len(user) != 1 {
		t.Fatalf("antigravityLines(user) = %d lines, want 1", len(user))
	}
	var u map[string]any
	if err := json.Unmarshal([]byte(user[0]), &u); err != nil {
		t.Fatal(err)
	}
	if transcriptEventType(u) != "TranscriptPrompt" {
		t.Fatalf("user turn classified as %q, want TranscriptPrompt", transcriptEventType(u))
	}

	plan := antigravityLines(map[string]any{
		"source": "MODEL", "type": "PLANNER_RESPONSE", "created_at": "2026-06-05T18:58:07Z",
		"tool_calls": []any{map[string]any{"name": "list_dir", "args": map[string]any{}}, map[string]any{"name": "run_command", "args": map[string]any{}}},
	})
	if len(plan) != 2 {
		t.Fatalf("antigravityLines(plan) = %d lines, want 2", len(plan))
	}

	result := antigravityLines(map[string]any{"source": "MODEL", "type": "RUN_COMMAND", "status": "DONE", "created_at": "2026-06-05T18:58:26Z", "content": "output"})
	if len(result) != 1 {
		t.Fatalf("antigravityLines(result) = %d lines, want 1", len(result))
	}
	var r map[string]any
	if err := json.Unmarshal([]byte(result[0]), &r); err != nil {
		t.Fatal(err)
	}
	if transcriptEventType(r) != "TranscriptToolResult" || r["tool_name"] != "run_command" {
		t.Fatalf("result turn = %v, want TranscriptToolResult named run_command", r)
	}

	if got := antigravityLines(map[string]any{"source": "SYSTEM", "type": "CONVERSATION_HISTORY"}); len(got) != 0 {
		t.Fatalf("antigravityLines(system) = %v, want empty", got)
	}
}

func TestFindAntigravityTranscript(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	convID := "abc-123"
	dir := filepath.Join(home, ".gemini", "antigravity", "brain", convID, ".system_generated", "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "transcript.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := findAntigravityTranscript(convID); got != path {
		t.Fatalf("findAntigravityTranscript() = %q, want %q", got, path)
	}
	if got := findAntigravityTranscript("unknown"); got != "" {
		t.Fatalf("findAntigravityTranscript() = %q, want empty", got)
	}
}

func TestIngestAntigravityTranscript(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	convID := "conv-1"
	dir := filepath.Join(home, ".gemini", "antigravity", "brain", convID, ".system_generated", "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	content := `{"source":"USER_EXPLICIT","type":"USER_INPUT","created_at":"2026-06-05T18:58:06Z","content":"hello"}` + "\n" +
		`{"source":"MODEL","type":"PLANNER_RESPONSE","created_at":"2026-06-05T18:58:07Z","tool_calls":[{"name":"list_dir","args":{}}]}` + "\n" +
		`{"source":"MODEL","type":"LIST_DIRECTORY","status":"DONE","created_at":"2026-06-05T18:58:08Z","content":"listing"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "transcript.jsonl"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	database, err := db.Open(filepath.Join(home, "trace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	parent := db.Event{ID: "e", TS: "2026-06-05T19:00:00Z", TraceID: "trace-ag", SpanID: "s", SessionID: convID, ProjectID: "p", SourceTool: "antigravity"}
	count, err := ingestAntigravityTranscript(database, parent)
	if err != nil || count != 3 {
		t.Fatalf("ingestAntigravityTranscript count=%d err=%v, want 3", count, err)
	}
	events, err := database.TraceEvents("trace-ag", 20)
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]int{}
	for _, e := range events {
		types[e.EventType]++
	}
	if types["TranscriptPrompt"] != 1 || types["TranscriptToolCall"] != 1 || types["TranscriptToolResult"] != 1 {
		t.Fatalf("event types = %v, want one prompt + one tool call + one tool result", types)
	}
}

func TestIngestOpencodeDBImportsTurns(t *testing.T) {
	dir := t.TempDir()
	opencodePath := filepath.Join(dir, "opencode.db")
	conn, err := sql.Open("sqlite", opencodePath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(`CREATE TABLE message (id text PRIMARY KEY, session_id text NOT NULL, time_created integer NOT NULL, time_updated integer NOT NULL, data text NOT NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(`CREATE TABLE part (id text PRIMARY KEY, message_id text NOT NULL, session_id text NOT NULL, time_created integer NOT NULL, time_updated integer NOT NULL, data text NOT NULL)`)
	if err != nil {
		t.Fatal(err)
	}

	sessID := "ses_test123"
	mustExec := func(query string, args ...any) {
		if _, err := conn.Exec(query, args...); err != nil {
			t.Fatalf("exec %q: %v", query, err)
		}
	}
	mustExec(`INSERT INTO message (id, session_id, time_created, time_updated, data) VALUES (?,?,?,?,?)`,
		"m1", sessID, 1000, 1000, `{"role":"user"}`)
	mustExec(`INSERT INTO part (id, message_id, session_id, time_created, time_updated, data) VALUES (?,?,?,?,?,?)`,
		"p1", "m1", sessID, 1001, 1001, `{"type":"text","text":"fix the bug"}`)
	mustExec(`INSERT INTO message (id, session_id, time_created, time_updated, data) VALUES (?,?,?,?,?)`,
		"m2", sessID, 2000, 2000, `{"role":"assistant","modelID":"test-model"}`)
	mustExec(`INSERT INTO part (id, message_id, session_id, time_created, time_updated, data) VALUES (?,?,?,?,?,?)`,
		"p2", "m2", sessID, 2001, 2001, `{"type":"text","text":"I will inspect"}`)
	mustExec(`INSERT INTO part (id, message_id, session_id, time_created, time_updated, data) VALUES (?,?,?,?,?,?)`,
		"p3", "m2", sessID, 2002, 2002, `{"type":"tool","tool":"bash","state":{"status":"completed","input":{"command":"ls"},"output":"ok","exit":0}}`)
	conn.Close()

	database, err := db.Open(filepath.Join(dir, "trace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	parent := db.Event{ID: "end", TS: "2026-08-15T12:00:00Z", TraceID: "trace-op", SpanID: "span-end", SessionID: sessID, ProjectID: "project-1", SourceTool: "opencode"}
	count, err := ingestOpencodeDB(database, parent, opencodePath)
	if err != nil || count != 4 {
		t.Fatalf("ingestOpencodeDB count=%d err=%v, want 4", count, err)
	}

	events, err := database.TraceEvents("trace-op", 20)
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]int{}
	var modelTurn *db.Event
	var toolResult *db.Event
	for i := range events {
		types[events[i].EventType]++
		switch events[i].EventType {
		case "ModelTurn":
			modelTurn = &events[i]
		case "TranscriptToolResult":
			toolResult = &events[i]
		}
	}
	if types["TranscriptPrompt"] != 1 || types["ModelTurn"] != 1 || types["TranscriptToolCall"] != 1 || types["TranscriptToolResult"] != 1 {
		t.Fatalf("event types = %v, want one of each", types)
	}
	if modelTurn == nil || modelTurn.Model != "test-model" {
		t.Fatalf("ModelTurn = %#v, want model test-model", modelTurn)
	}
	if toolResult == nil || toolResult.Status != "completed" || toolResult.ToolName != "bash" {
		t.Fatalf("TranscriptToolResult = %#v, want status completed tool bash", toolResult)
	}
}

func TestHandleEventMsgIngestsTranscriptAtSessionEnd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	transcriptPath := filepath.Join(home, "transcript.jsonl")
	content := "{" + `"type":"user","message":{"role":"user","content":"do it"}` + "}\n" +
		"{" + `"type":"assistant","model":"test-model","message":{"role":"assistant","content":"done"}` + "}\n"
	if err := os.WriteFile(transcriptPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(filepath.Join(home, "forge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	raw := map[string]json.RawMessage{}
	for key, value := range map[string]string{
		"type":            "event",
		"id":              "event-1",
		"trace_id":        "trace-1",
		"span_id":         "span-1",
		"session_id":      "session-1",
		"project_id":      "project-1",
		"source_tool":     "claude",
		"event_type":      "SessionEnd",
		"transcript_path": transcriptPath,
		"payload":         `{"message":"done"}`,
	} {
		raw[key] = json.RawMessage(strconvQuote(value))
	}
	handleEventMsg(raw, database)

	events, err := database.TraceEvents("trace-1", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("events = %#v, want 3 (session-end + 2 transcript turns)", events)
	}
	var sawModelTurn bool
	for i := range events {
		if events[i].EventType == "ModelTurn" && events[i].Model == "test-model" && events[i].SourceTool == "claude" {
			sawModelTurn = true
		}
	}
	if !sawModelTurn {
		t.Fatalf("events = %#v, want a ModelTurn with model test-model", events)
	}
}

func TestHandleEventMsgPersistsTraceAndSpan(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	database, err := db.Open(filepath.Join(home, "forge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	raw := map[string]json.RawMessage{}
	for key, value := range map[string]string{
		"type":        "event",
		"id":          "event-1",
		"trace_id":    "trace-x",
		"span_id":     "span-y",
		"session_id":  "session-1",
		"project_id":  "project-1",
		"source_tool": "claude",
		"event_type":  "PostToolUse",
		"tool_name":   "Edit",
		"payload":     `{"tool_input":{"file_path":"main.go"}}`,
	} {
		raw[key] = json.RawMessage(strconvQuote(value))
	}
	handleEventMsg(raw, database)

	events, err := database.TraceEvents("trace-x", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].SpanID != "span-y" {
		t.Fatalf("events = %#v, want one event with span_id span-y", events)
	}
}
