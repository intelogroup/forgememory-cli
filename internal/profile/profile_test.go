package profile

import (
	"strings"
	"testing"

	"github.com/forge/forge/internal/db"
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
	p := BuildPrompt(Signals{ProjectID: "demo", TotalCommits: 5, TotalPrompts: 10, SteeredPrompts: 2, AvgFilesPerCommit: 3.2, TestPairedCommitPct: 40})
	if !strings.Contains(p, "5 commits") || !strings.Contains(p, "2/10 prompts") {
		t.Errorf("prompt missing evidence: %s", p)
	}
	if !strings.Contains(p, "3.2 files/commit") || !strings.Contains(p, "40%") {
		t.Errorf("prompt missing hygiene evidence: %s", p)
	}
}

func TestCommitHygiene(t *testing.T) {
	paths := [][]string{
		{"foo.go", "foo_test.go"},    // paired
		{"bar.go", "baz.go"},         // not paired, 2 files
		{"internal/tests/helper.go"}, // test dir path, but no code file -> not paired
	}
	avg, pct := CommitHygiene(paths)
	if avg != 5.0/3.0 {
		t.Errorf("avgFiles = %v, want %v", avg, 5.0/3.0)
	}
	if pct != 100.0/3.0 {
		t.Errorf("testPairedPct = %v, want %v", pct, 100.0/3.0)
	}
}

func TestCommitHygiene_Empty(t *testing.T) {
	avg, pct := CommitHygiene(nil)
	if avg != 0 || pct != 0 {
		t.Errorf("expected zeros for empty input, got %v %v", avg, pct)
	}
}

func TestClassifyEvents(t *testing.T) {
	events := []db.Event{
		{EventType: "PostToolUse", ToolName: "Read", Payload: ""},
		{EventType: "PostToolUse", ToolName: "Bash", Payload: "go test ./..."},
		{EventType: "PostToolUse", ToolName: "Edit", Payload: ""},
		{EventType: "UserPromptSubmit", ToolName: "", Payload: "run go test"}, // wrong event type, ignored
	}
	counts, hasTestRun := ClassifyEvents(events)
	if counts["Read"] != 1 || counts["Bash"] != 1 || counts["Edit"] != 1 {
		t.Errorf("unexpected tool counts: %+v", counts)
	}
	if !hasTestRun {
		t.Error("expected hasTestRun=true from Bash payload containing 'go test'")
	}
}

func TestClassifyEvents_NoTestRun(t *testing.T) {
	events := []db.Event{{EventType: "PostToolUse", ToolName: "Edit", Payload: "changed foo.go"}}
	_, hasTestRun := ClassifyEvents(events)
	if hasTestRun {
		t.Error("expected hasTestRun=false, no test command present")
	}
}
