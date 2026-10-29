package main

import (
	"strings"
	"testing"

	"github.com/forge/forge/internal/db"
)

func TestParseResponse_ValidJSON(t *testing.T) {
	resp := `[{"operation":"UPDATE","original_id":"a","new_id":"a","title":"T","reason":"r","scope":"project","evidence":[]}]`
	results, err := parseResponse(resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Operation != "UPDATE" {
		t.Errorf("expected operation UPDATE, got %s", results[0].Operation)
	}
}

func TestParseResponse_CodeBlock(t *testing.T) {
	resp := "```json\n[{\"operation\":\"NONE\"}]\n```"
	results, err := parseResponse(resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}

func TestParseResponse_NoArray(t *testing.T) {
	resp := "Some random text with no JSON"
	_, err := parseResponse(resp)
	if err == nil {
		t.Fatal("expected error for missing array")
	}
}

func TestNewOpID(t *testing.T) {
	id1 := newOpID()
	id2 := newOpID()
	if id1 == id2 {
		t.Errorf("op IDs should be unique: %q", id1)
	}
	if !strings.HasPrefix(id1, "op-") {
		t.Errorf("expected op- prefix: %q", id1)
	}
	body := strings.TrimPrefix(id1, "op-")
	if strings.HasPrefix(body, "op-") {
		t.Errorf("duplicated op- prefix: %q", id1)
	}
	if len(body) != 36 {
		t.Errorf("expected a UUID body, got %q", id1)
	}
}

func TestTruncStr(t *testing.T) {
	if truncateStr("hello", 10) != "hello" {
		t.Errorf("unexpected truncation")
	}
	if truncateStr("hello world", 3) != "hel..." {
		t.Errorf("unexpected truncation result: %q", truncateStr("hello world", 3))
	}
}

func TestBuildPrompt_IncludesBatchAndRules(t *testing.T) {
	ctx := &consolidationCtx{}
	batch := []db.Principle{{
		ID:          "p1",
		Title:       "Use transactions",
		Narrative:   "wrap multi-row writes",
		ProjectID:   "proj-a",
		Type:        "pattern",
		ImpactScore: 0.7,
	}}
	prompt := ctx.buildPrompt(batch, `[{"id":"p1"}]`)
	for _, want := range []string{
		"Use transactions",
		"wrap multi-row writes",
		"Cross-project consolidation is allowed only when evidence from both projects",
		"Return JSON only",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}
