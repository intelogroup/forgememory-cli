package main

import (
	"os"
	"testing"

	"github.com/forge/forge/internal/coach"
	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/knowledgegap"
	"github.com/forge/forge/internal/skills"
)

func TestQueueKnowledgeGapsDryRun(t *testing.T) {
	tmp, _ := os.CreateTemp(t.TempDir(), "forge-kg-*.db")
	tmp.Close()
	database, err := db.Open(tmp.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	result := knowledgegap.Result{Gaps: []knowledgegap.Gap{
		{Topic: "Broad exception catching", Evidence: "caught generic Exception 4x", ConcreteLesson: "catch specific types", StudyPointer: "docs"},
	}}

	n1, err := queueKnowledgeGaps(database, "proj1", result, coach.ModeNormal)
	if err != nil {
		t.Fatal(err)
	}
	if n1 != 0 {
		t.Fatalf("expected 0 queued after first observation, got %d", n1)
	}

	n2, err := queueKnowledgeGaps(database, "proj1", result, coach.ModeNormal)
	if err != nil {
		t.Fatal(err)
	}
	if n2 != 1 {
		t.Fatalf("expected 1 queued after second observation crosses suspected_gap, got %d", n2)
	}

	state, err := database.GetSkillState(knowledgegap.GapSkillKey("Broad exception catching"), skills.ScopeProject, "proj1")
	if err != nil {
		t.Fatal(err)
	}
	if state == nil || state.State != skills.StateSuspectedGap {
		t.Fatalf("expected suspected_gap state, got %+v", state)
	}

	items, err := database.ListCoachingItems("proj1", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 coaching item, got %d", len(items))
	}
	if items[0].Lesson != "catch specific types" {
		t.Fatalf("unexpected lesson: %q", items[0].Lesson)
	}
}
