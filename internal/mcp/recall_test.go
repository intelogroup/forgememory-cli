package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/embed"
	"github.com/forge/forge/internal/recall"
)

// fakeOllamaServer puts text about speech on one axis and everything else on
// another, so a query can match a document it shares no words with.
func fakeOllamaServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		out := make([][]float32, len(body.Input))
		for i, text := range body.Input {
			lower := strings.ToLower(text)
			if strings.Contains(lower, "barge") || strings.Contains(lower, "talking") {
				out[i] = []float32{1, 0}
			} else {
				out[i] = []float32{0, 1}
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": out})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func seedRecall(t *testing.T, database *db.DB, url string) (target *db.Principle) {
	t.Helper()
	target = &db.Principle{Title: "Barge-in guard", Narrative: "the full narrative text", ProjectID: "project-alpha", Type: "pattern", ImpactScore: 0.5}
	other := &db.Principle{Title: "Pooling", Narrative: "reuse connections", ProjectID: "project-beta", Type: "pattern", ImpactScore: 0.5}
	for _, p := range []*db.Principle{target, other} {
		if ok, err := database.InsertPrinciple(p); err != nil || !ok {
			t.Fatalf("insert: ok=%v err=%v", ok, err)
		}
	}
	client, err := embed.New(url, embed.DefaultModel, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recall.IndexBatch(context.Background(), database, client, 10); err != nil {
		t.Fatalf("IndexBatch: %v", err)
	}
	return target
}

func TestRecallToolFindsMeaningAndReturnsFullText(t *testing.T) {
	database := openTestDB(t)
	server := New(database)
	srv := fakeOllamaServer(t)
	t.Setenv("FORGE_EMBED_URL", srv.URL)
	target := seedRecall(t, database, srv.URL)

	text := callToolText(t, server, "recall", map[string]any{
		"query":      "user interrupts while it is talking",
		"project_id": "project-alpha",
		"limit":      1,
	})
	if !strings.Contains(text, "Mode: hybrid") || !strings.Contains(text, "the full narrative text") {
		t.Fatalf("text = %q", text)
	}
	got, err := database.GetPrincipleByID(target.ID)
	if err != nil || got.UseCount != 1 {
		t.Fatalf("use count = %+v, err=%v, want 1", got, err)
	}
}

func TestRecallToolScopesToProjectUnlessAskedForAll(t *testing.T) {
	database := openTestDB(t)
	server := New(database)
	srv := fakeOllamaServer(t)
	t.Setenv("FORGE_EMBED_URL", srv.URL)
	seedRecall(t, database, srv.URL)

	scoped := callToolText(t, server, "recall", map[string]any{"query": "connections pooling", "project_id": "project-alpha"})
	if strings.Contains(scoped, "reuse connections") {
		t.Fatalf("another project's row leaked: %q", scoped)
	}
	all := callToolText(t, server, "recall", map[string]any{"query": "connections pooling", "project_id": "*"})
	if !strings.Contains(all, "reuse connections") || !strings.Contains(all, "(all projects)") {
		t.Fatalf("explicit all search = %q", all)
	}
}

func TestRecallToolFallsBackWhenModelIsDown(t *testing.T) {
	database := openTestDB(t)
	server := New(database)
	srv := fakeOllamaServer(t)
	seedRecall(t, database, srv.URL)
	t.Setenv("FORGE_EMBED_URL", "http://127.0.0.1:1")

	text := callToolText(t, server, "recall", map[string]any{"query": "barge guard", "project_id": "project-alpha"})
	if !strings.Contains(text, "Mode: fts-only") || !strings.Contains(text, "embedding unavailable") {
		t.Fatalf("missing fallback notice: %q", text)
	}
	if !strings.Contains(text, "the full narrative text") {
		t.Fatalf("keyword result missing: %q", text)
	}
}

func TestRecallToolValidatesInput(t *testing.T) {
	server := New(openTestDB(t))
	for name, args := range map[string]map[string]any{
		"missing": {},
		"blank":   {"query": "   "},
		"long":    {"query": strings.Repeat("x", 1001)},
	} {
		if result := server.recallMemories(args); !result.IsError {
			t.Errorf("%s: accepted invalid query: %+v", name, result)
		}
	}
}
