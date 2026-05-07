package distill

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// End-to-end regression for the v0.4.38 fix: the actual HTTP request must land
// on /v1/chat/completions exactly once, regardless of whether the user's
// --base-url already contained /v1. The unit-level TestNormalizeOpenAIBase
// covers the string transform; this test covers the integration with the
// http.Client and request construction.
func TestOpenAIBaseURLDoesNotDoubleAppendV1(t *testing.T) {
	cases := []struct {
		name string
		base string // appended onto the test server's URL
	}{
		{"no trailing slash", ""},
		{"trailing slash", "/"},
		{"trailing v1", "/v1"},
		{"trailing v1 slash", "/v1/"},
		{"double slash after v1", "/v1//"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var gotPath atomic.Value
			gotPath.Store("")
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath.Store(r.URL.Path)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
			}))
			defer srv.Close()

			d := New(nil, Config{
				Provider: ProviderOpenAI,
				APIKey:   "sk-test",
				Model:    "gpt-4o",
				BaseURL:  srv.URL + c.base,
				Timeout:  5 * time.Second,
			})
			out, err := d.callOpenAI("ping")
			if err != nil {
				t.Fatalf("callOpenAI: %v", err)
			}
			if out != "ok" {
				t.Fatalf("response = %q, want ok", out)
			}
			if got := gotPath.Load().(string); got != "/v1/chat/completions" {
				t.Fatalf("base %q produced request path %q, want /v1/chat/completions",
					srv.URL+c.base, got)
			}
		})
	}
}

// Same regression for the Groq path — it shares normalizeOpenAIBase but
// constructs the URL independently in callGroq, so the integration must
// also be asserted there.
func TestGroqBaseURLDoesNotDoubleAppendV1(t *testing.T) {
	var gotPath atomic.Value
	gotPath.Store("")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath.Store(r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	d := New(nil, Config{
		Provider: ProviderGroq,
		APIKey:   "gsk-test",
		Model:    "llama-3.3-70b-versatile",
		BaseURL:  srv.URL + "/v1",
		Timeout:  5 * time.Second,
	})
	if _, err := d.callGroq("ping"); err != nil {
		t.Fatalf("callGroq: %v", err)
	}
	if got := gotPath.Load().(string); got != "/v1/chat/completions" {
		t.Fatalf("groq path %q, want /v1/chat/completions", got)
	}
}

// Defense in depth: a 401 from the upstream surfaces as ErrProviderInvalid —
// not as the generic "context deadline exceeded" symptom that the bad-base-URL
// case used to manifest as. If this assertion fails, it likely means the URL
// is wrong (e.g. /v1/v1/...) and the server is responding 404 → unknown error,
// instead of a clean 401.
func TestOpenAI401SurfacesAsProviderInvalid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	d := New(nil, Config{
		Provider: ProviderOpenAI,
		APIKey:   "sk-bad",
		Model:    "gpt-4o",
		BaseURL:  srv.URL + "/v1",
		Timeout:  5 * time.Second,
	})
	_, err := d.callOpenAI("ping")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "Invalid OpenAI API key") {
		t.Fatalf("err = %v, want 'Invalid OpenAI API key' — wrong path or wrong status mapping", err)
	}
}
