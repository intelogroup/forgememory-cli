package db

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// SessionCommit links one git commit to the agent session that produced it.
type SessionCommit struct {
	ID         string `json:"id"`
	SessionID  string `json:"session_id"`
	ProjectID  string `json:"project_id"`
	SHA        string `json:"sha"`
	Author     string `json:"author"`
	CommitTS   string `json:"commit_ts"`
	Subject    string `json:"subject"`
	Files      int    `json:"files"`
	Insertions int    `json:"insertions"`
	Deletions  int    `json:"deletions"`
}

// sessionWindowPad widens each session's [min(ts), max(ts)] event range so a
// commit made just after the last captured tool-use event still counts.
const sessionWindowPad = 30 * time.Minute

// LinkCommitToSession finds which session was active (by event timestamp
// range) when commitTS happened, then upserts the commit under that session.
// Returns "" if no session in projectID overlaps commitTS.
func (d *DB) LinkCommitToSession(projectID string, c SessionCommit) (string, error) {
	commitT, err := time.Parse(time.RFC3339, c.CommitTS)
	if err != nil {
		return "", err
	}
	commitTS := commitT.UTC().Format(time.RFC3339)

	// Match against each session's full [min(ts), max(ts)] span (padded),
	// not individual event timestamps — a session can have gaps between
	// tool-use events larger than any fixed point-window.
	var sessionID string
	err = d.conn.QueryRow(
		`SELECT session_id FROM (
		   SELECT session_id, MIN(ts) AS lo, MAX(ts) AS hi
		   FROM events WHERE project_id = ? GROUP BY session_id
		 )
		 WHERE datetime(?) BETWEEN datetime(lo, ?) AND datetime(hi, ?)
		 ORDER BY hi DESC LIMIT 1`,
		projectID, commitTS,
		fmt.Sprintf("-%d seconds", int(sessionWindowPad.Seconds())),
		fmt.Sprintf("+%d seconds", int(sessionWindowPad.Seconds())),
	).Scan(&sessionID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}

	if c.ID == "" {
		c.ID = uuid.New().String()
	}
	c.SessionID = sessionID
	c.ProjectID = projectID
	_, err = d.conn.Exec(
		`INSERT INTO session_commits (id, session_id, project_id, sha, author, commit_ts, subject, files, insertions, deletions)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(project_id, sha) DO UPDATE SET
		   session_id=excluded.session_id, author=excluded.author, commit_ts=excluded.commit_ts,
		   subject=excluded.subject, files=excluded.files, insertions=excluded.insertions, deletions=excluded.deletions`,
		c.ID, sessionID, projectID, c.SHA, c.Author, c.CommitTS, c.Subject, c.Files, c.Insertions, c.Deletions,
	)
	return sessionID, err
}

// SessionCommitSummary aggregates commit stats for one session.
type SessionCommitSummary struct {
	SessionID  string `json:"session_id"`
	Commits    int    `json:"commits"`
	Insertions int    `json:"insertions"`
	Deletions  int    `json:"deletions"`
	FirstTS    string `json:"first_ts"`
	LastTS     string `json:"last_ts"`
}

// SessionCommitsByProject returns per-session commit aggregates for a project,
// most recently active session first.
func (d *DB) SessionCommitsByProject(projectID string, limit int) ([]SessionCommitSummary, error) {
	rows, err := d.conn.Query(
		`SELECT session_id, COUNT(*), COALESCE(SUM(insertions),0), COALESCE(SUM(deletions),0),
		        MIN(commit_ts), MAX(commit_ts)
		 FROM session_commits WHERE project_id = ?
		 GROUP BY session_id ORDER BY MAX(commit_ts) DESC LIMIT ?`, projectID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionCommitSummary
	for rows.Next() {
		var s SessionCommitSummary
		if err := rows.Scan(&s.SessionID, &s.Commits, &s.Insertions, &s.Deletions, &s.FirstTS, &s.LastTS); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// CommitsForSession returns the individual commits linked to a session.
func (d *DB) CommitsForSession(sessionID string) ([]SessionCommit, error) {
	rows, err := d.conn.Query(
		`SELECT id, session_id, project_id, sha, author, commit_ts, subject, files, insertions, deletions
		 FROM session_commits WHERE session_id = ? ORDER BY commit_ts ASC`, sessionID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionCommit
	for rows.Next() {
		var c SessionCommit
		if err := rows.Scan(&c.ID, &c.SessionID, &c.ProjectID, &c.SHA, &c.Author, &c.CommitTS, &c.Subject, &c.Files, &c.Insertions, &c.Deletions); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
