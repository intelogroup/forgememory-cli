package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/forge/forge/internal/db"
	"github.com/forge/forge/internal/ipc"
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

// ClaudeHookInput is the JSON structure Claude Code sends to hooks.
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
	_ = fs.Parse(args)

	sourceTool := envOr("FORGE_SOURCE_TOOL", "")
	eventType := envOr("FORGE_EVENT_TYPE", "")

	// If env vars not set but --event was provided, this is a Codex invocation.
	if *eventFlag != "" {
		if eventType == "" {
			eventType = *eventFlag
		}
		if sourceTool == "" {
			sourceTool = "codex"
		}
	}
	if sourceTool == "" {
		sourceTool = "unknown"
	}
	if eventType == "" {
		eventType = "unknown"
	}

	// Read and strip private data from payload
	payload := stripPrivate(string(readStdin()))

	// Parse Claude Code hook input to extract session/tool metadata
	var input ClaudeHookInput
	if sourceTool == "claude" && len(payload) > 0 {
		json.Unmarshal([]byte(payload), &input)
		if input.HookEventName != "" && eventType == "unknown" {
			eventType = input.HookEventName
		}
	}

	sessionID := input.SessionID
	if sessionID == "" {
		sessionID = envOr("FORGE_SESSION_ID", "unknown")
	}

	toolName := input.ToolName
	if toolName == "" {
		toolName = envOr("FORGE_TOOL_NAME", "")
	}

	projectID := detectProjectIDForPath(input.CWD)

	// Session end: synthesize summary asynchronously then forward event
	if isSessionEndEvent(eventType) {
		spawnSessionSynthesis(sessionID, projectID)
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

	// Session recall: inject recent context on session start after persisting the
	// prompt event, so startup summaries can include the latest prompt history.
	if eventType == "UserPromptSubmit" {
		handleSessionRecall(projectID)
		os.Exit(0)
	}
	os.Exit(0)
}

// handleSessionRecall injects recent memories as hookSpecificOutput.
// Opens the DB directly (read-only under WAL mode — safe with concurrent daemon writes).
func handleSessionRecall(projectID string) {
	database, err := db.Open("")
	if err != nil {
		return
	}
	defer database.Close()

	principles, summaries := loadSessionRecallContext(database, projectID)

	text := buildSessionRecallOutput(projectID, summaries, principles)
	if text == "" {
		return
	}

	output := map[string]any{"hookSpecificOutput": text}
	data, _ := json.Marshal(output)
	fmt.Println(string(data))
}

func loadSessionRecallContext(database *db.DB, projectID string) ([]db.Principle, []db.SessionSummary) {
	principles, _ := database.RecentPrinciplesByProject(projectID, 2)
	summaries, _ := database.GetRecentSessionSummariesByProject(projectID, 2)
	if projectID != "" && len(principles) == 0 && len(summaries) == 0 {
		principles, _ = database.RecentPrinciples(2)
		summaries, _ = database.GetRecentSessionSummaries(2)
	}
	return principles, summaries
}

// spawnSessionSynthesis detaches a `forge synthesize-session` process so the
// hook can return immediately without blocking on an LLM call.
func spawnSessionSynthesis(sessionID, projectID string) {
	if sessionID == "" || sessionID == "unknown" {
		return
	}
	cmd := exec.Command(os.Args[0], "synthesize-session",
		"--session-id", sessionID,
		"--project-id", projectID)
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil
	_ = startBackground(cmd)
}

func readStdin() []byte {
	if runtime.GOOS == "windows" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil
		}
		return data
	}
	data, err := os.ReadFile("/dev/stdin")
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

func execCommand(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.Output()
	return string(out), err
}

func buildSessionRecallOutput(projectID string, summaries []db.SessionSummary, principles []db.Principle) string {
	var sentences []string

	var learningBits []string
	for _, summary := range summaries {
		if text := firstNonEmpty(summary.Learnings, summary.Summary); text != "" {
			learningBits = append(learningBits, trimSentence(text))
		}
	}
	if len(learningBits) > 0 {
		prefix := "Recent lessons"
		if projectID != "" {
			prefix = fmt.Sprintf("Recent lessons for %s", projectID)
		}
		sentences = append(sentences, fmt.Sprintf("%s: %s.", prefix, strings.Join(learningBits, "; ")))
	}

	if len(principles) > 0 {
		sentences = append(sentences, fmt.Sprintf("Active principle: %s.", trimSentence(principles[0].Narrative)))
	} else {
		for _, summary := range summaries {
			if next := trimSentence(summary.NextSteps); next != "" {
				sentences = append(sentences, fmt.Sprintf("Next step: %s.", next))
				break
			}
		}
	}

	if len(sentences) == 0 {
		return ""
	}

	return "## Startup Context\n" + strings.Join(sentences, " ")
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
