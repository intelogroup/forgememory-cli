package db

import (
	"database/sql"
	"time"

	"github.com/google/uuid"
)

// SessionSummary is an LLM-synthesized summary of a single work session.
type SessionSummary struct {
	ID            string `json:"id"`
	TS            string `json:"ts"`
	SessionID     string `json:"session_id"`
	ProjectID     string `json:"project_id"`
	Summary       string `json:"summary"` // legacy / combined text
	Request       string `json:"request"`
	Investigation string `json:"investigation"`
	Learnings     string `json:"learnings"`
	NextSteps     string `json:"next_steps"`
	Tokens        int    `json:"tokens"`
}

// InsertSessionSummary stores a new session summary.
func (d *DB) InsertSessionSummary(s *SessionSummary) error {
	if s.ID == "" {
		s.ID = uuid.New().String()
	}
	if s.TS == "" {
		s.TS = time.Now().UTC().Format(time.RFC3339)
	}
	// Keep summary populated for backward compatibility.
	if s.Summary == "" && s.Learnings != "" {
		s.Summary = s.Learnings
	}
	_, err := d.conn.Exec(
		`INSERT INTO session_summaries
		 (id, ts, session_id, project_id, summary, request, investigation, learnings, next_steps, tokens)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.TS, s.SessionID, s.ProjectID, s.Summary,
		s.Request, s.Investigation, s.Learnings, s.NextSteps, s.Tokens,
	)
	return err
}

// GetRecentSessionSummaries returns the most recent session summaries.
func (d *DB) GetRecentSessionSummaries(limit int) ([]SessionSummary, error) {
	rows, err := d.conn.Query(
		`SELECT id, ts, session_id, project_id,
		        COALESCE(summary,''), COALESCE(request,''), COALESCE(investigation,''),
		        COALESCE(learnings,''), COALESCE(next_steps,''), tokens
		 FROM session_summaries ORDER BY ts DESC LIMIT ?`, limit,
	)
	if err != nil {
		return nil, err
	}
	return scanSessionSummaries(rows)
}

// GetRecentSessionSummariesByProject returns the most recent session summaries for a project.
func (d *DB) GetRecentSessionSummariesByProject(projectID string, limit int) ([]SessionSummary, error) {
	if projectID == "" {
		return d.GetRecentSessionSummaries(limit)
	}
	exact, unixLike, windowsLike := projectIDSelectors(projectID)
	rows, err := d.conn.Query(
		`SELECT id, ts, session_id, project_id,
		        COALESCE(summary,''), COALESCE(request,''), COALESCE(investigation,''),
		        COALESCE(learnings,''), COALESCE(next_steps,''), tokens
		 FROM session_summaries
		 WHERE project_id = ? OR project_id LIKE ? OR project_id LIKE ?
		 ORDER BY ts DESC LIMIT ?`, exact, unixLike, windowsLike, limit,
	)
	if err != nil {
		return nil, err
	}
	return scanSessionSummaries(rows)
}

func scanSessionSummaries(rows *sql.Rows) ([]SessionSummary, error) {
	var out []SessionSummary
	defer rows.Close()
	for rows.Next() {
		var s SessionSummary
		if err := rows.Scan(&s.ID, &s.TS, &s.SessionID, &s.ProjectID,
			&s.Summary, &s.Request, &s.Investigation, &s.Learnings, &s.NextSteps, &s.Tokens); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SessionSummaryCount returns total session summary count.
func (d *DB) SessionSummaryCount() (int, error) {
	var count int
	err := d.conn.QueryRow("SELECT COUNT(*) FROM session_summaries").Scan(&count)
	return count, err
}
