package db

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/forge/forge/internal/security"
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
	LastUsedTS     string   `json:"last_used_ts,omitempty"`
	UseCount       int      `json:"use_count,omitempty"`
	Signature      string   `json:"-"` // HMAC-SHA256(title+narrative); verified on read, never exposed
}

var (
	signingKeyOnce sync.Once
	signingKey     []byte
)

// hmacKey lazily fetches the HMAC signing key (keychain-backed, falls back
// to ~/.forge/forge.key). Logs once if the fallback path is used.
func hmacKey() []byte {
	signingKeyOnce.Do(func() {
		key, usedFallback, err := security.GetOrCreateKey()
		if err != nil {
			log.Printf("forge: signing key unavailable, principles will be stored unsigned: %v", err)
			return
		}
		if usedFallback {
			log.Printf("forge: OS keychain unavailable, HMAC key stored in ~/.forge/forge.key instead — signatures only protect against tampering that doesn't also read that file")
		}
		signingKey = key
	})
	return signingKey
}

// WarmSigningKey pre-fetches the HMAC/encryption key synchronously. Call it
// once at daemon startup, before the daemon signals it's ready to accept
// connections (writing forge.addr) — the keychain round-trip this can
// trigger is bounded (internal/security's keychainTimeout, up to ~500ms-1s
// on the very first call in a process) but real. Paying it once here, before
// any hook event can arrive, keeps that latency off the daemon's per-event
// IPC handling path — otherwise whichever InsertEvent/InsertPrinciple call
// happens to be first pays it inline, which is a real regression on a
// hot/frequent path (this surfaced as a race: hooks fired within the
// warm-up window saw their events land in SQLite later than a
// near-simultaneous read from a separate short-lived `forge hook` process
// expected, breaking `isFirstPromptOfSession`).
func WarmSigningKey() {
	hmacKey()
}

func signedPayload(title, narrative string) string {
	return title + "\x00" + narrative
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
	key := hmacKey()
	if key != nil {
		p.Signature = security.Sign(key, signedPayload(p.Title, p.Narrative))
	}
	conceptsJSON := encodeStringSlice(p.Concepts)
	filesJSON := encodeStringSlice(p.FilesModified)

	// Signature above is computed over the plaintext narrative; only the
	// stored column is encrypted, so verification on read still checks the
	// real content (decrypt happens before verify in scanPrinciples).
	storedNarrative := p.Narrative
	if key != nil {
		if enc, err := security.Encrypt(key, p.Narrative); err == nil {
			storedNarrative = enc
		} else {
			log.Printf("forge: encrypting principle narrative failed, storing plaintext: %v", err)
		}
	}

	outcome := p.Outcome
	if outcome == "" {
		outcome = "unknown"
	}
	result, err := d.conn.Exec(
		`INSERT OR IGNORE INTO principles
		 (id, ts, type, title, narrative, impact_score, project_id, source_event, fingerprint, concepts, files_modified, outcome, impl_hint, last_used_ts, use_count, signature)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.TS, p.Type, p.Title, storedNarrative, p.ImpactScore,
		p.ProjectID, p.SourceEvent, p.Fingerprint, conceptsJSON, filesJSON, outcome, p.ImplHint, p.LastUsedTS, p.UseCount, p.Signature,
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
		        COALESCE(outcome,'unknown'), COALESCE(impl_hint,''),
		        COALESCE(last_used_ts,''), COALESCE(use_count,0), COALESCE(signature,'')
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
		        COALESCE(outcome,'unknown'), COALESCE(impl_hint,''),
		        COALESCE(last_used_ts,''), COALESCE(use_count,0), COALESCE(signature,'')
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
		        COALESCE(outcome,'unknown'), COALESCE(impl_hint,''),
		        COALESCE(last_used_ts,''), COALESCE(use_count,0), COALESCE(signature,'')
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
		        COALESCE(outcome,'unknown'), COALESCE(impl_hint,''),
		        COALESCE(last_used_ts,''), COALESCE(use_count,0), COALESCE(signature,'')
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

// scanPrinciples is the single choke point every query path scans rows
// through, so signature verification lives here once rather than in every
// caller. A row whose HMAC doesn't match its title+narrative is dropped
// (not returned) and logged — it never reaches inject, MCP tools, or the
// dashboard. Rows written before signing was enabled (empty signature) are
// passed through unverified rather than dropped, so upgrading doesn't wipe
// existing memory; run a re-sign pass to close that gap if needed.
func scanPrinciples(rows *sql.Rows) ([]Principle, error) {
	var principles []Principle
	defer rows.Close()
	key := hmacKey()
	for rows.Next() {
		var p Principle
		var conceptsJSON, filesJSON string
		if err := rows.Scan(&p.ID, &p.TS, &p.Type, &p.Title, &p.Narrative,
			&p.ImpactScore, &p.ProjectID, &p.SourceEvent, &p.Fingerprint,
			&conceptsJSON, &filesJSON, &p.Status, &p.ConflictPeerID,
			&p.Outcome, &p.ImplHint, &p.LastUsedTS, &p.UseCount, &p.Signature); err != nil {
			return nil, err
		}
		if key != nil {
			if plain, err := security.Decrypt(key, p.Narrative); err == nil {
				p.Narrative = plain
			} else {
				log.Printf("forge: dropping principle %s — narrative decryption failed (tampered ciphertext or wrong key): %v", p.ID, err)
				continue
			}
		}
		if key != nil && p.Signature != "" {
			if !security.Verify(key, signedPayload(p.Title, p.Narrative), p.Signature) {
				log.Printf("forge: dropping principle %s — HMAC signature mismatch (tampered or corrupted)", p.ID)
				continue
			}
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
	             COALESCE(p.outcome,'unknown'), COALESCE(p.impl_hint,''),
	             COALESCE(p.last_used_ts,''), COALESCE(p.use_count,0), COALESCE(p.signature,'')
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
		        COALESCE(outcome,'unknown'), COALESCE(impl_hint,''),
		        COALESCE(last_used_ts,''), COALESCE(use_count,0), COALESCE(signature,'')
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
		        COALESCE(outcome,'unknown'), COALESCE(impl_hint,''),
		        COALESCE(last_used_ts,''), COALESCE(use_count,0), COALESCE(signature,'')
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

// UpdatePrincipleScore updates the impact score of a principle.
func (d *DB) UpdatePrincipleScore(id string, score float64) error {
	_, err := d.conn.Exec(
		`UPDATE principles SET impact_score = ? WHERE id = ?`,
		score, id,
	)
	return err
}

// RevokePrinciple marks a principle explicitly revoked. Reuses the existing
// status column (already filtered out of RecentActivePrinciples etc. by
// "WHERE status = 'active' OR ...") rather than adding a new mechanism —
// a revoked principle is excluded from every read path even if its HMAC
// signature is still valid, which is the point: revocation must survive an
// otherwise-legitimate signature.
func (d *DB) RevokePrinciple(id string) error {
	_, err := d.conn.Exec(`UPDATE principles SET status = 'revoked' WHERE id = ?`, id)
	return err
}

// ResignAllPrinciples re-signs every stored principle under a new key —
// used by `forge harden rotate-key` right after the key itself rotates, so
// existing principles don't all start failing verification. Reads and
// writes raw rows directly rather than going through scanPrinciples, which
// would drop every row up front (they're still signed under oldKey at read
// time) before they get a chance to be re-signed.
//
// Each row is verified against oldKey before it's re-signed. This matters:
// a row whose narrative was tampered directly in SQLite fails verification
// and gets dropped by scanPrinciples on every read — but if rotation just
// re-signed whatever content currently sits in the row, it would legitimize
// that tampered content under the new key, silently undoing the detection.
// Rows that fail the oldKey check are quarantined (status='revoked', same
// mechanism as RevokePrinciple) instead of re-signed. Rows with no prior
// signature (pre-signing legacy data) are treated as trusted and re-signed
// normally, matching scanPrinciples' pass-through behavior for empty sigs.
func (d *DB) ResignAllPrinciples(oldKey, newKey []byte) (resigned, quarantined int, err error) {
	rows, err := d.conn.Query(`SELECT id, title, narrative, signature FROM principles`)
	if err != nil {
		return 0, 0, err
	}
	type row struct{ id, title, narrative, signature string }
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.title, &r.narrative, &r.signature); err != nil {
			rows.Close()
			return 0, 0, err
		}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	rows.Close()

	tx, err := d.conn.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	for _, r := range all {
		plainNarrative, decErr := security.Decrypt(oldKey, r.narrative)
		if decErr != nil {
			// Ciphertext doesn't open under oldKey at all — same as a failed
			// signature check, quarantine rather than let re-encryption mask it.
			if _, err := tx.Exec(`UPDATE principles SET status = 'revoked' WHERE id = ?`, r.id); err != nil {
				return 0, 0, err
			}
			quarantined++
			continue
		}
		if r.signature != "" && !security.Verify(oldKey, signedPayload(r.title, plainNarrative), r.signature) {
			if _, err := tx.Exec(`UPDATE principles SET status = 'revoked' WHERE id = ?`, r.id); err != nil {
				return 0, 0, err
			}
			quarantined++
			continue
		}
		sig := security.Sign(newKey, signedPayload(r.title, plainNarrative))
		newNarrative, encErr := security.Encrypt(newKey, plainNarrative)
		if encErr != nil {
			return 0, 0, fmt.Errorf("re-encrypt principle %s: %w", r.id, encErr)
		}
		if _, err := tx.Exec(`UPDATE principles SET signature = ?, narrative = ? WHERE id = ?`, sig, newNarrative, r.id); err != nil {
			return 0, 0, err
		}
		resigned++
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return resigned, quarantined, nil
}

// IncrementPrincipleUsage updates the last used timestamp and increments use count.
func (d *DB) IncrementPrincipleUsage(id string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := d.conn.Exec(
		`UPDATE principles SET last_used_ts = ?, use_count = use_count + 1 WHERE id = ?`,
		now, id,
	)
	return err
}

