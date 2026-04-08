package main

import (
	"testing"
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
		{"forgememo", "claude-haiku-4-5-20251001"},
		{"forge", "claude-haiku-4-5-20251001"},
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
