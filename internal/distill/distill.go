package distill

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sort"
	"syscall"
	"time"

	"github.com/forge/forge/internal/config"
	"github.com/forge/forge/internal/db"
	"github.com/google/uuid"
)

// Provider represents an LLM inference provider.
type Provider string

const (
	ProviderOllama      Provider = "ollama"
	ProviderOpenAI      Provider = "openai"
	ProviderAnthropic   Provider = "anthropic"
	ProviderCodex       Provider = "codex"
	ProviderForgememo   Provider = "forgememo"
	ProviderGroq        Provider = "groq"
	ProviderNvidia      Provider = "nvidia"
	ProviderAntigravity Provider = "antigravity"
)

// UsageStats accumulates token usage across distillation calls.
type UsageStats struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

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
		return "No inference provider configured. Set FORGE_PROVIDER (forgememo/anthropic/openai/groq/nvidia/ollama/codex) and run 'forge config' to configure."
	case errors.Is(err, ErrProviderInvalid):
		return "Invalid provider or credentials. Check your FORGE_PROVIDER setting and any required login or API key."
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
			"Try Groq instead: forge config --provider groq --api-key gsk-...",
		}
	}
	if cfg.Provider == ProviderGroq {
		return []string{
			"Check GROQ_API_KEY is set or run: forge config --provider groq --api-key gsk-...",
			"Groq free tier: 12K tokens/min, 100K tokens/day — if rate limited, wait or upgrade plan",
			"Try a smaller model: forge config --provider groq --model llama3-8b-8192",
		}
	}
	if cfg.Provider == ProviderNvidia {
		return []string{
			"Check your NVIDIA API key (nvapi-...) is valid",
			"Verify model access at build.nvidia.com",
			"If using a local NIM, set --base-url to your container address",
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
	Provider          Provider
	APIKey            string
	Model             string
	BaseURL           string
	PaymentURL        string
	Timeout           time.Duration
	Retries           int
	DistillInterval   time.Duration
	OllamaTimeout     time.Duration
	OllamaStartupWait time.Duration
}

// LoadConfig loads distillation config from environment.
// Priority: Forgememo > OpenAI/Anthropic > Ollama (fallback)
func LoadConfig() Config {
	timeout := parseDurationOrDefault(os.Getenv("FORGE_TIMEOUT"), 30*time.Second)
	retries := parseIntOrDefault(os.Getenv("FORGE_RETRIES"), 3)
	distillInterval := parseDurationOrDefault(os.Getenv("FORGE_DISTILL_INTERVAL"), 10*time.Minute)
	ollamaTimeout := parseDurationOrDefault(os.Getenv("FORGE_OLLAMA_TIMEOUT"), 120*time.Second)
	ollamaStartupWait := parseDurationOrDefault(os.Getenv("FORGE_OLLAMA_STARTUP_WAIT"), 0)

	cfg := Config{
		Provider:          Provider(os.Getenv("FORGE_PROVIDER")),
		APIKey:            os.Getenv("FORGE_API_KEY"),
		Model:             os.Getenv("FORGE_MODEL"),
		BaseURL:           os.Getenv("FORGE_BASE_URL"),
		PaymentURL:        os.Getenv("FORGE_PAYMENT_URL"),
		Timeout:           timeout,
		Retries:           retries,
		DistillInterval:   distillInterval,
		OllamaTimeout:     ollamaTimeout,
		OllamaStartupWait: ollamaStartupWait,
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
			if os.Getenv("FORGE_OLLAMA_TIMEOUT") == "" && fileCfg.OllamaTimeout != "" {
				cfg.OllamaTimeout = parseDurationOrDefault(fileCfg.OllamaTimeout, cfg.OllamaTimeout)
			}
			if os.Getenv("FORGE_OLLAMA_STARTUP_WAIT") == "" && fileCfg.OllamaStartupWait != "" {
				cfg.OllamaStartupWait = parseDurationOrDefault(fileCfg.OllamaStartupWait, cfg.OllamaStartupWait)
			}
		}
	}

	if cfg.PaymentURL == "" {
		// Derive from BaseURL if login saved the server URL there
		if cfg.BaseURL != "" && (cfg.Provider == ProviderForgememo || cfg.Provider == "") {
			cfg.PaymentURL = strings.TrimSuffix(cfg.BaseURL, "/api/forge")
		} else {
			cfg.PaymentURL = "https://forgememo-server.onrender.com"
		}
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
		case ProviderCodex:
			cfg.Model = ""
		case ProviderForgememo:
			cfg.Model = "claude-haiku-4-5-20251001"
		case ProviderGroq:
			cfg.Model = "llama-3.3-70b-versatile"
		case ProviderNvidia:
			cfg.Model = "meta/llama-3.3-70b-instruct"
		case ProviderAntigravity:
			cfg.Model = "flash"
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
		case ProviderCodex:
			cfg.BaseURL = ""
		case ProviderForgememo:
			cfg.BaseURL = cfg.PaymentURL + "/api/forge"
		case ProviderGroq:
			cfg.BaseURL = "https://api.groq.com/openai"
		case ProviderNvidia:
			cfg.BaseURL = "https://integrate.api.nvidia.com/v1"
		case ProviderAntigravity:
			cfg.BaseURL = ""
		}
	}
	if cfg.APIKey == "" && cfg.Provider == ProviderGroq {
		cfg.APIKey = os.Getenv("GROQ_API_KEY")
	}
	if cfg.APIKey == "" && cfg.Provider == ProviderNvidia {
		cfg.APIKey = os.Getenv("NVIDIA_API_KEY")
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

func parseBoolOrDefault(raw string, fallback bool) bool {
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback
	}
	return v
}

// Distiller handles event distillation.
type Distiller struct {
	db             *db.DB
	config         Config
	client         *http.Client
	Usage          UsageStats
	sessionContext string // optional context prepended to synthesis prompts
}

// SetSessionContext sets optional context (existing principles, past sessions, patterns)
// that will be prepended to synthesis prompts. This allows the LLM to be aware of
// what it already knows before extracting new knowledge.
func (d *Distiller) SetSessionContext(context string) {
	d.sessionContext = context
}

// New creates a new Distiller.
func New(database *db.DB, config Config) *Distiller {
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if config.Provider == ProviderOllama && config.OllamaTimeout > 0 {
		timeout = config.OllamaTimeout
	}
	return &Distiller{
		db:     database,
		config: config,
		client: &http.Client{Timeout: timeout},
	}
}

func (d *Distiller) checkCredits() bool {
	url := d.config.PaymentURL + "/v1/balance"
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+d.config.APIKey)
	resp, err := d.client.Do(req)
	if err != nil {
		return true // Allow on error
	}
	defer resp.Body.Close()

	var result struct {
		BalanceUSD float64 `json:"balance_usd"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	return result.BalanceUSD > 0
}

// DistillBatch processes undistilled events into principles.
func (d *Distiller) DistillBatch(limit int) (int, error) {
	events, err := d.db.UndistilledEvents(limit)
	if err != nil {
		return 0, fmt.Errorf("get undistilled events: %w", err)
	}
	batch, boundarySeen := selectDistillationBatch(events, limit)
	if len(batch) == 0 {
		return 0, nil
	}
	if len(batch) < 3 {
		if boundarySeen {
			return 0, markEventsDistilled(d.db, batch)
		}
		return 0, nil // Not enough events to distill
	}

	// Pre-flight credit check (avoids a Groq round-trip if credits are already zero).
	// Server deducts atomically during inference; no client-side deduction needed.
	if d.config.Provider == ProviderForgememo && d.config.APIKey != "" {
		if !d.checkCredits() {
			return 0, fmt.Errorf("no credits remaining. Run 'forge login --purchase' to buy more")
		}
	}

	// Build prompt from events
	prompt := d.buildPrompt(batch)

	// Call LLM
	response, err := d.callLLM(prompt)
	if err != nil {
		return 0, fmt.Errorf("llm call failed: %w", err)
	}

	// Parse principles from response
	principles, err := d.parsePrinciples(response, batch[0].ProjectID)
	if err != nil {
		return 0, fmt.Errorf("parse principles: %w", err)
	}

	// Store principles; track only the ones actually inserted (not fingerprint-ignored duplicates).
	var ids []string
	var inserted []db.Principle
	for _, p := range principles {
		ok, err := d.db.InsertPrinciple(&p)
		if err != nil {
			return 0, fmt.Errorf("insert principle: %w", err)
		}
		if ok {
			inserted = append(inserted, p)
		}
	}

	// Best-effort conflict detection — never fails the batch.
	if len(inserted) > 0 {
		if err := d.detectAndMarkConflicts(inserted); err != nil {
			// Non-fatal: log but continue.
			fmt.Fprintf(os.Stderr, "forge: conflict detection: %v\n", err)
		}
	}

	// Mark events as distilled only when we extracted principles.
	// Empty responses mean the events may yield insights when combined
	// with later events from the same session — keep them in the queue.
	if len(principles) > 0 {
		for _, e := range batch {
			ids = append(ids, e.ID)
		}
		if err := d.db.MarkDistilled(ids); err != nil {
			return 0, fmt.Errorf("mark distilled: %w", err)
		}
	}

	return len(principles), nil
}

func (d *Distiller) buildPrompt(events []db.Event) string {
	var sb strings.Builder
	sb.WriteString("You are a senior staff engineer writing personal reference notes for your future self. Analyze these work session events and extract only durable, project-specific engineering principles worth remembering six months from now.\n\n")
	sb.WriteString("Return a JSON array. Each element must have:\n")
	sb.WriteString("- type: architecture/bugfix/pattern/preference\n")
	sb.WriteString("- title: short description (max 80 chars)\n")
	sb.WriteString("- narrative: 2-4 sentences. Sentence 1: state the tech stack and situation where this applies (e.g. \"In a Convex + WorkOS app using clerk for sessions...\", \"When running Go HTTP server behind an nginx reverse proxy...\"). Sentences 2-4: name the specific function, flag, config key, or data structure involved and explain WHY it matters — not just what happened. Write in first-person imperative. BAD: \"Fixed retry logic.\" GOOD: \"In a Go service using pgx connection pool. Always wrap write calls in WithMaxRetries(3) in store.go — the default transport silently drops retries after TCP reset; reads are idempotent so retrying them masks stale-read bugs.\"\n")
	sb.WriteString("- impl_hint: the exact function call, flag, pattern, or code shape — one tight sentence, max 120 chars. Empty string if not applicable.\n")
	sb.WriteString("- impact_score: 0.0-1.0\n")
	sb.WriteString("- concepts: array of zero or more from [security, pattern, gotcha, performance, trade-off, how-it-works]\n")
	sb.WriteString("- files_modified: array of file paths mentioned in the events (empty if none)\n\n")
	sb.WriteString("Only emit a principle when the events show a concrete decision, fix, failure mode, or reusable project insight.\n")
	sb.WriteString("Return [] if the evidence is thin, generic, or limited to routine tool usage.\n")
	sb.WriteString("Do not produce principles about transcript paths, session IDs, JSONL files, tool names, searching, curl, grep, logs, or generic debugging workflow.\n\n")
	sb.WriteString("Events:\n")

	for i, e := range events {
		sb.WriteString(fmt.Sprintf("%d. [%s] %s (%s): %s\n", i+1, shortTS(e.TS), e.EventType, e.ToolName, summarizeEventPayload(e)))
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

// summarizeEventPayload produces a human-readable summary of an event payload
// for inclusion in LLM prompts. For PostToolUse events it extracts the actual
// command / file path and response content rather than dumping raw JSON.
func summarizeEventPayload(e db.Event) string {
	if e.Payload == "" {
		return ""
	}
	if e.EventType == "UserPromptSubmit" {
		if prompt := strings.TrimSpace(extractPromptText(e.Payload)); prompt != "" {
			return truncatePayload(prompt, 500)
		}
	}
	if isSessionBoundaryEvent(e.EventType) {
		return "session boundary"
	}
	if e.EventType != "PostToolUse" {
		return truncatePayload(e.Payload, 500)
	}

	var raw struct {
		ToolInput    json.RawMessage `json:"tool_input"`
		ToolResponse json.RawMessage `json:"tool_response"`
	}
	if err := json.Unmarshal([]byte(e.Payload), &raw); err != nil {
		return truncatePayload(e.Payload, 500)
	}

	// Extract the most useful field from tool_input depending on tool type.
	var inputSummary string
	switch e.ToolName {
	case "Bash":
		var inp struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(raw.ToolInput, &inp) == nil && inp.Command != "" {
			inputSummary = truncatePayload(inp.Command, 300)
		}
	case "Edit", "Write", "MultiEdit":
		var inp struct {
			FilePath string `json:"file_path"`
			NewFile  string `json:"new_file"`
		}
		if json.Unmarshal(raw.ToolInput, &inp) == nil && inp.FilePath != "" {
			inputSummary = inp.FilePath
		}
	}
	if inputSummary == "" {
		inputSummary = truncatePayload(string(raw.ToolInput), 300)
	}

	// Extract a useful slice of the tool response.
	var respSummary string
	// tool_response may be a string or an object {stdout, stderr, exit_code}.
	var respObj struct {
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
		ExitCode int    `json:"exit_code"`
	}
	if json.Unmarshal(raw.ToolResponse, &respObj) == nil && (respObj.Stdout != "" || respObj.Stderr != "") {
		combined := strings.TrimSpace(respObj.Stdout + "\n" + respObj.Stderr)
		respSummary = fmt.Sprintf("exit=%d %s", respObj.ExitCode, truncatePayload(combined, 400))
	} else {
		// Plain string response (e.g. Edit confirmation).
		var s string
		if json.Unmarshal(raw.ToolResponse, &s) == nil {
			respSummary = truncatePayload(s, 400)
		} else {
			respSummary = truncatePayload(string(raw.ToolResponse), 400)
		}
	}

	if respSummary != "" {
		return fmt.Sprintf("input: %s → %s", inputSummary, respSummary)
	}
	return fmt.Sprintf("input: %s", inputSummary)
}

func selectDistillationBatch(events []db.Event, limit int) ([]db.Event, bool) {
	if len(events) == 0 || limit <= 0 {
		return nil, false
	}
	first := events[0]
	batch := make([]db.Event, 0, minInt(len(events), limit))
	boundarySeen := false
	for _, e := range events {
		if len(batch) >= limit {
			break
		}
		if len(batch) > 0 && (e.SessionID != first.SessionID || e.ProjectID != first.ProjectID) {
			boundarySeen = true
			break
		}
		batch = append(batch, e)
	}
	return batch, boundarySeen
}

func markEventsDistilled(database *db.DB, events []db.Event) error {
	ids := make([]string, 0, len(events))
	for _, e := range events {
		ids = append(ids, e.ID)
	}
	return database.MarkDistilled(ids)
}

func isSessionBoundaryEvent(eventType string) bool {
	switch eventType {
	case "Stop", "SessionEnd", "AfterAgent":
		return true
	default:
		return false
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// SynthesizeSession generates a session summary from events and stores it.
func (d *Distiller) SynthesizeSession(sessionID, projectID string, events []db.Event) error {
	_, err := d.SynthesizeCheckpoint(sessionID, projectID, "final", "", events)
	return err
}

// SynthesizeCrossSession generates cross-session patterns by analyzing recent
// session summaries for a project. It reads the last N summaries, asks the LLM
// to find recurring patterns, and stores them.
func (d *Distiller) SynthesizeCrossSession(projectID string, summaryCount int) (int, error) {
	limit := summaryCount
	if limit <= 0 {
		limit = 5
	}
	summaries, err := d.db.GetRecentSessionSummariesByProject(projectID, limit+10)
	if err != nil {
		return 0, fmt.Errorf("get session summaries: %w", err)
	}
	if len(summaries) < 3 {
		return 0, nil
	}
	if len(summaries) > limit {
		summaries = summaries[:limit]
	}

	prompt := d.buildCrossSessionPrompt(summaries)
	response, err := d.callLLM(prompt)
	if err != nil {
		return 0, fmt.Errorf("llm call: %w", err)
	}

	patterns, err := d.parseCrossSessionPatterns(response, projectID, summaries)
	if err != nil {
		return 0, fmt.Errorf("parse patterns: %w", err)
	}

	count := 0
	for _, p := range patterns {
		if err := d.db.InsertCrossSessionPattern(&p); err != nil {
			return count, fmt.Errorf("insert pattern: %w", err)
		}
		count++
	}
	return count, nil
}

func (d *Distiller) buildCrossSessionPrompt(summaries []db.SessionSummary) string {
	var sb strings.Builder
	sb.WriteString("You are a senior staff engineer reviewing recent work sessions. Analyze these session summaries and find recurring patterns, failure modes, tool preferences, project progress, or knowledge gaps that span multiple sessions.\n\n")
	sb.WriteString("Return a JSON array of patterns. Each element must have:\n")
	sb.WriteString("- pattern: 2-3 sentence description of the recurring pattern\n")
	sb.WriteString("- pattern_type: one of recurring_failure / tool_preference / project_progress / knowledge_gap / workflow\n")
	sb.WriteString("- confidence: 0.0-1.0 how confident you are this is a real pattern\n")
	sb.WriteString("- evidence_session_indices: array of 0-based indices into the session list below that support this pattern\n\n")
	sb.WriteString("Sessions:\n")
	for i, s := range summaries {
		sb.WriteString(fmt.Sprintf("%d. [%s] kind=%s\n", i, s.TS, firstNonEmptyString(s.CheckpointKind, "final")))
		sb.WriteString(fmt.Sprintf("   request: %s\n", firstNonEmptyString(s.Request, "(empty)")))
		sb.WriteString(fmt.Sprintf("   investigation: %s\n", firstNonEmptyString(s.Investigation, "(empty)")))
		sb.WriteString(fmt.Sprintf("   learnings: %s\n", firstNonEmptyString(s.Learnings, "(empty)")))
		sb.WriteString(fmt.Sprintf("   next_steps: %s\n", firstNonEmptyString(s.NextSteps, "(empty)")))
	}
	sb.WriteString("\nReturn ONLY the JSON array, no other text.\n")
	return sb.String()
}

func (d *Distiller) parseCrossSessionPatterns(response, projectID string, summaries []db.SessionSummary) ([]db.CrossSessionPattern, error) {
	response = strings.TrimSpace(response)
	start := strings.Index(response, "[")
	end := strings.LastIndex(response, "]")
	if start >= 0 && end > start {
		response = response[start : end+1]
	}

	var raw []struct {
		Pattern               string  `json:"pattern"`
		PatternType           string  `json:"pattern_type"`
		Confidence            float64 `json:"confidence"`
		EvidenceSessionIndices []int  `json:"evidence_session_indices"`
	}
	if err := json.Unmarshal([]byte(response), &raw); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}

	var out []db.CrossSessionPattern
	for _, r := range raw {
		if r.Pattern == "" || r.PatternType == "" {
			continue
		}
		validTypes := map[string]bool{
			"recurring_failure": true, "tool_preference": true,
			"project_progress": true, "knowledge_gap": true, "workflow": true,
		}
		if !validTypes[r.PatternType] {
			r.PatternType = "workflow"
		}

		sessionIDs := make([]string, 0, len(r.EvidenceSessionIndices))
		for _, idx := range r.EvidenceSessionIndices {
			if idx >= 0 && idx < len(summaries) {
				sessionIDs = append(sessionIDs, summaries[idx].SessionID)
			}
		}
		evidenceJSON, _ := json.Marshal(sessionIDs)

		out = append(out, db.CrossSessionPattern{
			ProjectID:          projectID,
			Pattern:            r.Pattern,
			PatternType:        r.PatternType,
			Confidence:         r.Confidence,
			EvidenceSessionIDs: string(evidenceJSON),
		})
	}
	return out, nil
}

// SynthesizeCheckpoint generates a checkpoint summary from events and stores it.
func (d *Distiller) SynthesizeCheckpoint(sessionID, projectID, checkpointKind, checkpointKey string, events []db.Event) (*db.SessionSummary, error) {
	if len(events) == 0 {
		return nil, nil
	}
	prompt := d.buildSessionPrompt(events)
	response, err := d.callLLM(prompt)
	if err != nil {
		return nil, fmt.Errorf("llm call: %w", err)
	}
	summary, err := d.parseSessionSummary(response)
	if err != nil {
		return nil, fmt.Errorf("parse session summary: %w", err)
	}
	summary.ID = uuid.New().String()
	summary.SessionID = sessionID
	summary.ProjectID = projectID
	summary.CheckpointKind = checkpointKind
	summary.CheckpointKey = checkpointKey
	if err := d.db.InsertSessionSummary(summary); err != nil {
		return nil, err
	}
	return summary, nil
}

func (d *Distiller) buildSessionPrompt(events []db.Event) string {
	var sb strings.Builder
	if d.sessionContext != "" {
		sb.WriteString(d.sessionContext)
		sb.WriteString("\n\n")
	}
	sb.WriteString("Analyze this coding session and produce a concise summary. Return JSON with these fields:\n")
	sb.WriteString("- request: What was the user trying to accomplish? (1 sentence)\n")
	sb.WriteString("- investigation: What was explored or debugged? (1-2 sentences)\n")
	sb.WriteString("- learnings: Key findings or solutions discovered (1-2 sentences)\n")
	sb.WriteString("- next_steps: Recommended follow-up actions (1 sentence, or empty string)\n")
	sb.WriteString("- keywords: Array of 3-5 key technical terms, libraries, frameworks, or concepts from this session (e.g. [\"sqlite\", \"fts5\", \"go\", \"migration\"]). These will be used to recall this session later when relevant topics come up.\n\n")
	sb.WriteString("Session events:\n")
	for i, e := range events {
		sb.WriteString(fmt.Sprintf("%d. [%s] %s (%s): %s\n", i+1, shortTS(e.TS), e.EventType, e.ToolName, summarizeEventPayload(e)))
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
		Request       string   `json:"request"`
		Investigation string   `json:"investigation"`
		Learnings     string   `json:"learnings"`
		NextSteps     string   `json:"next_steps"`
		Keywords      []string `json:"keywords"`
	}
	if err := json.Unmarshal([]byte(response), &raw); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}
	keywords := strings.Join(raw.Keywords, " ")
	return &db.SessionSummary{
		TS:            time.Now().UTC().Format(time.RFC3339),
		Request:       raw.Request,
		Investigation: raw.Investigation,
		Learnings:     raw.Learnings,
		NextSteps:     raw.NextSteps,
		Keywords:      keywords,
		Summary:       raw.Learnings, // backward compat
	}, nil
}

// DistillCheckpointSummary extracts durable principles from a synthesized checkpoint.
func (d *Distiller) DistillCheckpointSummary(summary db.SessionSummary, events []db.Event) (int, error) {
	if d.db == nil {
		return 0, nil
	}
	prompt := d.buildCheckpointPrinciplesPrompt(summary, events)
	response, err := d.callLLM(prompt)
	if err != nil {
		return 0, fmt.Errorf("llm call failed: %w", err)
	}
	principles, err := d.parsePrinciples(response, summary.ProjectID)
	if err != nil {
		return 0, fmt.Errorf("parse principles: %w", err)
	}

	var inserted []db.Principle
	for _, p := range principles {
		ok, err := d.db.InsertPrinciple(&p)
		if err != nil {
			return 0, fmt.Errorf("insert principle: %w", err)
		}
		if ok {
			inserted = append(inserted, p)
		}
	}
	if len(inserted) > 0 {
		if err := d.detectAndMarkConflicts(inserted); err != nil {
			fmt.Fprintf(os.Stderr, "forge: conflict detection: %v\n", err)
		}
	}
	return len(inserted), nil
}

func (d *Distiller) buildCheckpointPrinciplesPrompt(summary db.SessionSummary, events []db.Event) string {
	var sb strings.Builder
	if d.sessionContext != "" {
		sb.WriteString(d.sessionContext)
		sb.WriteString("\n\n")
	}
	sb.WriteString("You are a senior staff engineer writing personal reference notes for your future self. Extract only durable, project-specific engineering principles from this synthesized checkpoint — notes worth consulting before touching this area again.\n\n")
	sb.WriteString("Return a JSON array. Each element must have:\n")
	sb.WriteString("- type: architecture/bugfix/pattern/preference\n")
	sb.WriteString("- title: short description (max 80 chars)\n")
	sb.WriteString("- narrative: 2-4 sentences. Sentence 1: state the tech stack and situation where this applies (e.g. \"In a Next.js app using Convex + WorkOS...\", \"When deploying a Go service to Render behind a proxy...\"). Sentences 2-4: name the specific function, flag, config key, or data structure involved and explain WHY it matters. Write in first-person imperative. BAD: \"Caching was improved.\" GOOD: \"In a Next.js app with a reverse-proxy CDN. Always set Cache-Control: no-store on /auth endpoints via middleware/auth.go — the proxy caches 302 redirects and replays stale tokens to different users without it.\"\n")
	sb.WriteString("- impact_score: 0.0-1.0\n")
	sb.WriteString("- outcome: \"success\" if this approach worked, \"failure\" if it definitively failed, \"unknown\" if unclear\n")
	sb.WriteString("- impl_hint: the exact function call, flag, pattern, or code shape used — one tight sentence, max 120 chars. Empty string if not applicable.\n")
	sb.WriteString("- concepts: array of zero or more from [security, pattern, gotcha, performance, trade-off, how-it-works]\n")
	sb.WriteString("- files_modified: array of file paths mentioned in the checkpoint or evidence (empty if none)\n\n")
	sb.WriteString("Only emit a principle when there is a clear outcome (a concrete fix worked, or an approach definitively failed) AND a specific implementation pattern.\n")
	sb.WriteString("Return [] for vague progress, bookkeeping, situations where no concrete technique is identifiable, or routine workflow.\n")
	sb.WriteString("Do not create principles about transcript paths, session IDs, JSONL files, tool names, logs, grep, curl, or generic debugging process.\n")
	if d.sessionContext != "" {
		sb.WriteString("\nIMPORTANT: The existing knowledge block above shows what we already know. Only extract NEW principles that are not already covered. If the events extend or reinforce an existing principle, skip it. Avoid duplication.\n")
	}
	sb.WriteString("\n")
	sb.WriteString("Checkpoint summary:\n")
	sb.WriteString(fmt.Sprintf("kind: %s\n", firstNonEmptyString(summary.CheckpointKind, "final")))
	sb.WriteString(fmt.Sprintf("request: %s\n", firstNonEmptyString(summary.Request, "(empty)")))
	sb.WriteString(fmt.Sprintf("investigation: %s\n", firstNonEmptyString(summary.Investigation, "(empty)")))
	sb.WriteString(fmt.Sprintf("learnings: %s\n", firstNonEmptyString(summary.Learnings, "(empty)")))
	sb.WriteString(fmt.Sprintf("next_steps: %s\n", firstNonEmptyString(summary.NextSteps, "(empty)")))
	if len(events) > 0 {
		sb.WriteString("\nEvidence:\n")
		for i, e := range events {
			if i == 8 {
				break
			}
			sb.WriteString(fmt.Sprintf("%d. [%s] %s (%s): %s\n", i+1, shortTS(e.TS), e.EventType, e.ToolName, summarizeEventPayload(e)))
		}
	}
	sb.WriteString("\nReturn ONLY the JSON array, no other text.\n")
	return sb.String()
}

// InjectPrinciplesForPrompt fetches relevant active principles for projectID,
// formats them as a Markdown context block, and prepends them to the given prompt.
// When database is nil or FORGE_INJECT_PRINCIPLES is set to false, the original prompt is returned unchanged.
func InjectPrinciplesForPrompt(database *db.DB, prompt, projectID string) string {
	if database == nil {
		return prompt
	}
	if !parseBoolOrDefault(os.Getenv("FORGE_INJECT_PRINCIPLES"), true) {
		return prompt
	}

	if projectID == "" {
		projectID = DetectProjectID("")
	}
	if projectID == "" {
		return prompt
	}

	// Fetch up to 15 principles to allow decay/sorting choice
	principles, err := database.RecentPrinciplesByProject(projectID, 15)
	if err != nil || len(principles) == 0 {
		return prompt
	}

	type decayedPrinciple struct {
		p     db.Principle
		score float64
	}

	decayed := make([]decayedPrinciple, 0, len(principles))
	for _, p := range principles {
		refStr := p.LastUsedTS
		if refStr == "" {
			refStr = p.TS
		}
		refTime, err := time.Parse(time.RFC3339, refStr)
		score := p.ImpactScore
		if err == nil {
			days := time.Since(refTime).Hours() / 24.0
			if days > 0 {
				decayFactor := 1.0 - 0.1*(days/30.0)
				if decayFactor < 0 {
					decayFactor = 0
				}
				score = p.ImpactScore * decayFactor
			}
		}
		// Filter out principles that have decayed below 0.1
		if score >= 0.1 {
			decayed = append(decayed, decayedPrinciple{p: p, score: score})
		}
	}

	if len(decayed) == 0 {
		return prompt
	}

	// Sort by decayed score descending, falling back to TS descending
	sort.Slice(decayed, func(i, j int) bool {
		if decayed[i].score != decayed[j].score {
			return decayed[i].score > decayed[j].score
		}
		return decayed[i].p.TS > decayed[j].p.TS
	})

	// Take top 5
	limit := 5
	if len(decayed) < limit {
		limit = len(decayed)
	}
	topDecayed := decayed[:limit]

	var context strings.Builder
	context.WriteString("\n## RELEVANT LESSONS FROM PREVIOUS WORK\n")
	for _, dp := range topDecayed {
		p := dp.p
		context.WriteString(fmt.Sprintf("- %s: %s\n", p.Title, p.Narrative))
		// Increment usage count and update last used TS
		_ = database.IncrementPrincipleUsage(p.ID)
	}
	context.WriteString("\n---\n\n")

	return context.String() + prompt
}

// DetectProjectID resolves the project identifier.
// Priority: hint arg > FORGE_PROJECT_ID env > git repo root > ""
func DetectProjectID(hint string) string {
	if hint != "" {
		return hint
	}
	if v := strings.TrimSpace(os.Getenv("FORGE_PROJECT_ID")); v != "" {
		return v
	}
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	out, err := exec.Command("git", "-C", cwd, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return ""
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return ""
	}
	parts := strings.Split(root, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return ""
}

// CallLLM is a public wrapper around callLLM, needed by the distill-agent
// for custom prompt building (e.g., chunked session synthesis).
func (d *Distiller) CallLLM(prompt string) (string, error) {
	return d.callLLM(prompt)
}

func (d *Distiller) callLLM(prompt string) (string, error) {
	switch d.config.Provider {
	case ProviderOllama:
		return d.callOllama(prompt)
	case ProviderOpenAI:
		return d.callOpenAI(prompt)
	case ProviderAnthropic:
		return d.callAnthropic(prompt)
	case ProviderCodex:
		return d.callCodex(prompt)
	case ProviderForgememo:
		return d.callForgememo(prompt)
	case ProviderGroq:
		return d.callGroq(prompt)
	case ProviderNvidia:
		return d.callNvidia(prompt)
	case ProviderAntigravity:
		return d.callAntigravity(prompt)
	default:
		return "", fmt.Errorf("%w: unsupported provider %q", ErrNoProvider, d.config.Provider)
	}
}

func (d *Distiller) callNvidia(prompt string) (string, error) {
	// NVIDIA Build API is OpenAI-compatible.
	return d.callOpenAI(prompt)
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

// normalizeOpenAIBase returns base with any trailing "/" or "/v1" stripped.
// Users frequently set base-url to "http://host:port/v1" (the standard OpenAI
// convention), which produced "/v1/v1/chat/completions" → 404 when we appended
// our own "/v1/...". Tolerate both shapes.
func normalizeOpenAIBase(base string) string {
	base = strings.TrimRight(base, "/")
	base = strings.TrimSuffix(base, "/v1")
	return strings.TrimRight(base, "/")
}

func (d *Distiller) callOpenAI(prompt string) (string, error) {
	base := d.config.BaseURL
	if base == "" {
		base = "https://api.openai.com"
	}
	url := normalizeOpenAIBase(base) + "/v1/chat/completions"

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
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	json.Unmarshal(data, &result)
	d.Usage.PromptTokens += result.Usage.PromptTokens
	d.Usage.CompletionTokens += result.Usage.CompletionTokens
	d.Usage.TotalTokens += result.Usage.TotalTokens
	if len(result.Choices) > 0 {
		return result.Choices[0].Message.Content, nil
	}
	return "", fmt.Errorf("no response from OpenAI")
}

func (d *Distiller) callGroq(prompt string) (string, error) {
	url := normalizeOpenAIBase(d.config.BaseURL) + "/v1/chat/completions"

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
		return "", fmt.Errorf("%w: Invalid Groq API key", ErrProviderInvalid)
	}
	if resp.StatusCode == 429 {
		return "", fmt.Errorf("%w: Groq rate limit exceeded", ErrProviderUnreachable)
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("%w: Groq returned status %d: %s", ErrProviderInvalid, resp.StatusCode, string(body))
	}

	data, _ := io.ReadAll(resp.Body)
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	json.Unmarshal(data, &result)
	d.Usage.PromptTokens += result.Usage.PromptTokens
	d.Usage.CompletionTokens += result.Usage.CompletionTokens
	d.Usage.TotalTokens += result.Usage.TotalTokens
	if len(result.Choices) > 0 {
		return result.Choices[0].Message.Content, nil
	}
	return "", fmt.Errorf("no response from Groq")
}

func (d *Distiller) callAnthropic(prompt string) (string, error) {
	base := d.config.BaseURL
	if base == "" {
		base = "https://api.anthropic.com"
	}
	url := normalizeOpenAIBase(base) + "/v1/messages"

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

func (d *Distiller) callCodex(prompt string) (string, error) {
	cmdName, args := codexCommand()
	outFile, err := os.CreateTemp("", "forge-codex-output-*.txt")
	if err != nil {
		return "", err
	}
	outPath := outFile.Name()
	_ = outFile.Close()
	defer os.Remove(outPath)

	args = append(args, "exec", "--skip-git-repo-check", "--sandbox", "read-only", "--ephemeral", "-o", outPath)
	if strings.TrimSpace(d.config.Model) != "" {
		args = append(args, "-m", d.config.Model)
	}
	args = append(args, "-")

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, cmdName, args...)
	cmd.Stdin = strings.NewReader(prompt)
	var stderr bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("%w: codex command timed out after 45s", ErrProviderUnreachable)
		}
		if isProviderUnreachableError(err) {
			return "", fmt.Errorf("%w: %v", ErrProviderUnreachable, err)
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		lowered := strings.ToLower(msg)
		switch {
		case strings.Contains(lowered, "login"), strings.Contains(lowered, "auth"), strings.Contains(lowered, "not authenticated"):
			return "", fmt.Errorf("%w: %s", ErrProviderInvalid, msg)
		case errors.Is(err, fs.ErrNotExist), strings.Contains(lowered, "executable file not found"), strings.Contains(lowered, "no such file or directory"):
			return "", fmt.Errorf("%w: %s", ErrProviderUnreachable, msg)
		default:
			return "", fmt.Errorf("codex exec failed: %s", msg)
		}
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		return "", fmt.Errorf("read codex output: %w", err)
	}
	response := strings.TrimSpace(string(data))
	if response == "" {
		return "", fmt.Errorf("no response from Codex")
	}
	return response, nil
}

func (d *Distiller) callForgememo(prompt string) (string, error) {
	url := d.config.PaymentURL + "/v1/inference"

	body := map[string]any{
		"model":      d.config.Model,
		"prompt":     prompt,
		"max_tokens": 300,
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
		return "", fmt.Errorf("%w: No credits or invalid API key", ErrProviderInvalid)
	}
	if resp.StatusCode == 402 {
		return "", fmt.Errorf("%w: no credits remaining", ErrProviderInvalid)
	}
	if resp.StatusCode == 429 {
		return "", fmt.Errorf("%w: rate limit exceeded", ErrProviderUnreachable)
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("%w: Forgememo API returned status %d", ErrProviderInvalid, resp.StatusCode)
	}

	data, _ := io.ReadAll(resp.Body)
	var result struct {
		Text string `json:"text"`
	}
	json.Unmarshal(data, &result)
	if result.Text != "" {
		return result.Text, nil
	}
	return "", fmt.Errorf("no response from Forgememo")
}

func (d *Distiller) callAntigravity(prompt string) (string, error) {
	home := os.Getenv("HOME")
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve user home: %w", err)
		}
	}
	agentAPIPath := filepath.Join(home, ".gemini", "antigravity", "bin", "agentapi")
	if runtime.GOOS == "windows" {
		agentAPIPath += ".exe"
	}

	if _, err := os.Stat(agentAPIPath); err != nil {
		return "", fmt.Errorf("Antigravity agentapi binary not found at %s. Please ensure the Antigravity desktop application is installed and running.", agentAPIPath)
	}

	model := string(d.config.Model)
	if model == "" {
		model = "flash"
	}

	cmd := exec.Command(agentAPIPath, "new-conversation", "--model="+model, prompt)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		var apiResp struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(stdout.Bytes(), &apiResp) == nil && apiResp.Error != "" {
			return "", fmt.Errorf("Antigravity agentapi error: %s", apiResp.Error)
		}
		return "", fmt.Errorf("failed to run Antigravity agentapi: %w (stderr: %s)", err, stderr.String())
	}

	var apiResp struct {
		Response struct {
			NewConversation struct {
				ConversationID string `json:"conversationId"`
			} `json:"newConversation"`
		} `json:"response"`
		Error string `json:"error"`
	}

	if err := json.Unmarshal(stdout.Bytes(), &apiResp); err != nil {
		return "", fmt.Errorf("failed to parse agentapi response: %w (stdout: %s)", err, stdout.String())
	}

	if apiResp.Error != "" {
		return "", fmt.Errorf("Antigravity agentapi error: %s", apiResp.Error)
	}

	convID := apiResp.Response.NewConversation.ConversationID
	if convID == "" {
		return "", fmt.Errorf("Antigravity agentapi returned empty conversation ID (stdout: %s)", stdout.String())
	}

	return d.pollAntigravityTranscript(convID)
}

type TranscriptStep struct {
	StepIndex int    `json:"step_index"`
	Source    string `json:"source"`
	Type      string `json:"type"`
	Status    string `json:"status"`
	Content   string `json:"content"`
}

func (d *Distiller) pollAntigravityTranscript(conversationID string) (string, error) {
	home := os.Getenv("HOME")
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve user home: %w", err)
		}
	}
	transcriptPath := filepath.Join(home, ".gemini", "antigravity", "brain", conversationID, ".system_generated", "logs", "transcript.jsonl")

	timeout := d.config.Timeout
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if time.Now().After(deadline) {
				return "", fmt.Errorf("timeout waiting for Antigravity response")
			}

			data, err := os.ReadFile(transcriptPath)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return "", fmt.Errorf("read transcript: %w", err)
			}

			lines := strings.Split(string(data), "\n")
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				var step TranscriptStep
				if err := json.Unmarshal([]byte(line), &step); err != nil {
					continue
				}
				if step.Source == "MODEL" && step.Type == "PLANNER_RESPONSE" {
					if step.Status == "DONE" {
						return step.Content, nil
					}
					if step.Status == "ERROR" {
						return "", fmt.Errorf("Antigravity agent execution failed: %s", step.Content)
					}
				}
			}
		}
	}
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

func codexCommand() (string, []string) {
	if cmd := strings.TrimSpace(os.Getenv("FORGE_CODEX_CMD")); cmd != "" {
		return cmd, strings.Fields(os.Getenv("FORGE_CODEX_ARGS"))
	}
	return "codex", nil
}

// HasExplicitProviderConfig reports whether Forge has an explicit provider
// configuration instead of relying on the implicit Ollama default.
func HasExplicitProviderConfig() bool {
	if strings.TrimSpace(os.Getenv("FORGE_PROVIDER")) != "" || strings.TrimSpace(os.Getenv("FORGE_API_KEY")) != "" {
		return true
	}
	cfg, err := config.Load()
	if err != nil {
		return false
	}
	return strings.TrimSpace(cfg.Provider) != "" || strings.TrimSpace(cfg.APIKey) != ""
}

// CompletePrompt runs a single best-effort completion against the configured
// provider without needing a database-backed Distiller.
func CompletePrompt(cfg Config, prompt string) (string, error) {
	d := &Distiller{
		config: cfg,
		client: &http.Client{Timeout: 45 * time.Second},
	}
	return d.callLLM(prompt)
}

// ParsePrinciples is a public wrapper around parsePrinciples.
func (d *Distiller) ParsePrinciples(response string, projectID string) ([]db.Principle, error) {
	return d.parsePrinciples(response, projectID)
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
		Outcome       string   `json:"outcome"`
		ImplHint      string   `json:"impl_hint"`
		Concepts      []string `json:"concepts"`
		FilesModified []string `json:"files_modified"`
	}

	if err := json.Unmarshal([]byte(response), &raw); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}

	var principles []db.Principle
	for _, r := range raw {
		outcome := strings.TrimSpace(r.Outcome)
		if outcome != "success" && outcome != "failure" {
			outcome = "unknown"
		}
		implHint := strings.TrimSpace(r.ImplHint)
		if runes := []rune(implHint); len(runes) > 120 {
			implHint = string(runes[:120])
		}
		p := db.Principle{
			ID:            uuid.New().String(),
			TS:            time.Now().UTC().Format(time.RFC3339),
			Type:          strings.TrimSpace(r.Type),
			Title:         strings.TrimSpace(r.Title),
			Narrative:     strings.TrimSpace(r.Narrative),
			ImpactScore:   r.ImpactScore,
			ProjectID:     projectID,
			Outcome:       outcome,
			ImplHint:      implHint,
			Concepts:      db.FilterConcepts(r.Concepts),
			FilesModified: r.FilesModified,
		}
		if !shouldKeepPrinciple(p) {
			continue
		}
		principles = append(principles, p)
	}

	return principles, nil
}

var genericPrinciplePattern = regexp.MustCompile(`(?i)\b(curl|grep|rg|ripgrep|jsonl|transcript path|session id|tool call|tool metadata|log files?|debugging workflow|searching)\b`)

func shouldKeepPrinciple(p db.Principle) bool {
	if p.Title == "" || p.Narrative == "" || p.Type == "" {
		return false
	}
	if p.ImpactScore < 0 {
		p.ImpactScore = 0
	}
	if p.ImpactScore > 1 {
		p.ImpactScore = 1
	}
	text := strings.ToLower(strings.TrimSpace(p.Title + " " + p.Narrative))
	if text == "" {
		return false
	}
	if genericPrinciplePattern.MatchString(text) {
		return false
	}
	for _, phrase := range []string{
		"use curl", "use grep", "use rg", "grep is useful", "logs help debugging",
		"log files help", "search the codebase", "inspect transcript", "session ids help",
	} {
		if strings.Contains(text, phrase) {
			return false
		}
	}
	return true
}

func extractPromptText(payload string) string {
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return ""
	}

	var parsed any
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		return compactWhitespace(payload)
	}

	return compactWhitespace(flattenPromptValue(parsed))
}

func flattenPromptValue(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			if text := flattenPromptValue(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, " ")
	case map[string]any:
		parts := make([]string, 0, 6)
		for _, key := range []string{"prompt", "user_prompt", "message", "text", "content", "input"} {
			if text := flattenPromptValue(v[key]); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, " ")
	default:
		return ""
	}
}

func compactWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// SummarizeEventPayload is the public export of summarizeEventPayload.
func SummarizeEventPayload(e db.Event) string {
	return summarizeEventPayload(e)
}

// ShortTS is the public export of shortTS.
func ShortTS(ts string) string {
	return shortT(ts)
}

func shortTS(ts string) string {
	return shortT(ts)
}

func shortT(ts string) string {
	if len(ts) >= 16 {
		return ts[:16]
	}
	if strings.TrimSpace(ts) == "" {
		return "unknown-time"
	}
	return ts
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// detectAndMarkConflicts compares newly inserted principles against existing
// active principles in the same project using the LLM. Any pair the model
// judges as semantically contradictory is marked conflicting and quarantined
// from agent context. Errors are logged by the caller; this function returns
// the first error encountered.
func (d *Distiller) detectAndMarkConflicts(newPrinciples []db.Principle) error {
	if d.db == nil {
		return nil
	}

	// Group new principles by project — each project gets one LLM call.
	byProject := make(map[string][]db.Principle)
	for _, p := range newPrinciples {
		byProject[p.ProjectID] = append(byProject[p.ProjectID], p)
	}

	for projectID, newPs := range byProject {
		existing, err := d.db.RecentPrinciplesByProjectAll(projectID, 50)
		if err != nil {
			return err
		}
		// Build the "old" set: existing active principles that are not in newPs.
		// Capped at 25 to keep the conflict prompt size predictable regardless of
		// how large the project's principle set grows.
		newIDs := make(map[string]bool, len(newPs))
		for _, p := range newPs {
			newIDs[p.ID] = true
		}
		var oldPs []db.Principle
		for _, e := range existing {
			if !newIDs[e.ID] && (e.Status == "" || e.Status == "active") {
				oldPs = append(oldPs, e)
				if len(oldPs) == 25 {
					break
				}
			}
		}
		if len(oldPs) == 0 {
			continue // nothing to compare against
		}

		prompt := buildConflictPrompt(newPs, oldPs)
		response, err := d.callLLM(prompt)
		if err != nil {
			return fmt.Errorf("conflict detection llm call: %w", err)
		}

		pairs, err := parseConflictPairs(response)
		if err != nil {
			// Malformed response — skip rather than crash distillation.
			fmt.Fprintf(os.Stderr, "forge: conflict parse: %v\n", err)
			continue
		}

		for _, pair := range pairs {
			if err := d.db.MarkConflicting(pair[0], pair[1]); err != nil {
				return err
			}
		}
	}
	return nil
}

// buildConflictPrompt constructs the prompt sent to the LLM for conflict detection.
func buildConflictPrompt(newPs, oldPs []db.Principle) string {
	var sb strings.Builder
	sb.WriteString("You are reviewing software engineering principles for semantic contradictions.\n\n")
	sb.WriteString("EXISTING PRINCIPLES:\n")
	for _, p := range oldPs {
		sb.WriteString(fmt.Sprintf("- [id:%s] %s — %s\n", p.ID, p.Title, p.Narrative))
	}
	sb.WriteString("\nNEW PRINCIPLES:\n")
	for _, p := range newPs {
		sb.WriteString(fmt.Sprintf("- [id:%s] %s — %s\n", p.ID, p.Title, p.Narrative))
	}
	sb.WriteString(`
Identify pairs where a NEW principle directly contradicts or is incompatible with an EXISTING principle.
Only flag genuine contradictions — different advice on the same topic, not merely related topics.

Return a JSON array of conflict pairs. Each element: {"a": "<existing_id>", "b": "<new_id>"}
Return [] if there are no contradictions.
Return ONLY the JSON array, no other text.`)
	return sb.String()
}

// parseConflictPairs extracts conflict ID pairs from the LLM response.
func parseConflictPairs(response string) ([][2]string, error) {
	response = strings.TrimSpace(response)
	// Strip any markdown fences.
	if idx := strings.Index(response, "["); idx >= 0 {
		if end := strings.LastIndex(response, "]"); end > idx {
			response = response[idx : end+1]
		}
	}
	var raw []struct {
		A string `json:"a"`
		B string `json:"b"`
	}
	if err := json.Unmarshal([]byte(response), &raw); err != nil {
		return nil, fmt.Errorf("parse JSON: %w (response: %.120s)", err, response)
	}
	var pairs [][2]string
	for _, r := range raw {
		if r.A != "" && r.B != "" {
			pairs = append(pairs, [2]string{r.A, r.B})
		}
	}
	return pairs, nil
}
