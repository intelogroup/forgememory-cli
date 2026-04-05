package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

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
			Description: "Returns distilled memories and session summaries from past work sessions. Use when the user asks about past work, decisions, or patterns.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"limit": map[string]any{
						"type":        "number",
						"description": "Number of results to return (default 10)",
						"default":     10,
					},
				},
			},
		},
		{
			Name:        "search_memories",
			Description: "Full-text search on event payloads. Use when the user asks 'did I fix this before?' or about past errors.",
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
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        "get_principles",
			Description: "Returns distilled high-level principles (architecture decisions, patterns, preferences). Use when the user asks about project conventions or past decisions.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"limit": map[string]any{
						"type":        "number",
						"description": "Number of principles to return (default 10)",
						"default":     10,
					},
				},
			},
		},
		{
			Name:        "get_session_summaries",
			Description: "Returns synthesized summaries of recent work sessions. Use when the user asks what they were working on before a break or yesterday.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"limit": map[string]any{
						"type":        "number",
						"description": "Number of sessions to return (default 5)",
						"default":     5,
					},
				},
			},
		},
		{
			Name:        "get_project_timeline",
			Description: "Returns a cross-agent timeline for the current project showing all sessions from Claude, Gemini, and Codex. Use when the user asks what happened in this project across different agents.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"limit": map[string]any{
						"type":        "number",
						"description": "Number of timeline entries to return (default 10)",
						"default":     10,
					},
				},
			},
		},
		{
			Name:        "get_distillation_health",
			Description: "Returns latest distillation health state including last success/failure, error message, and undistilled backlog.",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			Name:        "get_alerts",
			Description: "Returns active distillation alerts for agent-visible failures and backlog growth.",
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

	principles, err := s.db.RecentPrinciples(limit)
	if err != nil {
		return toolError("Failed to get principles: " + err.Error())
	}
	summaries, _ := s.db.GetRecentSessionSummaries(3)

	text := "## Recent Memories\n\n"

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

	events, err := s.db.SearchEvents(query, limit)
	if err != nil {
		return toolError("Search failed: " + err.Error())
	}

	text := fmt.Sprintf("## Search Results for \"%s\"\n\n", query)

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

	principles, err := s.db.RecentPrinciples(limit)
	if err != nil {
		return toolError("Failed to get principles: " + err.Error())
	}

	text := "## Distilled Principles\n\n"

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

	summaries, err := s.db.GetRecentSessionSummaries(limit)
	if err != nil {
		return toolError("Failed to get session summaries: " + err.Error())
	}

	text := "## Recent Work Sessions\n\n"

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
	projectID := detectProject()

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

func detectProject() string {
	// Use current working directory as project ID
	cwd, _ := os.Getwd()
	parts := strings.Split(cwd, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return cwd
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
