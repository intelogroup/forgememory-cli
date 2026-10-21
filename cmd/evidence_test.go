package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forge/forge/internal/db"
)

func TestHandleEventMsgPreservesEvidenceMetadataAndCapturesTranscript(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	transcriptPath := filepath.Join(home, "transcript.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(`{"type":"message","role":"assistant","content":"token sk-test-secret-12345678901234567890"}`+"\n"), 0o600); err != nil {
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
		"session_id":      "session-1",
		"project_id":      "project-1",
		"source_tool":     "claude",
		"event_type":      "SessionEnd",
		"cwd":             home,
		"git_branch":      "feature/evidence",
		"git_commit":      "abc123",
		"task_id":         "task-1",
		"transcript_path": transcriptPath,
		"payload":         `{"message":"done"}`,
	} {
		raw[key] = json.RawMessage(strconvQuote(value))
	}
	handleEventMsg(raw, database)

	events, err := database.UndistilledEventsIncludingUnknown(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %#v, want two events (session-end + transcript turn)", events)
	}
	var modelTurn *db.Event
	for i := range events {
		if events[i].EventType == "ModelTurn" {
			modelTurn = &events[i]
		}
	}
	if modelTurn == nil {
		t.Fatalf("events = %#v, want a ModelTurn transcript turn", events)
	}
	if strings.Contains(modelTurn.Payload, "sk-test-secret-12345678901234567890") {
		t.Fatalf("transcript event was not redacted: %q", modelTurn.Payload)
	}

	artifacts, err := database.EvaluationArtifacts("session-1", "task-1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 || artifacts[0].Kind != "transcript" {
		t.Fatalf("artifacts = %#v, want one transcript", artifacts)
	}
	for _, want := range []string{home, "feature/evidence", "abc123", transcriptPath, "event-1"} {
		if !strings.Contains(artifacts[0].Metadata, want) {
			t.Fatalf("artifact metadata = %q, missing %q", artifacts[0].Metadata, want)
		}
	}
	content, err := os.ReadFile(filepath.Join(filepath.Dir(database.Path), "artifacts", artifacts[0].Path))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "sk-test-secret-12345678901234567890") {
		t.Fatalf("transcript was not redacted: %q", content)
	}
}

func TestHandleEventMsgCapturesBashTestReport(t *testing.T) {
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
		"id":          "event-2",
		"session_id":  "session-2",
		"project_id":  "project-1",
		"source_tool": "codex",
		"event_type":  "PostToolUse",
		"tool_name":   "Bash",
		"payload":     `{"tool_input":{"command":"go test ./internal/db"},"tool_response":"ok"}`,
	} {
		raw[key] = json.RawMessage(strconvQuote(value))
	}
	handleEventMsg(raw, database)

	artifacts, err := database.EvaluationArtifacts("session-2", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 || artifacts[0].Kind != "test-report" {
		t.Fatalf("artifacts = %#v, want one test-report", artifacts)
	}
}

func TestHandleEventMsgCapturesGitDiffAfterWrite(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "repo")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	run("init")
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "main.go")
	run("-c", "user.email=forge@example.com", "-c", "user.name=Forge", "commit", "--allow-empty", "-m", "init")
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\n\nfunc Changed() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	homeDB := filepath.Join(home, "forge.db")
	database, err := db.Open(homeDB)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	raw := map[string]json.RawMessage{}
	for key, value := range map[string]string{
		"type":        "event",
		"id":          "event-3",
		"session_id":  "session-3",
		"project_id":  "project-1",
		"source_tool": "claude",
		"event_type":  "PostToolUse",
		"tool_name":   "Edit",
		"cwd":         repo,
		"payload":     `{"tool_input":{"file_path":"main.go"}}`,
	} {
		raw[key] = json.RawMessage(strconvQuote(value))
	}
	handleEventMsg(raw, database)

	artifacts, err := database.EvaluationArtifacts("session-3", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 || artifacts[0].Kind != "diff" {
		t.Fatalf("artifacts = %#v, want one diff", artifacts)
	}
}

func strconvQuote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

// The hook computes status/exit_code/duration/sequence/parent span; the daemon
// must persist them (they were silently dropped, leaving the columns empty).
func TestHandleEventMsgPersistsOutcomeFields(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	database, err := db.Open(filepath.Join(home, "forge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	raw := map[string]json.RawMessage{
		"type":           json.RawMessage(`"event"`),
		"id":             json.RawMessage(`"event-out"`),
		"session_id":     json.RawMessage(`"session-out"`),
		"project_id":     json.RawMessage(`"project-out"`),
		"source_tool":    json.RawMessage(`"claude"`),
		"event_type":     json.RawMessage(`"PostToolUse"`),
		"tool_name":      json.RawMessage(`"Bash"`),
		"payload":        json.RawMessage(`"{}"`),
		"status":         json.RawMessage(`"error"`),
		"exit_code":      json.RawMessage(`2`),
		"duration_ms":    json.RawMessage(`842`),
		"sequence":       json.RawMessage(`7`),
		"parent_span_id": json.RawMessage(`"parent-1"`),
	}
	handleEventMsg(raw, database)

	events, err := database.UndistilledEventsIncludingUnknown(10)
	if err != nil {
		t.Fatal(err)
	}
	var got *db.Event
	for i := range events {
		if events[i].ID == "event-out" {
			got = &events[i]
		}
	}
	if got == nil {
		t.Fatalf("event-out not stored: %#v", events)
	}
	if got.Status != "error" || got.ExitCode != 2 || got.DurationMS != 842 || got.Sequence != 7 || got.ParentSpanID != "parent-1" {
		t.Fatalf("got status=%q exit=%d dur=%d seq=%d parent=%q, want error/2/842/7/parent-1",
			got.Status, got.ExitCode, got.DurationMS, got.Sequence, got.ParentSpanID)
	}
}
