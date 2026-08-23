package distill

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/forge/forge/internal/db"
)

func TestLoadConfigDefaults(t *testing.T) {
	// Isolate the config lookup so this test verifies code defaults rather than
	// any developer-specific ~/.forge/config state.
	t.Setenv("HOME", t.TempDir())

	// Clear env vars
	t.Setenv("FORGE_PROVIDER", "")
	t.Setenv("FORGE_API_KEY", "")
	t.Setenv("FORGE_MODEL", "")
	t.Setenv("FORGE_BASE_URL", "")
	t.Setenv("FORGE_TIMEOUT", "")
	t.Setenv("FORGE_RETRIES", "")
	t.Setenv("FORGE_OLLAMA_TIMEOUT", "")
	t.Setenv("FORGE_OLLAMA_STARTUP_WAIT", "")

	cfg := LoadConfig()
	if cfg.Provider != ProviderOllama {
		t.Errorf("default provider = %s, want %s", cfg.Provider, ProviderOllama)
	}
	if cfg.Model != "llama3:latest" {
		t.Errorf("default model = %s, want llama3:latest", cfg.Model)
	}
	if cfg.BaseURL != "http://localhost:11434" {
		t.Errorf("default base URL = %s, want http://localhost:11434", cfg.BaseURL)
	}
	if cfg.Timeout != 30*time.Second {
		t.Errorf("default timeout = %s, want 30s", cfg.Timeout)
	}
	if cfg.Retries != 3 {
		t.Errorf("default retries = %d, want 3", cfg.Retries)
	}
}

func TestLoadConfigOpenAI(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("FORGE_API_URL", "")
	t.Setenv("FORGE_PROVIDER", "openai")
	t.Setenv("FORGE_API_KEY", "sk-test123")
	t.Setenv("FORGE_MODEL", "gpt-4o")

	cfg := LoadConfig()
	if cfg.Provider != ProviderOpenAI {
		t.Errorf("provider = %s, want %s", cfg.Provider, ProviderOpenAI)
	}
	if cfg.Model != "gpt-4o" {
		t.Errorf("model = %s, want gpt-4o", cfg.Model)
	}
	if cfg.APIKey != "sk-test123" {
		t.Errorf("api key = %s, want sk-test123", cfg.APIKey)
	}
}

func TestLoadConfigLegacyAPIURL(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("FORGE_PROVIDER", "openai")
	t.Setenv("FORGE_API_KEY", "sk-test123")
	t.Setenv("FORGE_MODEL", "")
	t.Setenv("FORGE_BASE_URL", "")
	t.Setenv("FORGE_API_URL", "https://legacy.example.test")

	cfg := LoadConfig()
	if cfg.BaseURL != "https://legacy.example.test" {
		t.Errorf("base URL = %s, want legacy API URL", cfg.BaseURL)
	}
}

func TestLoadConfigAnthropic(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("FORGE_PROVIDER", "anthropic")
	t.Setenv("FORGE_API_KEY", "sk-ant-test")
	t.Setenv("FORGE_MODEL", "claude-3-sonnet-20240229")

	cfg := LoadConfig()
	if cfg.Provider != ProviderAnthropic {
		t.Errorf("provider = %s, want %s", cfg.Provider, ProviderAnthropic)
	}
	if cfg.Model != "claude-3-sonnet-20240229" {
		t.Errorf("model = %s, want claude-3-sonnet-20240229", cfg.Model)
	}
	if cfg.APIKey != "sk-ant-test" {
		t.Errorf("api key = %s, want sk-ant-test", cfg.APIKey)
	}
}

func TestLoadConfigCodex(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate from real ~/.forge/config
	t.Setenv("FORGE_PROVIDER", "codex")
	t.Setenv("FORGE_API_KEY", "")
	t.Setenv("FORGE_MODEL", "")
	t.Setenv("FORGE_BASE_URL", "")

	cfg := LoadConfig()
	if cfg.Provider != ProviderCodex {
		t.Errorf("provider = %s, want %s", cfg.Provider, ProviderCodex)
	}
	if cfg.Model != "" {
		t.Errorf("model = %q, want empty string to use Codex CLI default", cfg.Model)
	}
}

func TestLoadConfigAntigravity(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate from real ~/.forge/config
	t.Setenv("FORGE_PROVIDER", "antigravity")
	t.Setenv("FORGE_API_KEY", "")
	t.Setenv("FORGE_MODEL", "")
	t.Setenv("FORGE_BASE_URL", "")

	cfg := LoadConfig()
	if cfg.Provider != ProviderAntigravity {
		t.Errorf("provider = %s, want %s", cfg.Provider, ProviderAntigravity)
	}
	if cfg.Model != "flash" {
		t.Errorf("model = %s, want flash", cfg.Model)
	}
	if cfg.BaseURL != "" {
		t.Errorf("base URL = %q, want empty string", cfg.BaseURL)
	}
}

func TestParsePrinciples(t *testing.T) {
	d := &Distiller{}
	response := `[
		{"type": "bugfix", "title": "Windows daemon fix", "narrative": "Use polling loop", "impact_score": 0.9},
		{"type": "pattern", "title": "Go cross-compile", "narrative": "GOOS=windows GOARCH=amd64", "impact_score": 0.7}
	]`

	principles, err := d.parsePrinciples(response, "forge")
	if err != nil {
		t.Fatalf("parsePrinciples failed: %v", err)
	}
	if len(principles) != 2 {
		t.Fatalf("expected 2 principles, got %d", len(principles))
	}
	if principles[0].Title != "Windows daemon fix" {
		t.Errorf("title = %s, want 'Windows daemon fix'", principles[0].Title)
	}
	if principles[0].Type != "bugfix" {
		t.Errorf("type = %s, want bugfix", principles[0].Type)
	}
	if principles[0].ImpactScore != 0.9 {
		t.Errorf("impact = %f, want 0.9", principles[0].ImpactScore)
	}
	if principles[0].ProjectID != "forge" {
		t.Errorf("project = %s, want forge", principles[0].ProjectID)
	}
}

func TestParsePrinciplesWithMarkdown(t *testing.T) {
	d := &Distiller{}
	// LLM sometimes wraps JSON in markdown
	response := "```json\n[{\"type\": \"architecture\", \"title\": \"SQLite FTS5\", \"narrative\": \"Use FTS5 for search\", \"impact_score\": 0.8}]\n```"

	principles, err := d.parsePrinciples(response, "test")
	if err != nil {
		t.Fatalf("parsePrinciples failed: %v", err)
	}
	if len(principles) != 1 {
		t.Fatalf("expected 1 principle, got %d", len(principles))
	}
	if principles[0].Title != "SQLite FTS5" {
		t.Errorf("title = %s, want 'SQLite FTS5'", principles[0].Title)
	}
}

func TestParsePrinciplesInvalidJSON(t *testing.T) {
	// Non-array/malformed LLM responses are treated as "no principles" rather
	// than a hard error, so a single bad response doesn't stall the drain
	// loop forever (#38 cause 2: the batch must still get MarkDistilled'd).
	d := &Distiller{}
	principles, err := d.parsePrinciples("not json at all", "test")
	if err != nil {
		t.Fatalf("expected no error for malformed JSON, got: %v", err)
	}
	if principles != nil {
		t.Errorf("expected nil principles for malformed JSON, got: %v", principles)
	}
}

func TestParsePrinciplesNonArrayObject(t *testing.T) {
	d := &Distiller{}
	principles, err := d.parsePrinciples(`{"foo": "bar"}`, "test")
	if err != nil {
		t.Fatalf("expected no error for non-array JSON object, got: %v", err)
	}
	if principles != nil {
		t.Errorf("expected nil principles for non-array JSON object, got: %v", principles)
	}
}

func TestParsePrinciples_FiltersGenericToolAdvice(t *testing.T) {
	d := &Distiller{}
	response := `[
		{"type": "pattern", "title": "Use curl for API calls", "narrative": "curl is helpful for debugging APIs and logs help debugging too", "impact_score": 0.8},
		{"type": "architecture", "title": "ApplicationBuilder base_url fix", "narrative": "Use ApplicationBuilder.base_url() on 0.8 to bypass Cisco Umbrella through the Cloudflare Worker instead of hardcoding endpoints.", "impact_score": 0.9}
	]`

	principles, err := d.parsePrinciples(response, "forge")
	if err != nil {
		t.Fatalf("parsePrinciples failed: %v", err)
	}
	if len(principles) != 1 {
		t.Fatalf("expected only the specific principle to remain, got %d", len(principles))
	}
	if principles[0].Title != "ApplicationBuilder base_url fix" {
		t.Fatalf("unexpected principle kept: %#v", principles[0])
	}
}

func TestBuildPrompt(t *testing.T) {
	d := &Distiller{}
	prompt := d.buildPrompt(nil)
	if prompt == "" {
		t.Error("buildPrompt should not return empty string")
	}
	if !containsStr(prompt, "JSON array") {
		t.Error("prompt should request JSON array")
	}
	if !containsStr(prompt, "Return [] if the evidence is thin") {
		t.Error("prompt should tell the model to return an empty array for low-signal batches")
	}
}

func TestBuildPrompt_StripsHookMetadataFromUserPrompt(t *testing.T) {
	d := &Distiller{}
	prompt := d.buildPrompt([]db.Event{{
		SessionID: "sess-1",
		ProjectID: "proj",
		EventType: "UserPromptSubmit",
		Payload:   `{"session_id":"sess-1","transcript_path":"/tmp/transcript.jsonl","message":"Fix Cisco Umbrella bypass via Cloudflare Worker using ApplicationBuilder.base_url()","cwd":"/tmp/repo"}`,
	}})

	if !containsStr(prompt, "Fix Cisco Umbrella bypass via Cloudflare Worker using ApplicationBuilder.base_url()") {
		t.Fatalf("prompt missing extracted user request: %q", prompt)
	}
	if containsStr(prompt, "transcript.jsonl") {
		t.Fatalf("prompt should not include transcript metadata: %q", prompt)
	}
	if containsStr(prompt, `"session_id"`) {
		t.Fatalf("prompt should not include raw hook JSON: %q", prompt)
	}
}

func TestSummarizeEventPayload_ExtractsLastAssistantMessageFromStopEvent(t *testing.T) {
	e := db.Event{
		EventType: "Stop",
		Payload:   `{"session_id":"sess-1","hook_event_name":"Stop","last_assistant_message":"I fixed the retry logic by wrapping writes in WithMaxRetries(3)."}`,
	}
	got := summarizeEventPayload(e)
	want := "I fixed the retry logic by wrapping writes in WithMaxRetries(3)."
	if got != want {
		t.Fatalf("summarizeEventPayload = %q, want %q", got, want)
	}
}

func TestSummarizeEventPayload_FallsBackToSessionBoundaryWhenAbsent(t *testing.T) {
	cases := []string{
		`{"session_id":"sess-1","hook_event_name":"Stop"}`,
		`{"session_id":"sess-1","hook_event_name":"SessionEnd"}`,
		`not json`,
	}
	for _, payload := range cases {
		e := db.Event{EventType: "Stop", Payload: payload}
		if got := summarizeEventPayload(e); got != "session boundary" {
			t.Fatalf("summarizeEventPayload(%q) = %q, want %q", payload, got, "session boundary")
		}
	}
}

func TestSummarizeEventPayload_TruncatesLongLastAssistantMessage(t *testing.T) {
	long := strings.Repeat("a", 600)
	e := db.Event{
		EventType: "Stop",
		Payload:   fmt.Sprintf(`{"hook_event_name":"Stop","last_assistant_message":%q}`, long),
	}
	got := summarizeEventPayload(e)
	if len(got) != 503 { // 500 chars + "..."
		t.Fatalf("summarizeEventPayload length = %d, want 503 (500 + '...')", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("expected truncated message to end with '...', got %q", got[len(got)-10:])
	}
}

func TestBuildPrompt_IncludesLastAssistantMessageFromStopEvent(t *testing.T) {
	d := &Distiller{}
	prompt := d.buildPrompt([]db.Event{{
		SessionID: "sess-1",
		ProjectID: "proj",
		EventType: "Stop",
		Payload:   `{"hook_event_name":"Stop","last_assistant_message":"Switched to WithMaxRetries(3) because the default pgx transport silently drops retries after a TCP reset."}`,
	}})
	if !containsStr(prompt, "Switched to WithMaxRetries(3) because the default pgx transport silently drops retries after a TCP reset.") {
		t.Fatalf("prompt missing extracted assistant message: %q", prompt)
	}
	if containsStr(prompt, "Stop (): session boundary") {
		t.Fatalf("Stop event should not fall back to session boundary when last_assistant_message is present: %q", prompt)
	}
}

func TestCallOllama(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{
			"response": `[{"type":"bugfix","title":"test","narrative":"test","impact_score":0.5}]`,
		})
	}))
	defer server.Close()

	d := &Distiller{
		config: Config{
			Provider: ProviderOllama,
			Model:    "llama3:latest",
			BaseURL:  server.URL,
			Retries:  1,
		},
		client: server.Client(),
	}

	response, err := d.callOllama("test prompt")
	if err != nil {
		t.Fatalf("callOllama failed: %v", err)
	}
	if response == "" {
		t.Error("expected non-empty response")
	}
}

func TestCallOpenAI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": `[{"type":"bugfix","title":"test","narrative":"test","impact_score":0.5}]`}},
			},
		})
	}))
	defer server.Close()

	d := &Distiller{
		config: Config{
			Provider: ProviderOpenAI,
			Model:    "gpt-4o-mini",
			APIKey:   "sk-test",
			BaseURL:  server.URL,
		},
		client: server.Client(),
	}

	response, err := d.callOpenAI("test prompt")
	if err != nil {
		t.Fatalf("callOpenAI failed: %v", err)
	}
	if response == "" {
		t.Error("expected non-empty response")
	}
}

func TestCallAnthropic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]string{
				{"text": `[{"type":"bugfix","title":"test","narrative":"test","impact_score":0.5}]`},
			},
		})
	}))
	defer server.Close()

	d := &Distiller{
		config: Config{
			Provider: ProviderAnthropic,
			Model:    "claude-3-haiku-20240307",
			APIKey:   "sk-ant-test",
			BaseURL:  server.URL,
		},
		client: server.Client(),
	}

	response, err := d.callAnthropic("test prompt")
	if err != nil {
		t.Fatalf("callAnthropic failed: %v", err)
	}
	if response == "" {
		t.Error("expected non-empty response")
	}
}

func TestCallCodex(t *testing.T) {
	t.Setenv("FORGE_CODEX_CMD", os.Args[0])
	t.Setenv("FORGE_CODEX_ARGS", "-test.run=TestCodexProviderHelperProcess --")
	t.Setenv("GO_WANT_CODEX_HELPER", "1")

	d := &Distiller{
		config: Config{
			Provider: ProviderCodex,
		},
	}

	response, err := d.callCodex("test prompt")
	if err != nil {
		t.Fatalf("callCodex failed: %v", err)
	}
	if response != `{"relevant":true,"confidence":0.91,"hint":"Use the documented API surface instead of calling the missing method directly."}` {
		t.Fatalf("unexpected codex response %q", response)
	}
}

func TestCodexProviderHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_CODEX_HELPER") != "1" {
		return
	}
	for i := 0; i < len(os.Args)-1; i++ {
		if os.Args[i] == "-o" {
			if err := os.WriteFile(os.Args[i+1], []byte(`{"relevant":true,"confidence":0.91,"hint":"Use the documented API surface instead of calling the missing method directly."}`), 0o600); err != nil {
				t.Fatalf("write codex output: %v", err)
			}
			os.Exit(0)
		}
	}
	t.Fatalf("missing -o output flag in args %q", strings.Join(os.Args, " "))
}

func TestLoadConfigGroq(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("FORGE_PROVIDER", "groq")
	t.Setenv("FORGE_API_KEY", "")
	t.Setenv("FORGE_MODEL", "")
	t.Setenv("FORGE_BASE_URL", "")
	t.Setenv("GROQ_API_KEY", "gsk-test-key")

	cfg := LoadConfig()
	if cfg.Provider != ProviderGroq {
		t.Errorf("provider = %s, want %s", cfg.Provider, ProviderGroq)
	}
	if cfg.Model != "llama-3.3-70b-versatile" {
		t.Errorf("model = %s, want llama-3.3-70b-versatile", cfg.Model)
	}
	if cfg.BaseURL != "https://api.groq.com/openai" {
		t.Errorf("base URL = %s, want https://api.groq.com/openai", cfg.BaseURL)
	}
	if cfg.APIKey != "gsk-test-key" {
		t.Errorf("api key = %s, want gsk-test-key (from GROQ_API_KEY)", cfg.APIKey)
	}
}

func TestCallGroq(t *testing.T) {
	var capturedAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": `[{"type":"pattern","title":"Use connection pooling","narrative":"Reuse connections","impact_score":0.7}]`}},
			},
			"usage": map[string]int{
				"prompt_tokens":     120,
				"completion_tokens": 80,
				"total_tokens":      200,
			},
		})
	}))
	defer server.Close()

	d := &Distiller{
		config: Config{
			Provider: ProviderGroq,
			Model:    "llama-3.3-70b-versatile",
			APIKey:   "gsk-test",
			BaseURL:  server.URL,
		},
		client: server.Client(),
	}

	response, err := d.callGroq("test prompt")
	if err != nil {
		t.Fatalf("callGroq failed: %v", err)
	}
	if response == "" {
		t.Error("expected non-empty response")
	}
	if capturedAuth != "Bearer gsk-test" {
		t.Errorf("auth header = %q, want 'Bearer gsk-test'", capturedAuth)
	}
	if d.Usage.PromptTokens != 120 {
		t.Errorf("prompt tokens = %d, want 120", d.Usage.PromptTokens)
	}
	if d.Usage.CompletionTokens != 80 {
		t.Errorf("completion tokens = %d, want 80", d.Usage.CompletionTokens)
	}
	if d.Usage.TotalTokens != 200 {
		t.Errorf("total tokens = %d, want 200", d.Usage.TotalTokens)
	}
}

func TestCallGroqUsageAccumulates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": `[{"type":"pattern","title":"Test","narrative":"Test","impact_score":0.5}]`}},
			},
			"usage": map[string]int{
				"prompt_tokens":     100,
				"completion_tokens": 50,
				"total_tokens":      150,
			},
		})
	}))
	defer server.Close()

	d := &Distiller{
		config: Config{Provider: ProviderGroq, Model: "llama-3.3-70b-versatile", APIKey: "gsk-test", BaseURL: server.URL},
		client: server.Client(),
	}

	d.callGroq("prompt 1")
	d.callGroq("prompt 2")

	if d.Usage.TotalTokens != 300 {
		t.Errorf("total tokens after 2 calls = %d, want 300", d.Usage.TotalTokens)
	}
}

func TestCallGroqRateLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	d := &Distiller{
		config: Config{Provider: ProviderGroq, Model: "llama-3.3-70b-versatile", APIKey: "gsk-test", BaseURL: server.URL},
		client: server.Client(),
	}

	_, err := d.callGroq("test")
	if err == nil {
		t.Error("expected error for 429 rate limit")
	}
	if !errors.Is(err, ErrProviderUnreachable) {
		t.Errorf("expected ErrProviderUnreachable for 429, got %v", err)
	}
}

// TestDistillBatchNoReDistill verifies that events marked distilled=1 are
// never included in a subsequent DistillBatch call.
func TestDistillBatchNoReDistill(t *testing.T) {
	database, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": `[{"type":"pattern","title":"Batch principle","narrative":"From first batch","impact_score":0.6}]`}},
			},
			"usage": map[string]int{"prompt_tokens": 50, "completion_tokens": 30, "total_tokens": 80},
		})
	}))
	defer server.Close()

	d := &Distiller{
		db: database,
		config: Config{
			Provider: ProviderGroq,
			Model:    "llama-3.3-70b-versatile",
			APIKey:   "gsk-test",
			BaseURL:  server.URL,
		},
		client: server.Client(),
	}

	// Insert 5 events and distill them.
	for i := 0; i < 5; i++ {
		database.InsertEvent(&db.Event{
			SessionID: "sess-1", ProjectID: "proj", SourceTool: "claude",
			EventType: "PostToolUse", ToolName: "Edit",
			Payload: `{"file":"main.go"}`,
		})
	}
	if _, err := d.DistillBatch(50); err != nil {
		t.Fatalf("first DistillBatch: %v", err)
	}

	// All 5 events must now be distilled.
	_, undistilled, _ := database.EventCount()
	if undistilled != 0 {
		t.Errorf("undistilled after first batch = %d, want 0", undistilled)
	}

	// Insert 3 new events (below batch minimum of 3 — exactly 3 to trigger).
	for i := 0; i < 3; i++ {
		database.InsertEvent(&db.Event{
			SessionID: "sess-2", ProjectID: "proj", SourceTool: "claude",
			EventType: "PostToolUse", ToolName: "Bash",
			Payload: `{"command":"go test"}`,
		})
	}

	callsBefore := callCount
	if _, err := d.DistillBatch(50); err != nil {
		t.Fatalf("second DistillBatch: %v", err)
	}

	// The second batch should have only processed the 3 new events, not the 5 already-distilled ones.
	if callCount == callsBefore {
		t.Error("expected LLM to be called for second batch")
	}
	_, undistilled, _ = database.EventCount()
	if undistilled != 0 {
		t.Errorf("undistilled after second batch = %d, want 0", undistilled)
	}
	total, _, _ := database.EventCount()
	if total != 8 {
		t.Errorf("total events = %d, want 8", total)
	}
}

// TestDistillBatchShortSessionDoesNotStall verifies that the --all drain
// path (DistillBatchIncludingUnknown) marks a sub-3-event session distilled
// rather than re-selecting it forever, since UndistilledEventsFiltered
// scopes the batch to a single session and can never surface the
// multi-session boundary that used to be required to unblock it (#38 cause
// 1).
func TestDistillBatchShortSessionDoesNotStall(t *testing.T) {
	database, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": `[]`}}},
			"usage":   map[string]int{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
		})
	}))
	defer server.Close()

	d := &Distiller{
		db: database,
		config: Config{
			Provider: ProviderGroq,
			Model:    "llama-3.3-70b-versatile",
			APIKey:   "gsk-test",
			BaseURL:  server.URL,
		},
		client: server.Client(),
	}

	// Only 2 events in the oldest (and only) session — below the
	// distillation minimum of 3.
	for i := 0; i < 2; i++ {
		database.InsertEvent(&db.Event{
			SessionID: "short-sess", ProjectID: "proj", SourceTool: "claude",
			EventType: "PostToolUse", ToolName: "Edit",
			Payload: `{"file":"main.go"}`,
		})
	}

	n, err := d.DistillBatchIncludingUnknown(50)
	if err != nil {
		t.Fatalf("DistillBatchIncludingUnknown: %v", err)
	}
	if n != 0 {
		t.Errorf("principles = %d, want 0", n)
	}
	if callCount != 0 {
		t.Errorf("LLM should not be called for a sub-minimum batch, got %d calls", callCount)
	}

	_, undistilled, _ := database.EventCount()
	if undistilled != 0 {
		t.Errorf("undistilled after batch = %d, want 0 (short session must be marked distilled to avoid stalling the drain)", undistilled)
	}

	// A second call must be a true no-op (0 events left), not a re-select
	// of the same short session — that's the actual stall condition.
	n2, err := d.DistillBatchIncludingUnknown(50)
	if err != nil {
		t.Fatalf("second DistillBatchIncludingUnknown: %v", err)
	}
	if n2 != 0 {
		t.Errorf("second call principles = %d, want 0", n2)
	}
}

// TestDistillBatchShortSessionKeepsQueueForOneShot verifies the plain
// (non-drain) DistillBatch still leaves a sub-3-event session queued so it
// can grow past the minimum on a later run, preserving existing one-shot
// `forge distill` UX (see TestRunDistill_NotEnoughEventsKeepsQueue in cmd/).
func TestDistillBatchShortSessionKeepsQueueForOneShot(t *testing.T) {
	database, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	d := &Distiller{db: database, config: Config{Provider: ProviderGroq, Model: "llama-3.3-70b-versatile", APIKey: "gsk-test"}}

	for i := 0; i < 2; i++ {
		database.InsertEvent(&db.Event{
			SessionID: "short-sess", ProjectID: "proj", SourceTool: "claude",
			EventType: "PostToolUse", ToolName: "Edit",
			Payload: `{"file":"main.go"}`,
		})
	}

	n, err := d.DistillBatch(50)
	if err != nil {
		t.Fatalf("DistillBatch: %v", err)
	}
	if n != 0 {
		t.Errorf("principles = %d, want 0", n)
	}
	_, undistilled, _ := database.EventCount()
	if undistilled != 2 {
		t.Errorf("undistilled after one-shot DistillBatch = %d, want 2 (events must stay queued)", undistilled)
	}
}

// TestDistillBatchPrincipleDedup verifies that the same principle title
// produced by two separate LLM calls is only stored once (fingerprint dedup).
func TestDistillBatchPrincipleDedup(t *testing.T) {
	database, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	// LLM always returns the same principle title regardless of input.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": `[{"type":"pattern","title":"Always use connection pooling","narrative":"Reuse DB connections","impact_score":0.8}]`}},
			},
			"usage": map[string]int{"prompt_tokens": 60, "completion_tokens": 40, "total_tokens": 100},
		})
	}))
	defer server.Close()

	d := &Distiller{
		db: database,
		config: Config{
			Provider: ProviderGroq,
			Model:    "llama-3.3-70b-versatile",
			APIKey:   "gsk-test",
			BaseURL:  server.URL,
		},
		client: server.Client(),
	}

	// First batch.
	for i := 0; i < 5; i++ {
		database.InsertEvent(&db.Event{
			SessionID: "s1", ProjectID: "proj", SourceTool: "claude",
			EventType: "PostToolUse", Payload: `{"action":"query db"}`,
		})
	}
	if _, err := d.DistillBatch(50); err != nil {
		t.Fatalf("first DistillBatch: %v", err)
	}

	// Second batch with fresh events — LLM returns same title again.
	for i := 0; i < 5; i++ {
		database.InsertEvent(&db.Event{
			SessionID: "s2", ProjectID: "proj", SourceTool: "claude",
			EventType: "PostToolUse", Payload: `{"action":"open db connection"}`,
		})
	}
	if _, err := d.DistillBatch(50); err != nil {
		t.Fatalf("second DistillBatch: %v", err)
	}

	count, err := database.PrincipleCount()
	if err != nil {
		t.Fatalf("PrincipleCount: %v", err)
	}
	if count != 1 {
		t.Errorf("principles = %d, want 1 (duplicate title should be ignored by fingerprint)", count)
	}
}

func TestDistillBatch_ScopesToSingleSession(t *testing.T) {
	database, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": `[{"type":"pattern","title":"Session scoped principle","narrative":"A real project-specific principle.","impact_score":0.7}]`}},
			},
			"usage": map[string]int{"prompt_tokens": 40, "completion_tokens": 20, "total_tokens": 60},
		})
	}))
	defer server.Close()

	d := &Distiller{
		db: database,
		config: Config{
			Provider: ProviderGroq,
			Model:    "llama-3.3-70b-versatile",
			APIKey:   "gsk-test",
			BaseURL:  server.URL,
		},
		client: server.Client(),
	}

	for i := 0; i < 3; i++ {
		if err := database.InsertEvent(&db.Event{
			SessionID: "sess-1",
			ProjectID: "proj",
			EventType: "PostToolUse",
			ToolName:  "Edit",
			Payload:   `{"tool_input":{"file_path":"main.go"},"tool_response":"ok"}`,
		}); err != nil {
			t.Fatalf("InsertEvent: %v", err)
		}
	}
	for i := 0; i < 3; i++ {
		if err := database.InsertEvent(&db.Event{
			SessionID: "sess-2",
			ProjectID: "proj",
			EventType: "PostToolUse",
			ToolName:  "Edit",
			Payload:   `{"tool_input":{"file_path":"other.go"},"tool_response":"ok"}`,
		}); err != nil {
			t.Fatalf("InsertEvent: %v", err)
		}
	}

	if _, err := d.DistillBatch(50); err != nil {
		t.Fatalf("first DistillBatch: %v", err)
	}
	if callCount != 1 {
		t.Fatalf("expected one LLM call after first batch, got %d", callCount)
	}
	_, undistilled, _ := database.EventCount()
	if undistilled != 3 {
		t.Fatalf("undistilled after first batch = %d, want 3 remaining from second session", undistilled)
	}

	if _, err := d.DistillBatch(50); err != nil {
		t.Fatalf("second DistillBatch: %v", err)
	}
	if callCount != 2 {
		t.Fatalf("expected second LLM call for remaining session, got %d", callCount)
	}
	_, undistilled, _ = database.EventCount()
	if undistilled != 0 {
		t.Fatalf("undistilled after second batch = %d, want 0", undistilled)
	}
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStrHelper(s, substr))
}

func containsStrHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestUserMessageErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "ErrNoProvider",
			err:  ErrNoProvider,
			want: "No inference provider configured",
		},
		{
			name: "ErrProviderInvalid",
			err:  ErrProviderInvalid,
			want: "Invalid provider or credentials",
		},
		{
			name: "ErrProviderUnreachable",
			err:  ErrProviderUnreachable,
			want: "Cannot reach inference provider",
		},
		{
			name: "ConnectionRefused",
			err:  &testError{"connection refused to localhost:11434"},
			want: "Connection refused",
		},
		{
			name: "AuthError401",
			err:  &testError{"401 Unauthorized"},
			want: "Authentication failed",
		},
		{
			name: "AuthError403",
			err:  &testError{"403 Forbidden"},
			want: "Access forbidden",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := UserMessage(tt.err)
			if !containsStr(msg, tt.want) {
				t.Errorf("UserMessage(%v) = %q, want to contain %q", tt.err, msg, tt.want)
			}
		})
	}
}

type testError struct {
	msg string
}

func (e *testError) Error() string {
	return e.msg
}

func TestCallOllamaUnreachable(t *testing.T) {
	d := &Distiller{
		config: Config{
			Provider: ProviderOllama,
			Model:    "llama3:latest",
			BaseURL:  "http://localhost:19999",
			Retries:  0,
		},
		client: &http.Client{Timeout: 2 * time.Second},
	}

	_, err := d.callOllama("test")
	if err == nil {
		t.Error("expected error for unreachable Ollama")
	}
}

func TestLoadConfigTimeoutFromEnv(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("FORGE_TIMEOUT", "45s")
	t.Setenv("FORGE_RETRIES", "6")
	cfg := LoadConfig()
	if cfg.Timeout != 45*time.Second {
		t.Fatalf("timeout = %s, want 45s", cfg.Timeout)
	}
	if cfg.Retries != 6 {
		t.Fatalf("retries = %d, want 6", cfg.Retries)
	}
}

func TestIsProviderUnreachableError_WindowsConnectex(t *testing.T) {
	err := &testError{msg: `dial tcp 127.0.0.1:1: connectex: No connection could be made because the target machine actively refused it.`}
	if !isProviderUnreachableError(err) {
		t.Fatal("expected Windows connectex refusal to be treated as provider unreachable")
	}
}

func TestCallOpenAIUnauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	d := &Distiller{
		config: Config{
			Provider: ProviderOpenAI,
			Model:    "gpt-4o-mini",
			APIKey:   "invalid",
			BaseURL:  server.URL,
		},
		client: server.Client(),
	}

	_, err := d.callOpenAI("test")
	if err == nil {
		t.Error("expected error for unauthorized OpenAI")
	}
}

func TestCallAnthropicForbidden(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	d := &Distiller{
		config: Config{
			Provider: ProviderAnthropic,
			Model:    "claude-3-haiku-20240307",
			APIKey:   "invalid",
			BaseURL:  server.URL,
		},
		client: server.Client(),
	}

	_, err := d.callAnthropic("test")
	if err == nil {
		t.Error("expected error for forbidden Anthropic")
	}
}

func TestParseConflictPairs_Valid(t *testing.T) {
	response := `[{"a":"id-aaa","b":"id-bbb"},{"a":"id-ccc","b":"id-ddd"}]`
	pairs, err := parseConflictPairs(response)
	if err != nil {
		t.Fatalf("parseConflictPairs: %v", err)
	}
	if len(pairs) != 2 {
		t.Fatalf("len = %d, want 2", len(pairs))
	}
	if pairs[0][0] != "id-aaa" || pairs[0][1] != "id-bbb" {
		t.Errorf("pair[0] = %v, want [id-aaa id-bbb]", pairs[0])
	}
}

func TestParseConflictPairs_Empty(t *testing.T) {
	pairs, err := parseConflictPairs(`[]`)
	if err != nil {
		t.Fatalf("parseConflictPairs: %v", err)
	}
	if len(pairs) != 0 {
		t.Errorf("expected 0 pairs, got %d", len(pairs))
	}
}

func TestParseConflictPairs_StripsMarkdownFence(t *testing.T) {
	response := "```json\n[{\"a\":\"x\",\"b\":\"y\"}]\n```"
	pairs, err := parseConflictPairs(response)
	if err != nil {
		t.Fatalf("parseConflictPairs with fence: %v", err)
	}
	if len(pairs) != 1 {
		t.Fatalf("len = %d, want 1", len(pairs))
	}
}

func TestBuildConflictPrompt_ContainsPrincipleIDs(t *testing.T) {
	newPs := []db.Principle{
		{ID: "new-1", Title: "Avoid Redis for sessions", Narrative: "Redis causes issues."},
	}
	oldPs := []db.Principle{
		{ID: "old-1", Title: "Use Redis for session caching", Narrative: "Always use Redis."},
	}
	prompt := buildConflictPrompt(newPs, oldPs)
	for _, want := range []string{"new-1", "old-1", "EXISTING", "NEW", "JSON"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

func TestDetectAndMarkConflicts_NilDB(t *testing.T) {
	d := &Distiller{db: nil, config: Config{}}
	// Should return nil immediately without panicking.
	if err := d.detectAndMarkConflicts([]db.Principle{{ID: "x"}}); err != nil {
		t.Errorf("expected nil error with nil db, got %v", err)
	}
}

func TestDetectAndMarkConflicts_NoExistingPrinciples(t *testing.T) {
	database, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	// LLM would be called but there are no old principles — should skip without error.
	// Use a Distiller with no real LLM (will error if called).
	d := &Distiller{db: database, config: Config{Provider: ProviderOllama, BaseURL: "http://127.0.0.1:1"}}

	newP := db.Principle{ID: "only-one", Type: "pattern", Title: "Use Redis", Narrative: "Redis is good.", ProjectID: "proj"}
	if _, err := database.InsertPrinciple(&newP); err != nil {
		t.Fatalf("InsertPrinciple: %v", err)
	}

	// No old principles exist — detection should skip the LLM call entirely.
	if err := d.detectAndMarkConflicts([]db.Principle{newP}); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDetectAndMarkConflicts_MarksFromLLMResponse(t *testing.T) {
	database, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	oldP := &db.Principle{
		ID: "old-1", Type: "pattern",
		Title:     "Use optimistic locking for concurrent writes",
		Narrative: "Optimistic locking reduces contention.",
		ProjectID: "proj", ImpactScore: 0.8,
	}
	newP := &db.Principle{
		ID: "new-1", Type: "pattern",
		Title:     "Prefer row-level pessimistic locks under contention",
		Narrative: "Pessimistic locking is safer under high concurrency.",
		ProjectID: "proj", ImpactScore: 0.7,
	}
	for _, p := range []*db.Principle{oldP, newP} {
		if _, err := database.InsertPrinciple(p); err != nil {
			t.Fatalf("InsertPrinciple: %v", err)
		}
	}

	// Stub LLM: returns a conflict pair pointing at old-1 and new-1.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Ollama response shape.
		json.NewEncoder(w).Encode(map[string]string{
			"response": `[{"a":"old-1","b":"new-1"}]`,
		})
	}))
	defer srv.Close()

	d := &Distiller{
		db: database,
		config: Config{
			Provider: ProviderOllama,
			Model:    "llama3",
			BaseURL:  srv.URL,
			Timeout:  5 * time.Second,
		},
		client: srv.Client(),
	}

	if err := d.detectAndMarkConflicts([]db.Principle{*newP}); err != nil {
		t.Fatalf("detectAndMarkConflicts: %v", err)
	}

	for _, id := range []string{"old-1", "new-1"} {
		p, err := database.GetPrincipleByID(id)
		if err != nil || p == nil {
			t.Fatalf("GetPrincipleByID(%s): %v", id, err)
		}
		if p.Status != "conflicting" {
			t.Errorf("principle %s Status = %q, want conflicting", id, p.Status)
		}
	}
}

func TestNormalizeOpenAIBase(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://api.openai.com", "https://api.openai.com"},
		{"https://api.openai.com/", "https://api.openai.com"},
		{"https://api.openai.com/v1", "https://api.openai.com"},
		{"https://api.openai.com/v1/", "https://api.openai.com"},
		{"http://127.0.0.1:11434/v1", "http://127.0.0.1:11434"},
		{"http://127.0.0.1:11434", "http://127.0.0.1:11434"},
		{"http://host:8080/v1//", "http://host:8080"},
		{"", ""},
	}
	for _, c := range cases {
		got := normalizeOpenAIBase(c.in)
		if got != c.want {
			t.Errorf("normalizeOpenAIBase(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCallAntigravity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	binDir := filepath.Join(home, ".gemini", "antigravity", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("failed to create bin dir: %v", err)
	}

	mockAPI := filepath.Join(binDir, "agentapi")
	if runtime.GOOS == "windows" {
		mockAPI += ".exe"
	}

	// Compile a small Go binary to act as the mock agentapi (highly portable and executable on Windows/Unix)
	srcCode := `package main
import "fmt"
func main() {
	fmt.Println("{\"response\": {\"newConversation\": {\"conversationId\": \"mock-conv-456\"}}}")
}
`
	srcFile := filepath.Join(home, "main.go")
	if err := os.WriteFile(srcFile, []byte(srcCode), 0o600); err != nil {
		t.Fatalf("failed to write mock src: %v", err)
	}

	cmd := exec.Command("go", "build", "-o", mockAPI, srcFile)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build mock binary: %v (output: %s)", err, string(out))
	}

	logDir := filepath.Join(home, ".gemini", "antigravity", "brain", "mock-conv-456", ".system_generated", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("failed to create log dir: %v", err)
	}

	transcriptPath := filepath.Join(logDir, "transcript.jsonl")
	transcriptContent := `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","content":"hello"}
{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","content":"Mock response content"}
`
	if err := os.WriteFile(transcriptPath, []byte(transcriptContent), 0o644); err != nil {
		t.Fatalf("failed to write mock transcript: %v", err)
	}

	d := &Distiller{
		config: Config{
			Provider: ProviderAntigravity,
			Model:    "flash",
			Timeout:  2 * time.Second,
		},
	}

	res, err := d.callAntigravity("hello")
	if err != nil {
		t.Fatalf("callAntigravity failed: %v", err)
	}
	if res != "Mock response content" {
		t.Errorf("got %q, want 'Mock response content'", res)
	}
}

// TestDistillBatchIncludingUnknown_DrainsOrphanBacklog reproduces #34: when
// the entire undistilled backlog has session_id='unknown', DistillBatch
// (default) returns (0, nil) with no LLM call — silent no-op. The drain-safe
// DistillBatchIncludingUnknown variant MUST call the LLM on that backlog.
func TestDistillBatchIncludingUnknown_DrainsOrphanBacklog(t *testing.T) {
	database, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": `[]`}},
			},
			"usage": map[string]int{"prompt_tokens": 50, "completion_tokens": 5, "total_tokens": 55},
		})
	}))
	defer server.Close()

	d := &Distiller{
		db: database,
		config: Config{
			Provider: ProviderGroq,
			Model:    "llama-3.3-70b-versatile",
			APIKey:   "gsk-test",
			BaseURL:  server.URL,
		},
		client: server.Client(),
	}

	// Seed 5 orphan events (session_id='unknown').
	for i := 0; i < 5; i++ {
		database.InsertEvent(&db.Event{
			SessionID: "unknown", ProjectID: "proj", SourceTool: "claude",
			EventType: "PostToolUse", ToolName: "Bash",
			Payload: `{"command":"ls"}`,
		})
	}

	// Default path: 0 events returned, 0 LLM calls, 0 principles — matches #34 repro.
	count, err := d.DistillBatch(50)
	if err != nil {
		t.Fatalf("default DistillBatch: %v", err)
	}
	if count != 0 {
		t.Fatalf("default DistillBatch on unknown-only backlog should return 0 principles, got %d", count)
	}
	if callCount != 0 {
		t.Fatalf("default DistillBatch on unknown-only backlog should not call LLM, got %d calls", callCount)
	}

	// Drain-safe variant: must reach the LLM (even though it returns []) and
	// mark events distilled so the backlog actually flushes.
	count, err = d.DistillBatchIncludingUnknown(50)
	if err != nil {
		t.Fatalf("DistillBatchIncludingUnknown: %v", err)
	}
	if callCount != 1 {
		t.Fatalf("DistillBatchIncludingUnknown should call LLM exactly once on orphan backlog, got %d", callCount)
	}
	_, undistilled, _ := database.EventCount()
	if undistilled != 0 {
		t.Fatalf("DistillBatchIncludingUnknown should drain orphan backlog to 0, got %d undistilled", undistilled)
	}
}
