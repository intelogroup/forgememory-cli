package db

// SessionRange is a session's active time span, derived from its events.
type SessionRange struct {
	SessionID string
	Lo        string // RFC3339 UTC
	Hi        string // RFC3339 UTC
	Events    int
}

// SessionRangesByProject returns each session's [min(ts), max(ts)] event span
// for a project, chronologically ordered. Excludes the "unknown" bucket —
// forgememo's catch-all for events with no captured session boundary, which
// spans the whole project history and isn't a real work session (see
// LinkCommitToSession for the same exclusion and why).
func (d *DB) SessionRangesByProject(projectID string) ([]SessionRange, error) {
	rows, err := d.conn.Query(
		`SELECT session_id, MIN(ts), MAX(ts), COUNT(*)
		 FROM events WHERE project_id = ? AND session_id != 'unknown'
		 GROUP BY session_id ORDER BY MIN(ts) ASC`, projectID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionRange
	for rows.Next() {
		var r SessionRange
		if err := rows.Scan(&r.SessionID, &r.Lo, &r.Hi, &r.Events); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
