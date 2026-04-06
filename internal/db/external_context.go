package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// RetrievalJob represents planned external-context work.
type RetrievalJob struct {
	ID             string `json:"id"`
	TS             string `json:"ts"`
	ProjectID      string `json:"project_id"`
	TriggerType    string `json:"trigger_type"`
	Source         string `json:"source"`
	Query          string `json:"query"`
	LibraryName    string `json:"library_name"`
	LibraryVersion string `json:"library_version"`
	Status         string `json:"status"`
	Priority       int    `json:"priority"`
	DedupeKey      string `json:"dedupe_key"`
	LastError      string `json:"last_error"`
}

// ExternalContextSummary is compact injected context derived from an external source.
type ExternalContextSummary struct {
	ID             string   `json:"id"`
	TS             string   `json:"ts"`
	Source         string   `json:"source"`
	ProjectID      string   `json:"project_id"`
	Query          string   `json:"query"`
	LibraryName    string   `json:"library_name"`
	LibraryVersion string   `json:"library_version"`
	Title          string   `json:"title"`
	Narrative      string   `json:"narrative"`
	SummaryKind    string   `json:"summary_kind"`
	Hint           string   `json:"hint"`
	TrustScore     float64  `json:"trust_score"`
	RelevanceTags  []string `json:"relevance_tags,omitempty"`
	CacheRef       string   `json:"cache_ref"`
	LastUsedAt     string   `json:"last_used_at"`
	ExpiresAt      string   `json:"expires_at"`
}

// InsertRetrievalJob queues external retrieval work if it has not already been queued.
func (d *DB) InsertRetrievalJob(job *RetrievalJob) error {
	if job.ID == "" {
		job.ID = uuid.New().String()
	}
	if job.TS == "" {
		job.TS = time.Now().UTC().Format(time.RFC3339)
	}
	if job.Status == "" {
		job.Status = "queued"
	}
	_, err := d.conn.Exec(
		`INSERT INTO retrieval_jobs
		 (id, ts, project_id, trigger_type, source, query, library_name, library_version, status, priority, dedupe_key, last_error)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(dedupe_key) DO UPDATE SET
		   ts=excluded.ts,
		   trigger_type=excluded.trigger_type,
		   priority=CASE WHEN excluded.priority > retrieval_jobs.priority THEN excluded.priority ELSE retrieval_jobs.priority END,
		   status=CASE WHEN retrieval_jobs.status='running' THEN retrieval_jobs.status ELSE 'queued' END,
		   last_error=''`,
		job.ID, job.TS, job.ProjectID, job.TriggerType, job.Source, job.Query, job.LibraryName,
		job.LibraryVersion, job.Status, job.Priority, job.DedupeKey, job.LastError,
	)
	return err
}

// RetrievalJobsByProject returns recent jobs for a project.
func (d *DB) RetrievalJobsByProject(projectID string, limit int) ([]RetrievalJob, error) {
	if limit <= 0 {
		limit = 10
	}
	exact, unixLike, windowsLike := projectIDSelectors(projectID)
	rows, err := d.conn.Query(
		`SELECT id, ts, project_id, trigger_type, source, query, COALESCE(library_name,''), COALESCE(library_version,''),
		        status, priority, dedupe_key, COALESCE(last_error,'')
		 FROM retrieval_jobs
		 WHERE project_id = ? OR project_id LIKE ? OR project_id LIKE ?
		 ORDER BY ts DESC LIMIT ?`, exact, unixLike, windowsLike, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []RetrievalJob
	for rows.Next() {
		var job RetrievalJob
		if err := rows.Scan(&job.ID, &job.TS, &job.ProjectID, &job.TriggerType, &job.Source, &job.Query,
			&job.LibraryName, &job.LibraryVersion, &job.Status, &job.Priority, &job.DedupeKey, &job.LastError); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

// UpsertExternalContextSummary stores a distilled external-context summary.
func (d *DB) UpsertExternalContextSummary(summary *ExternalContextSummary) error {
	if summary.ID == "" {
		summary.ID = uuid.New().String()
	}
	if summary.TS == "" {
		summary.TS = time.Now().UTC().Format(time.RFC3339)
	}
	tagsJSON := encodeStringSlice(summary.RelevanceTags)
	_, err := d.conn.Exec(
		`INSERT INTO external_context_summaries
		 (id, ts, source, project_id, query, library_name, library_version, title, narrative, summary_kind, hint, trust_score, relevance_tags, cache_ref, last_used_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(project_id, source, query, library_name)
		 DO UPDATE SET
		   ts=excluded.ts,
		   library_version=excluded.library_version,
		   title=excluded.title,
		   narrative=excluded.narrative,
		   summary_kind=excluded.summary_kind,
		   hint=excluded.hint,
		   trust_score=excluded.trust_score,
		   relevance_tags=excluded.relevance_tags,
		   cache_ref=excluded.cache_ref,
		   expires_at=excluded.expires_at`,
		summary.ID, summary.TS, summary.Source, summary.ProjectID, summary.Query, summary.LibraryName,
		summary.LibraryVersion, summary.Title, summary.Narrative, summary.SummaryKind, summary.Hint, summary.TrustScore, tagsJSON,
		summary.CacheRef, summary.LastUsedAt, summary.ExpiresAt,
	)
	return err
}

// FreshExternalContextSummariesByProject returns non-expired cached external summaries for a project.
func (d *DB) FreshExternalContextSummariesByProject(projectID string, limit int) ([]ExternalContextSummary, error) {
	if limit <= 0 {
		limit = 3
	}
	now := time.Now().UTC().Format(time.RFC3339)
	exact, unixLike, windowsLike := projectIDSelectors(projectID)
	rows, err := d.conn.Query(
		`SELECT id, ts, source, project_id, query, COALESCE(library_name,''), COALESCE(library_version,''),
		        title, narrative, COALESCE(summary_kind,''), COALESCE(hint,''), trust_score, COALESCE(relevance_tags,''), COALESCE(cache_ref,''), COALESCE(last_used_at,''), COALESCE(expires_at,'')
		 FROM external_context_summaries
		 WHERE (project_id = ? OR project_id LIKE ? OR project_id LIKE ?)
		   AND (expires_at='' OR expires_at>?)
		 ORDER BY trust_score DESC,
		          CASE WHEN last_used_at <> '' THEN last_used_at ELSE ts END DESC
		 LIMIT ?`,
		exact, unixLike, windowsLike, now, limit,
	)
	if err != nil {
		return nil, err
	}
	return scanExternalContextSummaries(rows)
}

// SearchExternalContextSummariesByProject returns cached external summaries for a project,
// optionally filtered by source, library, and query text.
func (d *DB) SearchExternalContextSummariesByProject(projectID, source, libraryName, query string, limit int) ([]ExternalContextSummary, error) {
	if limit <= 0 {
		limit = 5
	}
	now := time.Now().UTC().Format(time.RFC3339)
	exact, unixLike, windowsLike := projectIDSelectors(projectID)
	args := []any{exact, unixLike, windowsLike, now}
	sqlText := `SELECT id, ts, source, project_id, query, COALESCE(library_name,''), COALESCE(library_version,''),
		        title, narrative, COALESCE(summary_kind,''), COALESCE(hint,''), trust_score, COALESCE(relevance_tags,''), COALESCE(cache_ref,''), COALESCE(last_used_at,''), COALESCE(expires_at,'')
		 FROM external_context_summaries
		 WHERE (project_id = ? OR project_id LIKE ? OR project_id LIKE ?)
		   AND (expires_at='' OR expires_at>?)`
	if source = strings.TrimSpace(source); source != "" {
		sqlText += ` AND source = ?`
		args = append(args, source)
	}
	if libraryName = strings.TrimSpace(libraryName); libraryName != "" {
		sqlText += ` AND library_name = ?`
		args = append(args, libraryName)
	}
	if query = strings.TrimSpace(query); query != "" {
		sqlText += ` AND (query LIKE ? OR title LIKE ? OR narrative LIKE ? OR hint LIKE ?)`
		like := "%" + query + "%"
		args = append(args, like, like, like, like)
	}
	sqlText += ` ORDER BY trust_score DESC,
	                CASE WHEN last_used_at <> '' THEN last_used_at ELSE ts END DESC
	             LIMIT ?`
	args = append(args, limit)
	rows, err := d.conn.Query(sqlText, args...)
	if err != nil {
		return nil, err
	}
	return scanExternalContextSummaries(rows)
}

// TouchExternalContextSummaries marks cached external summaries as recently used.
func (d *DB) TouchExternalContextSummaries(ids []string, usedAt string) error {
	if len(ids) == 0 {
		return nil
	}
	if strings.TrimSpace(usedAt) == "" {
		usedAt = time.Now().UTC().Format(time.RFC3339)
	}

	placeholders := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids)+1)
	args = append(args, usedAt)
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	if len(placeholders) == 0 {
		return nil
	}

	query := `UPDATE external_context_summaries
	          SET last_used_at=?
	          WHERE id IN (` + strings.Join(placeholders, ",") + `)`
	_, err := d.conn.Exec(query, args...)
	return err
}

func scanExternalContextSummaries(rows *sql.Rows) ([]ExternalContextSummary, error) {
	defer rows.Close()
	var summaries []ExternalContextSummary
	for rows.Next() {
		var summary ExternalContextSummary
		var tagsJSON string
		if err := rows.Scan(&summary.ID, &summary.TS, &summary.Source, &summary.ProjectID, &summary.Query,
			&summary.LibraryName, &summary.LibraryVersion, &summary.Title, &summary.Narrative, &summary.SummaryKind, &summary.Hint,
			&summary.TrustScore, &tagsJSON, &summary.CacheRef, &summary.LastUsedAt, &summary.ExpiresAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(tagsJSON), &summary.RelevanceTags)
		summaries = append(summaries, summary)
	}
	return summaries, rows.Err()
}

// NextQueuedRetrievalJob returns the oldest queued job and marks it running.
func (d *DB) NextQueuedRetrievalJob() (*RetrievalJob, error) {
	tx, err := d.conn.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	row := tx.QueryRow(
		`SELECT id, ts, project_id, trigger_type, source, query, COALESCE(library_name,''), COALESCE(library_version,''),
		        status, priority, dedupe_key, COALESCE(last_error,'')
		 FROM retrieval_jobs
		 WHERE status='queued'
		 ORDER BY priority DESC, ts ASC
		 LIMIT 1`,
	)
	var job RetrievalJob
	if err := row.Scan(&job.ID, &job.TS, &job.ProjectID, &job.TriggerType, &job.Source, &job.Query,
		&job.LibraryName, &job.LibraryVersion, &job.Status, &job.Priority, &job.DedupeKey, &job.LastError); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	if _, err := tx.Exec(`UPDATE retrieval_jobs SET status='running', last_error='' WHERE id=?`, job.ID); err != nil {
		return nil, err
	}
	job.Status = "running"
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &job, nil
}

// UpdateRetrievalJobStatus updates job status and error details.
func (d *DB) UpdateRetrievalJobStatus(id, status, lastError string) error {
	if id == "" {
		return fmt.Errorf("missing job id")
	}
	_, err := d.conn.Exec(`UPDATE retrieval_jobs SET status=?, last_error=? WHERE id=?`, status, lastError, id)
	return err
}
