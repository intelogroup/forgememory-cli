package db

// EvidenceExists reports whether id names a stored event or a stored session
// summary (by row id or by session_id). Consolidation decisions may cite such
// IDs as evidence; an ID that resolves to neither is a fabrication and the
// command rejects the decision instead of persisting it.
//
// Principle IDs are deliberately not resolved here: the consolidation command
// accepts a batch's own principle IDs separately, so a claim can only cite the
// rows under consideration, not arbitrary memory from another project.
func (d *DB) EvidenceExists(id string) (bool, error) {
	if id == "" {
		return false, nil
	}
	var n int
	err := d.conn.QueryRow(
		`SELECT CASE
		          WHEN EXISTS(SELECT 1 FROM events WHERE id = ?) THEN 1
		          WHEN EXISTS(SELECT 1 FROM session_summaries WHERE id = ? OR session_id = ?) THEN 1
		          ELSE 0
		        END`,
		id, id, id,
	).Scan(&n)
	if err != nil {
		return false, err
	}
	return n == 1, nil
}
