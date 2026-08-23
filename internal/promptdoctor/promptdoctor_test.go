package promptdoctor

import (
	"testing"
	"time"
)

func mkPrompt(text string, offsetSec int) Prompt {
	return Prompt{Text: text, TS: time.Unix(0, 0).Add(time.Duration(offsetSec) * time.Second)}
}

func TestGroupChains(t *testing.T) {
	prompts := []Prompt{
		mkPrompt("fix the bug", 0),
		mkPrompt("fix the bug now", 30),         // within 90s -> same chain
		mkPrompt("unrelated later prompt", 500), // >90s gap -> new chain
	}
	chains := GroupChains("sess1", prompts)
	if len(chains) != 2 {
		t.Fatalf("expected 2 chains, got %d", len(chains))
	}
	if len(chains[0].Prompts) != 2 {
		t.Fatalf("expected first chain to have 2 prompts, got %d", len(chains[0].Prompts))
	}
	if len(chains[1].Prompts) != 1 {
		t.Fatalf("expected second chain to have 1 prompt, got %d", len(chains[1].Prompts))
	}
}

func TestDetectFindingsRephraseLoop(t *testing.T) {
	c := Chain{SessionID: "sess1", Prompts: []Prompt{
		mkPrompt("please make the login flow work correctly", 0),
		mkPrompt("please make the login flow work correctly now", 10),
	}}
	findings := DetectFindings("chain1", c)
	if len(findings) != 1 || findings[0].AntiPattern != "rephrase_loop" {
		t.Fatalf("expected 1 rephrase_loop finding, got %+v", findings)
	}
}

func TestDetectFindingsDegenerate(t *testing.T) {
	c := Chain{SessionID: "sess1", Prompts: []Prompt{
		mkPrompt("yes", 0),
	}}
	findings := DetectFindings("chain1", c)
	if len(findings) != 1 || findings[0].AntiPattern != "degenerate" {
		t.Fatalf("expected 1 degenerate finding, got %+v", findings)
	}
}

func TestDetectFindingsVagueVerb(t *testing.T) {
	c := Chain{SessionID: "sess1", Prompts: []Prompt{
		mkPrompt("verify everything works before we ship", 0),
	}}
	findings := DetectFindings("chain1", c)
	if len(findings) != 1 || findings[0].AntiPattern != "vague_verb" {
		t.Fatalf("expected 1 vague_verb finding, got %+v", findings)
	}
}

func TestDetectFindingsVagueVerbWithArtifactIsClean(t *testing.T) {
	c := Chain{SessionID: "sess1", Prompts: []Prompt{
		mkPrompt("verify internal/db/events.go handles the nil case, should return err", 0),
	}}
	findings := DetectFindings("chain1", c)
	if len(findings) != 0 {
		t.Fatalf("expected no findings, got %+v", findings)
	}
}

func TestParseResultValidArray(t *testing.T) {
	resp := `[{"chain_id":"c1","anti_pattern":"degenerate","severity":"low","original_prompt":"yes","fixed_prompt":"apply the fix from the plan above","missing_scarf_field":"scope"}]`
	r, err := ParseResult(resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.Fixes) != 1 || r.Fixes[0].ChainID != "c1" {
		t.Fatalf("unexpected result: %+v", r)
	}
}

func TestParseResultEmptyArray(t *testing.T) {
	r, err := ParseResult("[]")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.Fixes) != 0 {
		t.Fatalf("expected 0 fixes, got %d", len(r.Fixes))
	}
}

func TestParseResultMalformed(t *testing.T) {
	if _, err := ParseResult("not json at all"); err == nil {
		t.Fatal("expected error for malformed response")
	}
}

func TestLevenshteinRatioIdentical(t *testing.T) {
	if r := levenshteinRatio("same text", "same text"); r != 1 {
		t.Fatalf("expected ratio 1, got %f", r)
	}
}
