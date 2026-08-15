package db

import (
	"database/sql"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/forge/forge/internal/sanitize"
	"github.com/google/uuid"
)

const eventSelect = `id, ts, trace_id, span_id, parent_span_id, sequence, duration_ms, status, exit_code, model, task_id, cwd, git_branch, git_commit, files, transcript_path, session_id, project_id, source_tool, event_type, COALESCE(tool_name,''), payload, distilled`

type eventScanner interface {
	Scan(dest ...any) error
}

func scanEvent(row eventScanner) (Event, error) {
	var e Event
	var distilled int
	err := row.Scan(&e.ID, &e.TS, &e.TraceID, &e.SpanID, &e.ParentSpanID, &e.Sequence, &e.DurationMS, &e.Status, &e.ExitCode, &e.Model, &e.TaskID, &e.CWD, &e.GitBranch, &e.GitCommit, &e.Files, &e.TranscriptPath, &e.SessionID, &e.ProjectID, &e.SourceTool, &e.EventType, &e.ToolName, &e.Payload, &distilled)
	e.Distilled = distilled == 1
	return e, err
}

// Event represents a raw hook event.
type Event struct {
	ID             string `json:"id"`
	TS             string `json:"ts"`
	TraceID        string `json:"trace_id"`
	SpanID         string `json:"span_id"`
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
	SourceTool     string `json:"source_tool"`
	EventType      string `json:"event_type"`
	ToolName       string `json:"tool_name,omitempty"`
	Payload        string `json:"payload"`
	Distilled      bool   `json:"distilled"`
}

// InsertEvent stores a new event.
func (d *DB) InsertEvent(e *Event) error {
	e.Payload = sanitize.ScrubSecrets(e.Payload)

	const maxPayloadBytes = 64 * 1024 // 64KB
	if len(e.Payload) > maxPayloadBytes {
		suffix := "...[TRUNCATED]"
		limit := maxPayloadBytes - len(suffix)
		if limit < 0 {
			limit = 0
		}
		truncated := e.Payload[:limit]
		for len(truncated) > 0 && !utf8.ValidString(truncated) {
			truncated = truncated[:len(truncated)-1]
		}
		e.Payload = truncated + suffix
	}

	if e.ID == "" {
		e.ID = uuid.New().String()
	}
	if e.TraceID == "" {
		e.TraceID = e.SessionID
	}
	if e.SpanID == "" {
		e.SpanID = e.ID
	}
	if e.TS == "" {
		e.TS = time.Now().UTC().Format(time.RFC3339)
	}
	_, err := d.conn.Exec(
		`INSERT OR IGNORE INTO events (id, ts, trace_id, span_id, parent_span_id, sequence, duration_ms, status, exit_code, model, task_id, cwd, git_branch, git_commit, files, transcript_path, session_id, project_id, source_tool, event_type, tool_name, payload, distilled)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`,
		e.ID, e.TS, e.TraceID, e.SpanID, e.ParentSpanID, e.Sequence, e.DurationMS, e.Status, e.ExitCode, e.Model, e.TaskID, e.CWD, e.GitBranch, e.GitCommit, e.Files, e.TranscriptPath, e.SessionID, e.ProjectID, e.SourceTool, e.EventType, e.ToolName, e.Payload,
	)
	return err
}

// UndistilledEvents returns events that haven't been processed.
// It finds the oldest session+project that has undistilled events, and returns
// undistilled events belonging to that specific session+project up to the limit.
// This prevents interleaving/fragmentation issues across projects.
//
// When includeUnknown is false (default), events with session_id='unknown' are
// excluded — orphans from hooks that fired without SessionStart context. This
// matches the v0.5.13 behavior and is what `forge distill` (one-shot) wants.
// When includeUnknown is true, orphans are included so `forge distill --all`
// can drain them instead of silently stalling the backlog forever (#34).
func (d *DB) UndistilledEvents(limit int) ([]Event, error) {
	return d.UndistilledEventsFiltered(limit, false)
}

// UndistilledEventsIncludingUnknown drains the next session+project batch
// regardless of session_id value. Used by `forge distill --all` so the drain
// loop is not blocked behind a permanently-excluded 'unknown' backlog.
func (d *DB) UndistilledEventsIncludingUnknown(limit int) ([]Event, error) {
	return d.UndistilledEventsFiltered(limit, true)
}

func (d *DB) UndistilledEventsFiltered(limit int, includeUnknown bool) ([]Event, error) {
	var targetSession, targetProject string
	query := `SELECT session_id, project_id FROM events
		 WHERE distilled=0 AND session_id != 'unknown'
		 ORDER BY ts ASC LIMIT 1`
	if includeUnknown {
		query = `SELECT session_id, project_id FROM events
		 WHERE distilled=0
		 ORDER BY ts ASC LIMIT 1`
	}
	err := d.conn.QueryRow(query).Scan(&targetSession, &targetProject)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	rows, err := d.conn.Query(
		`SELECT `+eventSelect+`
		 FROM events
		 WHERE distilled=0 AND session_id = ? AND project_id = ?
		 ORDER BY ts ASC LIMIT ?`, targetSession, targetProject, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// MarkDistilled marks events as processed.
func (d *DB) MarkDistilled(ids []string) error {
	tx, err := d.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare("UPDATE events SET distilled=1 WHERE id=?")
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, id := range ids {
		if _, err := stmt.Exec(id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// EventCount returns total and undistilled event counts.
func (d *DB) EventCount() (total, undistilled int, err error) {
	err = d.conn.QueryRow("SELECT COUNT(*) FROM events").Scan(&total)
	if err != nil {
		return 0, 0, err
	}
	err = d.conn.QueryRow("SELECT COUNT(*) FROM events WHERE distilled=0").Scan(&undistilled)
	if err != nil {
		return total, 0, err
	}
	return total, undistilled, nil
}

// SearchEvents does full-text search on event payloads.
// The query is wrapped in FTS5 double-quote phrase syntax so hyphens and other
// special characters are treated as literals rather than operators.
func (d *DB) SearchEvents(query string, limit int) ([]Event, error) {
	// Escape any embedded double-quotes by doubling them, then phrase-quote the whole thing.
	ftsQuery := `"` + strings.ReplaceAll(query, `"`, `""`) + `"`
	rows, err := d.conn.Query(
		`SELECT e.id, e.ts, e.trace_id, e.span_id, e.parent_span_id, e.sequence, e.duration_ms, e.status, e.exit_code, e.model, e.task_id, e.cwd, e.git_branch, e.git_commit, e.files, e.transcript_path, e.session_id, e.project_id, e.source_tool, e.event_type, COALESCE(e.tool_name,''), e.payload, e.distilled
		 FROM events e
		 JOIN events_fts f ON e.rowid = f.rowid
		 WHERE events_fts MATCH ?
		 ORDER BY rank
		 LIMIT ?`, ftsQuery, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// SearchEventsByProject does full-text search on event payloads scoped to a project.
func (d *DB) SearchEventsByProject(projectID, query string, limit int) ([]Event, error) {
	if projectID == "" {
		return d.SearchEvents(query, limit)
	}
	ftsQuery := `"` + strings.ReplaceAll(query, `"`, `""`) + `"`
	exact, unixLike, windowsLike := projectIDSelectors(projectID)
	rows, err := d.conn.Query(
		`SELECT e.id, e.ts, e.trace_id, e.span_id, e.parent_span_id, e.sequence, e.duration_ms, e.status, e.exit_code, e.model, e.task_id, e.cwd, e.git_branch, e.git_commit, e.files, e.transcript_path, e.session_id, e.project_id, e.source_tool, e.event_type, COALESCE(e.tool_name,''), e.payload, e.distilled
		 FROM events e
		 JOIN events_fts f ON e.rowid = f.rowid
		 WHERE events_fts MATCH ?
		   AND (e.project_id = ? OR e.project_id LIKE ? OR e.project_id LIKE ?)
		 ORDER BY rank
		 LIMIT ?`, ftsQuery, exact, unixLike, windowsLike, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// UndistilledCompletedSessions returns session IDs that have a boundary event
// (Stop/SessionEnd/AfterAgent) but no checkpoint summary yet. These are sessions
// that have ended and are ready for distillation. Returns at most `limit` sessions.
func (d *DB) UndistilledCompletedSessions(limit int) ([]string, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := d.conn.Query(
		`SELECT DISTINCT e.session_id FROM events e
		 WHERE e.event_type IN ('Stop','SessionEnd','AfterAgent')
		   AND e.session_id NOT IN (
		     SELECT session_id FROM session_summaries
		     WHERE session_id != ''
		   )
		   AND e.session_id != 'unknown'
		 ORDER BY e.ts DESC LIMIT ?`, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sessions []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}

// MarkSessionDistilled marks all events for a session as distilled.
func (d *DB) MarkSessionDistilled(sessionID string) error {
	_, err := d.conn.Exec("UPDATE events SET distilled=1 WHERE session_id=?", sessionID)
	return err
}

// SessionEvents returns the most recent events for a specific session.
func (d *DB) SessionEvents(sessionID string, limit int) ([]Event, error) {
	rows, err := d.conn.Query(
		`SELECT `+eventSelect+`
		 FROM events WHERE session_id=? ORDER BY ts DESC LIMIT ?`, sessionID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// TraceEvents returns events belonging to one correlated trace.
func (d *DB) TraceEvents(traceID string, limit int) ([]Event, error) {
	rows, err := d.conn.Query(
		`SELECT `+eventSelect+` FROM events WHERE trace_id=? ORDER BY ts ASC LIMIT ?`, traceID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// SessionEventsUpTo returns session events in chronological order, optionally
// capped at a cutoff timestamp. limit > 0 applies LIMIT n; limit == 0 returns
// all matching events (used by distill-agent for large sessions).
func (d *DB) SessionEventsUpTo(sessionID, cutoffTS string, limit int) ([]Event, error) {
	query := `SELECT ` + eventSelect + `
			 FROM events WHERE session_id=?`
	args := []any{sessionID}
	if strings.TrimSpace(cutoffTS) != "" {
		query += ` AND ts <= ?`
		args = append(args, cutoffTS)
	}
	query += ` ORDER BY ts ASC`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := d.conn.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// SessionEventsForCheckpoint returns session events up to cutoffTS for
// checkpoint synthesis, keeping headLimit events from the start of the
// session (the initial request/investigation) plus tailLimit events from
// the end (the outcome, and critically the terminal Stop/SessionEnd event
// carrying last_assistant_message) when the session has more events than
// fit in that combined budget. A plain LIMIT (as SessionEventsUpTo applies)
// always keeps the oldest N events, which silently drops the terminal
// boundary event — and the assistant's own final message with it — for any
// session longer than the limit. Result stays in chronological order.
func (d *DB) SessionEventsForCheckpoint(sessionID, cutoffTS string, headLimit, tailLimit int) ([]Event, error) {
	all, err := d.SessionEventsUpTo(sessionID, cutoffTS, 0)
	if err != nil {
		return nil, err
	}
	if len(all) <= headLimit+tailLimit {
		return all, nil
	}
	out := make([]Event, 0, headLimit+tailLimit)
	out = append(out, all[:headLimit]...)
	out = append(out, all[len(all)-tailLimit:]...)
	return out, nil
}

// RecentEvents returns the most recent events.
func (d *DB) RecentEvents(limit int) ([]Event, error) {
	rows, err := d.conn.Query(
		`SELECT `+eventSelect+`
		 FROM events ORDER BY ts DESC LIMIT ?`, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// ProjectTimeline returns a cross-agent timeline for a project.
// Groups events by session and aggregates across all agents (claude/gemini/codex).
func (d *DB) ProjectTimeline(projectID string, limit int) ([]ProjectTimelineEntry, error) {
	exact, unixLike, windowsLike := projectIDSelectors(projectID)
	rows, err := d.conn.Query(`
		SELECT session_id,
			CASE
				WHEN COUNT(DISTINCT source_tool)=1 THEN MIN(source_tool)
				ELSE 'multi'
			END as primary_agent,
			MIN(ts) as start_ts, MAX(ts) as end_ts,
			COUNT(*) as event_count
		FROM events
		WHERE project_id = ? OR project_id LIKE ? OR project_id LIKE ?
		GROUP BY session_id
		ORDER BY start_ts DESC
		LIMIT ?`, exact, unixLike, windowsLike, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []ProjectTimelineEntry
	for rows.Next() {
		var e ProjectTimelineEntry
		if err := rows.Scan(&e.SessionID, &e.PrimaryAgent, &e.StartTS, &e.EndTS, &e.EventCount); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// ProjectTimelineEntry represents a session summary in the project timeline.
type ProjectTimelineEntry struct {
	SessionID    string `json:"session_id"`
	PrimaryAgent string `json:"primary_agent"`
	StartTS      string `json:"start_ts"`
	EndTS        string `json:"end_ts"`
	EventCount   int    `json:"event_count"`
}

// ProjectAgents returns all unique agents that have worked on a project.
func (d *DB) ProjectAgents(projectID string) ([]string, error) {
	exact, unixLike, windowsLike := projectIDSelectors(projectID)
	rows, err := d.conn.Query(
		`SELECT DISTINCT source_tool FROM events
		 WHERE project_id = ? OR project_id LIKE ? OR project_id LIKE ?
		 ORDER BY source_tool`, exact, unixLike, windowsLike,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var agents []string
	for rows.Next() {
		var agent string
		if err := rows.Scan(&agent); err != nil {
			return nil, err
		}
		agents = append(agents, agent)
	}
	return agents, rows.Err()
}
