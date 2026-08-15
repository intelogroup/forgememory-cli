package db

import (
	"path/filepath"
	"testing"
)

func TestEvaluationTaskAndTraceEvaluationRoundTrip(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "eval.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	task := &EvaluationTask{
		ID:             "task-1",
		ProjectID:      "project-1",
		Name:           "fix compile failure",
		Prompt:         "Fix the failing build",
		BaselineCommit: "abc123",
		ExpectedTests:  "go test ./...",
		RubricVersion:  "2026-08-1",
		Metadata:       `{"split":"validation"}`,
	}
	if err := database.UpsertEvaluationTask(task); err != nil {
		t.Fatalf("UpsertEvaluationTask: %v", err)
	}
	loadedTask, err := database.EvaluationTaskByID(task.ID)
	if err != nil {
		t.Fatalf("EvaluationTaskByID: %v", err)
	}
	if loadedTask == nil || loadedTask.Prompt != task.Prompt || loadedTask.RubricVersion != task.RubricVersion {
		t.Fatalf("loaded task = %#v, want %#v", loadedTask, task)
	}

	if err := database.InsertEvent(&Event{ID: "event-1", TraceID: "trace-1", TaskID: task.ID, SessionID: "session-1", ProjectID: "project-1", EventType: "PostToolUse", Payload: "failure"}); err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}
	if err := database.InsertTraceEvaluation(&TraceEvaluation{
		TraceID:         "trace-1",
		TaskID:          task.ID,
		Evaluator:       "grader-v1",
		RubricVersion:   task.RubricVersion,
		TaskSuccess:     false,
		Score:           0.25,
		FailureCategory: "test_failure",
		Rationale:       "The agent changed the wrong package.",
		EvidenceSpans:   `["span-1"]`,
	}); err != nil {
		t.Fatalf("InsertTraceEvaluation: %v", err)
	}

	events, err := database.TraceEvents("trace-1", 10)
	if err != nil || len(events) != 1 || events[0].TaskID != task.ID {
		t.Fatalf("TraceEvents = %#v, err=%v", events, err)
	}
	evaluations, err := database.TraceEvaluations("trace-1")
	if err != nil || len(evaluations) != 1 || evaluations[0].TaskSuccess || evaluations[0].FailureCategory != "test_failure" {
		t.Fatalf("TraceEvaluations = %#v, err=%v", evaluations, err)
	}
	report, err := database.EvaluationReport(task.ID)
	if err != nil || len(report) != 1 || report[0].Runs != 1 || report[0].SuccessRate != 0 || report[0].Failures["test_failure"] != 1 {
		t.Fatalf("EvaluationReport = %#v, err=%v", report, err)
	}
}
