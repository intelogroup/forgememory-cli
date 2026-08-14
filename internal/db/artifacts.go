package db

import (
	"database/sql"
	"time"
)

// EvaluationArtifact describes a large evidence object stored outside SQLite.
// SQLite remains the source of truth for ownership, integrity, and lookup.
type EvaluationArtifact struct {
	ID        string `json:"id"`
	CreatedAt string `json:"created_at"`
	TraceID   string `json:"trace_id"`
	TaskID    string `json:"task_id,omitempty"`
	Kind      string `json:"kind"`
	MediaType string `json:"media_type"`
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	ByteSize  int64  `json:"byte_size"`
	Metadata  string `json:"metadata"`
}

// InsertEvaluationArtifact stores artifact metadata. The artifact ID is
// content-addressed by the filesystem store, so retries are idempotent.
func (d *DB) InsertEvaluationArtifact(artifact *EvaluationArtifact) error {
	if artifact.CreatedAt == "" {
		artifact.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if artifact.MediaType == "" {
		artifact.MediaType = "application/octet-stream"
	}
	if artifact.Metadata == "" {
		artifact.Metadata = "{}"
	}
	_, err := d.conn.Exec(`
		INSERT INTO evaluation_artifacts
			(id, created_at, trace_id, task_id, kind, media_type, path, sha256, byte_size, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			trace_id=excluded.trace_id, task_id=excluded.task_id,
			kind=excluded.kind, media_type=excluded.media_type,
			path=excluded.path, sha256=excluded.sha256,
			byte_size=excluded.byte_size, metadata=excluded.metadata`,
		artifact.ID, artifact.CreatedAt, artifact.TraceID, artifact.TaskID,
		artifact.Kind, artifact.MediaType, artifact.Path, artifact.SHA256,
		artifact.ByteSize, artifact.Metadata,
	)
	return err
}

// EvaluationArtifactByID returns one artifact metadata row, or nil if absent.
func (d *DB) EvaluationArtifactByID(id string) (*EvaluationArtifact, error) {
	var artifact EvaluationArtifact
	err := d.conn.QueryRow(`
		SELECT id, created_at, trace_id, task_id, kind, media_type, path, sha256, byte_size, metadata
		FROM evaluation_artifacts WHERE id=?`, id).Scan(
		&artifact.ID, &artifact.CreatedAt, &artifact.TraceID, &artifact.TaskID,
		&artifact.Kind, &artifact.MediaType, &artifact.Path, &artifact.SHA256,
		&artifact.ByteSize, &artifact.Metadata,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &artifact, nil
}

// EvaluationArtifacts lists artifacts owned by a trace or task.
func (d *DB) EvaluationArtifacts(traceID, taskID string, limit int) ([]EvaluationArtifact, error) {
	query := `SELECT id, created_at, trace_id, task_id, kind, media_type, path, sha256, byte_size, metadata
		FROM evaluation_artifacts WHERE 1=1`
	args := []any{}
	if traceID != "" {
		query += " AND trace_id=?"
		args = append(args, traceID)
	}
	if taskID != "" {
		query += " AND task_id=?"
		args = append(args, taskID)
	}
	query += " ORDER BY created_at DESC"
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := d.conn.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var artifacts []EvaluationArtifact
	for rows.Next() {
		var artifact EvaluationArtifact
		if err := rows.Scan(&artifact.ID, &artifact.CreatedAt, &artifact.TraceID, &artifact.TaskID,
			&artifact.Kind, &artifact.MediaType, &artifact.Path, &artifact.SHA256,
			&artifact.ByteSize, &artifact.Metadata); err != nil {
			return nil, err
		}
		artifacts = append(artifacts, artifact)
	}
	return artifacts, rows.Err()
}
