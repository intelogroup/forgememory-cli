package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/distill"
)

// ---------------------------------------------------------------------------
// Pure logic tests
// ---------------------------------------------------------------------------

func TestParseChunkSummaryResponse_ValidJSON(t *testing.T) {
	raw := `{"request":"fix bug","investigation":"read logs","learnings":"found root cause","next_steps":"add test","keywords":["go","sqlite"]}`
	got, err := parseChunkSummaryResponse(raw)
	if err != nil {
		t.Fatalf("parseChunkSummaryResponse: %v", err)
	}
	if got.request != "fix bug" || got.learnings != "found root cause" {
		t.Fatalf("unexpected parse: %+v", got)
	}
}

func TestParseChunkSummaryResponse_MarkdownWrapped(t *testing.T) {
	raw := "Here is the summary:\n```json\n{\"request\":\"r\",\"investigation\":\"i\",\"learnings\":\"l\",\"next_steps\":\"\",\"keywords\":[]}\n```\n"
	got, err := parseChunkSummaryResponse(raw)
	if err != nil {
		t.Fatalf("parseChunkSummaryResponse: %v", err)
	}
	if got.request != "r" {
		t.Fatalf("request = %q, want r", got.request)
	}
}

func TestParseChunkSummaryResponse_InvalidJSON(t *testing.T) {
	_, err := parseChunkSummaryResponse("not json at all")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestBuildChunkSummaryPrompt_IncludesChunkMeta(t *testing.T) {
	events := []db.Event{{EventType: "PostToolUse", ToolName: "Read", Payload: `{"path":"a.go"}`}}
	prompt := buildChunkSummaryPrompt(events, 2, 5)
	if !strings.Contains(prompt, "chunk 2 of 5") {
		t.Fatalf("prompt missing chunk meta: %q", prompt)
	}
}

func TestBuildMasterSummaryPrompt_AggregatesChunks(t *testing.T) {
	chunks := []parsedChunkSummary{
		{index: 0, events: 50, request: "req1", investigation: "inv1", learnings: "learn1", keywords: []string{"go"}},
		{index: 1, events: 50, request: "req2", investigation: "inv2", learnings: "learn2"},
	}
	prompt := buildMasterSummaryPrompt(chunks)
	for _, want := range []string{"req1", "inv1", "learn1", "req2", "Chunk 1", "Chunk 2"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestFormatMCPText_SkipsHeaders(t *testing.T) {
	in := "## Principles\n---\n- bullet line\nActual principle text\n"
	got := formatMCPText(in, "  ")
	if strings.Contains(got, "##") || strings.Contains(got, "---") {
		t.Fatalf("headers should be stripped, got %q", got)
	}
	if !strings.Contains(got, "Actual principle text") {
		t.Fatalf("content line missing: %q", got)
	}
}

func TestFormatMCPText_EmptyInput(t *testing.T) {
	if got := formatMCPText("", "  "); got != "" {
		t.Fatalf("empty input = %q, want empty", got)
	}
}

func TestMergeArgs_Overrides(t *testing.T) {
	a := map[string]any{"project_id": "a", "limit": float64(5)}
	b := map[string]any{"limit": float64(10)}
	got := mergeArgs(a, b)
	if got["project_id"] != "a" {
		t.Fatalf("project_id = %v", got["project_id"])
	}
	if got["limit"] != float64(10) {
		t.Fatalf("limit = %v, want 10", got["limit"])
	}
}

// ---------------------------------------------------------------------------
// Mock LLM for distill-agent integration
// ---------------------------------------------------------------------------

func distillAgentMockServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Prompt string `json:"prompt"`
		}
		_ = json.Unmarshal(body, &req)
		prompt := req.Prompt

		var response string
		switch {
		case strings.Contains(prompt, "Return a JSON array"):
			response = `[{"type":"pattern","title":"Agent principle","narrative":"test narrative for principles","impact_score":0.7,"outcome":"success","impl_hint":"","concepts":["pattern"],"files_modified":[]}]`
		case strings.Contains(prompt, "This is chunk"):
			response = `{"request":"chunk req","investigation":"chunk inv","learnings":"chunk learn","next_steps":"","keywords":["chunk"]}`
		case strings.Contains(prompt, "combined session summary"):
			response = `{"request":"master req","investigation":"master inv","learnings":"master learn","next_steps":"","keywords":["master"]}`
		default:
			response = `{"request":"session goal","investigation":"session work","learnings":"session learn","next_steps":"","keywords":["session"]}`
		}
		json.NewEncoder(w).Encode(map[string]string{"response": response})
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func seedDistillAgentSession(t *testing.T, sessionID string, n int) {
	t.Helper()
	home := os.Getenv("HOME")
	database, err := db.Open("")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()
	base := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		e := &db.Event{
			TS:         base.Add(time.Duration(i) * time.Minute).UTC().Format(time.RFC3339),
			SessionID:  sessionID,
			ProjectID:  "agent-test-proj",
			SourceTool: "claude",
			EventType:  "PostToolUse",
			ToolName:   "Bash",
			Payload:    `{"command":"echo"}`,
		}
		if err := database.InsertEvent(e); err != nil {
			t.Fatalf("InsertEvent: %v", err)
		}
	}
	_ = home
}

func runDistillAgentSubprocess(t *testing.T, home, mockURL string, args ...string) int {
	t.Helper()
	if forgeBin == "" {
		t.Skip("forgeBin not set (TestMain did not run)")
	}
	cmdArgs := append([]string{"distill-agent"}, args...)
	cmd := exec.Command(forgeBin, cmdArgs...)
	cmd.Env = append(baseEnv(),
		"HOME="+home,
		"FORGE_PROVIDER=ollama",
		"FORGE_BASE_URL="+mockURL,
		"FORGE_API_KEY=",
		"FORGE_MODEL=test-model",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.Logf("distill-agent stderr: %s", stderr.String())
			return exitErr.ExitCode()
		}
		t.Fatalf("distill-agent run: %v\nstderr: %s", err, stderr.String())
	}
	return 0
}

func TestRunDistillAgent_SmallSession_DirectPath(t *testing.T) {
	home := shortHome(t)
	t.Setenv("HOME", home)
	srv, calls := distillAgentMockServer(t)

	const sessionID = "sess-small"
	seedDistillAgentSession(t, sessionID, 10)

	code := runDistillAgentSubprocess(t, home, srv.URL,
		"--session-id", sessionID,
		"--project-id", "agent-test-proj",
		"--checkpoint-kind", "daemon",
		"--checkpoint-key", sessionID+":daemon",
	)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if calls.Load() < 2 {
		t.Fatalf("expected >=2 LLM calls (synthesis + principles), got %d", calls.Load())
	}

	database, err := db.Open("")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()
	exists, err := database.HasSessionCheckpoint(sessionID + ":daemon")
	if err != nil || !exists {
		t.Fatal("expected session checkpoint summary to exist")
	}
}

func TestRunDistillAgent_LargeSession_ChunkedPath(t *testing.T) {
	home := shortHome(t)
	t.Setenv("HOME", home)
	srv, calls := distillAgentMockServer(t)

	const sessionID = "sess-large"
	seedDistillAgentSession(t, sessionID, 150)

	code := runDistillAgentSubprocess(t, home, srv.URL,
		"--session-id", sessionID,
		"--project-id", "agent-test-proj",
		"--checkpoint-kind", "daemon",
		"--checkpoint-key", sessionID+":daemon",
	)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	// 150 events / 50 per chunk = 3 chunks + 1 master + 1 principles = 5
	wantCalls := int32(5)
	if calls.Load() != wantCalls {
		t.Fatalf("LLM calls = %d, want %d", calls.Load(), wantCalls)
	}
}

func TestRunDistillAgent_FewerThanThreeEvents_ExitsClean(t *testing.T) {
	home := shortHome(t)
	t.Setenv("HOME", home)
	srv, calls := distillAgentMockServer(t)

	const sessionID = "sess-few"
	seedDistillAgentSession(t, sessionID, 2)

	code := runDistillAgentSubprocess(t, home, srv.URL,
		"--session-id", sessionID,
		"--checkpoint-key", sessionID+":daemon",
	)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if calls.Load() != 0 {
		t.Fatalf("expected no LLM calls, got %d", calls.Load())
	}
}

func TestRunDistillAgent_CheckpointExists_Skips(t *testing.T) {
	home := shortHome(t)
	t.Setenv("HOME", home)
	srv, calls := distillAgentMockServer(t)

	const sessionID = "sess-skip"
	seedDistillAgentSession(t, sessionID, 10)

	database, err := db.Open("")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	key := sessionID + ":daemon"
	if err := database.InsertSessionSummary(&db.SessionSummary{
		SessionID:      sessionID,
		ProjectID:      "agent-test-proj",
		CheckpointKey:  key,
		CheckpointKind: "daemon",
		Summary:        "existing",
	}); err != nil {
		t.Fatalf("InsertSessionSummary: %v", err)
	}
	database.Close()

	code := runDistillAgentSubprocess(t, home, srv.URL,
		"--session-id", sessionID,
		"--checkpoint-key", key,
	)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if calls.Load() != 0 {
		t.Fatalf("expected no LLM calls when checkpoint exists, got %d", calls.Load())
	}
}

func TestRunDistillAgent_EmptySessionID_ExitsZero(t *testing.T) {
	home := shortHome(t)
	t.Setenv("HOME", home)
	srv, _ := distillAgentMockServer(t)

	code := runDistillAgentSubprocess(t, home, srv.URL)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 for empty session-id", code)
	}
}

// ---------------------------------------------------------------------------
// In-process synthesizeLargeSession test (no subprocess)
// ---------------------------------------------------------------------------

func TestSynthesizeLargeSession_ChunkCount(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv, calls := distillAgentMockServer(t)
	t.Setenv("FORGE_PROVIDER", "ollama")
	t.Setenv("FORGE_BASE_URL", srv.URL)
	t.Setenv("FORGE_API_KEY", "")
	t.Setenv("FORGE_MODEL", "test")

	database, err := db.Open("")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	const sessionID = "sess-unit-large"
	var events []db.Event
	base := time.Now().UTC()
	for i := 0; i < 120; i++ {
		events = append(events, db.Event{
			TS:         base.Add(time.Duration(i) * time.Second).Format(time.RFC3339),
			SessionID:  sessionID,
			ProjectID:  "unit-proj",
			SourceTool: "claude",
			EventType:  "PostToolUse",
			Payload:    "{}",
		})
	}

	cfg := distill.LoadConfig()
	d := distill.New(database, cfg)
	summary, count, err := synthesizeLargeSession(d, database, events, sessionID, "unit-proj", "daemon", sessionID+":daemon")
	if err != nil {
		t.Fatalf("synthesizeLargeSession: %v", err)
	}
	if summary == nil {
		t.Fatal("expected summary")
	}
	if count < 1 {
		t.Fatalf("principles count = %d, want >= 1", count)
	}
	// 120/50 = 3 chunks + master + principles
	if calls.Load() != 5 {
		t.Fatalf("LLM calls = %d, want 5", calls.Load())
	}
}
