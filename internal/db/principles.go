package db

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

// AllowedConcepts is the whitelist of concept tags for principles.
// Matches forgememo's concept whitelist to keep tags consistent.
var AllowedConcepts = map[string]bool{
	"security":     true,
	"pattern":      true,
	"gotcha":       true,
	"performance":  true,
	"trade-off":    true,
	"how-it-works": true,
}

// Principle represents a distilled high-level insight.
type Principle struct {
	ID             string   `json:"id"`
	TS             string   `json:"ts"`
	Type           string   `json:"type"` // architecture/bugfix/pattern/preference
	Title          string   `json:"title"`
	Narrative      string   `json:"narrative"`
	ImpactScore    float64  `json:"impact_score"`
	ProjectID      string   `json:"project_id"`
	SourceEvent    string   `json:"source_event,omitempty"`
	Fingerprint    string   `json:"fingerprint"`
	Concepts       []string `json:"concepts,omitempty"`
	FilesModified  []string `json:"files_modified,omitempty"`
	Status         string   `json:"status,omitempty"`           // "active", "conflicting", or "" (legacy = active)
	ConflictPeerID string   `json:"conflict_peer_id,omitempty"` // ID of the paired conflicting principle
	Outcome        string   `json:"outcome,omitempty"`          // "success" | "failure" | "unknown"
	ImplHint       string   `json:"impl_hint,omitempty"`        // ≤120 chars: exact pattern/call/approach used
}

// FilterConcepts returns only the concepts that are in AllowedConcepts.
func FilterConcepts(concepts []string) []string {
	var out []string
	for _, c := range concepts {
		c = strings.ToLower(strings.TrimSpace(c))
		if AllowedConcepts[c] {
			out = append(out, c)
		}
	}
	return out
}

// InsertPrinciple stores a new principle. Returns (true, nil) when the row was
// actually inserted, (false, nil) when it was silently ignored due to a
// duplicate fingerprint.
func (d *DB) InsertPrinciple(p *Principle) (inserted bool, err error) {
	if p.ID == "" {
		p.ID = uuid.New().String()
	}
	if p.TS == "" {
		p.TS = time.Now().UTC().Format(time.RFC3339)
	}
	if p.Fingerprint == "" {
		p.Fingerprint = fingerprint(p.Title, p.ProjectID)
	}
	conceptsJSON := encodeStringSlice(p.Concepts)
	filesJSON := encodeStringSlice(p.FilesModified)

	outcome := p.Outcome
	if outcome == "" {
		outcome = "unknown"
	}
	result, err := d.conn.Exec(
		`INSERT OR IGNORE INTO principles
		 (id, ts, type, title, narrative, impact_score, project_id, source_event, fingerprint, concepts, files_modified, outcome, impl_hint)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.TS, p.Type, p.Title, p.Narrative, p.ImpactScore,
		p.ProjectID, p.SourceEvent, p.Fingerprint, conceptsJSON, filesJSON, outcome, p.ImplHint,
	)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

// RecentActivePrinciples returns the most recent active principles across all projects.
// Excludes conflicting principles — safe to use for context injection.
func (d *DB) RecentActivePrinciples(limit int) ([]Principle, error) {
	rows, err := d.conn.Query(
		`SELECT id, ts, type, title, narrative, impact_score, project_id, source_event, fingerprint,
		        COALESCE(concepts,''), COALESCE(files_modified,''),
		        COALESCE(status,''), COALESCE(conflict_peer_id,''),
		        COALESCE(outcome,'unknown'), COALESCE(impl_hint,'')
		 FROM principles
		 WHERE status = 'active' OR status IS NULL OR status = ''
		 ORDER BY ts DESC LIMIT ?`, limit,
	)
	if err != nil {
		return nil, err
	}
	return scanPrinciples(rows)
}

// RecentPrinciples returns the most recent principles (all statuses — used by dashboard).
func (d *DB) RecentPrinciples(limit int) ([]Principle, error) {
	rows, err := d.conn.Query(
		`SELECT id, ts, type, title, narrative, impact_score, project_id, source_event, fingerprint,
		        COALESCE(concepts,''), COALESCE(files_modified,''),
		        COALESCE(status,''), COALESCE(conflict_peer_id,''),
		        COALESCE(outcome,'unknown'), COALESCE(impl_hint,'')
		 FROM principles ORDER BY ts DESC LIMIT ?`, limit,
	)
	if err != nil {
		return nil, err
	}
	return scanPrinciples(rows)
}

// RecentPrinciplesByProject returns active principles for a project (excludes conflicting).
func (d *DB) RecentPrinciplesByProject(projectID string, limit int) ([]Principle, error) {
	if projectID == "" {
		return d.activeOnly(d.RecentPrinciples(limit))
	}
	exact, unixLike, windowsLike := projectIDSelectors(projectID)
	rows, err := d.conn.Query(
		`SELECT id, ts, type, title, narrative, impact_score, project_id, source_event, fingerprint,
		        COALESCE(concepts,''), COALESCE(files_modified,''),
		        COALESCE(status,''), COALESCE(conflict_peer_id,''),
		        COALESCE(outcome,'unknown'), COALESCE(impl_hint,'')
		 FROM principles
		 WHERE (project_id = ? OR project_id LIKE ? OR project_id LIKE ?)
		   AND (status = 'active' OR status IS NULL OR status = '')
		 ORDER BY ts DESC LIMIT ?`, exact, unixLike, windowsLike, limit,
	)
	if err != nil {
		return nil, err
	}
	return scanPrinciples(rows)
}

// RecentPrinciplesByProjectAll returns all principles for a project including
// conflicting ones. Used internally by conflict detection.
func (d *DB) RecentPrinciplesByProjectAll(projectID string, limit int) ([]Principle, error) {
	if projectID == "" {
		return d.RecentPrinciples(limit)
	}
	exact, unixLike, windowsLike := projectIDSelectors(projectID)
	rows, err := d.conn.Query(
		`SELECT id, ts, type, title, narrative, impact_score, project_id, source_event, fingerprint,
		        COALESCE(concepts,''), COALESCE(files_modified,''),
		        COALESCE(status,''), COALESCE(conflict_peer_id,''),
		        COALESCE(outcome,'unknown'), COALESCE(impl_hint,'')
		 FROM principles
		 WHERE project_id = ? OR project_id LIKE ? OR project_id LIKE ?
		 ORDER BY ts DESC LIMIT ?`, exact, unixLike, windowsLike, limit,
	)
	if err != nil {
		return nil, err
	}
	return scanPrinciples(rows)
}

// activeOnly filters a principle list to only active/legacy ones.
func (d *DB) activeOnly(principles []Principle, err error) ([]Principle, error) {
	if err != nil {
		return nil, err
	}
	out := principles[:0]
	for _, p := range principles {
		if p.Status == "" || p.Status == "active" {
			out = append(out, p)
		}
	}
	return out, nil
}

func scanPrinciples(rows *sql.Rows) ([]Principle, error) {
	var principles []Principle
	defer rows.Close()
	for rows.Next() {
		var p Principle
		var conceptsJSON, filesJSON string
		if err := rows.Scan(&p.ID, &p.TS, &p.Type, &p.Title, &p.Narrative,
			&p.ImpactScore, &p.ProjectID, &p.SourceEvent, &p.Fingerprint,
			&conceptsJSON, &filesJSON, &p.Status, &p.ConflictPeerID,
			&p.Outcome, &p.ImplHint); err != nil {
			return nil, err
		}
		p.Concepts = decodeStringSlice(conceptsJSON)
		p.FilesModified = decodeStringSlice(filesJSON)
		principles = append(principles, p)
	}
	return principles, rows.Err()
}

// SearchPrinciplesCrossProject finds active principles across all projects matching
// any of the provided terms. Uses FTS5 for sub-millisecond lookup at scale.
// withinDays=0 disables the time filter.
func (d *DB) SearchPrinciplesCrossProject(terms []string, minImpact float64, withinDays int, limit int) ([]Principle, error) {
	if len(terms) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 5
	}

	ftsQuery := buildFTSQuery(terms)
	if ftsQuery == "" {
		return nil, nil
	}

	args := []any{ftsQuery, minImpact}
	cutoffClause := ""
	if withinDays > 0 {
		cutoff := time.Now().UTC().AddDate(0, 0, -withinDays).Format(time.RFC3339)
		cutoffClause = "AND p.ts >= ?"
		args = append(args, cutoff)
	}
	args = append(args, limit)

	q := `SELECT p.id, p.ts, p.type, p.title, p.narrative, p.impact_score, p.project_id,
	             p.source_event, p.fingerprint,
	             COALESCE(p.concepts,''), COALESCE(p.files_modified,''),
	             COALESCE(p.status,''), COALESCE(p.conflict_peer_id,''),
	             COALESCE(p.outcome,'unknown'), COALESCE(p.impl_hint,'')
	      FROM principles p
	      WHERE p.rowid IN (SELECT rowid FROM principles_fts WHERE principles_fts MATCH ?)
	        AND p.impact_score >= ?
	        AND (p.status = 'active' OR p.status IS NULL OR p.status = '')
	        ` + cutoffClause + `
	      ORDER BY p.impact_score DESC, p.ts DESC
	      LIMIT ?`

	rows, err := d.conn.Query(q, args...)
	if err != nil {
		return nil, err
	}
	return scanPrinciples(rows)
}

// buildFTSQuery converts a list of terms into a FTS5 OR query.
func buildFTSQuery(terms []string) string {
	var parts []string
	for _, term := range terms {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		term = strings.ReplaceAll(term, `"`, `""`)
		parts = append(parts, `"`+term+`"`)
	}
	return strings.Join(parts, " OR ")
}

// PrincipleCount returns total principle count.
func (d *DB) PrincipleCount() (int, error) {
	var count int
	err := d.conn.QueryRow("SELECT COUNT(*) FROM principles").Scan(&count)
	return count, err
}

// GetPrincipleByID fetches a single principle by ID. Returns (nil, nil) if not found.
func (d *DB) GetPrincipleByID(id string) (*Principle, error) {
	rows, err := d.conn.Query(
		`SELECT id, ts, type, title, narrative, impact_score, project_id, source_event, fingerprint,
		        COALESCE(concepts,''), COALESCE(files_modified,''),
		        COALESCE(status,''), COALESCE(conflict_peer_id,''),
		        COALESCE(outcome,'unknown'), COALESCE(impl_hint,'')
		 FROM principles WHERE id=? LIMIT 1`, id,
	)
	if err != nil {
		return nil, err
	}
	principles, err := scanPrinciples(rows)
	if err != nil || len(principles) == 0 {
		return nil, err
	}
	return &principles[0], nil
}

// ConflictingPrinciples returns all principles currently marked as conflicting.
func (d *DB) ConflictingPrinciples(limit int) ([]Principle, error) {
	rows, err := d.conn.Query(
		`SELECT id, ts, type, title, narrative, impact_score, project_id, source_event, fingerprint,
		        COALESCE(concepts,''), COALESCE(files_modified,''),
		        COALESCE(status,''), COALESCE(conflict_peer_id,''),
		        COALESCE(outcome,'unknown'), COALESCE(impl_hint,'')
		 FROM principles WHERE status='conflicting' ORDER BY ts DESC LIMIT ?`, limit,
	)
	if err != nil {
		return nil, err
	}
	return scanPrinciples(rows)
}

// MarkConflicting marks two principles as a conflicting pair.
func (d *DB) MarkConflicting(idA, idB string) error {
	tx, err := d.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`UPDATE principles SET status='conflicting', conflict_peer_id=? WHERE id=?`, idB, idA,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`UPDATE principles SET status='conflicting', conflict_peer_id=? WHERE id=?`, idA, idB,
	); err != nil {
		return err
	}
	return tx.Commit()
}

// DeletePrinciple removes a principle. The principles_ad trigger handles FTS cleanup.
func (d *DB) DeletePrinciple(id string) error {
	_, err := d.conn.Exec(`DELETE FROM principles WHERE id=?`, id)
	return err
}

// ClearConflictStatus resets a single principle back to active status.
// Used to repair orphaned principles whose conflict peer has been deleted.
func (d *DB) ClearConflictStatus(id string) error {
	_, err := d.conn.Exec(
		`UPDATE principles SET status='active', conflict_peer_id='' WHERE id=?`, id,
	)
	return err
}

// ResolvePrincipleConflict keeps the winner principle (sets it active) and
// permanently deletes the loser. Cleans up any orphaned back-pointers in one
// transaction.
func (d *DB) ResolvePrincipleConflict(keepID, deleteID string) error {
	tx, err := d.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Delete the rejected principle (FTS trigger fires automatically).
	if _, err := tx.Exec(`DELETE FROM principles WHERE id=?`, deleteID); err != nil {
		return err
	}
	// Reset winner to active.
	if _, err := tx.Exec(
		`UPDATE principles SET status='active', conflict_peer_id='' WHERE id=?`, keepID,
	); err != nil {
		return err
	}
	// Clear any other principles that still point to the deleted one.
	if _, err := tx.Exec(
		`UPDATE principles SET status='active', conflict_peer_id='' WHERE conflict_peer_id=?`, deleteID,
	); err != nil {
		return err
	}
	return tx.Commit()
}

func fingerprint(title, project string) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s:%s", title, project)))
	return fmt.Sprintf("%x", h[:8])
}

func encodeStringSlice(s []string) string {
	if len(s) == 0 {
		return ""
	}
	b, _ := json.Marshal(s)
	return string(b)
}

func decodeStringSlice(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	json.Unmarshal([]byte(s), &out)
	return out
}

func projectIDSelectors(projectID string) (exact, unixLike, windowsLike string) {
	base := filepath.Base(projectID)
	if base == "" {
		base = projectID
	}
	return projectID, "%/" + base, "%\\" + base
}
