package profile

import (
	"strings"
	"testing"
)

func TestParseScores_Clean(t *testing.T) {
	resp := `{"steering":{"score":7,"why":"a"},"execution":{"score":8,"why":"b"},"engineering":{"score":6,"why":"c"},"product_instinct":{"score":9,"why":"d"},"planning":{"score":5,"why":"e"}}`
	s, err := ParseScores(resp)
	if err != nil {
		t.Fatal(err)
	}
	if s.Steering.Score != 7 || s.Planning.Why != "e" {
		t.Errorf("unexpected scores: %+v", s)
	}
}

func TestParseScores_FencedWithProse(t *testing.T) {
	resp := "Here you go:\n```json\n" +
		`{"steering":{"score":3,"why":"a"},"execution":{"score":3,"why":"b"},"engineering":{"score":3,"why":"c"},"product_instinct":{"score":3,"why":"d"},"planning":{"score":3,"why":"e"}}` +
		"\n```\nHope that helps!"
	s, err := ParseScores(resp)
	if err != nil {
		t.Fatal(err)
	}
	if s.Execution.Score != 3 {
		t.Errorf("expected score 3, got %+v", s)
	}
}

func TestParseScores_NoJSON(t *testing.T) {
	if _, err := ParseScores("sorry, I can't help with that"); err == nil {
		t.Fatal("expected error for missing JSON")
	}
}

func TestBuildPrompt_IncludesEvidence(t *testing.T) {
	p := BuildPrompt(Signals{ProjectID: "demo", TotalCommits: 5, TotalPrompts: 10, SteeredPrompts: 2})
	if !strings.Contains(p, "5 commits") || !strings.Contains(p, "2/10 prompts") {
		t.Errorf("prompt missing evidence: %s", p)
	}
}
