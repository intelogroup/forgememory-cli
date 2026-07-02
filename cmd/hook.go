package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/ipc"
	"github.com/forge/forge/internal/retrieve"
	"github.com/google/uuid"
)

// HookMessage is the message sent by hooks to the daemon.
type HookMessage struct {
	Type       string `json:"type"` // always "event"
	ID         string `json:"id"`
	TS         string `json:"ts"`
	SessionID  string `json:"session_id"`
	ProjectID  string `json:"project_id"`
	SourceTool string `json:"source_tool"`
	EventType  string `json:"event_type"`
	ToolName   string `json:"tool_name,omitempty"`
	Payload    string `json:"payload"`
}

// ClaudeHookInput is the common JSON structure used by Claude and Gemini hooks.
type ClaudeHookInput struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	CWD            string `json:"cwd"`
	PermissionMode string `json:"permission_mode"`
	HookEventName  string `json:"hook_event_name"`
	ToolName       string `json:"tool_name"`
	ToolInput      any    `json:"tool_input"`
	ToolResponse   any    `json:"tool_response,omitempty"`
}

// writeTool is the set of tools whose events are worth capturing.
var writeTool = map[string]bool{
	"Edit": true, "Write": true, "Bash": true,
	"NotebookEdit": true, "MultiEdit": true,
}

// privatePattern matches <private>...</private> blocks (case-insensitive, dotall).
var privatePattern = regexp.MustCompile(`(?si)<private>.*?</private>`)

// noisePayloadPatterns match Docker/CI build output that pollutes forge memory.
// These are high-confidence: their presence means the tool output is build noise,
// not meaningful coding activity worth remembering.
var noisePayloadPatterns = []*regexp.Regexp{
	// Docker build markers
	regexp.MustCompile(`(?m)^#\d+\s*\[(builder|stage|internal)\]`),
	regexp.MustCompile(`(?m)extracting sha256:`),
	regexp.MustCompile(`(?m)^\d+\.\d+Z\s+#\d+`),
	regexp.MustCompile(`(?m)^sha256:[a-f0-9]{64}\s+\d+\.\d+[KMGT]B\s+/\s+\d+\.\d+[KMGT]B`),
	regexp.MustCompile(`(?m)Dockerfile:\d+`),
	// Docker build error
	regexp.MustCompile(`failed to solve:`),
	// CI/CD noise (GitHub Actions, etc.)
	regexp.MustCompile(`##\[(error|warning|debug|notice)\]`),
	regexp.MustCompile(`::(error|warning|debug|notice)::`),
	// Build tool noise
	regexp.MustCompile(`(?m)^make(\[\d+\])?: \*\*\*`),
	regexp.MustCompile(`(?m)^\s*npm\s+(notice|warn|ERR)[!]?\s`),
}

// isNoisePayload reports whether the tool output payload is build/deploy noise
// that should not be stored as a forge event.
func isNoisePayload(payload string) bool {
	if len(payload) < 100 {
		return false
	}
	for _, p := range noisePayloadPatterns {
		if p.MatchString(payload) {
			return true
		}
	}
	return false
}

var promptFieldPriority = map[string]int{
	"prompt":      0,
	"user_prompt": 0,
	"message":     1,
	"text":        1,
	"content":     2,
	"input":       3,
	"query":       3,
}

var recallStopWords = map[string]bool{
	"a": true, "about": true, "after": true, "agent": true, "all": true,
	"also": true, "and": true, "are": true, "been": true, "before": true,
	"build": true, "change": true, "claude": true, "codex": true, "create": true,
	"feature": true, "for": true, "from": true, "gemini": true, "have": true,
	"implement": true, "into": true, "just": true, "make": true, "need": true,
	"next": true, "prompt": true, "project": true, "repo": true, "same": true,
	"session": true, "some": true, "that": true, "the": true, "their": true,
	"there": true, "they": true, "this": true, "tool": true, "user": true,
	"using": true, "want": true, "with": true, "work": true, "working": true,
}

type promptCandidate struct {
	priority int
	text     string
}

type promptRecallMatch struct {
	SourceType   string
	ProjectID    string
	Narrative    string
	Title        string
	TS           string
	Score        float64
	MatchedTerms []string
	Outcome      string // "success" | "failure" | "unknown"
	ImplHint     string // lean implementation fingerprint (≤120 chars)
}

// isWriteTool reports whether tool events should be captured.
func isWriteTool(name string) bool {
	return writeTool[name]
}

// stripPrivate removes <private>...</private> blocks from a string.
func stripPrivate(s string) string {
	return privatePattern.ReplaceAllString(s, "[redacted]")
}

// isSessionEndEvent reports whether this event type signals end of session.
func isSessionEndEvent(eventType string) bool {
	switch eventType {
	case "Stop", "SessionEnd", "AfterAgent":
		return true
	}
	return false
}

// runHook is the entrypoint for `forge hook`.
// It reads the hook payload from stdin, packages it, and sends to the daemon.
// Fails silently in <1ms if daemon is down.
func runHook(args []string) {
	// Parse --event flag used by Codex hook invocations (e.g. forge hook --event Stop).
	// Claude/Gemini hooks pass event type via FORGE_EVENT_TYPE env var instead.
	fs := flag.NewFlagSet("hook", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	eventFlag := fs.String("event", "", "")
	sourceFlag := fs.String("source", "", "")
	_ = fs.Parse(args)

	sourceTool := envOr("FORGE_SOURCE_TOOL", "")
	eventType := envOr("FORGE_EVENT_TYPE", "")

	// If env vars not set but --event was provided, this is a Codex invocation.
	if *eventFlag != "" {
		if eventType == "" {
			eventType = *eventFlag
		}
		if sourceTool == "" && *sourceFlag == "" {
			sourceTool = "codex"
		}
	}
	if *sourceFlag != "" && sourceTool == "" {
		sourceTool = *sourceFlag
	}
	if sourceTool == "" {
		sourceTool = "unknown"
	}
	if eventType == "" {
		eventType = "unknown"
	}

	// Read and strip private data from payload
	payload := stripPrivate(string(readStdin()))

	// Skip build/deploy noise — Docker logs, CI output pollutes memory.
	if isNoisePayload(payload) {
		os.Exit(0)
	}

	// Parse hook input to extract session/tool metadata.
	input, eventType := parseHookInput(payload, eventType)

	sessionID := input.SessionID
	if sessionID == "" {
		sessionID = envOr("FORGE_SESSION_ID", "unknown")
	}

	toolName := input.ToolName
	if toolName == "" {
		toolName = envOr("FORGE_TOOL_NAME", "")
	}

	projectID := detectProjectIDForPath(input.CWD)

	// Session end: synthesize summary asynchronously then forward event.
	if isSessionEndEvent(eventType) {
		spawnSessionSynthesis(sessionID, projectID, "final", checkpointKey(sessionID, "final", ""), "")
		// Fall through — still record the stop event
	}

	// For PostToolUse, only capture write-type tools
	if eventType == "PostToolUse" && toolName != "" && !isWriteTool(toolName) {
		os.Exit(0)
	}

	msg := HookMessage{
		Type:       "event",
		ID:         uuid.New().String(),
		TS:         time.Now().UTC().Format(time.RFC3339),
		ProjectID:  projectID,
		SourceTool: sourceTool,
		EventType:  eventType,
		SessionID:  sessionID,
		ToolName:   toolName,
		Payload:    payload,
	}

	if err := ipc.Send(msg); err != nil {
		// Silent failure — daemon is down, hook exits in <1ms
		if eventType != "UserPromptSubmit" {
			os.Exit(0)
		}
	}

	if eventType == "UserPromptSubmit" {
		if kind := promptCheckpointKind(payload); kind != "" {
			spawnSessionSynthesis(sessionID, projectID, kind, checkpointKey(sessionID, kind, msg.TS), msg.TS)
		}
	}

	// Prompt recall: inject recent context after persisting the latest prompt so
	// both project-local summaries and cross-project matches can use it.
	if eventType == "UserPromptSubmit" {
		handleSessionRecall(projectID, payload)
		os.Exit(0)
	}
	os.Exit(0)
}

// handleSessionRecall injects recent memories as hookSpecificOutput.
// Opens the DB directly (read-only under WAL mode — safe with concurrent daemon writes).
func handleSessionRecall(projectID, payload string) {
	database, err := db.Open("")
	if err != nil {
		return
	}
	defer database.Close()

	sessionID := envOr("FORGE_SESSION_ID", "")
	isFirst := isFirstPromptOfSession(database, sessionID)

	promptText := extractPromptText(payload)
	_ = retrieve.EnqueuePromptRetrieval(database, projectID, promptText)
	principles, summaries, alerts, externalSummaries, promptMatch, distillError := loadSessionRecallContext(database, projectID, promptText)
	if shouldWaitForOfficialHint(alerts, externalSummaries) && hasPendingFailureRetrieval(database, projectID) {
		waitForOfficialHint(database, projectID)
		principles, summaries, alerts, externalSummaries, promptMatch, distillError = loadSessionRecallContext(database, projectID, promptText)
	}

	// On session start, promote the last session summary to full detail.
	var lastSession *db.SessionSummary
	if isFirst && projectID != "" {
		lastSession = loadLastProjectSession(database, projectID, sessionID)
	}

	// For subsequent prompts, suppress generic/unmatched recent lessons and active principles
	// to avoid repetitive, unrelated context injection.
	if !isFirst {
		principles = nil
		summaries = nil
	}

	text := buildSessionRecallOutput(projectID, summaries, principles, alerts, externalSummaries, promptMatch, lastSession, distillError)
	if text == "" {
		return
	}
	output := map[string]interface{}{
		"hookSpecificOutput": map[string]interface{}{
			"hookEventName":     "UserPromptSubmit",
			"additionalContext": text,
		},
	}
	data, _ := json.Marshal(output)
	fmt.Println(string(data))
}

// isFirstPromptOfSession returns true when no prior events exist for this session ID.
func isFirstPromptOfSession(database *db.DB, sessionID string) bool {
	if sessionID == "" || sessionID == "unknown" {
		return false
	}
	events, err := database.SessionEvents(sessionID, 1)
	return err == nil && len(events) == 0
}

// loadLastProjectSession returns the most recent session summary for a project,
// excluding the current session (which has not been synthesized yet).
func loadLastProjectSession(database *db.DB, projectID, currentSessionID string) *db.SessionSummary {
	summaries, err := database.GetRecentSessionSummariesByProject(projectID, 5)
	if err != nil {
		return nil
	}
	for i := range summaries {
		if summaries[i].SessionID != currentSessionID {
			return &summaries[i]
		}
	}
	return nil
}

func loadSessionRecallContext(database *db.DB, projectID, promptText string) ([]db.Principle, []db.SessionSummary, []db.Alert, []db.ExternalContextSummary, *promptRecallMatch, string) {
	principles, _ := database.RecentPrinciplesByProject(projectID, 2)

	// Load more summaries than needed and score them against the current prompt
	// for relevance, so the most contextually relevant lessons are injected.
	tokens := recallTokens(promptText)
	summaries := loadRankedSummaries(database, projectID, tokens, 10)

	alerts, _ := database.ActiveAlertsByProject(projectID, 2)
	externalSummaries, _ := database.FreshExternalContextSummariesByProject(projectID, 2)
	if projectID != "" && len(principles) == 0 && len(summaries) == 0 {
		principles, _ = database.RecentPrinciplesByProject("", 2)
		summaries = loadRankedSummaries(database, "", tokens, 10)
	}

	distillError := ""
	if h, err := database.GetDistillationHealth(); err == nil && h.LastStatus == "failed" {
		distillError = h.LastErrorMessage
	}

	return principles, summaries, alerts, externalSummaries, findBestPromptRecall(database, projectID, promptText), distillError
}

// loadRankedSummaries loads recent session summaries and returns the top n
// ranked by prompt-token relevance, falling back to most recent if no prompt
// tokens are available.
func loadRankedSummaries(database *db.DB, projectID string, tokens []string, n int) []db.SessionSummary {
	loadLimit := n * 3
	var summaries []db.SessionSummary
	var err error
	if projectID != "" {
		summaries, err = database.GetRecentSessionSummariesByProject(projectID, loadLimit)
	} else {
		summaries, err = database.GetRecentSessionSummaries(loadLimit)
	}
	if err != nil || len(summaries) == 0 {
		return summaries
	}

	// If few or no tokens, fall back to most recent.
	if len(tokens) < 2 {
		if len(summaries) > n {
			summaries = summaries[:n]
		}
		return summaries
	}

	type scored struct {
		summary db.SessionSummary
		score   float64
	}
	var scoredList []scored
	for _, s := range summaries {
		if match, ok := scorePromptSessionSummaryMatch(s, tokens); ok {
			scoredList = append(scoredList, scored{s, match.Score})
		} else {
			scoredList = append(scoredList, scored{s, 0})
		}
	}

	sort.SliceStable(scoredList, func(i, j int) bool {
		if scoredList[i].score == scoredList[j].score {
			return scoredList[i].summary.TS > scoredList[j].summary.TS
		}
		return scoredList[i].score > scoredList[j].score
	})

	out := make([]db.SessionSummary, 0, n)
	for i := 0; i < len(scoredList) && i < n; i++ {
		out = append(out, scoredList[i].summary)
	}
	if len(out) == 0 && len(summaries) > 0 {
		if len(summaries) > n {
			summaries = summaries[:n]
		}
		return summaries
	}
	return out
}

func shouldWaitForOfficialHint(alerts []db.Alert, externalSummaries []db.ExternalContextSummary) bool {
	for _, summary := range externalSummaries {
		if summary.Source == "context7" &&
			summary.SummaryKind != "docs_summary" &&
			strings.TrimSpace(firstNonEmpty(summary.Hint, summary.Narrative, summary.Title)) != "" {
			return false
		}
	}
	for _, alert := range alerts {
		if alert.AlertType == "repeated_failure" {
			return true
		}
	}
	return false
}

func hasPendingFailureRetrieval(database *db.DB, projectID string) bool {
	if database == nil || strings.TrimSpace(projectID) == "" {
		return false
	}
	jobs, err := database.RetrievalJobsByProject(projectID, 6)
	if err != nil {
		return false
	}
	for _, job := range jobs {
		if job.TriggerType != "failure" || job.Source != "context7" {
			continue
		}
		if job.Status == "queued" || job.Status == "running" {
			return true
		}
	}
	return false
}

func waitForOfficialHint(database *db.DB, projectID string) {
	if database == nil || strings.TrimSpace(projectID) == "" {
		return
	}
	deadline := time.Now().Add(recallWaitBudget())
	poll := recallPollInterval()
	for time.Now().Before(deadline) {
		summaries, err := database.FreshExternalContextSummariesByProject(projectID, 2)
		if err == nil {
			for _, summary := range summaries {
				if summary.Source == "context7" &&
					summary.SummaryKind != "docs_summary" &&
					strings.TrimSpace(firstNonEmpty(summary.Hint, summary.Narrative, summary.Title)) != "" {
					return
				}
			}
		}
		if !hasPendingFailureRetrieval(database, projectID) {
			return
		}
		time.Sleep(poll)
	}
}

func recallWaitBudget() time.Duration {
	ms := envInt("FORGE_HOOK_RECALL_WAIT_MS", 2500)
	if ms < 0 {
		ms = 0
	}
	if ms > 5000 {
		ms = 5000
	}
	return time.Duration(ms) * time.Millisecond
}

func recallPollInterval() time.Duration {
	ms := envInt("FORGE_HOOK_RECALL_POLL_MS", 100)
	if ms < 25 {
		ms = 25
	}
	if ms > 250 {
		ms = 250
	}
	return time.Duration(ms) * time.Millisecond
}

func envInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return n
}

// spawnSessionSynthesis detaches a `forge synthesize-session` process so the
// hook can return immediately without blocking on an LLM call.
func spawnSessionSynthesis(sessionID, projectID, checkpointKind, checkpointKey, cutoffTS string) {
	if sessionID == "" || sessionID == "unknown" {
		return
	}
	cmd := exec.Command(os.Args[0], "synthesize-session",
		"--session-id", sessionID,
		"--project-id", projectID,
		"--checkpoint-kind", checkpointKind,
		"--checkpoint-key", checkpointKey)
	if cutoffTS != "" {
		cmd.Args = append(cmd.Args, "--cutoff-ts", cutoffTS)
	}
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil
	_ = startBackground(cmd)
}

func readStdin() []byte {
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return nil
	}
	return data
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseHookInput(payload, currentEventType string) (ClaudeHookInput, string) {
	var input ClaudeHookInput
	if strings.TrimSpace(payload) == "" {
		return input, currentEventType
	}
	if err := json.Unmarshal([]byte(payload), &input); err != nil {
		return input, currentEventType
	}
	if input.HookEventName != "" && currentEventType == "unknown" {
		currentEventType = input.HookEventName
	}
	return input, currentEventType
}

func promptCheckpointKind(payload string) string {
	prompt := strings.ToLower(strings.TrimSpace(extractPromptText(payload)))
	switch {
	case strings.HasPrefix(prompt, "/clear"):
		return "clear"
	case strings.HasPrefix(prompt, "/compact"):
		return "compact"
	default:
		return ""
	}
}

func checkpointKey(sessionID, kind, suffix string) string {
	if suffix == "" {
		return sessionID + ":" + kind
	}
	return sessionID + ":" + kind + ":" + suffix
}

func buildSessionRecallOutput(projectID string, summaries []db.SessionSummary, principles []db.Principle, alerts []db.Alert, externalSummaries []db.ExternalContextSummary, promptMatch *promptRecallMatch, lastSession *db.SessionSummary, distillError string) string {
	var sentences []string

	matchThreshold := 2.0
	if promptMatch != nil && promptMatch.ImplHint != "" {
		matchThreshold = 1.5
	}
	if promptMatch != nil && promptMatch.Score >= matchThreshold {
		source := displayProjectName(promptMatch.ProjectID)
		if promptMatch.ImplHint != "" {
			outcome := promptMatch.Outcome
			label := "Past " + outcome
			if outcome == "" || outcome == "unknown" {
				label = "Prior"
			}
			title := promptMatch.Title
			if title == "" {
				title = promptMatch.SourceType
			}
			sentences = append(sentences, fmt.Sprintf(
				"%s [%s]: %s — %s.",
				label,
				source,
				title,
				promptMatch.ImplHint,
			))
		} else {
			sentences = append(sentences, fmt.Sprintf(
				"Prompt-matched %s from %s (%s confidence): %s.",
				promptMatch.SourceType,
				source,
				confidenceLabel(promptMatch.Score),
				trimSentence(promptMatch.Narrative),
			))
		}
	}

	// Docs/API insights
	hasOfficialHint := false
	for _, summary := range externalSummaries {
		label := summary.Source
		if summary.LibraryName != "" && summary.LibraryName != summary.Source {
			label = summary.Source + " " + summary.LibraryName
		}
		summaryText := trimSentence(firstNonEmpty(summary.Hint, summary.Narrative, summary.Title))
		if summary.Source == "context7" && summary.SummaryKind != "docs_summary" {
			hasOfficialHint = true
			sentences = append(sentences, fmt.Sprintf("Official docs hint from %s: %s.", label, summaryText))
		} else {
			sentences = append(sentences, fmt.Sprintf("Cached docs insight from %s: %s.", label, summaryText))
		}
		break
	}

	if !hasOfficialHint {
		for _, alert := range alerts {
			if alert.AlertType != "repeated_failure" {
				continue
			}
			sentences = append(sentences, fmt.Sprintf("Active repeated failure: %s.", trimSentence(firstNonEmpty(alert.Narrative, alert.Title))))
			break
		}
	}



	if distillError != "" && len(sentences) > 0 {
		errMsg := distillError
		if len(errMsg) > 80 {
			errMsg = errMsg[:80] + "..."
		}
		sentences = append([]string{fmt.Sprintf("[System: Memory layer degraded. Distillation failing: %s. Run 'forge health' to diagnose.]", errMsg)}, sentences...)
	}

	if len(sentences) == 0 {
		return ""
	}

	return "<forge-context>\n## Forge Context\n" + strings.Join(sentences, " ") + "\n</forge-context>"
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

	var candidates []promptCandidate
	collectPromptCandidates(parsed, "", &candidates)
	if len(candidates) == 0 {
		return compactWhitespace(payload)
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].priority < candidates[j].priority
	})

	var merged []string
	seen := make(map[string]bool)
	for _, candidate := range candidates {
		text := compactWhitespace(candidate.text)
		if text == "" || seen[text] {
			continue
		}
		seen[text] = true
		merged = append(merged, text)
		if len(merged) == 3 {
			break
		}
	}
	return strings.TrimSpace(strings.Join(merged, " "))
}

func collectPromptCandidates(node any, parentKey string, out *[]promptCandidate) {
	switch value := node.(type) {
	case map[string]any:
		role, _ := value["role"].(string)
		role = strings.TrimSpace(role)
		if strings.EqualFold(role, "user") {
			for _, key := range []string{"content", "text", "message", "input"} {
				if text := flattenPromptValue(value[key]); text != "" {
					*out = append(*out, promptCandidate{priority: 0, text: text})
				}
			}
		}

		roleScoped := role != ""
		if !roleScoped || strings.EqualFold(role, "user") {
			for key, priority := range promptFieldPriority {
				if text := flattenPromptValue(value[key]); text != "" {
					*out = append(*out, promptCandidate{priority: priority, text: text})
				}
			}
		}
		for key, child := range value {
			if roleScoped && !strings.EqualFold(role, "user") {
				if _, skip := promptFieldPriority[key]; skip {
					continue
				}
			}
			collectPromptCandidates(child, key, out)
		}
	case []any:
		for _, child := range value {
			collectPromptCandidates(child, parentKey, out)
		}
	case string:
		if _, ok := promptFieldPriority[parentKey]; ok {
			*out = append(*out, promptCandidate{priority: promptFieldPriority[parentKey], text: value})
		}
	}
}

func flattenPromptValue(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case []any:
		var parts []string
		for _, item := range v {
			if text := flattenPromptValue(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, " ")
	case map[string]any:
		var parts []string
		for _, key := range []string{"text", "content", "message", "prompt", "input"} {
			if text := flattenPromptValue(v[key]); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, " ")
	default:
		return ""
	}
}

func findBestPromptRecall(database *db.DB, projectID, promptText string) *promptRecallMatch {
	tokens := recallTokens(promptText)
	if len(tokens) < 2 {
		return nil
	}

	var matches []promptRecallMatch
	matches = append(matches, findPromptRelevantPrinciples(database, projectID, tokens)...)
	matches = append(matches, findPromptRelevantSessionSummaries(database, projectID, tokens)...)
	if len(matches) == 0 {
		return nil
	}

	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].Score == matches[j].Score {
			return matches[i].TS > matches[j].TS
		}
		return matches[i].Score > matches[j].Score
	})
	return &matches[0]
}

func findPromptRelevantPrinciples(database *db.DB, projectID string, tokens []string) []promptRecallMatch {
	// Scan same-project principles first (cheaper, higher signal). If we find a
	// high-confidence match there, skip the cross-project scan entirely.
	var matches []promptRecallMatch
	seen := make(map[string]bool)

	if projectID != "" {
		same, err := database.RecentPrinciplesByProject(projectID, 50)
		if err == nil {
			for _, p := range same {
				match, ok := scorePromptPrincipleMatch(p, tokens)
				if !ok || seen[p.Fingerprint] {
					continue
				}
				seen[p.Fingerprint] = true
				matches = append(matches, match)
			}
		}
		// If we already have a high-confidence same-project hit, skip cross-project.
		// Principles with a concrete impl_hint are promoted at a lower threshold (1.5).
		for _, m := range matches {
			threshold := 2.0
			if m.ImplHint != "" {
				threshold = 1.5
			}
			if m.Score >= threshold {
				sort.SliceStable(matches, func(i, j int) bool {
					if matches[i].Score == matches[j].Score {
						return matches[i].TS > matches[j].TS
					}
					return matches[i].Score > matches[j].Score
				})
				return matches
			}
		}
	}

	// Cross-project scan — active only, capped at 100.
	cross, err := database.RecentActivePrinciples(100)
	if err != nil {
		return matches
	}
	for _, p := range cross {
		if sameProject(projectID, p.ProjectID) {
			continue
		}
		match, ok := scorePromptPrincipleMatch(p, tokens)
		if !ok || seen[p.Fingerprint] {
			continue
		}
		seen[p.Fingerprint] = true
		matches = append(matches, match)
	}

	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].Score == matches[j].Score {
			return matches[i].TS > matches[j].TS
		}
		return matches[i].Score > matches[j].Score
	})
	return matches
}

func scorePromptPrincipleMatch(principle db.Principle, tokens []string) (promptRecallMatch, bool) {
	score, matched, ok := scorePromptMatch(strings.Join([]string{
		principle.Title,
		principle.Narrative,
		strings.Join(principle.Concepts, " "),
		strings.Join(principle.FilesModified, " "),
	}, " "), principle.TS, tokens, principle.ImpactScore, 1.8, 1.2)
	if !ok {
		return promptRecallMatch{}, false
	}

	return promptRecallMatch{
		SourceType:   "principle",
		ProjectID:    principle.ProjectID,
		Narrative:    principle.Narrative,
		Title:        principle.Title,
		TS:           principle.TS,
		Score:        score,
		MatchedTerms: matched,
		Outcome:      principle.Outcome,
		ImplHint:     principle.ImplHint,
	}, true
}

func findPromptRelevantSessionSummaries(database *db.DB, projectID string, tokens []string) []promptRecallMatch {
	var matches []promptRecallMatch
	seen := make(map[string]bool)

	if projectID != "" {
		same, err := database.GetRecentSessionSummariesByProject(projectID, 20)
		if err == nil {
			for _, s := range same {
				match, ok := scorePromptSessionSummaryMatch(s, tokens)
				if !ok || seen[s.SessionID] {
					continue
				}
				seen[s.SessionID] = true
				matches = append(matches, match)
			}
		}
		for _, m := range matches {
			if m.Score >= 2.0 {
				return matches
			}
		}
	}

	// Cross-project scan — capped at 80 most recent summaries.
	cross, err := database.GetRecentSessionSummaries(80)
	if err != nil {
		return matches
	}
	for _, s := range cross {
		if sameProject(projectID, s.ProjectID) {
			continue
		}
		match, ok := scorePromptSessionSummaryMatch(s, tokens)
		if !ok || seen[s.SessionID] {
			continue
		}
		seen[s.SessionID] = true
		matches = append(matches, match)
	}
	return matches
}

func scorePromptSessionSummaryMatch(summary db.SessionSummary, tokens []string) (promptRecallMatch, bool) {
	narrative := firstNonEmpty(summary.Learnings, summary.Investigation, summary.NextSteps, summary.Summary, summary.Request)
	if strings.TrimSpace(narrative) == "" {
		return promptRecallMatch{}, false
	}

	quality := 0.55
	if strings.TrimSpace(summary.Learnings) != "" {
		quality = 0.78
	} else if strings.TrimSpace(summary.Investigation) != "" {
		quality = 0.68
	}

	score, matched, ok := scorePromptMatch(strings.Join([]string{
		summary.Request,
		summary.Investigation,
		summary.Learnings,
		summary.NextSteps,
		summary.Summary,
		summary.Keywords,
	}, " "), summary.TS, tokens, quality, 1.55, 1.15)
	if !ok {
		return promptRecallMatch{}, false
	}

	return promptRecallMatch{
		SourceType:   "session lesson",
		ProjectID:    summary.ProjectID,
		Narrative:    narrative,
		TS:           summary.TS,
		Score:        score,
		MatchedTerms: matched,
	}, true
}

func scorePromptMatch(haystack, ts string, tokens []string, quality, overlapWeight, threshold float64) (float64, []string, bool) {
	haystackTokens := tokenSet(haystack)
	var matched []string
	for _, token := range tokens {
		if haystackTokens[token] {
			matched = append(matched, token)
		}
	}

	if len(matched) == 0 {
		return 0, nil, false
	}
	if len(matched) < 2 {
		if len(matched[0]) < 8 || quality < 0.75 {
			return 0, nil, false
		}
	}

	focus := len(tokens)
	if focus > 4 {
		focus = 4
	}
	if focus == 0 {
		return 0, nil, false
	}

	score := (float64(len(matched)) / float64(focus)) * overlapWeight
	score += quality * 0.9
	score += recentnessScore(ts)
	if score < threshold {
		return 0, nil, false
	}
	return score, matched, true
}

func recentnessScore(ts string) float64 {
	parsed, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return 0
	}
	age := time.Since(parsed)
	switch {
	case age <= 7*24*time.Hour:
		return 0.35
	case age <= 30*24*time.Hour:
		return 0.2
	case age <= 180*24*time.Hour:
		return 0.1
	default:
		return 0
	}
}

func recallTokens(text string) []string {
	return sortedKeys(tokenSet(text))
}

func tokenSet(text string) map[string]bool {
	parts := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	set := make(map[string]bool)
	for _, part := range parts {
		if len(part) < 3 || recallStopWords[part] {
			continue
		}
		set[part] = true
	}
	return set
}

func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) > 12 {
		keys = keys[:12]
	}
	return keys
}

func sameProject(a, b string) bool {
	if strings.TrimSpace(a) == "" || strings.TrimSpace(b) == "" {
		return false
	}
	if a == b {
		return true
	}
	return filepath.Base(a) == filepath.Base(b)
}

func displayProjectName(projectID string) string {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return "another project"
	}
	return filepath.Base(projectID)
}

func confidenceLabel(score float64) string {
	switch {
	case score >= 2.0:
		return "high"
	case score >= 1.45:
		return "medium"
	default:
		return "low"
	}
}

func compactWhitespace(text string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func trimSentence(text string) string {
	text = strings.TrimSpace(text)
	text = strings.TrimRight(text, ".!?\n\r\t ")
	return text
}
