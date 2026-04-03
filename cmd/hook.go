package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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
	sourceTool := envOr("FORGE_SOURCE_TOOL", "unknown")
	eventType := envOr("FORGE_EVENT_TYPE", "unknown")

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

	// Session recall: inject recent context on session start
	if eventType == "UserPromptSubmit" {
		handleSessionRecall()
		os.Exit(0)
	}

	// Session end: synthesize summary asynchronously then forward event
	if isSessionEndEvent(eventType) {
		projectID := detectProject()
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
		ProjectID:  detectProject(),
		SourceTool: sourceTool,
		EventType:  eventType,
		SessionID:  sessionID,
		ToolName:   toolName,
		Payload:    payload,
	}

	if err := ipc.Send(msg); err != nil {
		// Silent failure — daemon is down, hook exits in <1ms
		os.Exit(0)
	}
	os.Exit(0)
}

// handleSessionRecall injects recent memories as Claude Code hookSpecificOutput.
// Opens the DB directly (read-only under WAL mode — safe with concurrent daemon writes).
func handleSessionRecall() {
	database, err := db.Open("")
	if err != nil {
		return
	}
	defer database.Close()

	principles, _ := database.RecentPrinciples(5)
	summaries, _ := database.GetRecentSessionSummaries(3)

	if len(principles) == 0 && len(summaries) == 0 {
		return
	}

	var sb strings.Builder
	if len(summaries) > 0 {
		sb.WriteString("## Previous Sessions\n")
		for _, s := range summaries {
			ts := s.TS
			if len(ts) >= 10 {
				ts = ts[:10]
			}
			if s.Learnings != "" {
				sb.WriteString(fmt.Sprintf("- [%s] %s\n", ts, s.Learnings))
			} else if s.Summary != "" {
				sb.WriteString(fmt.Sprintf("- [%s] %s\n", ts, s.Summary))
			}
		}
		sb.WriteString("\n")
	}
	if len(principles) > 0 {
		sb.WriteString("## Active Principles\n")
		for _, p := range principles {
			sb.WriteString(fmt.Sprintf("- **%s** (%s, %.1f): %s\n",
				p.Title, p.Type, p.ImpactScore, p.Narrative))
		}
	}

	output := map[string]any{"hookSpecificOutput": sb.String()}
	data, _ := json.Marshal(output)
	fmt.Println(string(data))
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

func detectProject() string {
	if out, err := execCommand("git", "rev-parse", "--show-toplevel"); err == nil {
		return filepath.Base(strings.TrimSpace(out))
	}
	cwd, _ := os.Getwd()
	return filepath.Base(cwd)
}

func execCommand(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.Output()
	return string(out), err
}
