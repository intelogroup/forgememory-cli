package recall

import (
	"strings"
	"testing"

	"github.com/forge/forge/internal/db"
)

func TestRenderStatesModeAndReason(t *testing.T) {
	out := Render(Response{Mode: ModeFTSOnly, Reason: "embedding unavailable: refused"})
	if !strings.Contains(out, "fts-only (embedding unavailable: refused)") || !strings.Contains(out, "No matching") {
		t.Fatalf("out = %q", out)
	}
}

func TestRenderLabelsTitleOnlyResults(t *testing.T) {
	out := Render(Response{Mode: ModeHybrid, Results: []Result{
		{RecallDoc: db.RecallDoc{Kind: "principle", ID: "1", Title: "Locked", Partial: true, TS: "2026-10-21T22:00:00Z", ProjectID: "a"}, Similarity: 0.5},
		{RecallDoc: db.RecallDoc{Kind: "note", ID: "2", Title: "Saved note", Body: strings.Repeat("x", maxBodyChars+50)}},
	}})
	if !strings.Contains(out, "could not be verified") || !strings.Contains(out, "2026-10-21 | a | similarity 0.50") {
		t.Fatalf("out = %q", out)
	}
	if strings.Contains(out, strings.Repeat("x", maxBodyChars+1)) || !strings.Contains(out, "...") {
		t.Fatal("long body not clipped")
	}
}

func TestReadablePrinciplesSkipsPartialAndOtherKinds(t *testing.T) {
	got := ReadablePrinciples(Response{Results: []Result{
		{RecallDoc: db.RecallDoc{Kind: db.RecallKindPrinciple, ID: "full"}},
		{RecallDoc: db.RecallDoc{Kind: db.RecallKindPrinciple, ID: "locked", Partial: true}},
		{RecallDoc: db.RecallDoc{Kind: db.RecallKindSummary, ID: "s"}},
	}})
	if len(got) != 1 || got[0].ID != "full" {
		t.Fatalf("got %+v", got)
	}
}
