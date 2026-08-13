package db

import (
	"database/sql"
	"time"

	"github.com/google/uuid"
)

// FailureSignature tracks a repeating unresolved error fingerprint.
type FailureSignature struct {
	ID                string `json:"id"`
	ProjectID         string `json:"project_id"`
	SessionID         string `json:"session_id"`
	SourceTool        string `json:"source_tool"`
	ToolName          string `json:"tool_name"`
	CommandFamily     string `json:"command_family"`
	Fingerprint       string `json:"fingerprint"`
	ErrorKind         string `json:"error_kind"`
	NormalizedMessage string `json:"normalized_message"`
	FirstSeenTS       string `json:"first_seen_ts"`
	LastSeenTS        string `json:"last_seen_ts"`
	RepeatCount       int    `json:"repeat_count"`
	ResolvedTS        string `json:"resolved_ts"`
}

// Alert is an active prompt-injectable issue detected from prior events.
type Alert struct {
	ID           string  `json:"id"`
	TS           string  `json:"ts"`
	ProjectID    string  `json:"project_id"`
	SessionID    string  `json:"session_id"`
	AlertType    string  `json:"alert_type"`
	Severity     string  `json:"severity"`
	Title        string  `json:"title"`
	Narrative    string  `json:"narrative"`
	Fingerprint  string  `json:"fingerprint"`
	Score        float64 `json:"score"`
	SourceRef    string  `json:"source_ref"`
	ExpiresAt    string  `json:"expires_at"`
	Acknowledged bool    `json:"acknowledged"`
}

// UpsertFailureSignature atomically inserts or increments an unresolved failure fingerprint.
func (d *DB) UpsertFailureSignature(sig *FailureSignature) (*FailureSignature, error) {
	if sig.FirstSeenTS == "" {
		sig.FirstSeenTS = time.Now().UTC().Format(time.RFC3339)
	}
	if sig.LastSeenTS == "" {
		sig.LastSeenTS = sig.FirstSeenTS
	}
	if sig.ID == "" {
		sig.ID = uuid.New().String()
	}
	if sig.RepeatCount == 0 {
		sig.RepeatCount = 1
	}

	// Single atomic statement: no read-before-write race.
	_, err := d.conn.Exec(
		`INSERT INTO failure_signatures
		 (id, project_id, session_id, source_tool, tool_name, command_family, fingerprint, error_kind, normalized_message,
		  first_seen_ts, last_seen_ts, repeat_count, resolved_ts)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(project_id, session_id, fingerprint, resolved_ts) DO UPDATE
		 SET last_seen_ts = excluded.last_seen_ts,
		     repeat_count = repeat_count + 1`,
		sig.ID, sig.ProjectID, sig.SessionID, sig.SourceTool, sig.ToolName, sig.CommandFamily, sig.Fingerprint,
		sig.ErrorKind, sig.NormalizedMessage, sig.FirstSeenTS, sig.LastSeenTS, sig.RepeatCount, sig.ResolvedTS,
	)
	if err != nil {
		return nil, err
	}
	return d.getActiveFailureSignature(sig.ProjectID, sig.SessionID, sig.Fingerprint)
}

func (d *DB) getActiveFailureSignature(projectID, sessionID, fingerprint string) (*FailureSignature, error) {
	row := d.conn.QueryRow(
		`SELECT id, project_id, session_id, source_tool, tool_name, COALESCE(command_family,''), fingerprint, error_kind, normalized_message,
		        first_seen_ts, last_seen_ts, repeat_count, resolved_ts
		 FROM failure_signatures
		 WHERE project_id=? AND session_id=? AND fingerprint=? AND resolved_ts=''
		 LIMIT 1`,
		projectID, sessionID, fingerprint,
	)
	var sig FailureSignature
	err := row.Scan(
		&sig.ID, &sig.ProjectID, &sig.SessionID, &sig.SourceTool, &sig.ToolName, &sig.CommandFamily, &sig.Fingerprint,
		&sig.ErrorKind, &sig.NormalizedMessage, &sig.FirstSeenTS, &sig.LastSeenTS, &sig.RepeatCount, &sig.ResolvedTS,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &sig, nil
}

// FailureSignaturesByProject returns all failure signatures recorded for a project.
func (d *DB) FailureSignaturesByProject(projectID string) ([]FailureSignature, error) {
	return d.failureSignaturesByProject(projectID, 0, false)
}

// FailureSignaturesByProjectLimited returns at most limit unresolved, repeated
// failure signatures, ordered by most recently seen. It is intended for
// bounded background work that only needs detector-eligible history.
func (d *DB) FailureSignaturesByProjectLimited(projectID string, limit int) ([]FailureSignature, error) {
	if limit <= 0 {
		return nil, nil
	}
	return d.failureSignaturesByProject(projectID, limit, true)
}

func (d *DB) failureSignaturesByProject(projectID string, limit int, eligibleOnly bool) ([]FailureSignature, error) {
	query := `SELECT id, project_id, session_id, source_tool, tool_name, COALESCE(command_family,''), fingerprint, error_kind, normalized_message,
		        first_seen_ts, last_seen_ts, repeat_count, resolved_ts
		 FROM failure_signatures
		 WHERE project_id=?`
	args := []any{projectID}
	if eligibleOnly {
		query += ` AND repeat_count >= 2 AND resolved_ts=''`
	}
	if limit > 0 {
		query += ` ORDER BY last_seen_ts DESC, id DESC LIMIT ?`
		args = append(args, limit)
	}
	rows, err := d.conn.Query(
		query,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sigs []FailureSignature
	for rows.Next() {
		var sig FailureSignature
		if err := rows.Scan(
			&sig.ID, &sig.ProjectID, &sig.SessionID, &sig.SourceTool, &sig.ToolName, &sig.CommandFamily, &sig.Fingerprint,
			&sig.ErrorKind, &sig.NormalizedMessage, &sig.FirstSeenTS, &sig.LastSeenTS, &sig.RepeatCount, &sig.ResolvedTS,
		); err != nil {
			return nil, err
		}
		sigs = append(sigs, sig)
	}
	return sigs, rows.Err()
}

// ActiveAlertsByProject returns recent active alerts for a project.
func (d *DB) ActiveAlertsByProject(projectID string, limit int) ([]Alert, error) {
	if limit <= 0 {
		limit = 5
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if projectID == "" {
		rows, err := d.conn.Query(
			`SELECT id, ts, project_id, session_id, alert_type, severity, title, narrative,
			        fingerprint, score, source_ref, COALESCE(expires_at,''), acknowledged
			 FROM alerts
			 WHERE acknowledged=0 AND (expires_at='' OR expires_at>?)
			 ORDER BY score DESC, ts DESC LIMIT ?`,
			now, limit,
		)
		if err != nil {
			return nil, err
		}
		return scanAlerts(rows)
	}

	exact, unixLike, windowsLike := projectIDSelectors(projectID)
	rows, err := d.conn.Query(
		`SELECT id, ts, project_id, session_id, alert_type, severity, title, narrative,
		        fingerprint, score, source_ref, COALESCE(expires_at,''), acknowledged
		 FROM alerts
		 WHERE acknowledged=0
		   AND (expires_at='' OR expires_at>?)
		   AND (project_id=? OR project_id LIKE ? OR project_id LIKE ?)
		 ORDER BY score DESC, ts DESC LIMIT ?`,
		now, exact, unixLike, windowsLike, limit,
	)
	if err != nil {
		return nil, err
	}
	return scanAlerts(rows)
}

// ResolveFailureSignatures marks active signatures resolved for a command in a session/project.
// It returns the resolved signature IDs so matching alerts can be acknowledged precisely.
func (d *DB) ResolveFailureSignatures(projectID, sessionID, toolName, commandFamily, resolvedTS string) ([]string, error) {
	if projectID == "" || sessionID == "" || resolvedTS == "" {
		return nil, nil
	}
	query := `SELECT id FROM failure_signatures
		WHERE project_id=? AND session_id=? AND resolved_ts=''`
	args := []any{projectID, sessionID}
	if toolName != "" {
		query += ` AND tool_name=?`
		args = append(args, toolName)
	}
	if commandFamily != "" {
		query += ` AND command_family=?`
		args = append(args, commandFamily)
	}
	rows, err := d.conn.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}

	update := `UPDATE failure_signatures SET resolved_ts=? WHERE id=?`
	for _, id := range ids {
		if _, err := d.conn.Exec(update, resolvedTS, id); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

// AcknowledgeAlerts marks active repeated-failure alerts resolved for specific source refs.
func (d *DB) AcknowledgeAlerts(projectID, sessionID, alertType, resolvedTS string, sourceRefs []string) error {
	if projectID == "" || sessionID == "" || alertType == "" || len(sourceRefs) == 0 {
		return nil
	}
	query := `UPDATE alerts SET acknowledged=1, expires_at=? WHERE project_id=? AND session_id=? AND alert_type=? AND acknowledged=0 AND source_ref IN (?`
	args := []any{resolvedTS, projectID, sessionID, alertType, sourceRefs[0]}
	for _, sourceRef := range sourceRefs[1:] {
		query += ",?"
		args = append(args, sourceRef)
	}
	query += ")"
	_, err := d.conn.Exec(query, args...)
	return err
}

// UpsertActiveAlert inserts or refreshes an active alert.
func (d *DB) UpsertActiveAlert(alert *Alert) error {
	if alert.ID == "" {
		alert.ID = uuid.New().String()
	}
	if alert.TS == "" {
		alert.TS = time.Now().UTC().Format(time.RFC3339)
	}
	ack := 0
	if alert.Acknowledged {
		ack = 1
	}
	_, err := d.conn.Exec(
		`INSERT INTO alerts
		 (id, ts, project_id, session_id, alert_type, severity, title, narrative, fingerprint,
		  score, source_ref, expires_at, acknowledged)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(project_id, session_id, alert_type, fingerprint, acknowledged)
		 DO UPDATE SET
		   ts=excluded.ts,
		   severity=excluded.severity,
		   title=excluded.title,
		   narrative=excluded.narrative,
		   score=excluded.score,
		   source_ref=excluded.source_ref,
		   expires_at=excluded.expires_at`,
		alert.ID, alert.TS, alert.ProjectID, alert.SessionID, alert.AlertType, alert.Severity,
		alert.Title, alert.Narrative, alert.Fingerprint, alert.Score, alert.SourceRef, alert.ExpiresAt, ack,
	)
	return err
}

func scanAlerts(rows interface {
	Next() bool
	Scan(...any) error
	Close() error
	Err() error
}) ([]Alert, error) {
	defer rows.Close()
	var alerts []Alert
	for rows.Next() {
		var alert Alert
		var acknowledged int
		if err := rows.Scan(
			&alert.ID, &alert.TS, &alert.ProjectID, &alert.SessionID, &alert.AlertType, &alert.Severity,
			&alert.Title, &alert.Narrative, &alert.Fingerprint, &alert.Score, &alert.SourceRef,
			&alert.ExpiresAt, &acknowledged,
		); err != nil {
			return nil, err
		}
		alert.Acknowledged = acknowledged == 1
		alerts = append(alerts, alert)
	}
	return alerts, rows.Err()
}
