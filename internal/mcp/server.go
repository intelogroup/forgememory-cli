package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/forge/forge/internal/db"
)

// MCP Protocol types
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *MCPError       `json:"error,omitempty"`
}

type MCPError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type ToolResult struct {
	Content []ToolContent `json:"content"`
	IsError bool          `json:"isError"`
}

type ToolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Server handles MCP protocol over stdio.
type Server struct {
	db     *db.DB
	name   string
	reader *bufio.Reader
	writer *bufio.Writer
	Quiet  bool // If true, suppress startup banner (used for stdio transport)
}

// New creates a new MCP server.
func New(database *db.DB) *Server {
	return &Server{
		db:     database,
		name:   "forge",
		reader: bufio.NewReader(os.Stdin),
		writer: bufio.NewWriter(os.Stdout),
	}
}

// Run starts the MCP server loop.
func (s *Server) Run() error {
	log.SetOutput(os.Stderr)
	if !s.Quiet {
		log.Println("Forge MCP server starting (stdio transport)")
	}

	for {
		line, err := s.reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("read error: %w", err)
		}

		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			continue // skip malformed messages
		}

		resp := s.handleRequest(req)
		if resp != nil {
			data, err := json.Marshal(resp)
			if err != nil {
				return fmt.Errorf("marshal response: %w", err)
			}
			data = append(data, '\n')
			if _, err := s.writer.Write(data); err != nil {
				return fmt.Errorf("write response: %w", err)
			}
			if err := s.writer.Flush(); err != nil {
				return fmt.Errorf("flush response: %w", err)
			}
		}
	}
}

func (s *Server) handleRequest(req Request) *Response {
	switch req.Method {
	case "initialize":
		return s.handleInitialize(req)
	case "tools/list":
		return s.handleToolsList(req)
	case "tools/call":
		return s.handleToolsCall(req)
	case "notifications/initialized":
		return nil // notification, no response
	default:
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &MCPError{Code: -32601, Message: "Method not found"},
		}
	}
}

func (s *Server) handleInitialize(req Request) *Response {
	return &Response{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]any{
				"tools": map[string]any{},
			},
			"serverInfo": map[string]any{
				"name":    "forge",
				"version": "0.1.0",
			},
		},
	}
}

func (s *Server) handleToolsList(req Request) *Response {
	tools := []Tool{
		{
			Name:        "get_recent_context",
			Description: "Call at the start of every session to prime context. Returns session summaries, principles, active failures, and cached external context.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"limit": map[string]any{
						"type":        "number",
						"description": "Number of results to return (default 10)",
						"default":     10,
					},
					"project_id": map[string]any{
						"type":        "string",
						"description": "Optional project identifier. Defaults to the current repo/project.",
					},
				},
			},
		},
		{
			Name:        "search_memories",
			Description: "Call before solving an error or implementing a feature to check if this was done before. Full-text search on captured tool-use payloads.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "Search query",
					},
					"limit": map[string]any{
						"type":        "number",
						"description": "Number of results to return (default 10)",
						"default":     10,
					},
					"project_id": map[string]any{
						"type":        "string",
						"description": "Optional project identifier. Defaults to the current repo/project.",
					},
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        "get_principles",
			Description: "Call before making architecture or design decisions. Returns distilled high-level patterns and preferences for this project.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"limit": map[string]any{
						"type":        "number",
						"description": "Number of principles to return (default 10)",
						"default":     10,
					},
					"project_id": map[string]any{
						"type":        "string",
						"description": "Optional project identifier. Defaults to the current repo/project.",
					},
				},
			},
		},
		{
			Name:        "get_session_summaries",
			Description: "Call when you need a narrative of recent work. Returns synthesized summaries of past sessions across agents.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"limit": map[string]any{
						"type":        "number",
						"description": "Number of sessions to return (default 5)",
						"default":     5,
					},
					"project_id": map[string]any{
						"type":        "string",
						"description": "Optional project identifier. Defaults to the current repo/project.",
					},
				},
			},
		},
		{
			Name:        "get_project_timeline",
			Description: "Call to orient yourself on cross-agent history. Returns chronological session entries from Claude, Gemini, and Codex.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"limit": map[string]any{
						"type":        "number",
						"description": "Number of timeline entries to return (default 10)",
						"default":     10,
					},
					"project_id": map[string]any{
						"type":        "string",
						"description": "Optional project identifier. Defaults to the current repo/project.",
					},
				},
			},
		},
		{
			Name:        "get_external_context",
			Description: "Call to retrieve previously fetched library/API docs cached for this project. Does not perform live fetches.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project_id": map[string]any{
						"type":        "string",
						"description": "Optional project identifier. Defaults to the current repo/project.",
					},
					"source": map[string]any{
						"type":        "string",
						"description": "Optional source filter, e.g. context7.",
					},
					"library_name": map[string]any{
						"type":        "string",
						"description": "Optional library filter, e.g. rust or react.",
					},
					"query": map[string]any{
						"type":        "string",
						"description": "Optional text filter applied to cached query/title/narrative.",
					},
					"limit": map[string]any{
						"type":        "number",
						"description": "Number of summaries to return (default 5)",
						"default":     5,
					},
				},
			},
		},
		{
			Name:        "get_active_failures",
			Description: "Call when something is mysteriously broken. Returns unresolved failure alerts detected from prior sessions.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project_id": map[string]any{
						"type":        "string",
						"description": "Optional project identifier. Defaults to the current repo/project.",
					},
					"limit": map[string]any{
						"type":        "number",
						"description": "Number of active failures to return (default 5)",
						"default":     5,
					},
				},
			},
		},
		{
			Name:        "get_distillation_health",
			Description: "Call to check if distillation is running and healthy. Returns last run status, error message, and undistilled backlog count.",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			Name:        "get_alerts",
			Description: "Call to check for active distillation or backlog alerts that may affect memory quality.",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
	}

	return &Response{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  map[string]any{"tools": tools},
	}
}

func (s *Server) handleToolsCall(req Request) *Response {
	var params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return errorResponse(req.ID, "Invalid params")
	}

	var result ToolResult
	switch params.Name {
	case "get_recent_context":
		result = s.getRecentContext(params.Arguments)
	case "search_memories":
		result = s.searchMemories(params.Arguments)
	case "get_principles":
		result = s.getPrinciples(params.Arguments)
	case "get_session_summaries":
		result = s.getSessionSummaries(params.Arguments)
	case "get_project_timeline":
		result = s.getProjectTimeline(params.Arguments)
	case "get_external_context":
		result = s.getExternalContext(params.Arguments)
	case "get_active_failures":
		result = s.getActiveFailures(params.Arguments)
	case "get_distillation_health":
		result = s.getDistillationHealth(params.Arguments)
	case "get_alerts":
		result = s.getAlerts(params.Arguments)
	default:
		return errorResponse(req.ID, "Unknown tool: "+params.Name)
	}

	return &Response{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  result,
	}
}

func (s *Server) getRecentContext(args map[string]any) ToolResult {
	limit := intFromArgs(args, "limit", 10)
	projectID := projectIDFromArgs(args)

	principles, err := s.db.RecentPrinciplesByProject(projectID, limit)
	if err != nil {
		return toolError("Failed to get principles: " + err.Error())
	}
	summaries, _ := s.db.GetRecentSessionSummariesByProject(projectID, 3)
	alerts, _ := s.db.ActiveAlertsByProject(projectID, 3)
	externalSummaries, _ := s.db.FreshExternalContextSummariesByProject(projectID, 3)
	markExternalSummariesUsed(s.db, externalSummaries)

	text := fmt.Sprintf("## Recent Memories: %s\n\n", projectID)

	if len(summaries) > 0 {
		text += "### Recent Sessions\n"
		for _, ss := range summaries {
			ts := ss.TS
			if len(ts) >= 10 {
				ts = ts[:10]
			}
			if ss.Learnings != "" {
				text += fmt.Sprintf("- **[%s]** %s\n", ts, ss.Learnings)
			} else if ss.Summary != "" {
				text += fmt.Sprintf("- **[%s]** %s\n", ts, ss.Summary)
			}
		}
		text += "\n"
	}

	if len(alerts) > 0 {
		text += "### Active Failures\n"
		for _, alert := range alerts {
			text += fmt.Sprintf("- **%s** (%s) %s\n", alert.Title, alert.Severity, alert.Narrative)
		}
		text += "\n"
	}

	if len(externalSummaries) > 0 {
		text += "### Cached External Context\n"
		for _, summary := range externalSummaries {
			text += fmt.Sprintf("- **%s** [%s] %s\n", summary.Title, summary.Source, summary.Narrative)
		}
		text += "\n"
	}

	if len(principles) == 0 {
		text += "No distilled memories yet. Keep working — Forge will capture your patterns.\n"
	} else {
		text += "### Principles\n"
		for _, p := range principles {
			text += fmt.Sprintf("#### %s\n", p.Title)
			text += fmt.Sprintf("- **Type**: %s | **Score**: %.1f | **Date**: %s\n", p.Type, p.ImpactScore, prefix(p.TS, 10))
			if len(p.Concepts) > 0 {
				text += fmt.Sprintf("- **Concepts**: %s\n", strings.Join(p.Concepts, ", "))
			}
			text += fmt.Sprintf("- %s\n\n", p.Narrative)
		}
	}

	total, undistilled, _ := s.db.EventCount()
	text += fmt.Sprintf("---\n*Total events: %d | Undistilled: %d*\n", total, undistilled)

	return ToolResult{
		Content: []ToolContent{{Type: "text", Text: text}},
	}
}

func (s *Server) searchMemories(args map[string]any) ToolResult {
	query, ok := args["query"].(string)
	if !ok || query == "" {
		return toolError("Missing required parameter: query")
	}
	limit := intFromArgs(args, "limit", 10)
	projectID := projectIDFromArgs(args)

	candidateLimit := limit * 6
	if candidateLimit < 20 {
		candidateLimit = 20
	}
	if candidateLimit > 120 {
		candidateLimit = 120
	}

	candidates := make(map[string]db.Event)
	events, err := s.db.SearchEventsByProject(projectID, query, candidateLimit)
	if err != nil {
		return toolError("Search failed: " + err.Error())
	}
	for _, event := range events {
		candidates[event.ID] = event
	}
	for _, token := range topQueryTokens(query, 6) {
		tokenEvents, err := s.db.SearchEventsByProject(projectID, token, candidateLimit)
		if err != nil {
			return toolError("Search failed: " + err.Error())
		}
		for _, event := range tokenEvents {
			candidates[event.ID] = event
		}
	}
	events = make([]db.Event, 0, len(candidates))
	for _, event := range candidates {
		events = append(events, event)
	}
	events = rerankEvents(query, events, limit)

	text := fmt.Sprintf("## Search Results for \"%s\" (%s)\n\n", query, projectID)

	if len(events) == 0 {
		text += "No matching events found.\n"
	} else {
		for _, e := range events {
			text += fmt.Sprintf("### [%s] %s (%s)\n", prefix(e.TS, 10), e.EventType, e.SourceTool)
			payload := e.Payload
			if len(payload) > 300 {
				payload = payload[:300] + "..."
			}
			text += fmt.Sprintf("```\n%s\n```\n\n", payload)
		}
	}

	return ToolResult{
		Content: []ToolContent{{Type: "text", Text: text}},
	}
}

func (s *Server) getPrinciples(args map[string]any) ToolResult {
	limit := intFromArgs(args, "limit", 10)
	projectID := projectIDFromArgs(args)

	principles, err := s.db.RecentPrinciplesByProject(projectID, limit)
	if err != nil {
		return toolError("Failed to get principles: " + err.Error())
	}

	text := fmt.Sprintf("## Distilled Principles: %s\n\n", projectID)

	if len(principles) == 0 {
		text += "No principles distilled yet.\n"
	} else {
		for _, p := range principles {
			text += fmt.Sprintf("### %s [%s]\n", p.Title, p.Type)
			text += fmt.Sprintf("- **Impact**: %.1f | **Date**: %s | **Project**: %s\n",
				p.ImpactScore, prefix(p.TS, 10), p.ProjectID)
			if len(p.Concepts) > 0 {
				text += fmt.Sprintf("- **Concepts**: %s\n", strings.Join(p.Concepts, ", "))
			}
			if len(p.FilesModified) > 0 {
				text += fmt.Sprintf("- **Files**: %s\n", strings.Join(p.FilesModified, ", "))
			}
			text += fmt.Sprintf("- %s\n\n", p.Narrative)
		}
	}

	return ToolResult{
		Content: []ToolContent{{Type: "text", Text: text}},
	}
}

func (s *Server) getSessionSummaries(args map[string]any) ToolResult {
	limit := intFromArgs(args, "limit", 5)
	projectID := projectIDFromArgs(args)

	summaries, err := s.db.GetRecentSessionSummariesByProject(projectID, limit)
	if err != nil {
		return toolError("Failed to get session summaries: " + err.Error())
	}

	text := fmt.Sprintf("## Recent Work Sessions: %s\n\n", projectID)

	if len(summaries) == 0 {
		text += "No session summaries yet. Session summaries are generated automatically at the end of each coding session.\n"
	} else {
		for _, s := range summaries {
			ts := s.TS
			if len(ts) >= 10 {
				ts = ts[:10]
			}
			text += fmt.Sprintf("### Session [%s]\n", ts)
			if s.Request != "" {
				text += fmt.Sprintf("**Goal**: %s\n", s.Request)
			}
			if s.Investigation != "" {
				text += fmt.Sprintf("**Explored**: %s\n", s.Investigation)
			}
			if s.Learnings != "" {
				text += fmt.Sprintf("**Learned**: %s\n", s.Learnings)
			}
			if s.NextSteps != "" {
				text += fmt.Sprintf("**Next**: %s\n", s.NextSteps)
			}
			text += "\n"
		}
	}

	return ToolResult{
		Content: []ToolContent{{Type: "text", Text: text}},
	}
}

func errorResponse(id json.RawMessage, msg string) *Response {
	return &Response{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &MCPError{Code: -32602, Message: msg},
	}
}

func toolError(msg string) ToolResult {
	return ToolResult{
		Content: []ToolContent{{Type: "text", Text: "Error: " + msg}},
		IsError: true,
	}
}

func intFromArgs(args map[string]any, key string, defaultVal int) int {
	if v, ok := args[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		}
	}
	return defaultVal
}

func (s *Server) getProjectTimeline(args map[string]any) ToolResult {
	limit := intFromArgs(args, "limit", 10)
	projectID := projectIDFromArgs(args)

	entries, err := s.db.ProjectTimeline(projectID, limit)
	if err != nil {
		return toolError("Failed to get project timeline: " + err.Error())
	}

	agents, _ := s.db.ProjectAgents(projectID)

	text := fmt.Sprintf("## Project Timeline: %s\n\n", projectID)

	if len(agents) > 0 {
		text += fmt.Sprintf("**Agents in this project**: %s\n\n", strings.Join(agents, ", "))
	}

	if len(entries) == 0 {
		text += "No sessions recorded for this project yet.\n"
	} else {
		text += "| Session | Agent | Events | Start | End |\n"
		text += "|---------|-------|--------|-------|-----|\n"
		for _, e := range entries {
			start := e.StartTS
			if len(start) >= 10 {
				start = start[:10]
			}
			end := e.EndTS
			if len(end) >= 10 {
				end = end[:10]
			}
			text += fmt.Sprintf("| `%s` | %s | %d | %s | %s |\n",
				prefix(e.SessionID, 8), e.PrimaryAgent, e.EventCount, start, end)
		}
	}

	return ToolResult{
		Content: []ToolContent{{Type: "text", Text: text}},
	}
}

func (s *Server) getExternalContext(args map[string]any) ToolResult {
	limit := intFromArgs(args, "limit", 5)
	projectID := projectIDFromArgs(args)
	source := stringFromArgs(args, "source")
	libraryName := stringFromArgs(args, "library_name")
	query := stringFromArgs(args, "query")

	summaries, err := s.db.SearchExternalContextSummariesByProject(projectID, source, libraryName, query, limit)
	if err != nil {
		return toolError("Failed to get external context: " + err.Error())
	}
	markExternalSummariesUsed(s.db, summaries)

	text := fmt.Sprintf("## Cached External Context: %s\n\n", projectID)
	if len(summaries) == 0 {
		text += "No cached external context found.\n"
	} else {
		for _, summary := range summaries {
			text += fmt.Sprintf("### %s [%s]\n", summary.Title, summary.Source)
			text += fmt.Sprintf("- **Library**: %s\n", fallback(summary.LibraryName, "n/a"))
			if summary.Query != "" {
				text += fmt.Sprintf("- **Query**: %s\n", summary.Query)
			}
			if summary.CacheRef != "" {
				text += fmt.Sprintf("- **Cache Ref**: %s\n", summary.CacheRef)
			}
			text += fmt.Sprintf("- %s\n\n", summary.Narrative)
		}
	}

	return ToolResult{
		Content: []ToolContent{{Type: "text", Text: text}},
	}
}

func markExternalSummariesUsed(database *db.DB, summaries []db.ExternalContextSummary) {
	if database == nil || len(summaries) == 0 {
		return
	}
	ids := make([]string, 0, len(summaries))
	for _, summary := range summaries {
		if strings.TrimSpace(summary.ID) != "" {
			ids = append(ids, summary.ID)
		}
	}
	_ = database.TouchExternalContextSummaries(ids, time.Now().UTC().Format(time.RFC3339))
}

type rankedEvent struct {
	event db.Event
	score float64
}

func rerankEvents(query string, events []db.Event, limit int) []db.Event {
	if len(events) == 0 {
		return events
	}
	queryTokens := tokenSet(query)
	queryLower := strings.ToLower(strings.TrimSpace(query))

	ranked := make([]rankedEvent, 0, len(events))
	for _, event := range events {
		score := 0.0
		payload := strings.ToLower(event.Payload)
		if queryLower != "" && strings.Contains(payload, queryLower) {
			score += 1.0
		}

		payloadTokens := tokenSet(event.Payload)
		matches := 0
		for token := range queryTokens {
			if payloadTokens[token] {
				matches++
			}
		}
		if len(queryTokens) > 0 {
			score += (float64(matches) / float64(len(queryTokens))) * 2.2
		}
		if event.Distilled {
			score += 0.15
		}
		score += eventRecencyScore(event.TS)
		ranked = append(ranked, rankedEvent{event: event, score: score})
	}

	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score == ranked[j].score {
			return ranked[i].event.TS > ranked[j].event.TS
		}
		return ranked[i].score > ranked[j].score
	})

	if limit <= 0 || limit > len(ranked) {
		limit = len(ranked)
	}
	out := make([]db.Event, 0, limit)
	for i := 0; i < limit; i++ {
		out = append(out, ranked[i].event)
	}
	return out
}

func eventRecencyScore(ts string) float64 {
	parsed, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return 0
	}
	age := time.Since(parsed)
	switch {
	case age <= 7*24*time.Hour:
		return 0.45
	case age <= 30*24*time.Hour:
		return 0.25
	case age <= 180*24*time.Hour:
		return 0.1
	default:
		return 0
	}
}

func tokenSet(text string) map[string]bool {
	parts := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	set := make(map[string]bool)
	for _, part := range parts {
		if len(part) >= 3 {
			set[part] = true
		}
	}
	return set
}

func topQueryTokens(query string, limit int) []string {
	set := tokenSet(query)
	tokens := make([]string, 0, len(set))
	for token := range set {
		tokens = append(tokens, token)
	}
	sort.Strings(tokens)
	if limit <= 0 || len(tokens) <= limit {
		return tokens
	}
	return tokens[:limit]
}

func (s *Server) getActiveFailures(args map[string]any) ToolResult {
	limit := intFromArgs(args, "limit", 5)
	projectID := projectIDFromArgs(args)

	alerts, err := s.db.ActiveAlertsByProject(projectID, limit)
	if err != nil {
		return toolError("Failed to get active failures: " + err.Error())
	}

	text := fmt.Sprintf("## Active Failures: %s\n\n", projectID)
	if len(alerts) == 0 {
		text += "No active failures.\n"
	} else {
		for _, alert := range alerts {
			text += fmt.Sprintf("### %s [%s]\n", alert.Title, alert.Severity)
			text += fmt.Sprintf("- %s\n", alert.Narrative)
			if alert.SourceRef != "" {
				text += fmt.Sprintf("- **Source Ref**: %s\n", alert.SourceRef)
			}
			text += fmt.Sprintf("- **Score**: %.1f\n\n", alert.Score)
		}
	}

	return ToolResult{
		Content: []ToolContent{{Type: "text", Text: text}},
	}
}

func detectProject() string {
	cwd, _ := os.Getwd()
	if cwd == "" {
		return ""
	}
	if out, err := exec.Command("git", "-C", cwd, "rev-parse", "--show-toplevel").Output(); err == nil {
		root := strings.TrimSpace(string(out))
		if root != "" {
			return filepath.Base(root)
		}
	}
	return filepath.Base(cwd)
}

func projectIDFromArgs(args map[string]any) string {
	if projectID := stringFromArgs(args, "project_id"); projectID != "" {
		return projectID
	}
	return detectProject()
}

func stringFromArgs(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func fallback(value, defaultValue string) string {
	if strings.TrimSpace(value) == "" {
		return defaultValue
	}
	return value
}

func (s *Server) getDistillationHealth(args map[string]any) ToolResult {
	health, err := s.db.GetDistillationHealth()
	if err != nil {
		return toolError("Failed to get distillation health: " + err.Error())
	}
	_, undistilled, _ := s.db.EventCount()

	payload := map[string]any{
		"last_run_at":           health.LastRunAt,
		"last_success_at":       health.LastSuccessAt,
		"status":                health.LastStatus,
		"error_message":         health.LastErrorMessage,
		"undistilled_count":     undistilled,
		"last_error_at":         health.LastErrorAt,
		"consecutive_failures":  health.ConsecutiveFailures,
		"next_scheduled_at":     health.NextScheduledAt,
		"last_attempted_events": health.LastAttemptedEvents,
	}
	b, _ := json.MarshalIndent(payload, "", "  ")
	return ToolResult{
		Content: []ToolContent{{Type: "text", Text: string(b)}},
	}
}

func (s *Server) getAlerts(args map[string]any) ToolResult {
	health, err := s.db.GetDistillationHealth()
	if err != nil {
		return toolError("Failed to get alerts: " + err.Error())
	}
	_, undistilled, _ := s.db.EventCount()

	type alert struct {
		Type     string `json:"type"`
		Severity string `json:"severity"`
		Message  string `json:"message"`
	}
	var alerts []alert
	if health.ConsecutiveFailures >= 1 {
		severity := "warning"
		if health.ConsecutiveFailures >= 3 {
			severity = "error"
		}
		alerts = append(alerts, alert{
			Type:     "distillation_failed",
			Severity: severity,
			Message:  fmt.Sprintf("%d consecutive failures", health.ConsecutiveFailures),
		})
	}
	if undistilled >= 25 {
		alerts = append(alerts, alert{
			Type:     "undistilled_backlog",
			Severity: "warning",
			Message:  fmt.Sprintf("%d undistilled events", undistilled),
		})
	}
	b, _ := json.MarshalIndent(alerts, "", "  ")
	return ToolResult{
		Content: []ToolContent{{Type: "text", Text: string(b)}},
	}
}

func prefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
