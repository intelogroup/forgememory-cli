package db

import (
	"database/sql"
	"time"

	"github.com/google/uuid"
)

// CrossSessionPattern is a pattern mined across multiple work sessions.
type CrossSessionPattern struct {
	ID                 string  `json:"id"`
	TS                 string  `json:"ts"`
	ProjectID          string  `json:"project_id"`
	Pattern            string  `json:"pattern"`
	PatternType        string  `json:"pattern_type"`
	EvidenceSessionIDs string  `json:"evidence_session_ids"`
	Confidence         float64 `json:"confidence"`
	Synthesized        bool    `json:"synthesized"`
}

// InsertCrossSessionPattern stores a new cross-session pattern.
func (d *DB) InsertCrossSessionPattern(p *CrossSessionPattern) error {
	if p.ID == "" {
		p.ID = uuid.New().String()
	}
	if p.TS == "" {
		p.TS = time.Now().UTC().Format(time.RFC3339)
	}
	if p.EvidenceSessionIDs == "" {
		p.EvidenceSessionIDs = "[]"
	}
	_, err := d.conn.Exec(
		`INSERT INTO cross_session_patterns
		 (id, ts, project_id, pattern, pattern_type, evidence_session_ids, confidence, synthesized)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.TS, p.ProjectID, p.Pattern, p.PatternType,
		p.EvidenceSessionIDs, p.Confidence, boolToInt(p.Synthesized),
	)
	return err
}

// RecentCrossSessionPatternsByProject returns the most recent patterns for a project.
func (d *DB) RecentCrossSessionPatternsByProject(projectID string, limit int) ([]CrossSessionPattern, error) {
	if projectID == "" {
		rows, err := d.conn.Query(
			`SELECT id, ts, project_id, pattern, pattern_type, evidence_session_ids, confidence, synthesized
			 FROM cross_session_patterns ORDER BY ts DESC LIMIT ?`, limit,
		)
		if err != nil {
			return nil, err
		}
		return scanCrossSessionPatterns(rows)
	}
	exact, unixLike, windowsLike := projectIDSelectors(projectID)
	rows, err := d.conn.Query(
		`SELECT id, ts, project_id, pattern, pattern_type, evidence_session_ids, confidence, synthesized
		 FROM cross_session_patterns
		 WHERE project_id = ? OR project_id LIKE ? OR project_id LIKE ?
		 ORDER BY ts DESC LIMIT ?`, exact, unixLike, windowsLike, limit,
	)
	if err != nil {
		return nil, err
	}
	return scanCrossSessionPatterns(rows)
}

// CrossSessionPatternCount returns total pattern count.
func (d *DB) CrossSessionPatternCount() (int, error) {
	var count int
	err := d.conn.QueryRow("SELECT COUNT(*) FROM cross_session_patterns").Scan(&count)
	return count, err
}

// MarkCrossSessionSynthesized marks patterns as synthesized (used in principles).
func (d *DB) MarkCrossSessionSynthesized(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := d.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		if _, err := tx.Exec("UPDATE cross_session_patterns SET synthesized=1 WHERE id=?", id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UnSynthesizedPatterns returns patterns that haven't been incorporated into principles.
func (d *DB) UnSynthesizedPatterns(limit int) ([]CrossSessionPattern, error) {
	rows, err := d.conn.Query(
		`SELECT id, ts, project_id, pattern, pattern_type, evidence_session_ids, confidence, synthesized
		 FROM cross_session_patterns WHERE synthesized=0 ORDER BY ts DESC LIMIT ?`, limit,
	)
	if err != nil {
		return nil, err
	}
	return scanCrossSessionPatterns(rows)
}

// ProjectIDsWithEnoughSessions returns project IDs that have enough session summaries.
func (d *DB) ProjectIDsWithEnoughSessions(minSessions int) ([]string, error) {
	rows, err := d.conn.Query(
		`SELECT project_id FROM session_summaries
		 GROUP BY project_id HAVING COUNT(*) >= ?
		 ORDER BY MAX(ts) DESC`, minSessions,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var projects []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func scanCrossSessionPatterns(rows *sql.Rows) ([]CrossSessionPattern, error) {
	var out []CrossSessionPattern
	defer rows.Close()
	for rows.Next() {
		var p CrossSessionPattern
		var synthesized int
		if err := rows.Scan(&p.ID, &p.TS, &p.ProjectID, &p.Pattern, &p.PatternType,
			&p.EvidenceSessionIDs, &p.Confidence, &synthesized); err != nil {
			return nil, err
		}
		p.Synthesized = synthesized == 1
		out = append(out, p)
	}
	return out, rows.Err()
}
