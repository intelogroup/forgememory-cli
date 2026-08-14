package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/forge/forge/internal/db"
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
