package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forge/forge/internal/db"
)

type stubEmbedder struct {
	err   error
	calls int
}

func (s *stubEmbedder) Model() string { return "stub" }
func (s *stubEmbedder) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{1, 0}
	}
	return out, nil
}
func (s *stubEmbedder) EmbedQuery(context.Context, string) ([]float32, error) {
	return []float32{1, 0}, nil
}

func recallTestDB(t *testing.T, principles int) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "recall.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	for i := 0; i < principles; i++ {
		p := &db.Principle{Title: "lesson " + string(rune('a'+i)), Narrative: "n", ProjectID: "a", Type: "pattern", ImpactScore: 0.5}
		if ok, err := d.InsertPrinciple(p); err != nil || !ok {
			t.Fatalf("insert: ok=%v err=%v", ok, err)
		}
	}
	return d
}

func TestRunEmbedBatchesDrainsBacklogThenLeavesModelAlone(t *testing.T) {
	d := recallTestDB(t, 3)
	stub := &stubEmbedder{}
	stop := make(chan struct{})
	n, err := runEmbedBatches(d, stub, stop)
	if err != nil || n != 3 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	calls := stub.calls
	if n, err := runEmbedBatches(d, stub, stop); err != nil || n != 0 {
		t.Fatalf("second run n=%d err=%v", n, err)
	}
	if stub.calls != calls {
		t.Fatal("idle run called the model")
	}
}

func TestRunEmbedBatchesStopsOnRequest(t *testing.T) {
	d := recallTestDB(t, 2)
	stub := &stubEmbedder{}
	stop := make(chan struct{})
	close(stop)
	if n, err := runEmbedBatches(d, stub, stop); n != 0 || err != nil || stub.calls != 0 {
		t.Fatalf("n=%d err=%v calls=%d", n, err, stub.calls)
	}
}

func TestRunEmbedBatchesReportsServerDown(t *testing.T) {
	d := recallTestDB(t, 1)
	if _, err := runEmbedBatches(d, &stubEmbedder{err: errors.New("connection refused")}, make(chan struct{})); err == nil {
		t.Fatal("failure swallowed")
	}
}

func TestEmbedErrorMessageHintsAtMissingModel(t *testing.T) {
	if got := embedErrorMessage(nil, "m"); got != "" {
		t.Fatalf("nil error = %q", got)
	}
	got := embedErrorMessage(errors.New(`model "m" not found`), "m")
	if !strings.Contains(got, "ollama pull m") {
		t.Fatalf("got %q", got)
	}
}
