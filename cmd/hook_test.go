package main

import (
	"strings"
	"testing"

	"github.com/forge/forge/internal/db"
)

func TestBuildSessionRecallOutput_ProjectSummaryAndPrinciple(t *testing.T) {
	summaries := []db.SessionSummary{
		{ProjectID: "forgememory-cli", Learnings: "Codex was only capturing startup and stop hooks.", NextSteps: "Add PostToolUse for Codex."},
		{ProjectID: "forgememory-cli", Summary: "Prompt recall should be scoped to the current repo."},
	}
	principles := []db.Principle{
		{ProjectID: "forgememory-cli", Narrative: "Normalize project IDs to the repo basename before storing events and principles."},
	}

	out := buildSessionRecallOutput("forgememory-cli", summaries, principles)

	if !strings.Contains(out, "## Startup Context") {
		t.Fatalf("missing startup heading: %q", out)
	}
	if !strings.Contains(out, "Recent lessons for forgememory-cli") {
		t.Fatalf("missing project-scoped summary: %q", out)
	}
	if !strings.Contains(out, "Active principle: Normalize project IDs to the repo basename before storing events and principles.") {
		t.Fatalf("missing active principle sentence: %q", out)
	}
	if strings.Contains(out, "\n-") {
		t.Fatalf("startup context should be compact prose, got: %q", out)
	}
}

func TestBuildSessionRecallOutput_NextStepFallback(t *testing.T) {
	summaries := []db.SessionSummary{
		{ProjectID: "forgememory-cli", Learnings: "Session synthesis is landing successfully.", NextSteps: "Verify the next Codex session writes summaries."},
	}

	out := buildSessionRecallOutput("forgememory-cli", summaries, nil)

	if !strings.Contains(out, "Next step: Verify the next Codex session writes summaries.") {
		t.Fatalf("expected next-step fallback, got: %q", out)
	}
}

func TestLoadSessionRecallContext_FallsBackToGlobalRecentContext(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	database, err := db.Open("")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	if err := database.InsertPrinciple(&db.Principle{
		ProjectID:   "forgememory-cli",
		Title:       "Cross-project fallback",
		Narrative:   "Use recent global context when project-specific recall is empty.",
		Type:        "pattern",
		ImpactScore: 0.6,
	}); err != nil {
		t.Fatalf("InsertPrinciple: %v", err)
	}
	if err := database.InsertSessionSummary(&db.SessionSummary{
		SessionID: "s1",
		ProjectID: "forgememory-cli",
		Learnings: "Recent summaries should still surface when project IDs do not match.",
	}); err != nil {
		t.Fatalf("InsertSessionSummary: %v", err)
	}

	principles, summaries := loadSessionRecallContext(database, "proj")
	if len(principles) == 0 {
		t.Fatal("expected global principle fallback")
	}
	if len(summaries) == 0 {
		t.Fatal("expected global session summary fallback")
	}
	if principles[0].ProjectID != "forgememory-cli" {
		t.Fatalf("expected fallback principle project_id to be forgememory-cli, got %q", principles[0].ProjectID)
	}
	if summaries[0].ProjectID != "forgememory-cli" {
		t.Fatalf("expected fallback summary project_id to be forgememory-cli, got %q", summaries[0].ProjectID)
	}

	out := buildSessionRecallOutput("proj", summaries, principles)
	if !strings.Contains(out, "Recent lessons for proj") {
		t.Fatalf("expected output to stay scoped to requested project id, got %q", out)
	}
	if !strings.Contains(out, "Active principle: Use recent global context when project-specific recall is empty.") {
		t.Fatalf("expected fallback principle narrative in output, got %q", out)
	}
}
