package recall

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/embed"
)

// TestRecallQualityOnRealData is the release gate for recall quality. It needs a
// live model and a real memory store, so it runs only when pointed at a copy:
//
//	FORGE_RECALL_EVAL_DB=/path/copy.db FORGE_RECALL_EVAL_GOLD=/path/gold.json go test ./internal/recall -run RecallQuality -v
//
// The gold file is a JSON list of {"query": ..., "title": ...}, where query
// rewords the principle with that title. It describes private memories, so it
// lives outside the repo. Setting FORGE_RECALL_EVAL_SCALE=N adds synthetic rows
// up to N vectors for the latency check, which is why this must be a copy.
func TestRecallQualityOnRealData(t *testing.T) {
	path, goldPath := os.Getenv("FORGE_RECALL_EVAL_DB"), os.Getenv("FORGE_RECALL_EVAL_GOLD")
	if path == "" || goldPath == "" {
		t.Skip("set FORGE_RECALL_EVAL_DB and FORGE_RECALL_EVAL_GOLD to run")
	}
	raw, err := os.ReadFile(goldPath)
	if err != nil {
		t.Fatal(err)
	}
	var gold []struct{ Query, Title string }
	if err := json.Unmarshal(raw, &gold); err != nil {
		t.Fatal(err)
	}
	if len(gold) < 30 {
		t.Fatalf("gold set has %d queries, want at least 30", len(gold))
	}

	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	client, err := embed.FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if n := drain(t, d, client); n > 0 {
		t.Logf("indexed %d documents", n)
	}
	embedded, total, partial, err := d.EmbeddingCoverage(client.Model())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("coverage %d/%d, %d title-only", embedded, total, partial)

	scope := db.RecallScope{All: true}
	targets := func(title string) map[string]bool {
		rows, err := d.Conn().Query(`SELECT id FROM principles WHERE title = ?`, title)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]bool{}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			out[id] = true
		}
		if len(out) == 0 {
			t.Fatalf("no principle titled %q", title)
		}
		return out
	}
	rankOf := func(keys []db.RecallKey, ids map[string]bool) int {
		for i, k := range keys {
			if k.Kind == db.RecallKindPrinciple && ids[k.ID] {
				return i + 1
			}
		}
		return 0
	}
	type tally struct{ at5, at10 int }
	score := func(tl *tally, rank int) {
		if rank > 0 && rank <= 5 {
			tl.at5++
		}
		if rank > 0 && rank <= 10 {
			tl.at10++
		}
	}
	evaluate := func(query string, ids map[string]bool, kw, vec, hybrid *tally) (int, int, int) {
		keyword, err := d.RecallFTS(query, scope, candidatesPerRanking)
		if err != nil {
			t.Fatal(err)
		}
		semantic, reason, err := vectorRanking(ctx, d, client, query, scope)
		if err != nil || reason != "" {
			t.Fatalf("vector ranking: err=%v reason=%q", err, reason)
		}
		vectorKeys := make([]db.RecallKey, len(semantic))
		for i, s := range semantic {
			vectorKeys[i] = s.key
		}
		rk, rv, rh := rankOf(onlyPrinciples(keyword), ids), rankOf(onlyPrinciples(vectorKeys), ids), rankOf(fuse(onlyPrinciples(vectorKeys), onlyPrinciples(keyword)), ids)
		score(kw, rk)
		score(vec, rv)
		score(hybrid, rh)
		return rk, rv, rh
	}

	var kw, vec, hybrid, kwWords, vecWords, hybridWords tally
	for _, g := range gold {
		ids := targets(g.Title)
		rk, rv, rh := evaluate(g.Query, ids, &kw, &vec, &hybrid)
		t.Logf("%-70.70s fts=%-3d vec=%-3d hybrid=%d", g.Query, rk, rv, rh)
		evaluate(g.Title, ids, &kwWords, &vecWords, &hybridWords)
	}
	n := len(gold)
	t.Logf("all columns rank among principles only")
	t.Logf("reworded  recall@5  fts %d vector %d hybrid %d of %d", kw.at5, vec.at5, hybrid.at5, n)
	t.Logf("reworded  recall@10 fts %d vector %d hybrid %d of %d", kw.at10, vec.at10, hybrid.at10, n)
	t.Logf("same word recall@10 fts %d vector %d hybrid %d of %d", kwWords.at10, vecWords.at10, hybridWords.at10, n)

	if hybrid.at10*100 < n*80 {
		t.Errorf("reworded recall@10 = %d/%d, want at least 80%%", hybrid.at10, n)
	}
	if hybrid.at10 < kw.at10*2 {
		t.Errorf("reworded recall@10 hybrid %d, want at least double FTS %d", hybrid.at10, kw.at10)
	}
	if hybridWords.at10 < kwWords.at10-1 {
		t.Errorf("same-word recall@10 hybrid %d, want within 1 of FTS %d", hybridWords.at10, kwWords.at10)
	}

	if want, _ := strconv.Atoi(os.Getenv("FORGE_RECALL_EVAL_SCALE")); want > embedded {
		addSyntheticVectors(t, d, client.Model(), want-embedded)
	}
	vectors, err := d.LoadVectors(client.Model(), scope)
	if err != nil {
		t.Fatal(err)
	}
	var times []time.Duration
	for round := 0; round < 3; round++ {
		for _, g := range gold {
			start := time.Now()
			resp, err := Search(ctx, d, client, g.Query, scope, 5)
			if err != nil || resp.Mode != ModeHybrid {
				t.Fatalf("Search: mode=%s reason=%q err=%v", resp.Mode, resp.Reason, err)
			}
			times = append(times, time.Since(start))
		}
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	p50, p95 := times[len(times)/2], times[len(times)*95/100]
	t.Logf("search latency over %d vectors: p50 %v, p95 %v", len(vectors), p50, p95)
	if len(vectors) >= 10000 && p95 > 150*time.Millisecond {
		t.Errorf("p95 = %v at %d vectors, want under 150ms", p95, len(vectors))
	}
}

func onlyPrinciples(keys []db.RecallKey) []db.RecallKey {
	var out []db.RecallKey
	for _, k := range keys {
		if k.Kind == db.RecallKindPrinciple {
			out = append(out, k)
		}
	}
	return out
}

func addSyntheticVectors(t *testing.T, d *db.DB, model string, n int) {
	t.Helper()
	sample, err := d.LoadVectors(model, db.RecallScope{All: true})
	if err != nil || len(sample) == 0 {
		t.Fatalf("no vectors to size synthetic rows from: %v", err)
	}
	dim := len(sample[0].Vec)
	rng := rand.New(rand.NewSource(1))
	const batch = 500
	for done := 0; done < n; done += batch {
		var items []db.Embedding
		for i := 0; i < batch && done+i < n; i++ {
			s := &db.SessionSummary{ProjectID: "synthetic", SessionID: fmt.Sprintf("syn-%d-%d", done, i), Summary: "synthetic filler row"}
			if err := d.InsertSessionSummary(s); err != nil {
				t.Fatal(err)
			}
			v := make([]float32, dim)
			for j := range v {
				v[j] = rng.Float32() - 0.5
			}
			items = append(items, db.Embedding{Kind: db.RecallKindSummary, ID: s.ID, Model: model, Vec: v})
		}
		if err := d.SaveEmbeddings(items); err != nil {
			t.Fatal(err)
		}
	}
}
