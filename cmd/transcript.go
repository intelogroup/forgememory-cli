package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/forge/forge/internal/db"
)

const transcriptMaxLineBytes = 4 * 1024 * 1024

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
		var raw map[string]any
		if json.Unmarshal([]byte(line), &raw) != nil {
			continue
		}
		kind := transcriptEventType(raw)
		if kind == "" {
			continue
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
		event := &db.Event{
			ID:             spanID,
			TS:             ts,
			TraceID:        parent.TraceID,
			SpanID:         spanID,
			ParentSpanID:   previousSpan,
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
			ToolName:       stringValue(raw["tool_name"]),
			Payload:        line,
		}
		if err := database.InsertEvent(event); err != nil {
			return count, fmt.Errorf("insert transcript line %d: %w", lineNumber, err)
		}
		previousSpan = spanID
		count++
	}
	if err := scanner.Err(); err != nil {
		return count, fmt.Errorf("read transcript: %w", err)
	}
	return count, nil
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
	if payload, ok := raw["payload"].(map[string]any); ok {
		if role == "" {
			role = strings.ToLower(stringValue(payload["role"]))
		}
		if typeName == "" {
			typeName = strings.ToLower(stringValue(payload["type"]))
		}
	}
	switch {
	case role == "assistant" || typeName == "assistant":
		return "ModelTurn"
	case role == "user" || typeName == "user":
		return "TranscriptPrompt"
	case typeName == "tool_use" || typeName == "tool_call":
		return "TranscriptToolCall"
	case typeName == "tool_result" || typeName == "tool_response":
		return "TranscriptToolResult"
	default:
		return ""
	}
}

// findCodexTranscript resolves the rollout JSONL written by Codex when its
// hook payload does not include transcript_path. It is intentionally bounded
// to the event's calendar day and exact session prefix.
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
	matches, err := filepath.Glob(filepath.Join(day, sessionID+"*.jsonl"))
	if err != nil || len(matches) == 0 {
		return ""
	}
	return matches[0]
}

func transcriptSpanID(traceID string, lineNumber int64, line string) string {
	hash := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%s", traceID, lineNumber, line)))
	return "transcript-" + hex.EncodeToString(hash[:16])
}
