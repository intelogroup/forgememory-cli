package main

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/forge/forge/internal/ipc"
	"github.com/google/uuid"
)

// HookMessage is the message sent by hooks to the daemon.
type HookMessage struct {
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

// runHook is the entrypoint for `forge hook`.
// It reads the hook payload from stdin, packages it, and sends to the daemon.
// Fails silently in <1ms if daemon is down.
func runHook(args []string) {
	msg := HookMessage{
		ID:         uuid.New().String(),
		TS:         time.Now().UTC().Format(time.RFC3339),
		ProjectID:  detectProject(),
		SourceTool: envOr("FORGE_SOURCE_TOOL", "unknown"),
		EventType:  envOr("FORGE_EVENT_TYPE", "unknown"),
	}

	// Read payload from stdin
	payload := readStdin()
	if len(payload) > 0 {
		msg.Payload = string(payload)

		// Try to parse as Claude Code hook input
		if msg.SourceTool == "claude" {
			var input ClaudeHookInput
			if err := json.Unmarshal(payload, &input); err == nil {
				msg.SessionID = input.SessionID
				msg.ToolName = input.ToolName
				if input.HookEventName != "" && msg.EventType == "unknown" {
					msg.EventType = input.HookEventName
				}
			}
		}
	}

	if msg.SessionID == "" {
		msg.SessionID = envOr("FORGE_SESSION_ID", "unknown")
	}

	if err := ipc.Send(msg); err != nil {
		// Silent failure — daemon is down, hook exits in <1ms
		// Don't print to stderr — Claude Code shows stderr in verbose mode
		os.Exit(0)
	}
	os.Exit(0)
}

func readStdin() []byte {
	if runtime.GOOS == "windows" {
		// On Windows, read from os.Stdin directly
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil
		}
		return data
	}
	// On Unix, /dev/stdin works reliably
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
	// Try git root
	if out, err := execCommand("git", "rev-parse", "--show-toplevel"); err == nil {
		return filepath.Base(strings.TrimSpace(out))
	}
	// Fall back to cwd name
	cwd, _ := os.Getwd()
	return filepath.Base(cwd)
}

func execCommand(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.Output()
	return string(out), err
}
