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

	"github.com/forge/forge/internal/coach"
	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/ipc"
	"github.com/forge/forge/internal/retrieve"
	"github.com/forge/forge/internal/tokens"
	"github.com/google/uuid"
)

// HookMessage is the message sent by hooks to the daemon.
type HookMessage struct {
	Type           string `json:"type"` // always "event"
	ID             string `json:"id"`
	TS             string `json:"ts"`
	TraceID        string `json:"trace_id,omitempty"`
	SpanID         string `json:"span_id,omitempty"`
	ParentSpanID   string `json:"parent_span_id,omitempty"`
	Sequence       int64  `json:"sequence,omitempty"`
	DurationMS     int64  `json:"duration_ms,omitempty"`
	Status         string `json:"status,omitempty"`
	ExitCode       int    `json:"exit_code,omitempty"`
	Model          string `json:"model,omitempty"`
	TaskID         string `json:"task_id,omitempty"`
	CWD            string `json:"cwd,omitempty"`
	GitBranch      string `json:"git_branch,omitempty"`
	GitCommit      string `json:"git_commit,omitempty"`
	Files          string `json:"files,omitempty"`
	TranscriptPath string `json:"transcript_path,omitempty"`
	SessionID      string `json:"session_id"`
	ProjectID      string `json:"project_id"`
	ProjectRoot    string `json:"project_root,omitempty"`
	SourceTool     string `json:"source_tool"`
	EventType      string `json:"event_type"`
	ToolName       string `json:"tool_name,omitempty"`
	Payload        string `json:"payload"`
}

// ClaudeHookInput is the common JSON structure used by Claude and Gemini hooks.
// Custom UnmarshalJSON handles multiple session ID field names across agents.
type ClaudeHookInput struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	CWD            string `json:"cwd"`
	PermissionMode string `json:"permission_mode"`
	HookEventName  string `json:"hook_event_name"`
	ToolName       string `json:"tool_name"`
	ToolInput      any    `json:"tool_input"`
	ToolResponse   any    `json:"tool_response,omitempty"`
	Sequence       int64  `json:"sequence,omitempty"`
	DurationMS     int64  `json:"duration_ms,omitempty"`
	Status         string `json:"status,omitempty"`
	ExitCode       int    `json:"exit_code,omitempty"`
	Model          string `json:"model,omitempty"`
	ParentSpanID   string `json:"parent_span_id,omitempty"`
	exitCodeSet    bool
}

func (c *ClaudeHookInput) UnmarshalJSON(data []byte) error {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for _, key := range []string{"session_id", "sessionID", "conversationId"} {
		if v, ok := raw[key].(string); ok && v != "" {
			c.SessionID = v
			break
		}
	}
	if v, ok := raw["transcript_path"].(string); ok {
		c.TranscriptPath = v
	}
	if v, ok := raw["cwd"].(string); ok {
		c.CWD = v
	}
	if v, ok := raw["permission_mode"].(string); ok {
		c.PermissionMode = v
	}
	if v, ok := raw["hook_event_name"].(string); ok {
		c.HookEventName = v
	}
	if v, ok := raw["tool_name"].(string); ok {
		c.ToolName = v
	}
	c.ToolInput = raw["tool_input"]
	c.ToolResponse = raw["tool_response"]
	c.Sequence = int64Value(raw["sequence"])
	if c.Sequence == 0 {
		c.Sequence = int64Value(raw["event_sequence"])
	}
	c.DurationMS = int64Value(raw["duration_ms"])
	if c.DurationMS == 0 {
		c.DurationMS = int64Value(raw["elapsed_ms"])
	}
	c.Status = stringValue(raw["status"])
	if _, ok := raw["exit_code"]; ok {
		c.ExitCode = int(int64Value(raw["exit_code"]))
		c.exitCodeSet = true
	}
	c.Model = firstStringValue(raw, "model", "model_name")
	c.ParentSpanID = firstStringValue(raw, "parent_span_id", "parent_span")
	return nil
}

func stringValue(value any) string {
	s, _ := value.(string)
	return s
}

func firstStringValue(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := stringValue(values[key]); value != "" {
			return value
		}
	}
	return ""
}

func int64Value(value any) int64 {
	switch n := value.(type) {
	case float64:
		return int64(n)
	case float32:
		return int64(n)
	case int:
		return int64(n)
	case int64:
		return n
	case json.Number:
		parsed, _ := n.Int64()
		return parsed
	case string:
		parsed, _ := strconv.ParseInt(strings.TrimSpace(n), 10, 64)
		return parsed
	default:
		return 0
	}
}

func hookEventMetadata(input ClaudeHookInput) (status string, exitCode int, durationMS, sequence int64, model, parentSpanID string) {
	exitCode = -1
	hadExitCode := input.exitCodeSet
	if input.exitCodeSet {
		exitCode = input.ExitCode
	}
	durationMS = input.DurationMS
	sequence = input.Sequence
	model = input.Model
	parentSpanID = input.ParentSpanID
	status = input.Status
	if response, ok := input.ToolResponse.(map[string]any); ok {
		if durationMS == 0 {
			durationMS = int64Value(response["duration_ms"])
		}
		if !input.exitCodeSet {
			exitCode = int(int64Value(response["exit_code"]))
			if _, ok := response["exit_code"]; ok {
				input.exitCodeSet = true
				hadExitCode = true
			}
		}
		if status == "" {
			status = stringValue(response["status"])
			if status == "" {
				if failed, ok := response["is_error"].(bool); ok && failed {
					status = "error"
				}
			}
		}
	}
	if status == "" && hadExitCode {
		if exitCode > 0 {
			status = "error"
		} else {
			status = "success"
		}
	}
	return
}

func extractFilePaths(payload string) string {
	var root any
	if json.Unmarshal([]byte(payload), &root) != nil {
		return "[]"
	}
	seen := map[string]bool{}
	var walk func(any)
	walk = func(value any) {
		switch node := value.(type) {
		case map[string]any:
			for key, child := range node {
				if key == "file_path" || key == "filePath" || key == "path" {
					if path, ok := child.(string); ok && path != "" {
						seen[path] = true
					}
				}
				walk(child)
			}
		case []any:
			for _, child := range node {
				walk(child)
			}
		}
	}
	walk(root)
	paths := make([]string, 0, len(seen))
	for path := range seen {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	encoded, err := json.Marshal(paths)
	if err != nil {
		return "[]"
	}
	return string(encoded)
}

// writeTool is the set of tools whose events are worth capturing.
var writeTool = map[string]bool{
	"Edit": true, "Write": true, "Bash": true,
	"NotebookEdit": true, "MultiEdit": true,
}

const (
	observabilityMinimal  = "minimal"
	observabilityStandard = "standard"
	observabilityForensic = "forensic"
)

// observabilityMode controls how much agent activity hooks retain.
// Minimal preserves Forge's historical low-noise behavior. Standard captures
// every tool result. Forensic additionally retains build/CI payloads so failed
// evaluations can be reconstructed from raw evidence.
func observabilityMode() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("FORGE_OBSERVABILITY_MODE"))) {
	case observabilityStandard:
		return observabilityStandard
	case observabilityForensic:
		return observabilityForensic
	default:
		return observabilityMinimal
	}
}

func shouldCapturePostToolUse(mode, toolName string) bool {
	if mode == observabilityStandard || mode == observabilityForensic {
		return true
	}
	return toolName == "" || isWriteTool(toolName)
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

var recallStopWords = tokens.CommonStopWords

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
	sourceFlag := fs.String("source", "", "source tool emitting this event: claude, codex, gemini, opencode, antigravity, or a custom runtime agent name (e.g. clixen)")
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

	mode := observabilityMode()
	// Skip build/deploy noise in normal modes. Forensic mode keeps it so an
	// evaluation can reconstruct failures that would otherwise be discarded.
	if mode != observabilityForensic && isNoisePayload(payload) {
		os.Exit(0)
	}

	// Parse hook input to extract session/tool metadata.
	input, eventType := parseHookInput(payload, eventType)
	status, exitCode, durationMS, sequence, model, parentSpanID := hookEventMetadata(input)

	sessionID := input.SessionID
	if sessionID == "" {
		sessionID = envOr("FORGE_SESSION_ID", "unknown")
	}

	toolName := input.ToolName
	if toolName == "" {
		toolName = envOr("FORGE_TOOL_NAME", "")
	}

	projectID, projectRoot := resolveProjectRoot(input.CWD)
	gitBranch, gitCommit := gitState(projectRoot)

	// Session end: synthesize summary asynchronously then forward event.
	if isSessionEndEvent(eventType) {
		spawnSessionSynthesis(sessionID, projectID, "final", checkpointKey(sessionID, "final", ""), "")
		// Fall through — still record the stop event
	}

	// Minimal mode captures write-type tools; standard and forensic modes capture
	// every PostToolUse event.
	if eventType == "PostToolUse" && !shouldCapturePostToolUse(mode, toolName) {
		os.Exit(0)
	}

	msg := HookMessage{
		Type:           "event",
		ID:             uuid.New().String(),
		TS:             time.Now().UTC().Format(time.RFC3339),
		TraceID:        sessionID,
		SpanID:         uuid.New().String(),
		ParentSpanID:   parentSpanID,
		Sequence:       sequence,
		DurationMS:     durationMS,
		Status:         status,
		ExitCode:       exitCode,
		Model:          model,
		TaskID:         os.Getenv("FORGE_TASK_ID"),
		CWD:            input.CWD,
		GitBranch:      gitBranch,
		GitCommit:      gitCommit,
		Files:          extractFilePaths(payload),
		TranscriptPath: input.TranscriptPath,
		ProjectID:      projectID,
		ProjectRoot:    projectRoot,
		SourceTool:     sourceTool,
		EventType:      eventType,
		SessionID:      sessionID,
		ToolName:       toolName,
		Payload:        payload,
	}

	if err := ipc.Send(msg); err != nil {
		// Preserve the event locally when the daemon is down. The hook still exits
		// quickly; the daemon replays the outbox on its next startup.
		if queueErr := ipc.Enqueue(msg); queueErr != nil {
			// Keep the hook best-effort if the local filesystem is unavailable.
			// The failure is intentionally silent to avoid disrupting the agent.
		}
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

	// Context is only injected once, on the first prompt of a session — not on
	// every message — so background retrieval still runs but nothing is printed.
	if !isFirst {
		return
	}
	var coachingItem *db.CoachingItem
	if item, suggestionErr := coach.SafeBoundarySuggestion(database, projectID); suggestionErr == nil {
		coachingItem = item
	}

	principles, summaries, alerts, externalSummaries, promptMatch, distillError := loadSessionRecallContext(database, projectID, promptText)
	if shouldWaitForOfficialHint(alerts, externalSummaries) && hasPendingFailureRetrieval(database, projectID) {
		waitForOfficialHint(database, projectID)
		principles, summaries, alerts, externalSummaries, promptMatch, distillError = loadSessionRecallContext(database, projectID, promptText)
	}

	// On session start, promote the last session summary to full detail.
	var lastSession *db.SessionSummary
	if projectID != "" {
		lastSession = loadLastProjectSession(database, projectID, sessionID)
	}

	text := buildSessionRecallOutput(projectID, summaries, principles, alerts, externalSummaries, promptMatch, lastSession, distillError)
	text = appendCoachSuggestion(text, coachingItem)
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

func appendCoachSuggestion(recall string, item *db.CoachingItem) string {
	if item == nil {
		return recall
	}
	suggestion := fmt.Sprintf("Forge coaching (%s): %s Next action: %s", item.SkillKey, item.Question, item.NextAction)
	if strings.TrimSpace(recall) == "" {
		return suggestion
	}
	return recall + "\n\n" + suggestion
}

// isFirstPromptOfSession returns true when no prior events exist for this session ID.
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
