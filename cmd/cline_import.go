package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/forge/forge/internal/db"
)

// Cline stores task history under a data root (default ~/.cline):
//
//	data/state/taskHistory.json        — index of tasks (id, prompt, cwd, model, tokens)
//	data/tasks/<id>/ui_messages.json   — say/ask message stream with per-message ts + model info
//	data/tasks/<id>/task_metadata.json — files in context and model usage
//
// Cline has no hook system, so `forge cline-import` backfills finished task
// history as forge events with source=cline. Imports are idempotent: event ids
// are deterministic hashes of (session, message index, event type, timestamp,
// payload), so re-running the command skips already-imported messages.

type clineHistoryEntry struct {
	ID      string `json:"id"`
	ULID    string `json:"ulid"`
	TS      int64  `json:"ts"`
	Task    string `json:"task"`
	CWD     string `json:"cwdOnTaskInitialization"`
	ModelID string `json:"modelId"`
}

type clineUIMessage struct {
	TS        int64           `json:"ts"`
	Type      string          `json:"type"`
	Say       string          `json:"say"`
	Text      string          `json:"text"`
	ModelInfo *clineModelInfo `json:"modelInfo"`
}

type clineModelInfo struct {
	ModelID string `json:"modelId"`
}

type clineFileRecord struct {
	Path string `json:"path"`
}

type clineTaskMetadata struct {
	FilesInContext []clineFileRecord `json:"files_in_context"`
}

// clineOpenDB is swappable for tests.
var clineOpenDB = func(path string) (*db.DB, error) { return db.Open(path) }

// mapClineMessage translates a Cline ui_messages entry into a forge event.
// Only type=say entries are mapped; ask entries are approval prompts.
func mapClineMessage(msg clineUIMessage) (eventType, toolName string, ok bool) {
	if msg.Type != "say" {
		return "", "", false
	}
	switch msg.Say {
	case "task", "user_feedback":
		return "UserPromptSubmit", "", true
	case "text", "reasoning", "completion_result":
		return "ModelTurn", "", true
	case "command":
		return "PostToolUse", "execute_command", true
	case "tool":
		return "PostToolUse", "cline_tool", true
	case "error_retry":
		return "PostToolUseFailure", "", true
	}
	return "", "", false
}

func clineSessionID(entry clineHistoryEntry) string {
	switch {
	case entry.ULID != "":
		return "cline-" + entry.ULID
	case entry.ID != "":
		return "cline-" + entry.ID
	default:
		return "cline-unknown"
	}
}

// clineEventID derives a deterministic event id so imports are idempotent
// (events.id is the primary key and inserts use OR IGNORE).
func clineEventID(sessionID string, index int, eventType, ts, payload string) string {
	sum := sha256.Sum256([]byte(strings.Join(
		[]string{"cline", sessionID, strconv.Itoa(index), eventType, ts, payload}, "\x1f")))
	return hex.EncodeToString(sum[:])
}

func clineTimestamp(msgTS, fallback int64) string {
	value := msgTS
	if value == 0 {
		value = fallback
	}
	if value == 0 {
		return time.Now().UTC().Format(time.RFC3339)
	}
	return time.UnixMilli(value).UTC().Format(time.RFC3339)
}

// buildClineEvents converts one Cline task into forge events, ending with a
// SessionEnd marker so the distiller sees a session boundary.
func buildClineEvents(entry clineHistoryEntry, msgs []clineUIMessage, projectID, filesJSON, transcriptPath string) []db.Event {
	sessionID := clineSessionID(entry)
	model := entry.ModelID
	base := db.Event{
		SessionID:      sessionID,
		ProjectID:      projectID,
		SourceTool:     "cline",
		CWD:            entry.CWD,
		TaskID:         entry.ID,
		Files:          filesJSON,
		TranscriptPath: transcriptPath,
	}
	var events []db.Event
	var lastTS string
	for index, msg := range msgs {
		eventType, toolName, ok := mapClineMessage(msg)
		if !ok {
			continue
		}
		if msg.ModelInfo != nil && msg.ModelInfo.ModelID != "" {
			model = msg.ModelInfo.ModelID
		}
		ts := clineTimestamp(msg.TS, entry.TS)
		lastTS = ts
		var payload map[string]any
		switch eventType {
		case "UserPromptSubmit":
			payload = map[string]any{
				"session_id": sessionID, "cwd": entry.CWD, "transcript_path": transcriptPath,
				"prompt": msg.Text, "cline_say": msg.Say,
			}
		case "ModelTurn":
			payload = map[string]any{
				"session_id": sessionID, "model": model, "transcript_path": transcriptPath,
				"say": msg.Say, "text": msg.Text,
			}
		case "PostToolUse":
			payload = map[string]any{
				"session_id": sessionID, "tool_name": toolName,
				"transcript_path": transcriptPath, "content": msg.Text,
			}
		case "PostToolUseFailure":
			payload = map[string]any{
				"session_id": sessionID, "transcript_path": transcriptPath, "error": msg.Text,
			}
		}
		data, err := json.Marshal(payload)
		if err != nil {
			continue
		}
		event := base
		event.ID = clineEventID(sessionID, index, eventType, ts, string(data))
		event.TS = ts
		event.EventType = eventType
		event.ToolName = toolName
		event.Model = model
		event.Sequence = int64(index)
		event.Payload = string(data)
		events = append(events, event)
		lastTS = ts
	}
	if len(events) > 0 {
		endTS := lastTS
		payload, _ := json.Marshal(map[string]any{"session_id": sessionID, "transcript_path": transcriptPath, "ts": endTS})
		event := base
		event.ID = clineEventID(sessionID, len(msgs), "SessionEnd", endTS, string(payload))
		event.TS = endTS
		event.EventType = "SessionEnd"
		event.Payload = string(payload)
		events = append(events, event)
	}
	return events
}

// runClineImport backfills Cline task history into the forge database.
func runClineImport(args []string) {
	fs := flag.NewFlagSet("cline-import", flag.ContinueOnError)
	pathFlag := fs.String("path", "", "Cline data root (default ~/.cline)")
	sinceFlag := fs.String("since", "", "only import tasks newer than this duration (e.g. 720h, 30d)")
	limitFlag := fs.Int("limit", 0, "import at most N tasks (0 = all)")
	dryRun := fs.Bool("dry-run", false, "parse and report without writing events")
	synthesize := fs.Bool("synthesize", false, "also synthesize session summaries for imported tasks")
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	root := *pathFlag
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		root = filepath.Join(home, ".cline")
	}
	tasksDir := filepath.Join(root, "data", "tasks")
	historyPath := filepath.Join(root, "data", "state", "taskHistory.json")

	entries, err := loadClineHistory(historyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	since, err := parseClineSince(*sinceFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: invalid --since %q: %v\n", *sinceFlag, err)
		os.Exit(1)
	}
	if since > 0 {
		cutoff := time.Now().Add(-since).UnixMilli()
		var filtered []clineHistoryEntry
		for _, entry := range entries {
			if entry.TS >= cutoff {
				filtered = append(filtered, entry)
			}
		}
		entries = filtered
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].TS < entries[j].TS })
	if *limitFlag > 0 && len(entries) > *limitFlag {
		entries = entries[:*limitFlag]
	}

	fmt.Printf("Cline task history: %s (%d task(s))\n", historyPath, len(entries))

	var database *db.DB
	if !*dryRun {
		database, err = clineOpenDB("")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		defer database.Close()
	}

	typeCounts := map[string]int{}
	var tasksImported, tasksUnchanged, tasksSkipped, eventsNew int
	for _, entry := range entries {
		uiPath := filepath.Join(tasksDir, entry.ID, "ui_messages.json")
		msgs, err := loadClineUIMessages(uiPath)
		if err != nil || len(msgs) == 0 {
			tasksSkipped++
			fmt.Printf("  Skipping %s: no importable messages\n", entry.ID)
			continue
		}
		projectID := "unknown"
		var projectRoot string
		if entry.CWD != "" {
			projectID, projectRoot = resolveProjectRoot(entry.CWD)
		}
		filesJSON := loadClineFilesJSON(filepath.Join(tasksDir, entry.ID, "task_metadata.json"))
		events := buildClineEvents(entry, msgs, projectID, filesJSON, uiPath)
		if len(events) == 0 {
			tasksSkipped++
			continue
		}
		if !*dryRun && projectRoot != "" {
			if err := database.UpsertProject(projectID, projectRoot, projectID, "cline"); err != nil {
				fmt.Fprintf(os.Stderr, "  Warning: could not update projects table: %v\n", err)
			}
		}
		newCount := 0
		for _, ev := range events {
			typeCounts[ev.EventType]++
			if *dryRun {
				eventsNew++
				newCount++
				continue
			}
			exists, err := database.HasEvent(ev.ID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  Warning: lookup %s: %v\n", ev.EventType, err)
				continue
			}
			if exists {
				continue
			}
			if err := database.InsertEvent(&ev); err != nil {
				fmt.Fprintf(os.Stderr, "  Warning: insert %s: %v\n", ev.EventType, err)
				continue
			}
			eventsNew++
			newCount++
		}
		if newCount > 0 {
			tasksImported++
			fmt.Printf("  %s -> %s (%d events) %s\n", entry.ID, projectID, newCount, truncate(entry.Task, 60))
			sessionID := events[0].SessionID
			if *synthesize && !*dryRun && sessionID != "" && sessionID != "cline-unknown" && projectID != "unknown" {
				spawnSessionSynthesis(sessionID, projectID, "final", checkpointKey(sessionID, "final", ""), "")
			}
		} else {
			tasksUnchanged++
		}
	}

	if *dryRun {
		fmt.Println("  Dry run complete — nothing written.")
		fmt.Printf("\nSummary: %d task(s) would be imported, 0 already up to date, %d skipped\n", tasksImported, tasksSkipped)
		fmt.Printf("Events: %d would be written\n", eventsNew)
	} else {
		fmt.Printf("\nSummary: %d task(s) imported, %d already up to date, %d skipped\n", tasksImported, tasksUnchanged, tasksSkipped)
		fmt.Printf("Events: %d new\n", eventsNew)
	}
	for _, eventType := range []string{"UserPromptSubmit", "ModelTurn", "PostToolUse", "PostToolUseFailure", "SessionEnd"} {
		if typeCounts[eventType] > 0 {
			fmt.Printf("  %-20s %d\n", eventType, typeCounts[eventType])
		}
	}
	if eventsNew > 0 && !*dryRun {
		fmt.Println("The daemon picks these up on its next distillation cycle; `forge distill` forces it now.")
	}
}

func loadClineHistory(path string) ([]clineHistoryEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s (is Cline installed?): %w", path, err)
	}
	var entries []clineHistoryEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return entries, nil
}

func loadClineUIMessages(path string) ([]clineUIMessage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var msgs []clineUIMessage
	if err := json.Unmarshal(data, &msgs); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return msgs, nil
}

// loadClineFilesJSON returns a JSON array of file paths recorded in the
// task's context metadata (capped at 32 paths).
func loadClineFilesJSON(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "[]"
	}
	var meta clineTaskMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return "[]"
	}
	files := make([]string, 0, len(meta.FilesInContext))
	for i, record := range meta.FilesInContext {
		if i >= 32 || record.Path == "" {
			break
		}
		files = append(files, record.Path)
	}
	out, err := json.Marshal(files)
	if err != nil {
		return "[]"
	}
	return string(out)
}

// parseClineSince accepts Go durations (90m, 12h) plus an Nd suffix for days.
func parseClineSince(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	if strings.HasSuffix(raw, "d") {
		if days, err := strconv.Atoi(strings.TrimSuffix(raw, "d")); err == nil {
			return time.Duration(days) * 24 * time.Hour, nil
		}
	}
	return time.ParseDuration(raw)
}
