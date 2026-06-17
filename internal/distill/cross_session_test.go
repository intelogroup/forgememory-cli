package distill

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/forge/forge/internal/db"
)

func crossSessionMockServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		response := `[{"pattern":"Repeated cargo failures on Windows","pattern_type":"recurring_failure","confidence":0.85,"evidence_session_indices":[0,1,2]}]`
		json.NewEncoder(w).Encode(map[string]string{"response": response})
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestParseCrossSessionPatterns_ValidArray(t *testing.T) {
	d := &Distiller{}
	summaries := []db.SessionSummary{
		{SessionID: "s0"},
		{SessionID: "s1"},
		{SessionID: "s2"},
	}
	response := `[{"pattern":"Rust borrow errors","pattern_type":"recurring_failure","confidence":0.9,"evidence_session_indices":[0,2]}]`
	got, err := d.parseCrossSessionPatterns(response, "my-proj", summaries)
	if err != nil {
		t.Fatalf("parseCrossSessionPatterns: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].ProjectID != "my-proj" {
		t.Fatalf("project = %q", got[0].ProjectID)
	}
	var ids []string
	if err := json.Unmarshal([]byte(got[0].EvidenceSessionIDs), &ids); err != nil {
		t.Fatalf("evidence JSON: %v", err)
	}
	if len(ids) != 2 || ids[0] != "s0" || ids[1] != "s2" {
		t.Fatalf("evidence session IDs = %v, want [s0 s2]", ids)
	}
}

func TestParseCrossSessionPatterns_InvalidType_DefaultsWorkflow(t *testing.T) {
	d := &Distiller{}
	summaries := []db.SessionSummary{{SessionID: "s0"}}
	response := `[{"pattern":"Uses grep before edit","pattern_type":"not_a_real_type","confidence":0.5,"evidence_session_indices":[0]}]`
	got, err := d.parseCrossSessionPatterns(response, "p", summaries)
	if err != nil {
		t.Fatalf("parseCrossSessionPatterns: %v", err)
	}
	if len(got) != 1 || got[0].PatternType != "workflow" {
		t.Fatalf("PatternType = %q, want workflow", got[0].PatternType)
	}
}

func TestParseCrossSessionPatterns_SkipsEmptyPattern(t *testing.T) {
	d := &Distiller{}
	summaries := []db.SessionSummary{{SessionID: "s0"}}
	response := `[{"pattern":"","pattern_type":"workflow","confidence":0.5,"evidence_session_indices":[0]},{"pattern":"valid","pattern_type":"workflow","confidence":0.6,"evidence_session_indices":[0]}]`
	got, err := d.parseCrossSessionPatterns(response, "p", summaries)
	if err != nil {
		t.Fatalf("parseCrossSessionPatterns: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (empty pattern skipped)", len(got))
	}
}

func TestSynthesizeCrossSession_TooFewSummaries(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	database, err := db.Open("")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	d := New(database, LoadConfig())
	n, err := d.SynthesizeCrossSession("proj", 5)
	if err != nil {
		t.Fatalf("SynthesizeCrossSession: %v", err)
	}
	if n != 0 {
		t.Fatalf("count = %d, want 0 with no summaries", n)
	}
}

func TestSynthesizeCrossSession_InsertsPatterns(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv, calls := crossSessionMockServer(t)
	t.Setenv("FORGE_PROVIDER", "ollama")
	t.Setenv("FORGE_BASE_URL", srv.URL)
	t.Setenv("FORGE_API_KEY", "")
	t.Setenv("FORGE_MODEL", "test")

	database, err := db.Open("")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	for i := 0; i < 5; i++ {
		if err := database.InsertSessionSummary(&db.SessionSummary{
			SessionID: fmt.Sprintf("sess-%d", i),
			ProjectID: "cross-proj",
			Summary:   "worked on feature",
			Learnings: "learned something",
		}); err != nil {
			t.Fatalf("InsertSessionSummary: %v", err)
		}
	}

	d := New(database, LoadConfig())
	n, err := d.SynthesizeCrossSession("cross-proj", 5)
	if err != nil {
		t.Fatalf("SynthesizeCrossSession: %v", err)
	}
	if n < 1 {
		t.Fatalf("inserted = %d, want >= 1", n)
	}
	if calls.Load() != 1 {
		t.Fatalf("LLM calls = %d, want 1", calls.Load())
	}
	count, err := database.CrossSessionPatternCount()
	if err != nil {
		t.Fatalf("CrossSessionPatternCount: %v", err)
	}
	if count < 1 {
		t.Fatal("expected patterns in DB")
	}
}
