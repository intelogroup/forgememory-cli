// Package recall finds memories by meaning and by keyword, then merges the two
// rankings. Keywords win when a query shares words with the memory, vectors win
// when it does not; reciprocal rank fusion keeps both without tuning weights.
package recall

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/embed"
)

const (
	// Search modes reported to the caller.
	ModeHybrid  = "hybrid"
	ModeFTSOnly = "fts-only"

	// DefaultLimit is how many principles a search returns unless asked otherwise.
	DefaultLimit = 10

	candidatesPerRanking = 50
	rrfK                 = 60
	keywordWeight        = 0.25
)

// Embedder is the part of embed.Client recall needs.
type Embedder interface {
	Model() string
	EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error)
	EmbedQuery(ctx context.Context, query string) ([]float32, error)
}

// Result is one recalled document. Similarity is 0 when only keywords found it.
type Result struct {
	db.RecallDoc
	Similarity float32
}

// Response carries the results and how they were produced. Reason explains why
// vectors were not used when Mode is ModeFTSOnly.
type Response struct {
	Mode    string
	Reason  string
	Results []Result
}

// IndexBatch embeds up to batch backlog documents and returns how many it stored.
func IndexBatch(ctx context.Context, d *db.DB, e Embedder, batch int) (int, error) {
	docs, err := d.EmbeddingBacklog(e.Model(), batch)
	if err != nil {
		return 0, fmt.Errorf("read backlog: %w", err)
	}
	if len(docs) == 0 {
		return 0, nil
	}
	texts := make([]string, len(docs))
	for i, doc := range docs {
		texts[i] = doc.Text
	}
	// The model call stays outside any DB transaction: daemon and hooks share
	// one SQLite writer and must not wait on a model load.
	vecs, err := e.EmbedDocuments(ctx, texts)
	if err != nil {
		return 0, err
	}
	if len(vecs) != len(docs) {
		return 0, fmt.Errorf("got %d vectors for %d documents", len(vecs), len(docs))
	}
	items := make([]db.Embedding, len(docs))
	for i, doc := range docs {
		items[i] = db.Embedding{Kind: doc.Kind, ID: doc.ID, Model: e.Model(), Vec: vecs[i], Partial: doc.Partial}
	}
	if err := d.SaveEmbeddings(items); err != nil {
		return 0, fmt.Errorf("save embeddings: %w", err)
	}
	return len(items), nil
}

// SearchFromEnv is Search with the embedder configured by the environment. A
// refused or invalid embed server degrades to keyword search, with the reason
// reported, rather than failing recall.
func SearchFromEnv(ctx context.Context, d *db.DB, query string, scope db.RecallScope, limit int) (Response, error) {
	client, err := embed.FromEnv()
	if err != nil {
		resp, searchErr := Search(ctx, d, nil, query, scope, limit)
		resp.Reason = err.Error()
		return resp, searchErr
	}
	return Search(ctx, d, client, query, scope, limit)
}

// Search returns up to limit documents visible in scope. Any embedder failure
// falls back to keyword results: an unavailable model must never block recall.
func Search(ctx context.Context, d *db.DB, e Embedder, query string, scope db.RecallScope, limit int) (Response, error) {
	if query == "" {
		return Response{}, errors.New("empty query")
	}
	if limit <= 0 {
		limit = DefaultLimit
	}
	keyword, err := d.RecallFTS(query, scope, candidatesPerRanking)
	if err != nil {
		return Response{}, fmt.Errorf("keyword search: %w", err)
	}

	resp := Response{Mode: ModeFTSOnly}
	var semantic []scored
	if e == nil {
		resp.Reason = "no embedder configured"
	} else {
		semantic, resp.Reason, err = vectorRanking(ctx, d, e, query, scope)
		if err != nil {
			return Response{}, err
		}
		if resp.Reason == "" {
			resp.Mode = ModeHybrid
		}
	}

	similarity := make(map[db.RecallKey]float32, len(semantic))
	vectorKeys := make(map[string][]db.RecallKey)
	for _, item := range semantic {
		vectorKeys[item.key.Kind] = append(vectorKeys[item.key.Kind], item.key)
		similarity[item.key] = item.score
	}
	keywordKeys := make(map[string][]db.RecallKey)
	for _, key := range keyword {
		keywordKeys[key.Kind] = append(keywordKeys[key.Kind], key)
	}

	var merged []db.RecallKey
	for _, group := range resultGroups(limit) {
		ranked := fuse(vectorKeys[group.kind], keywordKeys[group.kind])
		if len(ranked) > group.slots {
			ranked = ranked[:group.slots]
		}
		merged = append(merged, ranked...)
	}
	docs, err := d.RecallDocs(merged)
	if err != nil {
		return Response{}, fmt.Errorf("load documents: %w", err)
	}
	resp.Results = make([]Result, len(docs))
	for i, doc := range docs {
		resp.Results[i] = Result{RecallDoc: doc, Similarity: similarity[db.RecallKey{Kind: doc.Kind, ID: doc.ID}]}
	}
	return resp, nil
}

type resultGroup struct {
	kind  string
	slots int
}

// resultGroups gives each kind its own slots: limit principles, plus half that
// many summaries and notes. Long summaries outrank short principles on raw
// similarity, so one shared ranking buried the distilled lessons in testing.
func resultGroups(limit int) []resultGroup {
	secondary := (limit + 1) / 2
	return []resultGroup{
		{db.RecallKindPrinciple, limit},
		{db.RecallKindSummary, secondary},
		{db.RecallKindNote, secondary},
	}
}

// vectorRanking returns a non-empty reason, not an error, for the failures that
// should fall back to keywords. Only a database failure is a real error.
func vectorRanking(ctx context.Context, d *db.DB, e Embedder, query string, scope db.RecallScope) ([]scored, string, error) {
	qctx, cancel := context.WithTimeout(ctx, embed.QueryTimeout)
	defer cancel()
	qv, err := e.EmbedQuery(qctx, query)
	if err != nil {
		return nil, "embedding unavailable: " + err.Error(), nil
	}
	rows, err := d.LoadVectors(e.Model(), scope)
	if err != nil {
		return nil, "", fmt.Errorf("load vectors: %w", err)
	}
	if len(rows) == 0 {
		return nil, "no vectors indexed yet for " + e.Model(), nil
	}
	return topKPerKind(qv, rows, candidatesPerRanking), "", nil
}

type scored struct {
	key   db.RecallKey
	score float32
}

type minHeap []scored

func (h minHeap) Len() int           { return len(h) }
func (h minHeap) Less(i, j int) bool { return h[i].score < h[j].score }
func (h minHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *minHeap) Push(x any)        { *h = append(*h, x.(scored)) }
func (h *minHeap) Pop() any {
	old := *h
	last := old[len(old)-1]
	*h = old[:len(old)-1]
	return last
}

// topK scans every vector. A scan of 50k vectors took 30ms in testing, far more
// than this corpus holds, so an approximate index would add nothing.
func topK(q []float32, rows []db.VectorRow, k int) []scored {
	h := &minHeap{}
	for _, r := range rows {
		// A vector of another size came from a different model configuration.
		if len(r.Vec) != len(q) {
			continue
		}
		var s float32
		for i, x := range q {
			s += x * r.Vec[i]
		}
		if h.Len() < k {
			heap.Push(h, scored{db.RecallKey{Kind: r.Kind, ID: r.ID}, s})
		} else if s > (*h)[0].score {
			(*h)[0] = scored{db.RecallKey{Kind: r.Kind, ID: r.ID}, s}
			heap.Fix(h, 0)
		}
	}
	out := []scored(*h)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].key.ID < out[j].key.ID
	})
	return out
}

// topKPerKind keeps k candidates of each kind, so a kind with shorter documents
// isn't squeezed out before fusion sees it.
func topKPerKind(q []float32, rows []db.VectorRow, k int) []scored {
	split := map[string][]db.VectorRow{}
	for _, r := range rows {
		split[r.Kind] = append(split[r.Kind], r)
	}
	var out []scored
	for _, kindRows := range split {
		out = append(out, topK(q, kindRows, k)...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].key.ID < out[j].key.ID
	})
	return out
}

// fuse merges a vector and a keyword ranking by reciprocal rank fusion. Keywords
// count for less: matching any one query word is weak evidence, and at equal
// weight that noise pushed reworded matches out of the results in testing.
func fuse(vector, keyword []db.RecallKey) []db.RecallKey {
	scores := map[db.RecallKey]float64{}
	var order []db.RecallKey
	add := func(ranking []db.RecallKey, weight float64) {
		for rank, key := range ranking {
			if _, seen := scores[key]; !seen {
				order = append(order, key)
			}
			scores[key] += weight / float64(rrfK+rank+1)
		}
	}
	add(vector, 1)
	add(keyword, keywordWeight)
	sort.SliceStable(order, func(i, j int) bool { return scores[order[i]] > scores[order[j]] })
	return order
}
