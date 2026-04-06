package retrieve

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/forge/forge/internal/config"
	"github.com/forge/forge/internal/db"
)

// fetchExaSearch calls the Exa neural search API and returns a summarized external context.
func (w *Worker) fetchExaSearch(job *db.RetrievalJob) (*db.ExternalContextSummary, error) {
	apiKey := exaAPIKey()
	if apiKey == "" {
		return nil, fmt.Errorf("exa: no API key configured (set FORGE_EXA_API_KEY or EXA_API_KEY)")
	}

	query := buildWebQuery(job)
	reqBody := map[string]any{
		"query":              query,
		"numResults":         5,
		"type":               "neural",
		"startPublishedDate": webSearchStartDate(),
		"useAutoprompt":      true,
		"contents": map[string]any{
			"text": map[string]any{
				"maxCharacters": 800,
			},
		},
	}
	body, _ := json.Marshal(reqBody)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, exaBaseURL()+"/search", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("exa: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("exa: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("exa: status %d: %s", resp.StatusCode, compactText(string(data)))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("exa: read response: %w", err)
	}

	parts := parseExaResponseParts(data)
	narrative, hint := buildExaNarrative(data, job, parts)
	if narrative == "" {
		return nil, fmt.Errorf("exa: no usable results for %q", query)
	}

	refinedHint, aiRefined, _ := maybeRefineContext7Hint(job, hint, parts)
	if aiRefined {
		hint = refinedHint
	}
	kind := aiSummaryKind(webSummaryKind(job), aiRefined)
	tags := aiSummaryTags(summaryTags(job), aiRefined)

	return &db.ExternalContextSummary{
		TS:            time.Now().UTC().Format(time.RFC3339),
		Source:        "exa",
		ProjectID:     job.ProjectID,
		Query:         job.Query,
		LibraryName:   job.LibraryName,
		Title:         summaryTitle(job),
		Narrative:     truncate(narrative, 420),
		SummaryKind:   kind,
		Hint:          truncate(hint, 220),
		TrustScore:    trustScore("exa"),
		RelevanceTags: tags,
		ExpiresAt:     time.Now().UTC().Add(summaryTTL("exa")).Format(time.RFC3339),
	}, nil
}

// fetchTavilySearch calls the Tavily search API and returns a summarized external context.
func (w *Worker) fetchTavilySearch(job *db.RetrievalJob) (*db.ExternalContextSummary, error) {
	apiKey := tavilyAPIKey()
	if apiKey == "" {
		return nil, fmt.Errorf("tavily: no API key configured (set FORGE_TAVILY_API_KEY or TAVILY_API_KEY)")
	}

	query := buildWebQuery(job)
	reqBody := map[string]any{
		"api_key":        apiKey,
		"query":          query,
		"search_depth":   "advanced",
		"max_results":    5,
		"include_answer": true,
	}
	body, _ := json.Marshal(reqBody)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tavilyBaseURL()+"/search", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("tavily: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tavily: request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("tavily: status %d: %s", resp.StatusCode, compactText(string(data)))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("tavily: read response: %w", err)
	}

	parts, narrative, hint := parseTavilyResponseFull(data, job)
	if narrative == "" {
		return nil, fmt.Errorf("tavily: no usable results for %q", query)
	}

	refinedHint, aiRefined, _ := maybeRefineContext7Hint(job, hint, parts)
	if aiRefined {
		hint = refinedHint
	}
	kind := aiSummaryKind(webSummaryKind(job), aiRefined)
	tags := aiSummaryTags(summaryTags(job), aiRefined)

	return &db.ExternalContextSummary{
		TS:            time.Now().UTC().Format(time.RFC3339),
		Source:        "tavily",
		ProjectID:     job.ProjectID,
		Query:         job.Query,
		LibraryName:   job.LibraryName,
		Title:         summaryTitle(job),
		Narrative:     truncate(narrative, 420),
		SummaryKind:   kind,
		Hint:          truncate(hint, 220),
		TrustScore:    trustScore("tavily"),
		RelevanceTags: tags,
		ExpiresAt:     time.Now().UTC().Add(summaryTTL("tavily")).Format(time.RFC3339),
	}, nil
}

type exaResult struct {
	Title string  `json:"title"`
	URL   string  `json:"url"`
	Text  string  `json:"text"`
	Score float64 `json:"score"`
}

type exaSearchResponse struct {
	Results []exaResult `json:"results"`
}

// parseExaResponseParts extracts raw text parts from Exa results (for AI refinement).
func parseExaResponseParts(data []byte) []string {
	var resp exaSearchResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil
	}
	var parts []string
	for _, r := range resp.Results {
		if text := compactText(r.Text); text != "" {
			parts = append(parts, text)
		}
	}
	return parts
}

// buildExaNarrative ranks results and returns (narrative, hint).
func buildExaNarrative(data []byte, job *db.RetrievalJob, rawParts []string) (narrative, hint string) {
	var resp exaSearchResponse
	if err := json.Unmarshal(data, &resp); err != nil || len(resp.Results) == 0 {
		return "", ""
	}

	queryTokens := tokenize(firstNonEmpty(job.Query, job.LibraryName))

	type scored struct {
		text  string
		score float64
	}
	var candidates []scored
	for _, r := range resp.Results {
		text := compactText(r.Text)
		if text == "" {
			continue
		}
		s := r.Score
		lowered := strings.ToLower(text)
		for _, token := range queryTokens {
			if len(token) >= 4 && strings.Contains(lowered, token) {
				s += 0.05
			}
		}
		candidates = append(candidates, scored{text: text, score: s})
	}
	if len(candidates) == 0 {
		return "", ""
	}

	// Sort by score descending (insertion sort — small N)
	for i := 1; i < len(candidates); i++ {
		for j := i; j > 0 && candidates[j].score > candidates[j-1].score; j-- {
			candidates[j], candidates[j-1] = candidates[j-1], candidates[j]
		}
	}

	var parts []string
	for i, c := range candidates {
		if i >= 3 {
			break
		}
		if snip := extractBestSnippet(c.text, queryTokens); snip != "" {
			parts = append(parts, snip)
		}
	}
	if len(parts) == 0 {
		return "", ""
	}
	return strings.Join(parts, " "), parts[0]
}

type tavilyResult struct {
	Title   string  `json:"title"`
	URL     string  `json:"url"`
	Content string  `json:"content"`
	Score   float64 `json:"score"`
}

type tavilySearchResponse struct {
	Answer  string         `json:"answer"`
	Results []tavilyResult `json:"results"`
}

// parseTavilyResponseFull returns raw parts (for AI refinement), narrative, and hint.
func parseTavilyResponseFull(data []byte, job *db.RetrievalJob) (parts []string, narrative, hint string) {
	var resp tavilySearchResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, "", ""
	}

	queryTokens := tokenize(firstNonEmpty(job.Query, job.LibraryName))

	if answer := compactText(resp.Answer); answer != "" {
		parts = append(parts, answer)
		hint = truncate(answer, 220)
		narrative = hint
	}

	var snippets []string
	for i, r := range resp.Results {
		if i >= 3 {
			break
		}
		text := compactText(r.Content)
		if text == "" {
			continue
		}
		parts = append(parts, text)
		if snip := extractBestSnippet(text, queryTokens); snip != "" {
			snippets = append(snippets, snip)
		}
	}

	if len(snippets) > 0 {
		limit := 2
		if len(snippets) < limit {
			limit = len(snippets)
		}
		extra := strings.Join(snippets[:limit], " ")
		if narrative == "" {
			narrative = extra
		} else {
			narrative = narrative + " " + extra
		}
		if hint == "" {
			hint = snippets[0]
		}
	}

	return parts, truncate(narrative, 420), truncate(hint, 220)
}

// extractBestSnippet picks the most query-relevant sentence from a text block.
func extractBestSnippet(text string, queryTokens []string) string {
	sentences := strings.FieldsFunc(text, func(r rune) bool {
		return r == '\n' || r == '!' || r == '?'
	})

	bestScore := -1
	best := ""
	for _, sentence := range sentences {
		// Also split on '. ' to handle paragraph-style text
		for _, part := range strings.Split(sentence, ". ") {
			part = strings.TrimSpace(part)
			if len(strings.Fields(part)) < 5 {
				continue
			}
			lowered := strings.ToLower(part)
			score := 0
			for _, token := range queryTokens {
				if len(token) >= 4 && strings.Contains(lowered, token) {
					score++
				}
			}
			if score > bestScore {
				bestScore = score
				best = part
			}
		}
	}
	if best == "" {
		for _, sentence := range sentences {
			sentence = strings.TrimSpace(sentence)
			if len(strings.Fields(sentence)) >= 5 {
				return truncate(ensureSentence(sentence), 220)
			}
		}
		return ""
	}
	return truncate(ensureSentence(best), 220)
}

// buildWebQuery builds a time-anchored web search query from a retrieval job.
// Unlike Context7 queries (which target library API docs), web queries aim
// for recent best-practice articles, GitHub issues, and Stack Overflow answers.
func buildWebQuery(job *db.RetrievalJob) string {
	base := compactQuery(job.Query)
	if base == "" {
		base = firstNonEmpty(job.LibraryName, "developer best practice")
	}
	// Strip the context7 boilerplate suffix if present
	if idx := strings.Index(base, "official docs explanation"); idx > 0 {
		base = strings.TrimSpace(base[:idx])
	}
	year := "2025"
	if strings.Contains(base, year) || strings.Contains(base, "2024") {
		return compactQuery(base)
	}
	if job.TriggerType == "failure" {
		return compactQuery(base + " fix " + year)
	}
	return compactQuery(base + " best practice " + year)
}

// buildWebFailureQuery builds a web search query specifically for a failure event.
func buildWebFailureQuery(library, commandFamily, normalizedMessage string) string {
	parts := []string{library}
	if commandFamily != "" && commandFamily != library {
		parts = append(parts, commandFamily)
	}
	if normalizedMessage != "" {
		parts = append(parts, normalizedMessage)
	}
	parts = append(parts, "fix 2025")
	return compactQuery(strings.Join(parts, " "))
}

// buildWebPromptQuery builds a web search query for a prompt-triggered retrieval.
func buildWebPromptQuery(library, promptText string) string {
	query := compactQuery(promptText)
	if library != "" && !strings.Contains(strings.ToLower(query), strings.ToLower(library)) {
		query = library + " " + query
	}
	if !strings.Contains(query, "2025") && !strings.Contains(query, "2024") {
		query = query + " best practice 2025"
	}
	return compactQuery(query)
}

// webSearchSource returns "exa" or "tavily" if an API key is configured, else "".
// Used by the planner to decide whether to enqueue a web search job.
func webSearchSource() string {
	if strings.TrimSpace(os.Getenv("FORGE_EXA_API_KEY")) != "" || strings.TrimSpace(os.Getenv("EXA_API_KEY")) != "" {
		return "exa"
	}
	if strings.TrimSpace(os.Getenv("FORGE_TAVILY_API_KEY")) != "" || strings.TrimSpace(os.Getenv("TAVILY_API_KEY")) != "" {
		return "tavily"
	}
	cfg, err := config.Load()
	if err == nil {
		if strings.TrimSpace(cfg.ExaAPIKey) != "" {
			return "exa"
		}
		if strings.TrimSpace(cfg.TavilyAPIKey) != "" {
			return "tavily"
		}
	}
	return ""
}

func webSummaryKind(job *db.RetrievalJob) string {
	if job.TriggerType == "failure" {
		return "web_hint"
	}
	return "web_summary"
}

func webSearchStartDate() string {
	return time.Now().UTC().AddDate(-1, 0, 0).Format("2006-01-02")
}

func exaAPIKey() string {
	if key := strings.TrimSpace(os.Getenv("FORGE_EXA_API_KEY")); key != "" {
		return key
	}
	if key := strings.TrimSpace(os.Getenv("EXA_API_KEY")); key != "" {
		return key
	}
	cfg, err := config.Load()
	if err == nil {
		return strings.TrimSpace(cfg.ExaAPIKey)
	}
	return ""
}

func tavilyAPIKey() string {
	if key := strings.TrimSpace(os.Getenv("FORGE_TAVILY_API_KEY")); key != "" {
		return key
	}
	if key := strings.TrimSpace(os.Getenv("TAVILY_API_KEY")); key != "" {
		return key
	}
	cfg, err := config.Load()
	if err == nil {
		return strings.TrimSpace(cfg.TavilyAPIKey)
	}
	return ""
}

func exaBaseURL() string {
	if u := strings.TrimSpace(os.Getenv("FORGE_EXA_BASE_URL")); u != "" {
		return strings.TrimRight(u, "/")
	}
	return "https://api.exa.ai"
}

func tavilyBaseURL() string {
	if u := strings.TrimSpace(os.Getenv("FORGE_TAVILY_BASE_URL")); u != "" {
		return strings.TrimRight(u, "/")
	}
	return "https://api.tavily.com"
}
