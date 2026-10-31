package recall

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/forge/forge/internal/db"
)

// fakeEmbedder maps any text containing a keyword to that keyword's axis, so a
// query can match a document it shares no literal words with.
type fakeEmbedder struct {
	axes     map[string]int
	dim      int
	docErr   error
	queryErr error
	block    bool
	seen     []string
}

func (f *fakeEmbedder) Model() string { return "fake" }

func (f *fakeEmbedder) vector(text string) []float32 {
	v := make([]float32, f.dim)
	for word, axis := range f.axes {
		if strings.Contains(strings.ToLower(text), word) {
			v[axis] = 1
			return v
		}
	}
	v[f.dim-1] = 1
	return v
}

func (f *fakeEmbedder) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	f.seen = append(f.seen, texts...)
	if f.docErr != nil {
		return nil, f.docErr
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = f.vector(t)
	}
	return out, nil
}

func (f *fakeEmbedder) EmbedQuery(ctx context.Context, q string) ([]float32, error) {
	if f.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if f.queryErr != nil {
		return nil, f.queryErr
	}
	return f.vector(q), nil
}

func openDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "recall.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func addPrinciple(t *testing.T, d *db.DB, title, narrative, project string) *db.Principle {
	t.Helper()
	p := &db.Principle{Title: title, Narrative: narrative, ProjectID: project, Type: "pattern", ImpactScore: 0.5, Scope: "global"}
	if inserted, err := d.InsertPrinciple(p); err != nil || !inserted {
		t.Fatalf("InsertPrinciple(%s): inserted=%v err=%v", title, inserted, err)
	}
	return p
}

func drain(t *testing.T, d *db.DB, e Embedder) int {
	t.Helper()
	total := 0
	for {
		n, err := IndexBatch(context.Background(), d, e, 32)
		if err != nil {
			t.Fatalf("IndexBatch: %v", err)
		}
		if n == 0 {
			return total
		}
		total += n
	}
}

var all = db.RecallScope{All: true}

func TestSearchFindsMeaningWithNoSharedWords(t *testing.T) {
	d := openDB(t)
	e := &fakeEmbedder{dim: 3, axes: map[string]int{"barge": 0, "interrupt": 0, "database": 1}}
	target := addPrinciple(t, d, "Barge-in guard", "stop speech playback", "a")
	addPrinciple(t, d, "Database pooling", "reuse connections", "a")
	if n := drain(t, d, e); n != 2 {
		t.Fatalf("indexed %d, want 2", n)
	}

	resp, err := Search(context.Background(), d, e, "user interrupts assistant", all, 1)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Mode != ModeHybrid || resp.Reason != "" {
		t.Fatalf("mode=%s reason=%q", resp.Mode, resp.Reason)
	}
	if len(resp.Results) == 0 || resp.Results[0].ID != target.ID {
		t.Fatalf("results = %+v, want the barge-in principle", resp.Results)
	}
	if resp.Results[0].Body != "stop speech playback" || resp.Results[0].Similarity < 0.99 {
		t.Fatalf("result = %+v, want full text and similarity", resp.Results[0])
	}
}

func TestFusionPrefersDocumentsFoundByBothRankings(t *testing.T) {
	a := db.RecallKey{Kind: "principle", ID: "a"}
	b := db.RecallKey{Kind: "principle", ID: "b"}
	c := db.RecallKey{Kind: "principle", ID: "c"}
	got := fuse([]db.RecallKey{a, b}, []db.RecallKey{c, b})
	if got[0] != b {
		t.Fatalf("top = %v, want the document in both rankings", got[0])
	}
	if len(got) != 3 {
		t.Fatalf("got %d documents, want 3 with no duplicates", len(got))
	}
}

func TestFusionWeightsVectorsAboveAnyWordMatch(t *testing.T) {
	v := db.RecallKey{Kind: "principle", ID: "vector"}
	k := db.RecallKey{Kind: "principle", ID: "keyword"}
	if got := fuse([]db.RecallKey{v}, []db.RecallKey{k}); got[0] != v {
		t.Fatalf("top = %v, want the vector match", got[0])
	}
}

// Short principles must not lose every slot to long summaries.
func TestSearchGivesEachKindItsOwnSlots(t *testing.T) {
	d := openDB(t)
	e := &fakeEmbedder{dim: 2, axes: map[string]int{"topic": 0}}
	for i := 0; i < 6; i++ {
		if err := d.InsertSessionSummary(&db.SessionSummary{ProjectID: "a", SessionID: "s", Summary: "topic topic topic"}); err != nil {
			t.Fatal(err)
		}
	}
	p := addPrinciple(t, d, "Unrelated", "body", "a")
	drain(t, d, e)

	resp, err := Search(context.Background(), d, e, "topic", all, 2)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, r := range resp.Results {
		counts[r.Kind]++
	}
	if counts[db.RecallKindSummary] != 1 {
		t.Fatalf("summaries = %d, want 1 slot at limit 2: %+v", counts[db.RecallKindSummary], resp.Results)
	}
	if len(resp.Results) == 0 || resp.Results[0].ID != p.ID {
		t.Fatalf("results = %+v, want principles listed first", resp.Results)
	}
}

func TestSearchFallsBackToKeywordsWhenEmbedderFails(t *testing.T) {
	d := openDB(t)
	p := addPrinciple(t, d, "Keychain overwrite", "never mint on failed read", "a")
	cases := map[string]Embedder{
		"nil":   nil,
		"error": &fakeEmbedder{dim: 2, queryErr: errors.New("connection refused")},
	}
	for name, e := range cases {
		t.Run(name, func(t *testing.T) {
			resp, err := Search(context.Background(), d, e, "keychain", all, 5)
			if err != nil {
				t.Fatalf("Search returned error, want fallback: %v", err)
			}
			if resp.Mode != ModeFTSOnly || resp.Reason == "" {
				t.Fatalf("mode=%s reason=%q", resp.Mode, resp.Reason)
			}
			if len(resp.Results) != 1 || resp.Results[0].ID != p.ID {
				t.Fatalf("results = %+v", resp.Results)
			}
		})
	}

	t.Run("hung embedder respects the deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		start := time.Now()
		resp, err := Search(ctx, d, &fakeEmbedder{dim: 2, block: true}, "keychain", all, 5)
		if err != nil || resp.Mode != ModeFTSOnly || len(resp.Results) != 1 {
			t.Fatalf("resp=%+v err=%v", resp, err)
		}
		if time.Since(start) > 2*time.Second {
			t.Fatalf("took %v", time.Since(start))
		}
	})

	t.Run("no vectors indexed yet", func(t *testing.T) {
		resp, err := Search(context.Background(), d, &fakeEmbedder{dim: 2}, "keychain", all, 5)
		if err != nil || resp.Mode != ModeFTSOnly || !strings.Contains(resp.Reason, "no vectors") {
			t.Fatalf("resp=%+v err=%v", resp, err)
		}
	})
}

func TestSearchFromEnvDegradesWhenServerIsRefused(t *testing.T) {
	d := openDB(t)
	addPrinciple(t, d, "Keychain overwrite", "body", "a")
	t.Setenv("FORGE_EMBED_URL", "http://example.com")
	t.Setenv("FORGE_EMBED_ALLOW_REMOTE", "")
	resp, err := SearchFromEnv(context.Background(), d, "keychain", all, 5)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Mode != ModeFTSOnly || !strings.Contains(resp.Reason, "non-loopback") || len(resp.Results) != 1 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestSearchIgnoresVectorsOfAnotherSize(t *testing.T) {
	rows := []db.VectorRow{
		{Kind: "principle", ID: "good", Vec: []float32{1, 0}},
		{Kind: "principle", ID: "old-model", Vec: []float32{1, 0, 0}},
	}
	got := topK([]float32{1, 0}, rows, 5)
	if len(got) != 1 || got[0].key.ID != "good" {
		t.Fatalf("topK = %+v", got)
	}
}

func TestTopKKeepsBestScoresInOrder(t *testing.T) {
	rows := []db.VectorRow{
		{Kind: "p", ID: "low", Vec: []float32{0.1}},
		{Kind: "p", ID: "high", Vec: []float32{0.9}},
		{Kind: "p", ID: "mid", Vec: []float32{0.5}},
		{Kind: "p", ID: "lowest", Vec: []float32{-0.5}},
	}
	got := topK([]float32{1}, rows, 2)
	if len(got) != 2 || got[0].key.ID != "high" || got[1].key.ID != "mid" {
		t.Fatalf("topK = %+v", got)
	}
}

func TestSearchRespectsScope(t *testing.T) {
	d := openDB(t)
	e := &fakeEmbedder{dim: 2, axes: map[string]int{"lesson": 0}}
	local := &db.Principle{Title: "foreign lesson", Narrative: "n", ProjectID: "other", Type: "pattern", ImpactScore: 0.5}
	if _, err := d.InsertPrinciple(local); err != nil {
		t.Fatal(err)
	}
	drain(t, d, e)

	resp, err := Search(context.Background(), d, e, "lesson", db.RecallScope{ProjectID: "mine"}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 0 {
		t.Fatalf("project-scoped row leaked: %+v", resp.Results)
	}
	resp, err = Search(context.Background(), d, e, "lesson", all, 5)
	if err != nil || len(resp.Results) != 1 {
		t.Fatalf("explicit all: resp=%+v err=%v", resp, err)
	}
}

func TestSearchRejectsEmptyQuery(t *testing.T) {
	if _, err := Search(context.Background(), openDB(t), nil, "", all, 5); err == nil {
		t.Fatal("empty query accepted")
	}
}

func TestIndexBatchFailureKeepsBacklog(t *testing.T) {
	d := openDB(t)
	addPrinciple(t, d, "One", "body", "a")
	fail := &fakeEmbedder{dim: 2, docErr: errors.New("model not found")}
	if _, err := IndexBatch(context.Background(), d, fail, 10); err == nil {
		t.Fatal("embedder failure swallowed")
	}
	docs, err := d.EmbeddingBacklog("fake", 10)
	if err != nil || len(docs) != 1 {
		t.Fatalf("backlog after failure = %d, err=%v", len(docs), err)
	}
	if n := drain(t, d, &fakeEmbedder{dim: 2}); n != 1 {
		t.Fatalf("retry indexed %d, want 1", n)
	}
}

// A bad server reply must not store mismatched vectors against documents.
type shortEmbedder struct{ fakeEmbedder }

func (s *shortEmbedder) EmbedDocuments(context.Context, []string) ([][]float32, error) {
	return [][]float32{}, nil
}

func TestIndexBatchRejectsVectorCountMismatch(t *testing.T) {
	d := openDB(t)
	addPrinciple(t, d, "One", "body", "a")
	if _, err := IndexBatch(context.Background(), d, &shortEmbedder{fakeEmbedder{dim: 2}}, 10); err == nil {
		t.Fatal("vector count mismatch accepted")
	}
}
