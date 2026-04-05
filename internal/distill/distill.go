package distill

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/forge/forge/internal/config"
	"github.com/forge/forge/internal/db"
	"github.com/google/uuid"
)

// Provider represents an LLM inference provider.
type Provider string

const (
	ProviderOllama    Provider = "ollama"
	ProviderOpenAI    Provider = "openai"
	ProviderAnthropic Provider = "anthropic"
	ProviderForgememo Provider = "forgememo"
)

// Error types for provider configuration issues.
var (
	ErrNoProvider          = errors.New("no inference provider configured")
	ErrProviderInvalid     = errors.New("invalid provider or credentials")
	ErrProviderUnreachable = errors.New("provider unreachable")
)

// UserMessage returns a human-readable error message for end users.
func UserMessage(err error) string {
	switch {
	case errors.Is(err, ErrNoProvider):
		return "No inference provider configured. Set FORGE_PROVIDER (forgememo/anthropic/openai/ollama) and run 'forge config' to configure."
	case errors.Is(err, ErrProviderInvalid):
		return "Invalid provider or credentials. Check your FORGE_PROVIDER and FORGE_API_KEY settings."
	case errors.Is(err, ErrProviderUnreachable):
		return "Cannot reach inference provider. Model loading can take 30-60s on first Ollama run. Retry, or increase timeout: forge config --timeout 60s"
	case strings.Contains(err.Error(), "connection refused"):
		return "Connection refused. Is your inference provider running? For Ollama, run: ollama serve"
	case strings.Contains(err.Error(), "401"):
		return "Authentication failed. Check your FORGE_API_KEY is valid."
	case strings.Contains(err.Error(), "403"):
		return "Access forbidden. Check your API key permissions."
	default:
		return fmt.Sprintf("Distillation error: %v. Run 'forge doctor' to diagnose.", err)
	}
}

// DiagnosticHints returns actionable troubleshooting suggestions for a distillation error.
func DiagnosticHints(cfg Config, err error) []string {
	if cfg.Provider == ProviderOllama || strings.Contains(strings.ToLower(err.Error()), "ollama") {
		return []string{
			"Check Ollama is running: lsof -i :11434",
			"Load a model: ollama pull llama3",
			"Increase timeout: forge config --timeout 60s",
			"Try Anthropic instead: forge config --provider anthropic --api-key sk-ant-...",
		}
	}
	return []string{
		"Run: forge config --show",
		"Verify provider credentials and model access",
		"Run: forge doctor",
	}
}

// Config holds distillation configuration.
type Config struct {
	Provider        Provider
	APIKey          string
	Model           string
	BaseURL         string
	PaymentURL      string
	Timeout         time.Duration
	Retries         int
	DistillInterval time.Duration
}

// LoadConfig loads distillation config from environment.
// Priority: Forgememo > OpenAI/Anthropic > Ollama (fallback)
func LoadConfig() Config {
	timeout := parseDurationOrDefault(os.Getenv("FORGE_TIMEOUT"), 30*time.Second)
	retries := parseIntOrDefault(os.Getenv("FORGE_RETRIES"), 3)
	distillInterval := parseDurationOrDefault(os.Getenv("FORGE_DISTILL_INTERVAL"), 10*time.Minute)

	cfg := Config{
		Provider:        Provider(os.Getenv("FORGE_PROVIDER")),
		APIKey:          os.Getenv("FORGE_API_KEY"),
		Model:           os.Getenv("FORGE_MODEL"),
		BaseURL:         os.Getenv("FORGE_BASE_URL"),
		PaymentURL:      os.Getenv("FORGE_PAYMENT_URL"),
		Timeout:         timeout,
		Retries:         retries,
		DistillInterval: distillInterval,
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = os.Getenv("FORGE_API_URL") // legacy compatibility
	}

	// If env vars not set, load from config file
	if cfg.Provider == "" || cfg.APIKey == "" {
		if fileCfg, err := config.Load(); err == nil {
			if cfg.Provider == "" && fileCfg.Provider != "" {
				cfg.Provider = Provider(fileCfg.Provider)
			}
			if cfg.APIKey == "" && fileCfg.APIKey != "" {
				cfg.APIKey = fileCfg.APIKey
			}
			if cfg.Model == "" && fileCfg.Model != "" {
				cfg.Model = fileCfg.Model
			}
			if cfg.BaseURL == "" && fileCfg.BaseURL != "" {
				cfg.BaseURL = fileCfg.BaseURL
			}
			if os.Getenv("FORGE_TIMEOUT") == "" && fileCfg.Timeout != "" {
				cfg.Timeout = parseDurationOrDefault(fileCfg.Timeout, cfg.Timeout)
			}
			if os.Getenv("FORGE_RETRIES") == "" && fileCfg.Retries > 0 {
				cfg.Retries = fileCfg.Retries
			}
			if os.Getenv("FORGE_DISTILL_INTERVAL") == "" && fileCfg.DistillInterval != "" {
				cfg.DistillInterval = parseDurationOrDefault(fileCfg.DistillInterval, cfg.DistillInterval)
			}
		}
	}

	if cfg.PaymentURL == "" {
		cfg.PaymentURL = "https://forge.sh"
	}

	if cfg.Provider == "" {
		cfg.Provider = ProviderForgememo
	}
	if cfg.Provider == "forge" {
		cfg.Provider = ProviderForgememo
	}

	if cfg.Model == "" {
		switch cfg.Provider {
		case ProviderOllama:
			cfg.Model = "llama3:latest"
		case ProviderOpenAI:
			cfg.Model = "gpt-4o"
		case ProviderAnthropic:
			cfg.Model = "claude-haiku-4-5-20251001"
		case ProviderForgememo:
			cfg.Model = "claude-haiku-4-5-20251001"
		}
	}
	if cfg.BaseURL == "" {
		switch cfg.Provider {
		case ProviderOllama:
			cfg.BaseURL = "http://localhost:11434"
		case ProviderOpenAI:
			cfg.BaseURL = "https://api.openai.com"
		case ProviderAnthropic:
			cfg.BaseURL = "https://api.anthropic.com"
		case ProviderForgememo:
			cfg.BaseURL = cfg.PaymentURL + "/api/forge"
		}
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.Retries < 0 {
		cfg.Retries = 0
	}
	if cfg.DistillInterval <= 0 {
		cfg.DistillInterval = 10 * time.Minute
	}

	return cfg
}

func parseDurationOrDefault(raw string, fallback time.Duration) time.Duration {
	if raw == "" {
		return fallback
	}
	v, err := time.ParseDuration(raw)
	if err != nil || v <= 0 {
		return fallback
	}
	return v
}

func parseIntOrDefault(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return v
}

// Distiller handles event distillation.
type Distiller struct {
	db     *db.DB
	config Config
	client *http.Client
}

// New creates a new Distiller.
func New(database *db.DB, config Config) *Distiller {
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Distiller{
		db:     database,
		config: config,
		client: &http.Client{Timeout: timeout},
	}
}

func (d *Distiller) checkCredits() bool {
	url := d.config.PaymentURL + "/api/credits"
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", d.config.APIKey)
	resp, err := d.client.Do(req)
	if err != nil {
		return true // Allow on error
	}
	defer resp.Body.Close()

	var result struct {
		Data struct {
			Credits int `json:"credits"`
		} `json:"data"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	return result.Data.Credits > 0
}

func (d *Distiller) deductCredit() {
	url := d.config.PaymentURL + "/api/deduct"
	req, _ := http.NewRequest("POST", url, nil)
	req.Header.Set("Authorization", d.config.APIKey)
	d.client.Do(req)
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

	// Check credits if using Forge provider
	if d.config.Provider == ProviderForgememo && d.config.APIKey != "" {
		if !d.checkCredits() {
			return 0, fmt.Errorf("no credits remaining. Run 'forge login --purchase' to buy more")
		}
		d.deductCredit()
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
	case ProviderForgememo:
		return d.callForgememo(prompt)
	default:
		return "", fmt.Errorf("%w: unsupported provider %q", ErrNoProvider, d.config.Provider)
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

	maxAttempts := maxInt(1, d.config.Retries+1)
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		resp, err := d.client.Post(url, "application/json", bytes.NewReader(jsonBody))
		if err != nil {
			lastErr = err
		} else {
			data, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode >= 400 {
				lastErr = fmt.Errorf("%w: Ollama returned status %d", ErrProviderInvalid, resp.StatusCode)
			} else {
				var result struct {
					Response string `json:"response"`
				}
				json.Unmarshal(data, &result)
				return result.Response, nil
			}
		}
		if !shouldRetryOllama(lastErr) || attempt == maxAttempts {
			break
		}
		time.Sleep(retryBackoff(attempt))
	}
	if isProviderUnreachableError(lastErr) {
		return "", fmt.Errorf("%w: %v", ErrProviderUnreachable, lastErr)
	}
	return "", lastErr
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
		if isProviderUnreachableError(err) {
			return "", fmt.Errorf("%w: %v", ErrProviderUnreachable, err)
		}
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 {
		return "", fmt.Errorf("%w: Invalid OpenAI API key", ErrProviderInvalid)
	}
	if resp.StatusCode == 403 {
		return "", fmt.Errorf("%w: OpenAI access forbidden", ErrProviderInvalid)
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("%w: OpenAI returned status %d", ErrProviderInvalid, resp.StatusCode)
	}

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
		if isProviderUnreachableError(err) {
			return "", fmt.Errorf("%w: %v", ErrProviderUnreachable, err)
		}
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 {
		return "", fmt.Errorf("%w: Invalid Anthropic API key", ErrProviderInvalid)
	}
	if resp.StatusCode == 403 {
		return "", fmt.Errorf("%w: Anthropic access forbidden", ErrProviderInvalid)
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("%w: Anthropic returned status %d", ErrProviderInvalid, resp.StatusCode)
	}

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

func (d *Distiller) callForgememo(prompt string) (string, error) {
	url := d.config.PaymentURL + "/api/distill"

	body := map[string]any{
		"model":   d.config.Model,
		"prompt":  prompt,
		"api_key": d.config.APIKey,
	}
	jsonBody, _ := json.Marshal(body)

	resp, err := d.client.Post(url, "application/json", bytes.NewReader(jsonBody))
	if err != nil {
		if isProviderUnreachableError(err) {
			return "", fmt.Errorf("%w: %v", ErrProviderUnreachable, err)
		}
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 {
		return "", fmt.Errorf("%w: No credits or invalid API key", ErrProviderInvalid)
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("%w: Forgememo API returned status %d", ErrProviderInvalid, resp.StatusCode)
	}

	data, _ := io.ReadAll(resp.Body)
	var result struct {
		Response string `json:"response"`
	}
	json.Unmarshal(data, &result)
	if result.Response != "" {
		return result.Response, nil
	}
	return "", fmt.Errorf("no response from Forgememo")
}

func isProviderUnreachableError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "connection refused") ||
		strings.Contains(text, "actively refused") ||
		strings.Contains(text, "no such host") ||
		strings.Contains(text, "deadline exceeded") ||
		strings.Contains(text, "timeout")
}

func shouldRetryOllama(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return isProviderUnreachableError(err) ||
		strings.Contains(msg, "status 503") ||
		strings.Contains(msg, "timed out") ||
		strings.Contains(msg, "deadline exceeded")
}

func retryBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	seconds := math.Pow(2, float64(attempt-1))
	if seconds > 8 {
		seconds = 8
	}
	return time.Duration(seconds * float64(time.Second))
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ValidateConfig checks provider/model/API key access by making a small inference call.
func ValidateConfig(cfg Config) error {
	d := New(nil, cfg)
	_, err := d.callLLM(`Return only the word "ok".`)
	return err
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
