package db

import (
	"time"

	"github.com/google/uuid"
)

// Event represents a raw hook event.
type Event struct {
	ID         string `json:"id"`
	TS         string `json:"ts"`
	SessionID  string `json:"session_id"`
	ProjectID  string `json:"project_id"`
	SourceTool string `json:"source_tool"`
	EventType  string `json:"event_type"`
	ToolName   string `json:"tool_name,omitempty"`
	Payload    string `json:"payload"`
	Distilled  bool   `json:"distilled"`
}

// InsertEvent stores a new event.
func (d *DB) InsertEvent(e *Event) error {
	if e.ID == "" {
		e.ID = uuid.New().String()
	}
	if e.TS == "" {
		e.TS = time.Now().UTC().Format(time.RFC3339)
	}
	_, err := d.conn.Exec(
		`INSERT INTO events (id, ts, session_id, project_id, source_tool, event_type, tool_name, payload, distilled)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0)`,
		e.ID, e.TS, e.SessionID, e.ProjectID, e.SourceTool, e.EventType, e.ToolName, e.Payload,
	)
	return err
}

// UndistilledEvents returns events that haven't been processed.
func (d *DB) UndistilledEvents(limit int) ([]Event, error) {
	rows, err := d.conn.Query(
		`SELECT id, ts, session_id, project_id, source_tool, event_type, tool_name, payload, distilled
		 FROM events WHERE distilled=0 ORDER BY ts ASC LIMIT ?`, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []Event
	for rows.Next() {
		var e Event
		var distilled int
		if err := rows.Scan(&e.ID, &e.TS, &e.SessionID, &e.ProjectID, &e.SourceTool, &e.EventType, &e.ToolName, &e.Payload, &distilled); err != nil {
			return nil, err
		}
		e.Distilled = distilled == 1
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
func (d *DB) SearchEvents(query string, limit int) ([]Event, error) {
	rows, err := d.conn.Query(
		`SELECT e.id, e.ts, e.session_id, e.project_id, e.source_tool, e.event_type, e.tool_name, e.payload, e.distilled
		 FROM events e
		 JOIN events_fts f ON e.rowid = f.rowid
		 WHERE events_fts MATCH ?
		 ORDER BY rank
		 LIMIT ?`, query, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []Event
	for rows.Next() {
		var e Event
		var distilled int
		if err := rows.Scan(&e.ID, &e.TS, &e.SessionID, &e.ProjectID, &e.SourceTool, &e.EventType, &e.ToolName, &e.Payload, &distilled); err != nil {
			return nil, err
		}
		e.Distilled = distilled == 1
		events = append(events, e)
	}
	return events, rows.Err()
}

// SessionEvents returns the most recent events for a specific session.
func (d *DB) SessionEvents(sessionID string, limit int) ([]Event, error) {
	rows, err := d.conn.Query(
		`SELECT id, ts, session_id, project_id, source_tool, event_type, tool_name, payload, distilled
		 FROM events WHERE session_id=? ORDER BY ts DESC LIMIT ?`, sessionID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []Event
	for rows.Next() {
		var e Event
		var distilled int
		if err := rows.Scan(&e.ID, &e.TS, &e.SessionID, &e.ProjectID, &e.SourceTool, &e.EventType, &e.ToolName, &e.Payload, &distilled); err != nil {
			return nil, err
		}
		e.Distilled = distilled == 1
		events = append(events, e)
	}
	return events, rows.Err()
}

// RecentEvents returns the most recent events.
func (d *DB) RecentEvents(limit int) ([]Event, error) {
	rows, err := d.conn.Query(
		`SELECT id, ts, session_id, project_id, source_tool, event_type, tool_name, payload, distilled
		 FROM events ORDER BY ts DESC LIMIT ?`, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []Event
	for rows.Next() {
		var e Event
		var distilled int
		if err := rows.Scan(&e.ID, &e.TS, &e.SessionID, &e.ProjectID, &e.SourceTool, &e.EventType, &e.ToolName, &e.Payload, &distilled); err != nil {
			return nil, err
		}
		e.Distilled = distilled == 1
		events = append(events, e)
	}
	return events, rows.Err()
}
