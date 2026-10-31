package embed

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestNewRefusesRemoteServerByDefault(t *testing.T) {
	for _, raw := range []string{
		"http://example.com:11434",
		"https://api.openai.com/v1",
		"http://192.168.1.10:11434",
		"http://localhost.evil.com",
		"http://127.0.0.1.evil.com",
		"http://[2001:db8::1]:11434",
	} {
		if _, err := New(raw, DefaultModel, false); err == nil {
			t.Errorf("New(%q) accepted a non-loopback server", raw)
		}
	}
	for _, raw := range []string{"http://localhost:11434", "http://LOCALHOST:1", "http://127.0.0.1:11434", "http://127.9.9.9", "http://[::1]:11434"} {
		if _, err := New(raw, DefaultModel, false); err != nil {
			t.Errorf("New(%q): %v", raw, err)
		}
	}
	if _, err := New("http://example.com", DefaultModel, true); err != nil {
		t.Errorf("explicit opt-in refused: %v", err)
	}
	if _, err := New("file:///etc/passwd", DefaultModel, true); err == nil {
		t.Error("non-http scheme accepted")
	}
}

func TestFromEnvAppliesGuard(t *testing.T) {
	t.Setenv("FORGE_EMBED_URL", "http://example.com")
	t.Setenv("FORGE_EMBED_ALLOW_REMOTE", "")
	if _, err := FromEnv(); err == nil {
		t.Fatal("FromEnv accepted a remote URL without opt-in")
	}
	t.Setenv("FORGE_EMBED_URL", "")
	t.Setenv("FORGE_EMBED_MODEL", "other")
	c, err := FromEnv()
	if err != nil || c.Model() != "other" {
		t.Fatalf("FromEnv = %+v, err=%v", c, err)
	}
}

func TestRedirectIsNotFollowed(t *testing.T) {
	var followed atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed.Store(true) }))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	c, err := New(srv.URL, DefaultModel, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.EmbedQuery(context.Background(), "secret"); err == nil {
		t.Fatal("redirect response treated as success")
	}
	if followed.Load() {
		t.Fatal("memory text followed a redirect")
	}
}

func fakeOllama(t *testing.T, seen *[]string, reply func(n int) any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			t.Errorf("path = %s", r.URL.Path)
		}
		var body struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		if seen != nil {
			*seen = append(*seen, body.Input...)
		}
		_ = json.NewEncoder(w).Encode(reply(len(body.Input)))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func vectors(n int) any {
	out := make([][]float32, n)
	for i := range out {
		out[i] = []float32{3, 4}
	}
	return map[string]any{"embeddings": out}
}

func TestEmbedPrefixesAndNormalizes(t *testing.T) {
	var seen []string
	srv := fakeOllama(t, &seen, vectors)
	c, err := New(srv.URL, "nomic-embed-text", false)
	if err != nil {
		t.Fatal(err)
	}
	docs, err := c.EmbedDocuments(context.Background(), []string{"a", "b"})
	if err != nil || len(docs) != 2 {
		t.Fatalf("docs = %v, err=%v", docs, err)
	}
	if math.Abs(float64(docs[0][0])-0.6) > 1e-6 || math.Abs(float64(docs[0][1])-0.8) > 1e-6 {
		t.Fatalf("vector not normalized: %v", docs[0])
	}
	if _, err := c.EmbedQuery(context.Background(), "q"); err != nil {
		t.Fatal(err)
	}
	want := []string{"search_document: a", "search_document: b", "search_query: q"}
	if strings.Join(seen, "|") != strings.Join(want, "|") {
		t.Fatalf("inputs = %v, want %v", seen, want)
	}

	seen = nil
	other, _ := New(srv.URL, "bge-m3", false)
	if _, err := other.EmbedQuery(context.Background(), "q"); err != nil {
		t.Fatal(err)
	}
	if seen[0] != "q" {
		t.Fatalf("non-nomic input = %q, want no prefix", seen[0])
	}
}

func TestMxbaiQueriesGetTheirRetrievalPrefix(t *testing.T) {
	var seen []string
	c, err := New(fakeOllama(t, &seen, vectors).URL, "mxbai-embed-large", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.EmbedDocuments(context.Background(), []string{"doc"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.EmbedQuery(context.Background(), "q"); err != nil {
		t.Fatal(err)
	}
	if seen[0] != "doc" || !strings.HasSuffix(seen[1], "passages: q") {
		t.Fatalf("inputs = %q", seen)
	}
}

func TestEmbedRejectsBadResponses(t *testing.T) {
	for name, reply := range map[string]func(int) any{
		"count mismatch": func(int) any { return map[string]any{"embeddings": [][]float32{}} },
		"zero vector":    func(int) any { return map[string]any{"embeddings": [][]float32{{0, 0}}} },
	} {
		t.Run(name, func(t *testing.T) {
			c, _ := New(fakeOllama(t, nil, reply).URL, DefaultModel, false)
			if _, err := c.EmbedQuery(context.Background(), "q"); err == nil {
				t.Fatal("bad response accepted")
			}
		})
	}
	down, _ := New("http://127.0.0.1:1", DefaultModel, false)
	if _, err := down.EmbedQuery(context.Background(), "q"); err == nil {
		t.Fatal("unreachable server reported success")
	}
}
