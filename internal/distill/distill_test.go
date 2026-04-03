package distill

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLoadConfigDefaults(t *testing.T) {
	// Clear env vars
	t.Setenv("FORGE_PROVIDER", "")
	t.Setenv("FORGE_API_KEY", "")
	t.Setenv("FORGE_MODEL", "")
	t.Setenv("FORGE_BASE_URL", "")

	cfg := LoadConfig()
	if cfg.Provider != ProviderOllama {
		t.Errorf("default provider = %s, want %s", cfg.Provider, ProviderOllama)
	}
	if cfg.Model != "llama3.2" {
		t.Errorf("default model = %s, want llama3.2", cfg.Model)
	}
	if cfg.BaseURL != "http://localhost:11434" {
		t.Errorf("default base URL = %s, want http://localhost:11434", cfg.BaseURL)
	}
}

func TestLoadConfigOpenAI(t *testing.T) {
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
			Model:    "llama3.2",
			BaseURL:  server.URL,
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
