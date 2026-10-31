// Package embed turns text into vectors through a local Ollama server.
//
// Input can be decrypted principle narratives, so by default only a loopback
// server is allowed. Pointing it elsewhere needs FORGE_EMBED_ALLOW_REMOTE=1.
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	defaultURL   = "http://localhost:11434"
	DefaultModel = "mxbai-embed-large"
	maxResponse  = 64 << 20
)

// Client calls Ollama's /api/embed.
type Client struct {
	url   string
	model string
	http  *http.Client
}

// FromEnv builds a client from FORGE_EMBED_URL and FORGE_EMBED_MODEL, refusing a
// non-loopback server unless FORGE_EMBED_ALLOW_REMOTE=1.
func FromEnv() (*Client, error) {
	rawURL := strings.TrimSpace(os.Getenv("FORGE_EMBED_URL"))
	if rawURL == "" {
		rawURL = defaultURL
	}
	model := strings.TrimSpace(os.Getenv("FORGE_EMBED_MODEL"))
	if model == "" {
		model = DefaultModel
	}
	return New(rawURL, model, os.Getenv("FORGE_EMBED_ALLOW_REMOTE") == "1")
}

// New builds a client after validating the server URL.
func New(rawURL, model string, allowRemote bool) (*Client, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid embed URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("embed URL must be http or https, got %q", u.Scheme)
	}
	if !allowRemote && !isLoopback(u.Hostname()) {
		return nil, fmt.Errorf("refusing to send memory text to non-loopback embed server %q (set FORGE_EMBED_ALLOW_REMOTE=1 to allow)", u.Hostname())
	}
	return &Client{
		url:   strings.TrimRight(rawURL, "/"),
		model: model,
		http: &http.Client{
			// No proxy and no redirects: either could carry text off this machine
			// after the loopback check passed.
			Transport: &http.Transport{Proxy: nil},
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Model names the model that produced these vectors.
func (c *Client) Model() string { return c.model }

// EmbedDocuments embeds documents for storage.
func (c *Client) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	return c.embed(ctx, withPrefix(c.model, "search_document: ", texts))
}

// EmbedQuery embeds one search query.
func (c *Client) EmbedQuery(ctx context.Context, query string) ([]float32, error) {
	input := withPrefix(c.model, "search_query: ", []string{query})
	if strings.HasPrefix(c.model, "mxbai-embed") {
		input = []string{"Represent this sentence for searching relevant passages: " + query}
	}
	out, err := c.embed(ctx, input)
	if err != nil {
		return nil, err
	}
	return out[0], nil
}

// withPrefix adds the task prefix nomic models are trained to expect.
//
// shortcut: only known models get a prefix, add others here if they need one.
func withPrefix(model, prefix string, texts []string) []string {
	if !strings.HasPrefix(model, "nomic-embed") {
		return texts
	}
	out := make([]string, len(texts))
	for i, t := range texts {
		out[i] = prefix + t
	}
	return out
}

func (c *Client) embed(ctx context.Context, input []string) ([][]float32, error) {
	body, err := json.Marshal(map[string]any{"model": c.model, "input": input, "truncate": true})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embed server returned %d: %s", resp.StatusCode, truncate(string(data), 200))
	}
	var parsed struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("parse embed response: %w", err)
	}
	if len(parsed.Embeddings) != len(input) {
		return nil, fmt.Errorf("embed server returned %d vectors for %d inputs", len(parsed.Embeddings), len(input))
	}
	for i, v := range parsed.Embeddings {
		if err := normalize(v); err != nil {
			return nil, fmt.Errorf("vector %d: %w", i, err)
		}
	}
	return parsed.Embeddings, nil
}

// normalize scales to unit length, so cosine similarity is a plain dot product.
func normalize(v []float32) error {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 || math.IsNaN(sum) || math.IsInf(sum, 0) {
		return errors.New("empty or invalid vector")
	}
	n := float32(math.Sqrt(sum))
	for i := range v {
		v[i] /= n
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// QueryTimeout bounds a query embedding on an interactive path. A cold model can
// outlast it, in which case the caller falls back and the load still finishes.
const QueryTimeout = 3 * time.Second

// BatchTimeout bounds one background batch, including a cold model load.
const BatchTimeout = 2 * time.Minute
