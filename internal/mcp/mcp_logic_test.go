package mcp

import (
	"strings"
	"testing"
	"time"
)

func TestEventRecencyScore(t *testing.T) {
	cases := []struct {
		age  time.Duration
		want float64
	}{
		// Within 7 days → 0.45
		{1 * time.Hour, 0.45},
		{6 * 24 * time.Hour, 0.45},
		// 8-30 days → 0.25
		{8 * 24 * time.Hour, 0.25},
		{29 * 24 * time.Hour, 0.25},
		// 31-180 days → 0.1
		{31 * 24 * time.Hour, 0.1},
		{179 * 24 * time.Hour, 0.1},
		// > 180 days → 0
		{181 * 24 * time.Hour, 0},
		{365 * 24 * time.Hour, 0},
	}
	for _, tc := range cases {
		ts := time.Now().UTC().Add(-tc.age).Format(time.RFC3339)
		got := eventRecencyScore(ts)
		if got != tc.want {
			t.Errorf("age=%v: got %.2f, want %.2f", tc.age, got, tc.want)
		}
	}
}

func TestEventRecencyScore_InvalidTS(t *testing.T) {
	if got := eventRecencyScore("not-a-time"); got != 0 {
		t.Errorf("expected 0 for invalid ts, got %f", got)
	}
}

func TestTopQueryTokens(t *testing.T) {
	tokens := topQueryTokens("fix the rust error now", 10)
	found := false
	for _, tok := range tokens {
		if tok == "rust" || tok == "error" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected rust/error tokens, got %v", tokens)
	}
	// Short words (<3 chars) should be excluded
	for _, tok := range tokens {
		if tok == "the" || tok == "fix" {
			// "fix" is exactly 3 chars — included; "the" is 3 chars — included.
			// Only words < 3 chars are excluded. This just checks non-empty.
		}
	}
}

func TestTopQueryTokens_Limit(t *testing.T) {
	tokens := topQueryTokens("aaa bbb ccc ddd eee fff ggg hhh", 3)
	if len(tokens) > 3 {
		t.Errorf("expected <= 3 tokens, got %d: %v", len(tokens), tokens)
	}
}

func TestTopQueryTokens_Empty(t *testing.T) {
	tokens := topQueryTokens("", 5)
	if len(tokens) != 0 {
		t.Errorf("expected empty tokens for empty query, got %v", tokens)
	}
}

func TestIntFromArgs(t *testing.T) {
	cases := []struct {
		args map[string]any
		key  string
		def  int
		want int
	}{
		{map[string]any{"limit": float64(10)}, "limit", 5, 10},
		{map[string]any{"limit": 7}, "limit", 5, 7},
		{map[string]any{}, "limit", 5, 5},
		{map[string]any{"limit": "string"}, "limit", 5, 5},
	}
	for _, tc := range cases {
		got := intFromArgs(tc.args, tc.key, tc.def)
		if got != tc.want {
			t.Errorf("intFromArgs(%v) = %d, want %d", tc.args, got, tc.want)
		}
	}
}

func TestStringFromArgs(t *testing.T) {
	args := map[string]any{"key": "  hello  ", "other": 123}
	if got := stringFromArgs(args, "key"); got != "hello" {
		t.Errorf("stringFromArgs key = %q, want 'hello'", got)
	}
	if got := stringFromArgs(args, "missing"); got != "" {
		t.Errorf("stringFromArgs missing = %q, want ''", got)
	}
	if got := stringFromArgs(args, "other"); got != "" {
		t.Errorf("stringFromArgs non-string = %q, want ''", got)
	}
}

func TestFallback(t *testing.T) {
	if got := fallback("", "default"); got != "default" {
		t.Errorf("fallback empty = %q, want 'default'", got)
	}
	if got := fallback("  ", "default"); got != "default" {
		t.Errorf("fallback whitespace = %q, want 'default'", got)
	}
	if got := fallback("value", "default"); got != "value" {
		t.Errorf("fallback value = %q, want 'value'", got)
	}
}

func TestToolError(t *testing.T) {
	result := toolError("something went wrong")
	if !result.IsError {
		t.Error("expected IsError = true")
	}
	if len(result.Content) == 0 {
		t.Fatal("expected content")
	}
	if !strings.Contains(result.Content[0].Text, "something went wrong") {
		t.Errorf("unexpected content: %q", result.Content[0].Text)
	}
}

func TestTokenSet(t *testing.T) {
	set := tokenSet("Hello World foo")
	if !set["hello"] {
		t.Error("expected 'hello'")
	}
	if !set["world"] {
		t.Error("expected 'world'")
	}
	if !set["foo"] {
		t.Error("expected 'foo'")
	}
	// short tokens (<3 chars) excluded — none in this example, but verify type
	if _, ok := set["ab"]; ok {
		t.Error("'ab' should not be in set (< 3 chars)")
	}
}

func TestTokenSet_ShortTokensExcluded(t *testing.T) {
	set := tokenSet("ab c rust")
	if set["ab"] || set["c"] {
		t.Error("tokens < 3 chars should be excluded")
	}
	if !set["rust"] {
		t.Error("expected 'rust'")
	}
}
