package retrieve

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/forge/forge/internal/db"
)

// Worker consumes queued retrieval jobs and stores compact external summaries.
type Worker struct {
	db *db.DB
}

// NewWorker creates a retrieval worker.
func NewWorker(database *db.DB) *Worker {
	return &Worker{db: database}
}

// RunOnce processes a single queued retrieval job.
func (w *Worker) RunOnce() (int, error) {
	if w == nil || w.db == nil {
		return 0, nil
	}
	job, err := w.db.NextQueuedRetrievalJob()
	if err != nil || job == nil {
		return 0, err
	}

	summary, err := w.fetch(job)
	if err != nil {
		_ = w.db.UpdateRetrievalJobStatus(job.ID, "failed", truncate(err.Error(), 300))
		return 0, err
	}
	if err := w.db.UpsertExternalContextSummary(summary); err != nil {
		_ = w.db.UpdateRetrievalJobStatus(job.ID, "failed", truncate(err.Error(), 300))
		return 0, err
	}
	if err := w.db.UpdateRetrievalJobStatus(job.ID, "done", ""); err != nil {
		return 0, err
	}
	return 1, nil
}

func (w *Worker) fetch(job *db.RetrievalJob) (*db.ExternalContextSummary, error) {
	switch job.Source {
	case "context7":
		return w.fetchContext7MCP(job)
	case "context7_setup":
		return w.fetchContext7Docs(job)
	}

	cmdName := sourceCommand(job.Source)
	if cmdName == "" {
		return nil, fmt.Errorf("no command configured for source %s", job.Source)
	}
	args := sourceArgs(job)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, cmdName, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("%s fetch failed: %s", job.Source, msg)
	}

	body := compactText(stdout.String())
	if body == "" {
		body = compactText(stderr.String())
	}
	if body == "" {
		return nil, fmt.Errorf("%s returned empty output", job.Source)
	}

	return &db.ExternalContextSummary{
		TS:             time.Now().UTC().Format(time.RFC3339),
		Source:         job.Source,
		ProjectID:      job.ProjectID,
		Query:          job.Query,
		LibraryName:    job.LibraryName,
		LibraryVersion: job.LibraryVersion,
		Title:          summaryTitle(job),
		Narrative:      truncate(body, 420),
		SummaryKind:    summaryKind(job),
		TrustScore:     trustScore(job.Source),
		RelevanceTags:  summaryTags(job),
		ExpiresAt:      time.Now().UTC().Add(summaryTTL(job.Source)).Format(time.RFC3339),
	}, nil
}

var htmlTagPattern = regexp.MustCompile(`(?s)<[^>]+>`)

func (w *Worker) fetchContext7Docs(job *db.RetrievalJob) (*db.ExternalContextSummary, error) {
	baseURL := os.Getenv("FORGE_CONTEXT7_SETUP_DOCS_BASE")
	if baseURL == "" {
		baseURL = os.Getenv("FORGE_CONTEXT7_DOCS_BASE")
	}
	if baseURL == "" {
		baseURL = "https://context7.com"
	}

	installationText, err := fetchPageText(baseURL + "/docs/installation")
	if err != nil {
		return nil, err
	}
	cliText, err := fetchPageText(baseURL + "/docs/clients/cli")
	if err != nil {
		cliText = ""
	}

	body := summarizeContext7Setup(job.Query, installationText, cliText)
	if body == "" {
		return nil, fmt.Errorf("context7 docs did not contain usable setup text")
	}

	return &db.ExternalContextSummary{
		TS:             time.Now().UTC().Format(time.RFC3339),
		Source:         "context7_setup",
		ProjectID:      job.ProjectID,
		Query:          job.Query,
		LibraryName:    job.LibraryName,
		LibraryVersion: job.LibraryVersion,
		Title:          "context7 local setup",
		Narrative:      truncate(body, 420),
		SummaryKind:    summaryKind(job),
		TrustScore:     0.95,
		RelevanceTags:  summaryTags(job),
		ExpiresAt:      time.Now().UTC().Add(summaryTTL(job.Source)).Format(time.RFC3339),
	}, nil
}

func fetchPageText(url string) (string, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("fetch %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("fetch %s: status %d", url, resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", url, err)
	}
	text := html.UnescapeString(string(data))
	text = htmlTagPattern.ReplaceAllString(text, " ")
	return compactText(text), nil
}

func summarizeContext7Setup(query, installationText, cliText string) string {
	lowered := strings.ToLower(query)
	clientName := "OpenAI Codex"
	switch {
	case strings.Contains(lowered, "claude"):
		clientName = "Claude Code"
	case strings.Contains(lowered, "gemini"):
		clientName = "Gemini CLI"
	case strings.Contains(lowered, "codex"):
		clientName = "OpenAI Codex"
	}

	sections := []string{}
	if line := context7Section(installationText, clientName, "Local Server:"); line != "" {
		sections = append(sections, clientName+" local setup: "+line)
	}
	if line := context7Section(installationText, "Recommended Setup", "Remote Server (HTTP)"); line != "" {
		sections = append(sections, "Recommended setup: "+line)
	}
	if cliText != "" {
		if line := context7Section(cliText, "CLI", "context7"); line != "" {
			sections = append(sections, "CLI tools: "+line)
		}
	}
	if len(sections) == 0 {
		if line := context7Section(installationText, "OpenAI Codex", "Local Server:"); line != "" {
			sections = append(sections, "OpenAI Codex local setup: "+line)
		}
	}
	return compactText(strings.Join(sections, " "))
}

func context7Section(text, anchor, needle string) string {
	start := strings.Index(text, anchor)
	if start < 0 {
		return ""
	}
	window := text[start:]
	if nextHeading := nextContext7Heading(window, anchor); nextHeading > 0 {
		window = window[:nextHeading]
	} else if len(window) > 1400 {
		window = window[:1400]
	}
	if needle != "" {
		needleIdx := strings.Index(window, needle)
		if needleIdx >= 0 {
			window = window[needleIdx:]
		}
	}
	if len(window) > 380 {
		window = window[:380]
	}
	return strings.TrimSpace(window)
}

func nextContext7Heading(window, current string) int {
	headings := []string{
		"Recommended Setup",
		"OpenAI Codex",
		"Claude Code",
		"Gemini CLI",
		"CLI",
	}
	cut := -1
	for _, heading := range headings {
		if heading == current {
			continue
		}
		idx := strings.Index(window, heading)
		if idx > 0 && (cut == -1 || idx < cut) {
			cut = idx
		}
	}
	return cut
}

func sourceCommand(source string) string {
	switch source {
	case "opensrc":
		if cmd := os.Getenv("FORGE_OPENSRC_CMD"); cmd != "" {
			return cmd
		}
		return "opensrc"
	case "exa":
		if cmd := os.Getenv("FORGE_EXA_CMD"); cmd != "" {
			return cmd
		}
		return "exa"
	default:
		return ""
	}
}

func sourceArgs(job *db.RetrievalJob) []string {
	switch job.Source {
	case "opensrc":
		if job.LibraryName != "" {
			return []string{job.LibraryName}
		}
		return []string{job.Query}
	default:
		return []string{job.Query}
	}
}

func trustScore(source string) float64 {
	switch source {
	case "context7":
		return 0.92
	case "context7_setup":
		return 0.95
	case "opensrc":
		return 0.78
	case "exa":
		return 0.65
	default:
		return 0.5
	}
}

func summaryTTL(source string) time.Duration {
	switch source {
	case "context7":
		return 7 * 24 * time.Hour
	case "context7_setup":
		return 7 * 24 * time.Hour
	case "opensrc":
		return 14 * 24 * time.Hour
	case "exa":
		return 3 * 24 * time.Hour
	default:
		return 24 * time.Hour
	}
}

func summaryTitle(job *db.RetrievalJob) string {
	switch {
	case job.LibraryName != "":
		return job.Source + " " + job.LibraryName
	case job.Query != "":
		return job.Source + " " + truncate(job.Query, 60)
	default:
		return job.Source + " context"
	}
}

func summaryTags(job *db.RetrievalJob) []string {
	var tags []string
	if job.LibraryName != "" {
		tags = append(tags, job.LibraryName)
	}
	tags = append(tags, job.Source)
	return tags
}

func summaryKind(job *db.RetrievalJob) string {
	if job == nil {
		return ""
	}
	if job.Source == "context7" && job.TriggerType == "failure" {
		return "official_hint"
	}
	switch job.Source {
	case "context7":
		return "docs_summary"
	case "context7_setup":
		return "setup_summary"
	default:
		return "summary"
	}
}

func compactText(text string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
}

func truncate(text string, max int) string {
	if len(text) <= max {
		return text
	}
	return strings.TrimSpace(text[:max]) + "..."
}
