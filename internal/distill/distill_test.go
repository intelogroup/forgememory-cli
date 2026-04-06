package distill

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
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

	cfg := LoadConfig()
	if cfg.Provider != ProviderForgememo {
		t.Errorf("default provider = %s, want %s", cfg.Provider, ProviderForgememo)
	}
	if cfg.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("default model = %s, want claude-haiku-4-5-20251001", cfg.Model)
	}
	if cfg.BaseURL == "" {
		t.Errorf("default base URL should be set")
	}
	if cfg.Timeout != 30*time.Second {
		t.Errorf("default timeout = %s, want 30s", cfg.Timeout)
	}
	if cfg.Retries != 3 {
		t.Errorf("default retries = %d, want 3", cfg.Retries)
	}
}

func TestLoadConfigOpenAI(t *testing.T) {
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
	d := &Distiller{}
	_, err := d.parsePrinciples("not json at all", "test")
	if err == nil {
		t.Error("expected error for invalid JSON")
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
