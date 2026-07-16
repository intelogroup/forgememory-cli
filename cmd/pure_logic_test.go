package main

import (
	"testing"
	"time"

	"github.com/forge/forge/internal/config"
)

func TestMin(t *testing.T) {
	cases := []struct{ a, b, want int }{
		{1, 2, 1},
		{2, 1, 1},
		{5, 5, 5},
		{-1, 0, -1},
	}
	for _, tc := range cases {
		if got := min(tc.a, tc.b); got != tc.want {
			t.Errorf("min(%d,%d) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestMax(t *testing.T) {
	cases := []struct{ a, b, want int }{
		{1, 2, 2},
		{2, 1, 2},
		{5, 5, 5},
		{-1, 0, 0},
	}
	for _, tc := range cases {
		if got := max(tc.a, tc.b); got != tc.want {
			t.Errorf("max(%d,%d) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestContains(t *testing.T) {
	slice := []string{"claude", "gemini", "codex"}
	if !contains(slice, "claude") {
		t.Error("expected contains(claude) = true")
	}
	if contains(slice, "gpt") {
		t.Error("expected contains(gpt) = false")
	}
	if contains(nil, "anything") {
		t.Error("expected contains(nil) = false")
	}
}

func TestMaskKey(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"", ""},
		{"short", "***"},
		{"12345678", "***"},
		{"1234567890abcdef", "1234****cdef"},
	}
	for _, tc := range cases {
		got := maskKey(tc.input)
		if got != tc.want {
			t.Errorf("maskKey(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestDefaultModelForProvider(t *testing.T) {
	cases := []struct {
		provider string
		want     string
	}{
		{"anthropic", "claude-haiku-4-5-20251001"},
		{"openai", "gpt-4o"},
		{"groq", "llama-3.3-70b-versatile"},
		{"ollama", "llama3:latest"},
		{"unknown", "claude-haiku-4-5-20251001"},
		{"", "claude-haiku-4-5-20251001"},
	}
	for _, tc := range cases {
		got := defaultModelForProvider(tc.provider)
		if got != tc.want {
			t.Errorf("defaultModelForProvider(%q) = %q, want %q", tc.provider, got, tc.want)
		}
	}
}

func TestNormalizePathLikeString(t *testing.T) {
	got := normalizePathLikeString(`C:\Users\foo\bar`)
	want := `c:/users/foo/bar`
	if got != want {
		t.Errorf("normalizePathLikeString = %q, want %q", got, want)
	}
	got2 := normalizePathLikeString("/home/user/PROJ")
	if got2 != "/home/user/proj" {
		t.Errorf("normalizePathLikeString(unix) = %q", got2)
	}
}

func TestTruncatePure(t *testing.T) {
	cases := []struct {
		input string
		max   int
		want  string
	}{
		{"hello", 10, "hello"},
		{"hello world", 5, "hello..."},
		{"abc", 3, "abc"},
		{"abcd", 3, "abc..."},
	}
	for _, tc := range cases {
		got := truncate(tc.input, tc.max)
		if got != tc.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tc.input, tc.max, got, tc.want)
		}
	}
}

func TestTransientForgeString_String(t *testing.T) {
	// Should detect go-build path
	result := transientForgeString("/tmp/go-build1234/forge")
	if result == "" {
		t.Error("expected transient string for go-build path")
	}
	// Should detect forge-bin- path
	result2 := transientForgeString("/tmp/forge-bin-abc/forge")
	if result2 == "" {
		t.Error("expected transient string for forge-bin- path")
	}
	// Regular path should return empty
	result3 := transientForgeString("/usr/local/bin/forge")
	if result3 != "" {
		t.Errorf("expected empty for normal path, got %q", result3)
	}
}

func TestTransientForgeString_Map(t *testing.T) {
	m := map[string]any{
		"command": "/tmp/go-build9999/exe/forge",
	}
	result := transientForgeString(m)
	if result == "" {
		t.Error("expected transient string from nested map")
	}
}

func TestTransientForgeString_Slice(t *testing.T) {
	s := []any{"/usr/bin/other", "/tmp/go-build0001/cmd"}
	result := transientForgeString(s)
	if result == "" {
		t.Error("expected transient string from slice")
	}
}

func TestCheckProviderModelCompat(t *testing.T) {
	ok := ""         // empty string = no error expected
	const defaultOpenAI = "https://api.openai.com"
	const defaultGroq = "https://api.groq.com/openai"
	const customURL = "http://localhost:8080"

	cases := []struct {
		name     string
		provider string
		model    string
		apiKey   string
		baseURL  string
		wantErr  bool
	}{
		// anthropic: must use claude- models and provide a key
		{"anthropic ok", "anthropic", "claude-haiku-4-5-20251001", "sk-ant-abc", defaultOpenAI, false},
		{"anthropic wrong model gpt", "anthropic", "gpt-4o", "sk-ant-abc", ok, true},
		{"anthropic wrong model llama", "anthropic", "llama3:latest", "sk-ant-abc", ok, true},
		{"anthropic no key", "anthropic", "claude-haiku-4-5-20251001", "", ok, true},
		{"anthropic empty model ok", "anthropic", "", "sk-ant-abc", ok, false},

		// openai: requires api key; wrong models rejected on default base
		{"openai ok", "openai", "gpt-4o", "sk-abc", defaultOpenAI, false},
		{"openai no key", "openai", "gpt-4o", "", defaultOpenAI, true},
		{"openai claude model default base", "openai", "claude-haiku-4-5-20251001", "sk-abc", defaultOpenAI, true},
		{"openai llama model default base", "openai", "llama3:latest", "sk-abc", defaultOpenAI, true},
		{"openai gemini model default base", "openai", "gemini-1.5-pro", "sk-abc", defaultOpenAI, true},
		{"openai claude model custom base (proxy)", "openai", "claude-haiku-4-5-20251001", "sk-abc", customURL, false},
		{"openai empty model ok", "openai", "", "sk-abc", defaultOpenAI, false},

		// groq: requires api key; gpt-/claude- rejected on default base
		{"groq ok", "groq", "llama-3.3-70b-versatile", "gsk-abc", defaultGroq, false},
		{"groq no key", "groq", "llama-3.3-70b-versatile", "", defaultGroq, true},
		{"groq gpt model default base", "groq", "gpt-4o", "gsk-abc", defaultGroq, true},
		{"groq claude model default base", "groq", "claude-haiku-4-5-20251001", "gsk-abc", defaultGroq, true},
		{"groq gemini model default base", "groq", "gemini-2.0-flash", "gsk-abc", defaultGroq, true},
		{"groq gpt model custom base (proxy)", "groq", "gpt-4o", "gsk-abc", customURL, false},

		// ollama: gpt-/claude- always rejected regardless of base URL
		{"ollama ok llama", "ollama", "llama3:latest", "", ok, false},
		{"ollama ok custom name", "ollama", "my-fine-tune:v1", "", ok, false},
		{"ollama gpt model", "ollama", "gpt-4o", "", ok, true},
		{"ollama claude model", "ollama", "claude-haiku-4-5-20251001", "", ok, true},
		{"ollama gemini model", "ollama", "gemini-1.5-flash", "", ok, true},
		{"ollama no key needed", "ollama", "llama3:latest", "", ok, false},

		// antigravity/gemini: supports flash, pro, and any gemini model
		{"antigravity ok", "antigravity", "flash", "", ok, false},
		{"antigravity gemini model ok", "antigravity", "gemini-2.0-flash", "", ok, false},
		{"gemini provider ok", "gemini", "gemini-1.5-pro", "", ok, false},
		{"antigravity wrong model", "antigravity", "gpt-4o", "", ok, true},
		{"openai gemini model custom base (proxy) allowed", "openai", "gemini-1.5-pro", "sk-abc", customURL, false},
		{"openrouter ok gemini", "openrouter", "google/gemini-2.5-pro", "sk-or-v1-abc", "", false},
		{"openrouter no key", "openrouter", "google/gemini-2.5-pro", "", "", true},

		// codex: no constraints checked
		{"codex no constraints", "codex", "any-model", "", ok, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := config.CheckProviderModelCompat(tc.provider, tc.model, tc.apiKey, tc.baseURL)
			if tc.wantErr && err == nil {
				t.Errorf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestDistillBackoff(t *testing.T) {
	// 0 failures and 1 failure → no backoff (recover quickly from a transient
	// blip). 2+ failures → exponential 1m, 2m, 4m, 8m, 16m, capped at 30m.
	cases := []struct {
		failures int
		want     time.Duration
	}{
		{0, 0},
		{1, 0},
		{2, 1 * time.Minute},
		{3, 2 * time.Minute},
		{4, 4 * time.Minute},
		{5, 8 * time.Minute},
		{6, 16 * time.Minute},
		{7, 30 * time.Minute},
		{20, 30 * time.Minute},
		{1000, 30 * time.Minute},
	}
	for _, c := range cases {
		got := distillBackoff(c.failures)
		if got != c.want {
			t.Errorf("distillBackoff(%d) = %v, want %v", c.failures, got, c.want)
		}
	}
}
