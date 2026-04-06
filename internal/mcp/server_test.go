package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/forge/forge/internal/db"
)

func TestHandleToolsListIncludesScopedReadTools(t *testing.T) {
	database := openTestDB(t)
	server := New(database)

	req := Request{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "tools/list"}
	resp := server.handleRequest(req)
	if resp == nil {
		t.Fatal("expected response")
	}

	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result type %T", resp.Result)
	}
	tools, ok := result["tools"].([]Tool)
	if !ok {
		t.Fatalf("unexpected tools type %T", result["tools"])
	}

	names := make(map[string]Tool)
	for _, tool := range tools {
		names[tool.Name] = tool
	}

	for _, name := range []string{
		"get_recent_context",
		"search_memories",
		"get_principles",
		"get_session_summaries",
		"get_project_timeline",
		"get_external_context",
		"get_active_failures",
	} {
		if _, ok := names[name]; !ok {
			t.Fatalf("missing tool %q", name)
		}
	}

	for _, name := range []string{
		"get_recent_context",
		"search_memories",
		"get_principles",
		"get_session_summaries",
		"get_project_timeline",
		"get_external_context",
		"get_active_failures",
	} {
		props := names[name].InputSchema["properties"].(map[string]any)
		if _, ok := props["project_id"]; !ok {
			t.Fatalf("%s missing project_id schema", name)
		}
	}
}

func TestGetPrinciplesDefaultsToCurrentProject(t *testing.T) {
	database := openTestDB(t)
	server := New(database)

	projectDir := filepath.Join(t.TempDir(), "project-alpha")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project dir: %v", err)
	}
	t.Chdir(projectDir)

	mustInsertPrinciple(t, database, "project-alpha", "alpha principle")
	mustInsertPrinciple(t, database, "project-beta", "beta principle")

	text := callToolText(t, server, "get_principles", map[string]any{"limit": 10})
	if !strings.Contains(text, "alpha principle") {
		t.Fatalf("expected alpha principle in %q", text)
	}
	if strings.Contains(text, "beta principle") {
		t.Fatalf("unexpected beta principle in %q", text)
	}
}

func TestSearchMemoriesUsesProjectScope(t *testing.T) {
	database := openTestDB(t)
	server := New(database)

	mustInsertEvent(t, database, "project-alpha", "panic: repeated rust failure")
	mustInsertEvent(t, database, "project-beta", "panic: repeated rust failure")

	text := callToolText(t, server, "search_memories", map[string]any{
		"project_id": "project-alpha",
		"query":      "repeated rust failure",
		"limit":      10,
	})
	if !strings.Contains(text, "project-alpha") && !strings.Contains(text, "Search Results") {
		t.Fatalf("unexpected search text %q", text)
	}
	if strings.Count(text, "panic: repeated rust failure") != 1 {
		t.Fatalf("expected one scoped match, got %q", text)
	}
}

func TestSearchMemoriesFindsTokenMatchesWhenPhraseDoesNotMatch(t *testing.T) {
	database := openTestDB(t)
	server := New(database)

	if err := database.InsertEvent(&db.Event{
		TS:         time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339),
		SessionID:  "sess-alpha",
		ProjectID:  "project-alpha",
		SourceTool: "codex",
		EventType:  "PostToolUse",
		ToolName:   "Bash",
		Payload:    "cargo build error e0599 no method named serve found for struct appstate",
	}); err != nil {
		t.Fatalf("insert event: %v", err)
	}

	text := callToolText(t, server, "search_memories", map[string]any{
		"project_id": "project-alpha",
		"query":      "fix appstate serve method",
		"limit":      5,
	})
	if !strings.Contains(text, "no method named serve found for struct appstate") {
		t.Fatalf("expected token-ranked match in %q", text)
	}
}

func TestGetExternalContextReturnsCachedSummaries(t *testing.T) {
	database := openTestDB(t)
	server := New(database)

	now := time.Now().UTC()
	mustUpsertExternalSummary(t, database, &db.ExternalContextSummary{
		ProjectID:   "project-alpha",
		Source:      "context7",
		Query:       "rust result map_err",
		LibraryName: "rust",
		Title:       "Rust Result docs",
		Narrative:   "Use map_err to transform error values.",
		TrustScore:  0.9,
		CacheRef:    "/rust-lang/rust",
		ExpiresAt:   now.Add(time.Hour).Format(time.RFC3339),
	})
	mustUpsertExternalSummary(t, database, &db.ExternalContextSummary{
		ProjectID:   "project-beta",
		Source:      "context7",
		Query:       "react use effect",
		LibraryName: "react",
		Title:       "React Effect docs",
		Narrative:   "Effects synchronize with external systems.",
		TrustScore:  0.9,
		CacheRef:    "/facebook/react",
		ExpiresAt:   now.Add(time.Hour).Format(time.RFC3339),
	})

	text := callToolText(t, server, "get_external_context", map[string]any{
		"project_id":   "project-alpha",
		"library_name": "rust",
		"source":       "context7",
	})
	if !strings.Contains(text, "Rust Result docs") || !strings.Contains(text, "/rust-lang/rust") {
		t.Fatalf("expected cached rust summary in %q", text)
	}
	if strings.Contains(text, "React Effect docs") {
		t.Fatalf("unexpected cross-project summary in %q", text)
	}

	summaries, err := database.SearchExternalContextSummariesByProject("project-alpha", "context7", "rust", "", 5)
	if err != nil {
		t.Fatalf("SearchExternalContextSummariesByProject: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("len(summaries) = %d, want 1", len(summaries))
	}
	if strings.TrimSpace(summaries[0].LastUsedAt) == "" {
		t.Fatalf("expected LastUsedAt to be updated after read, got %#v", summaries[0])
	}
}

func TestGetActiveFailuresReturnsScopedAlerts(t *testing.T) {
	database := openTestDB(t)
	server := New(database)

	now := time.Now().UTC()
	mustUpsertAlert(t, database, &db.Alert{
		ProjectID: "project-alpha",
		SessionID: "sess-1",
		AlertType: "repeated_failure",
		Severity:  "high",
		Title:     "Repeated rust build failure",
		Narrative: "cargo test failed repeatedly with the same compiler error.",
		Score:     0.92,
		SourceRef: "failure:alpha",
		ExpiresAt: now.Add(time.Hour).Format(time.RFC3339),
	})
	mustUpsertAlert(t, database, &db.Alert{
		ProjectID:    "project-alpha",
		SessionID:    "sess-2",
		AlertType:    "repeated_failure",
		Severity:     "low",
		Title:        "Acknowledged failure",
		Narrative:    "should not show",
		Score:        0.4,
		SourceRef:    "failure:ack",
		ExpiresAt:    now.Add(time.Hour).Format(time.RFC3339),
		Acknowledged: true,
	})
	mustUpsertAlert(t, database, &db.Alert{
		ProjectID: "project-beta",
		SessionID: "sess-3",
		AlertType: "repeated_failure",
		Severity:  "medium",
		Title:     "Other project failure",
		Narrative: "should not show",
		Score:     0.7,
		SourceRef: "failure:beta",
		ExpiresAt: now.Add(time.Hour).Format(time.RFC3339),
	})

	text := callToolText(t, server, "get_active_failures", map[string]any{
		"project_id": "project-alpha",
		"limit":      10,
	})
	if !strings.Contains(text, "Repeated rust build failure") {
		t.Fatalf("expected active failure in %q", text)
	}
	if strings.Contains(text, "Acknowledged failure") || strings.Contains(text, "Other project failure") {
		t.Fatalf("unexpected alert leakage in %q", text)
	}
}

func TestGetProjectTimeline_ShortSessionIDDoesNotPanic(t *testing.T) {
	tmp := t.TempDir()
	database, err := db.Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("db.Open failed: %v", err)
	}
	defer database.Close()

	if err := database.InsertEvent(&db.Event{
		ID:         "e1",
		SessionID:  "s",
		ProjectID:  detectProject(),
		SourceTool: "claude",
		EventType:  "PostToolUse",
		Payload:    "{}",
	}); err != nil {
		t.Fatalf("InsertEvent failed: %v", err)
	}

	s := New(database)
	result := s.getProjectTimeline(map[string]any{"limit": 10.0})
	if result.IsError {
		t.Fatalf("getProjectTimeline returned error: %+v", result)
	}
	if len(result.Content) == 0 || !strings.Contains(result.Content[0].Text, "`s`") {
		t.Fatalf("expected short session id in output, got: %q", result.Content[0].Text)
	}
}

func TestGetDistillationHealth_ReturnsJSON(t *testing.T) {
	tmp := t.TempDir()
	database, err := db.Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("db.Open failed: %v", err)
	}
	defer database.Close()
	now := time.Now().UTC()
	if err := database.RecordDistillationSuccess(now, 500*time.Millisecond, 5, 1, now.Add(10*time.Minute)); err != nil {
		t.Fatalf("RecordDistillationSuccess failed: %v", err)
	}

	s := New(database)
	result := s.getDistillationHealth(nil)
	if result.IsError {
		t.Fatalf("getDistillationHealth returned error: %+v", result)
	}
	if len(result.Content) == 0 || !strings.Contains(result.Content[0].Text, `"status": "success"`) {
		t.Fatalf("unexpected health payload: %q", result.Content[0].Text)
	}
}

func TestGetAlerts_ReturnsFailureAlert(t *testing.T) {
	tmp := t.TempDir()
	database, err := db.Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("db.Open failed: %v", err)
	}
	defer database.Close()
	now := time.Now().UTC()
	if err := database.RecordDistillationFailure(now, 500*time.Millisecond, 20, "bad key", now.Add(10*time.Minute)); err != nil {
		t.Fatalf("RecordDistillationFailure failed: %v", err)
	}

	s := New(database)
	result := s.getAlerts(nil)
	if result.IsError {
		t.Fatalf("getAlerts returned error: %+v", result)
	}
	if len(result.Content) == 0 || !strings.Contains(result.Content[0].Text, "distillation_failed") {
		t.Fatalf("expected distillation_failed alert, got: %q", result.Content[0].Text)
	}
}

func TestGetPrinciples_ShortTimestampDoesNotPanic(t *testing.T) {
	tmp := t.TempDir()
	database, err := db.Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("db.Open failed: %v", err)
	}
	defer database.Close()

	if _, err := database.InsertPrinciple(&db.Principle{
		ID:        "p1",
		TS:        "x",
		Type:      "pattern",
		Title:     "title",
		Narrative: "narrative",
		ProjectID: "proj",
	}); err != nil {
		t.Fatalf("InsertPrinciple failed: %v", err)
	}

	s := New(database)
	result := s.getPrinciples(map[string]any{"limit": 10.0, "project_id": "proj"})
	if result.IsError {
		t.Fatalf("getPrinciples returned error: %+v", result)
	}
	if len(result.Content) == 0 || !strings.Contains(result.Content[0].Text, "**Date**: x") {
		t.Fatalf("expected short date in output, got: %q", result.Content[0].Text)
	}
}

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func callToolText(t *testing.T, server *Server, name string, args map[string]any) string {
	t.Helper()
	result := server.handleToolsCall(Request{
		JSONRPC: "2.0",
		ID:      json.RawMessage("1"),
		Params: mustMarshal(t, map[string]any{
			"name":      name,
			"arguments": args,
		}),
	})
	if result == nil || result.Error != nil {
		t.Fatalf("tool %s returned error: %+v", name, result)
	}
	toolResult, ok := result.Result.(ToolResult)
	if !ok {
		t.Fatalf("unexpected tool result type %T", result.Result)
	}
	if toolResult.IsError {
		t.Fatalf("tool %s returned tool error: %+v", name, toolResult)
	}
	if len(toolResult.Content) == 0 {
		t.Fatalf("tool %s returned no content", name)
	}
	return toolResult.Content[0].Text
}

func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

func mustInsertPrinciple(t *testing.T, database *db.DB, projectID, title string) {
	t.Helper()
	if _, err := database.InsertPrinciple(&db.Principle{
		TS:          time.Now().UTC().Format(time.RFC3339),
		Type:        "pattern",
		Title:       title,
		Narrative:   title + " narrative",
		ImpactScore: 0.8,
		ProjectID:   projectID,
	}); err != nil {
		t.Fatalf("insert principle: %v", err)
	}
}

func mustInsertEvent(t *testing.T, database *db.DB, projectID, payload string) {
	t.Helper()
	if err := database.InsertEvent(&db.Event{
		TS:         time.Now().UTC().Format(time.RFC3339),
		SessionID:  "sess-" + projectID,
		ProjectID:  projectID,
		SourceTool: "codex",
		EventType:  "PostToolUse",
		ToolName:   "Bash",
		Payload:    payload,
	}); err != nil {
		t.Fatalf("insert event: %v", err)
	}
}

func mustUpsertExternalSummary(t *testing.T, database *db.DB, summary *db.ExternalContextSummary) {
	t.Helper()
	if err := database.UpsertExternalContextSummary(summary); err != nil {
		t.Fatalf("upsert external summary: %v", err)
	}
}

func mustUpsertAlert(t *testing.T, database *db.DB, alert *db.Alert) {
	t.Helper()
	if err := database.UpsertActiveAlert(alert); err != nil {
		t.Fatalf("upsert alert: %v", err)
	}
}
