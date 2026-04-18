package db

// SessionEventCount returns the number of events recorded for a given session.
func (d *DB) SessionEventCount(sessionID string) (int, error) {
	var count int
	err := d.conn.QueryRow("SELECT COUNT(*) FROM events WHERE session_id = ?", sessionID).Scan(&count)
	return count, err
}
