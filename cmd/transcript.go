package main

import (
	"bufio"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/forge/forge/internal/db"
	_ "modernc.org/sqlite"
)

const transcriptMaxLineBytes = 4 * 1024 * 1024

// ingestSessionTranscript ingests a session's transcript as child span events
// once the session ends. It resolves the transcript source per agent and is
// safe to retry (span IDs are deterministic, inserts are OR IGNORE).
func ingestSessionTranscript(database *db.DB, event *db.Event) (int, error) {
	if event == nil {
		return 0, nil
	}
	if event.TraceID == "" {
		event.TraceID = event.SessionID
	}
	if event.SpanID == "" {
		event.SpanID = event.ID
	}
	switch event.SourceTool {
	case "opencode":
		return ingestOpencodeTranscript(database, *event)
	case "antigravity", "gemini":
		return ingestAntigravityTranscript(database, *event)
	default:
		parent := *event
		parent.TranscriptPath = resolveTranscriptPath(&parent)
		return ingestTranscript(database, parent)
	}
}

// resolveTranscriptPath returns the transcript file to ingest for an event,
// falling back to provider-specific locations when the hook omitted the path.
func resolveTranscriptPath(event *db.Event) string {
	if strings.TrimSpace(event.TranscriptPath) != "" {
		return event.TranscriptPath
	}
	if event.SourceTool == "codex" {
		return findCodexTranscript(event.SessionID, event.TS)
	}
	return ""
}

// ingestTranscript imports JSONL transcript turns as child spans of the
// completed agent trace. It stores redacted raw lines rather than attempting
// to reconstruct provider-specific message content and is safe to retry.
func ingestTranscript(database *db.DB, parent db.Event) (int, error) {
	if database == nil || strings.TrimSpace(parent.TranscriptPath) == "" {
		return 0, nil
	}
	file, err := os.Open(parent.TranscriptPath)
	if err != nil {
		return 0, fmt.Errorf("open transcript: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), transcriptMaxLineBytes)
	count := 0
	lineNumber := int64(0)
	previousSpan := parent.SpanID
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		inserted, err := insertTranscriptTurn(database, parent, &previousSpan, line, lineNumber)
		if err != nil {
			return count, err
		}
		if inserted {
			count++
		}
	}
	if err := scanner.Err(); err != nil {
		return count, fmt.Errorf("read transcript: %w", err)
	}
	return count, nil
}

// insertTranscriptTurn normalizes one raw transcript line and inserts it as a
// child span. It reports inserted=false when the line is not a recognized turn.
func insertTranscriptTurn(database *db.DB, parent db.Event, previousSpan *string, line string, lineNumber int64) (bool, error) {
	var raw map[string]any
	if json.Unmarshal([]byte(line), &raw) != nil {
		return false, nil
	}
	kind := transcriptEventType(raw)
	if kind == "" {
		return false, nil
	}
	spanID := transcriptSpanID(parent.TraceID, lineNumber, line)
	ts := stringValue(raw["timestamp"])
	if ts == "" {
		ts = parent.TS
	}
	status := stringValue(raw["status"])
	if status == "" {
		if failed, ok := raw["is_error"].(bool); ok && failed {
			status = "error"
		}
	}
	toolName := stringValue(raw["tool_name"])
	if toolName == "" {
		if payload, ok := raw["payload"].(map[string]any); ok {
			toolName = firstStringValue(payload, "name", "tool_name")
			if toolName == "" {
				if fn, ok := payload["function"].(map[string]any); ok {
					toolName = stringValue(fn["name"])
				}
			}
		}
	}
	event := &db.Event{
		ID:             spanID,
		TS:             ts,
		TraceID:        parent.TraceID,
		SpanID:         spanID,
		ParentSpanID:   *previousSpan,
		Sequence:       lineNumber,
		Status:         status,
		Model:          firstStringValue(raw, "model", "model_name"),
		TaskID:         parent.TaskID,
		CWD:            parent.CWD,
		GitBranch:      parent.GitBranch,
		GitCommit:      parent.GitCommit,
		Files:          extractFilePaths(line),
		TranscriptPath: parent.TranscriptPath,
		SessionID:      parent.SessionID,
		ProjectID:      parent.ProjectID,
		SourceTool:     parent.SourceTool,
		EventType:      kind,
		ToolName:       toolName,
		Payload:        line,
	}
	if err := database.InsertEvent(event); err != nil {
		return false, fmt.Errorf("insert transcript line %d: %w", lineNumber, err)
	}
	*previousSpan = spanID
	return true, nil
}

func transcriptEventType(raw map[string]any) string {
	typeName := strings.ToLower(stringValue(raw["type"]))
	role := ""
	if message, ok := raw["message"].(map[string]any); ok {
		role = strings.ToLower(stringValue(message["role"]))
	}
	if role == "" {
		role = strings.ToLower(stringValue(raw["role"]))
	}
	innerType := ""
	if payload, ok := raw["payload"].(map[string]any); ok {
		if role == "" {
			role = strings.ToLower(stringValue(payload["role"]))
		}
		innerType = strings.ToLower(stringValue(payload["type"]))
	}
	// Codex wraps items in response_item with the meaningful type in payload.
	if typeName == "response_item" && innerType != "" {
		typeName = innerType
	}
	switch {
	case role == "assistant" || typeName == "assistant":
		return "ModelTurn"
	case role == "user" || typeName == "user":
		return "TranscriptPrompt"
	case typeName == "tool_use" || typeName == "tool_call" || typeName == "custom_tool_call" || typeName == "function_call":
		return "TranscriptToolCall"
	case typeName == "tool_result" || typeName == "tool_response" || typeName == "custom_tool_call_output" || typeName == "function_call_output":
		return "TranscriptToolResult"
	default:
		return ""
	}
}

// findCodexTranscript resolves the rollout JSONL written by Codex when its
// hook payload does not include transcript_path. It is intentionally bounded
// to the event's calendar day; Codex names the file rollout-<ts>-<sessionID>.jsonl
// so the session ID is matched as a filename suffix, not a prefix.
func findCodexTranscript(sessionID, ts string) string {
	if sessionID == "" || sessionID == "unknown" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	when, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		when = time.Now()
	}
	day := filepath.Join(home, ".codex", "sessions", fmt.Sprintf("%d/%02d/%02d", when.Year(), when.Month(), when.Day()))
	matches, err := filepath.Glob(filepath.Join(day, "*"+sessionID+".jsonl"))
	if err != nil || len(matches) == 0 {
		return ""
	}
	return matches[0]
}

func transcriptSpanID(traceID string, lineNumber int64, line string) string {
	hash := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%s", traceID, lineNumber, line)))
	return "transcript-" + hex.EncodeToString(hash[:16])
}

// opencodeTranscriptDB returns the path to opencode's SQLite store holding
// the message/part transcript rows.
func opencodeTranscriptDB() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "share", "opencode", "opencode.db")
}

// ingestOpencodeTranscript reads opencode's SQLite message/part tables for the
// session and imports each turn as child span events. It is a no-op when the
// session is unknown or opencode has no store on this machine.
func ingestOpencodeTranscript(database *db.DB, parent db.Event) (int, error) {
	if parent.SessionID == "" || parent.SessionID == "unknown" {
		return 0, nil
	}
	path := opencodeTranscriptDB()
	if path == "" {
		return 0, nil
	}
	return ingestOpencodeDB(database, parent, path)
}

// ingestOpencodeDB opens opencode.db read-only and imports the given session's
// turns. Best-effort: any open/query error yields zero turns, never an error
// that would disturb the daemon.
func ingestOpencodeDB(database *db.DB, parent db.Event, path string) (int, error) {
	if database == nil {
		return 0, nil
	}
	if _, err := os.Stat(path); err != nil {
		return 0, nil
	}
	conn, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(1000)")
	if err != nil {
		return 0, nil
	}
	defer conn.Close()

	rows, err := conn.Query(`
		SELECT m.id, m.time_created, m.data, p.time_created, p.data
		FROM message m
		LEFT JOIN part p ON p.message_id = m.id
		WHERE m.session_id = ?
		ORDER BY m.time_created, m.id, p.time_created, p.id`, parent.SessionID)
	if err != nil {
		return 0, nil
	}
	defer rows.Close()

	count := 0
	lineNumber := int64(0)
	previousSpan := parent.SpanID

	var curID string
	var curTS int64
	var curData string
	var curParts []opencodePart

	emit := func(msgID string, msgTS int64, msgData string, parts []opencodePart) error {
		lines, err := opencodeMessageLines(msgTS, msgData, parts)
		if err != nil {
			return err
		}
		for _, line := range lines {
			lineNumber++
			inserted, err := insertTranscriptTurn(database, parent, &previousSpan, line, lineNumber)
			if err != nil {
				return err
			}
			if inserted {
				count++
			}
		}
		return nil
	}

	for rows.Next() {
		var msgID string
		var msgTS int64
		var msgData string
		var partTS sql.NullInt64
		var partData sql.NullString
		if err := rows.Scan(&msgID, &msgTS, &msgData, &partTS, &partData); err != nil {
			return count, nil
		}
		if curID == "" {
			curID = msgID
			curTS = msgTS
			curData = msgData
		} else if curID != msgID {
			if err := emit(curID, curTS, curData, curParts); err != nil {
				return count, err
			}
			curID = msgID
			curTS = msgTS
			curData = msgData
			curParts = nil
		}
		if partTS.Valid && partData.Valid {
			curParts = append(curParts, opencodePart{ts: partTS.Int64, data: partData.String})
		}
	}
	if curID != "" {
		if err := emit(curID, curTS, curData, curParts); err != nil {
			return count, err
		}
	}
	return count, nil
}

// opencodePart is one part row belonging to an opencode message.
type opencodePart struct {
	ts   int64
	data string
}

// opencodeMessageLines converts one opencode message (plus its parts) into the
// normalized JSONL lines ingested by insertTranscriptTurn.
func opencodeMessageLines(msgTS int64, msgData string, parts []opencodePart) ([]string, error) {
	var msg struct {
		Role    string `json:"role"`
		ModelID string `json:"modelID"`
	}
	if err := json.Unmarshal([]byte(msgData), &msg); err != nil {
		return nil, nil
	}

	var textParts []string
	var toolParts []map[string]any
	for _, p := range parts {
		var part map[string]any
		if err := json.Unmarshal([]byte(p.data), &part); err != nil {
			continue
		}
		switch part["type"] {
		case "text":
			if s := stringValue(part["text"]); s != "" {
				textParts = append(textParts, s)
			}
		case "tool":
			toolParts = append(toolParts, part)
		}
	}

	var lines []string
	ts := msToRFC3339(msgTS)

	switch msg.Role {
	case "user", "assistant":
		turn := map[string]any{
			"type":      msg.Role,
			"role":      msg.Role,
			"timestamp": ts,
			"message":   map[string]any{"role": msg.Role, "content": strings.Join(textParts, "\n")},
		}
		if msg.Role == "assistant" && msg.ModelID != "" {
			turn["model"] = msg.ModelID
		}
		line, err := json.Marshal(turn)
		if err != nil {
			return nil, err
		}
		lines = append(lines, string(line))
	}

	for _, tool := range toolParts {
		lines = append(lines, opencodeToolLines(msgTS, tool)...)
	}

	return lines, nil
}

func opencodeToolLines(msgTS int64, tool map[string]any) []string {
	toolName := stringValue(tool["tool"])
	state, _ := tool["state"].(map[string]any)
	status := stringValue(state["status"])
	isError := status == "error"
	if exit, ok := state["exit"].(float64); ok && exit != 0 {
		isError = true
	}

	call := map[string]any{
		"type":      "tool_use",
		"tool_name": toolName,
		"timestamp": msToRFC3339(msgTS),
		"payload":   map[string]any{"type": "tool_use", "input": state["input"]},
	}
	callLine, err := json.Marshal(call)
	if err != nil {
		return nil
	}

	result := map[string]any{
		"type":      "tool_result",
		"tool_name": toolName,
		"status":    status,
		"is_error":  isError,
		"timestamp": msToRFC3339(msgTS),
		"payload":   map[string]any{"type": "tool_result", "output": state["output"]},
	}
	resultLine, err := json.Marshal(result)
	if err != nil {
		return []string{string(callLine)}
	}

	return []string{string(callLine), string(resultLine)}
}

func msToRFC3339(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

// findAntigravityTranscript resolves the transcript JSONL written by the
// Antigravity (Gemini CLI) runtime: brain/<conversationID>/.system_generated/logs/transcript.jsonl.
func findAntigravityTranscript(sessionID string) string {
	if sessionID == "" || sessionID == "unknown" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".gemini", "antigravity", "brain", sessionID, ".system_generated", "logs", "transcript.jsonl")
}

// ingestAntigravityTranscript imports Antigravity's transcript.jsonl turns as
// child span events. Its lines carry source/type instead of role/type, so each
// line is normalized through antigravityLines before the shared insert path.
func ingestAntigravityTranscript(database *db.DB, parent db.Event) (int, error) {
	if parent.SessionID == "" || parent.SessionID == "unknown" {
		return 0, nil
	}
	path := findAntigravityTranscript(parent.SessionID)
	if path == "" {
		return 0, nil
	}
	if _, err := os.Stat(path); err != nil {
		return 0, nil
	}

	file, err := os.Open(path)
	if err != nil {
		return 0, nil
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), transcriptMaxLineBytes)
	count := 0
	lineNumber := int64(0)
	previousSpan := parent.SpanID
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var raw map[string]any
		if json.Unmarshal([]byte(line), &raw) != nil {
			continue
		}
		for _, turn := range antigravityLines(raw) {
			inserted, err := insertTranscriptTurn(database, parent, &previousSpan, turn, lineNumber)
			if err != nil {
				return count, err
			}
			if inserted {
				count++
			}
		}
	}
	return count, nil
}

// antigravityToolResultTypes is the set of Antigravity MODEL step types that
// carry an executed-tool result in their content field.
var antigravityToolResultTypes = map[string]bool{
	"RUN_COMMAND":    true,
	"VIEW_FILE":      true,
	"SEARCH_WEB":     true,
	"CODE_ACTION":    true,
	"LIST_DIRECTORY": true,
	"GREP_SEARCH":    true,
	"READ_FILE":      true,
	"WRITE_FILE":     true,
	"EDIT_FILE":      true,
	"SEARCH_CODE":    true,
}

// antigravityLines converts one Antigravity transcript line into zero or more
// normalized JSONL turns. A PLANNER_RESPONSE line expands to one turn per
// planned tool call; a tool-execution line becomes a single tool result.
func antigravityLines(raw map[string]any) []string {
	source := stringValue(raw["source"])
	typeName := strings.ToUpper(stringValue(raw["type"]))
	ts := stringValue(raw["created_at"])
	status := strings.ToLower(stringValue(raw["status"]))

	switch {
	case source == "USER_EXPLICIT":
		turn := map[string]any{
			"type":      "user",
			"role":      "user",
			"timestamp": ts,
			"message":   map[string]any{"role": "user", "content": stringValue(raw["content"])},
		}
		line, err := json.Marshal(turn)
		if err != nil {
			return nil
		}
		return []string{string(line)}

	case source == "MODEL" && typeName == "PLANNER_RESPONSE":
		calls, _ := raw["tool_calls"].([]any)
		lines := make([]string, 0, len(calls))
		for _, c := range calls {
			cm, ok := c.(map[string]any)
			if !ok {
				continue
			}
			turn := map[string]any{
				"type":      "tool_use",
				"tool_name": stringValue(cm["name"]),
				"timestamp": ts,
				"payload":   map[string]any{"type": "tool_use", "input": cm["args"]},
			}
			line, err := json.Marshal(turn)
			if err == nil {
				lines = append(lines, string(line))
			}
		}
		return lines

	case source == "MODEL" && antigravityToolResultTypes[typeName] && stringValue(raw["content"]) != "":
		turn := map[string]any{
			"type":      "tool_result",
			"tool_name": strings.ToLower(typeName),
			"status":    status,
			"is_error":  status != "" && status != "done",
			"timestamp": ts,
			"payload":   map[string]any{"type": "tool_result", "output": stringValue(raw["content"])},
		}
		line, err := json.Marshal(turn)
		if err != nil {
			return nil
		}
		return []string{string(line)}
	}
	return nil
}
