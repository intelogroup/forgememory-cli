package db

import (
	"database/sql"
	"strings"
)

// MemoryOp is one row from the consolidation audit trail.
type MemoryOp struct {
	ID         string `json:"id"`
	TS         string `json:"ts"`
	Op         string `json:"op"`
	TargetID   string `json:"target_id"`
	BeforeJSON string `json:"before_json,omitempty"`
	AfterJSON  string `json:"after_json,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Model      string `json:"model,omitempty"`
	BatchID    string `json:"batch_id,omitempty"`
}

// MemoryOps returns consolidation audit rows, newest first. An empty targetID
// returns the global trail; a non-empty one returns that principle's history.
func (d *DB) MemoryOps(targetID string, limit int) ([]MemoryOp, error) {
	if limit <= 0 {
		limit = 50
	}
	const cols = `id, ts, op, target_id, COALESCE(before_json,''), COALESCE(after_json,''),
	              COALESCE(reason,''), COALESCE(model,''), COALESCE(batch_id,'')`

	var (
		rows *sql.Rows
		err  error
	)
	if strings.TrimSpace(targetID) != "" {
		rows, err = d.conn.Query(
			`SELECT `+cols+` FROM memory_ops WHERE target_id = ? ORDER BY ts DESC, id DESC LIMIT ?`,
			targetID, limit,
		)
	} else {
		rows, err = d.conn.Query(
			`SELECT `+cols+` FROM memory_ops ORDER BY ts DESC, id DESC LIMIT ?`, limit,
		)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ops []MemoryOp
	for rows.Next() {
		var op MemoryOp
		if err := rows.Scan(&op.ID, &op.TS, &op.Op, &op.TargetID, &op.BeforeJSON,
			&op.AfterJSON, &op.Reason, &op.Model, &op.BatchID); err != nil {
			return nil, err
		}
		ops = append(ops, op)
	}
	return ops, rows.Err()
}
