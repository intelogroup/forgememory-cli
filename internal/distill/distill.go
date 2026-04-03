package distill

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/forge/forge/internal/db"
	"github.com/google/uuid"
)

// Provider represents an LLM inference provider.
type Provider string

const (
	ProviderOllama    Provider = "ollama"
	ProviderOpenAI    Provider = "openai"
	ProviderAnthropic Provider = "anthropic"
)

// Config holds distillation configuration.
type Config struct {
	Provider Provider
	APIKey   string
	Model    string
	BaseURL  string
}

// LoadConfig loads distillation config from environment.
func LoadConfig() Config {
	cfg := Config{
		Provider: Provider(os.Getenv("FORGE_PROVIDER")),
		APIKey:   os.Getenv("FORGE_API_KEY"),
		Model:    os.Getenv("FORGE_MODEL"),
		BaseURL:  os.Getenv("FORGE_BASE_URL"),
	}

	if cfg.Provider == "" {
		cfg.Provider = ProviderOllama
	}
	if cfg.Model == "" {
		switch cfg.Provider {
		case ProviderOllama:
			cfg.Model = "llama3.2"
		case ProviderOpenAI:
			cfg.Model = "gpt-4o-mini"
		case ProviderAnthropic:
			cfg.Model = "claude-haiku-4-5-20251001"
		}
	}
	if cfg.BaseURL == "" && cfg.Provider == ProviderOllama {
		cfg.BaseURL = "http://localhost:11434"
	}

	return cfg
}

// Distiller handles event distillation.
type Distiller struct {
	db     *db.DB
	config Config
	client *http.Client
}

// New creates a new Distiller.
func New(database *db.DB, config Config) *Distiller {
	return &Distiller{
		db:     database,
		config: config,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

// DistillBatch processes undistilled events into principles.
func (d *Distiller) DistillBatch(limit int) (int, error) {
	events, err := d.db.UndistilledEvents(limit)
	if err != nil {
		return 0, fmt.Errorf("get undistilled events: %w", err)
	}
	if len(events) < 3 {
		return 0, nil // Not enough events to distill
	}

	// Build prompt from events
	prompt := d.buildPrompt(events)

	// Call LLM
	response, err := d.callLLM(prompt)
	if err != nil {
		return 0, fmt.Errorf("llm call failed: %w", err)
	}

	// Parse principles from response
	principles, err := d.parsePrinciples(response, events[0].ProjectID)
	if err != nil {
		return 0, fmt.Errorf("parse principles: %w", err)
	}

	// Store principles
	var ids []string
	for _, p := range principles {
		if err := d.db.InsertPrinciple(&p); err != nil {
			return 0, fmt.Errorf("insert principle: %w", err)
		}
	}

	// Mark events as distilled
	for _, e := range events {
		ids = append(ids, e.ID)
	}
	if err := d.db.MarkDistilled(ids); err != nil {
		return 0, fmt.Errorf("mark distilled: %w", err)
	}

	return len(principles), nil
}

func (d *Distiller) buildPrompt(events []db.Event) string {
	var sb strings.Builder
	sb.WriteString("You are a memory distillation engine. Analyze these work session events and extract high-level principles.\n\n")
	sb.WriteString("Return a JSON array. Each element must have:\n")
	sb.WriteString("- type: architecture/bugfix/pattern/preference\n")
	sb.WriteString("- title: short description (max 80 chars)\n")
	sb.WriteString("- narrative: detailed explanation (2-3 sentences)\n")
	sb.WriteString("- impact_score: 0.0-1.0\n")
	sb.WriteString("- concepts: array of zero or more from [security, pattern, gotcha, performance, trade-off, how-it-works]\n")
	sb.WriteString("- files_modified: array of file paths mentioned in the events (empty if none)\n\n")
	sb.WriteString("Events:\n")

	for i, e := range events {
		sb.WriteString(fmt.Sprintf("%d. [%s] %s (%s): %s\n", i+1, e.TS[:16], e.EventType, e.ToolName, truncatePayload(e.Payload, 200)))
	}

	sb.WriteString("\nReturn ONLY the JSON array, no other text.\n")
	return sb.String()
}

func truncatePayload(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// SynthesizeSession generates a session summary from events and stores it.
func (d *Distiller) SynthesizeSession(sessionID, projectID string, events []db.Event) error {
	if len(events) == 0 {
		return nil
	}
	prompt := d.buildSessionPrompt(events)
	response, err := d.callLLM(prompt)
	if err != nil {
		return fmt.Errorf("llm call: %w", err)
	}
	summary, err := d.parseSessionSummary(response)
	if err != nil {
		return fmt.Errorf("parse session summary: %w", err)
	}
	summary.ID = uuid.New().String()
	summary.SessionID = sessionID
	summary.ProjectID = projectID
	return d.db.InsertSessionSummary(summary)
}

func (d *Distiller) buildSessionPrompt(events []db.Event) string {
	var sb strings.Builder
	sb.WriteString("Analyze this coding session and produce a concise summary. Return JSON with these fields:\n")
	sb.WriteString("- request: What was the user trying to accomplish? (1 sentence)\n")
	sb.WriteString("- investigation: What was explored or debugged? (1-2 sentences)\n")
	sb.WriteString("- learnings: Key findings or solutions discovered (1-2 sentences)\n")
	sb.WriteString("- next_steps: Recommended follow-up actions (1 sentence, or empty string)\n\n")
	sb.WriteString("Session events:\n")
	for i, e := range events {
		sb.WriteString(fmt.Sprintf("%d. [%s] %s (%s): %s\n", i+1, e.TS[:16], e.EventType, e.ToolName, truncatePayload(e.Payload, 150)))
	}
	sb.WriteString("\nReturn ONLY the JSON object, no other text.\n")
	return sb.String()
}

func (d *Distiller) parseSessionSummary(response string) (*db.SessionSummary, error) {
	response = strings.TrimSpace(response)
	// Extract JSON object from response
	start := strings.Index(response, "{")
	end := strings.LastIndex(response, "}")
	if start >= 0 && end > start {
		response = response[start : end+1]
	}

	var raw struct {
		Request       string `json:"request"`
		Investigation string `json:"investigation"`
		Learnings     string `json:"learnings"`
		NextSteps     string `json:"next_steps"`
	}
	if err := json.Unmarshal([]byte(response), &raw); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}
	return &db.SessionSummary{
		TS:            time.Now().UTC().Format(time.RFC3339),
		Request:       raw.Request,
		Investigation: raw.Investigation,
		Learnings:     raw.Learnings,
		NextSteps:     raw.NextSteps,
		Summary:       raw.Learnings, // backward compat
	}, nil
}

func (d *Distiller) callLLM(prompt string) (string, error) {
	switch d.config.Provider {
	case ProviderOllama:
		return d.callOllama(prompt)
	case ProviderOpenAI:
		return d.callOpenAI(prompt)
	case ProviderAnthropic:
		return d.callAnthropic(prompt)
	default:
		return "", fmt.Errorf("unknown provider: %s", d.config.Provider)
	}
}

func (d *Distiller) callOllama(prompt string) (string, error) {
	url := fmt.Sprintf("%s/api/generate", d.config.BaseURL)

	body := map[string]any{
		"model":  d.config.Model,
		"prompt": prompt,
		"stream": false,
	}
	jsonBody, _ := json.Marshal(body)

	resp, err := d.client.Post(url, "application/json", bytes.NewReader(jsonBody))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	var result struct {
		Response string `json:"response"`
	}
	json.Unmarshal(data, &result)
	return result.Response, nil
}

func (d *Distiller) callOpenAI(prompt string) (string, error) {
	base := d.config.BaseURL
	if base == "" {
		base = "https://api.openai.com"
	}
	url := base + "/v1/chat/completions"

	body := map[string]any{
		"model": d.config.Model,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	}
	jsonBody, _ := json.Marshal(body)

	req, _ := http.NewRequest("POST", url, bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+d.config.APIKey)

	resp, err := d.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	json.Unmarshal(data, &result)
	if len(result.Choices) > 0 {
		return result.Choices[0].Message.Content, nil
	}
	return "", fmt.Errorf("no response from OpenAI")
}

func (d *Distiller) callAnthropic(prompt string) (string, error) {
	base := d.config.BaseURL
	if base == "" {
		base = "https://api.anthropic.com"
	}
	url := base + "/v1/messages"

	body := map[string]any{
		"model":      d.config.Model,
		"max_tokens": 1024,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	}
	jsonBody, _ := json.Marshal(body)

	req, _ := http.NewRequest("POST", url, bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", d.config.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := d.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	json.Unmarshal(data, &result)
	if len(result.Content) > 0 {
		return result.Content[0].Text, nil
	}
	return "", fmt.Errorf("no response from Anthropic")
}

func (d *Distiller) parsePrinciples(response string, projectID string) ([]db.Principle, error) {
	// Extract JSON array from response
	response = strings.TrimSpace(response)
	if !strings.HasPrefix(response, "[") {
		start := strings.Index(response, "[")
		end := strings.LastIndex(response, "]")
		if start >= 0 && end > start {
			response = response[start : end+1]
		}
	}

	var raw []struct {
		Type          string   `json:"type"`
		Title         string   `json:"title"`
		Narrative     string   `json:"narrative"`
		ImpactScore   float64  `json:"impact_score"`
		Concepts      []string `json:"concepts"`
		FilesModified []string `json:"files_modified"`
	}

	if err := json.Unmarshal([]byte(response), &raw); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}

	var principles []db.Principle
	for _, r := range raw {
		principles = append(principles, db.Principle{
			ID:            uuid.New().String(),
			TS:            time.Now().UTC().Format(time.RFC3339),
			Type:          r.Type,
			Title:         r.Title,
			Narrative:     r.Narrative,
			ImpactScore:   r.ImpactScore,
			ProjectID:     projectID,
			Concepts:      db.FilterConcepts(r.Concepts),
			FilesModified: r.FilesModified,
		})
	}

	return principles, nil
}
