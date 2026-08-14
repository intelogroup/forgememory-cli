package db

import (
	"path/filepath"
	"testing"
)

func TestEvaluationArtifactRoundTripAndScopedList(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "artifacts.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	artifact := &EvaluationArtifact{
		ID: "artifact-1", TraceID: "trace-1", TaskID: "task-1",
		Kind: "diff", MediaType: "text/x-diff", Path: "sha256/ab/hash",
		SHA256: "hash", ByteSize: 42, Metadata: `{"source":"git"}`,
	}
	if err := database.InsertEvaluationArtifact(artifact); err != nil {
		t.Fatalf("InsertEvaluationArtifact: %v", err)
	}
	if err := database.InsertEvaluationArtifact(artifact); err != nil {
		t.Fatalf("retry InsertEvaluationArtifact: %v", err)
	}
	loaded, err := database.EvaluationArtifactByID(artifact.ID)
	if err != nil || loaded == nil || loaded.TraceID != "trace-1" || loaded.ByteSize != 42 {
		t.Fatalf("EvaluationArtifactByID = %#v, err=%v", loaded, err)
	}
	listed, err := database.EvaluationArtifacts("trace-1", "", 10)
	if err != nil || len(listed) != 1 || listed[0].ID != artifact.ID {
		t.Fatalf("EvaluationArtifacts = %#v, err=%v", listed, err)
	}
}
