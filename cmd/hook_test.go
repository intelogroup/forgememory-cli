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
