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

	"github.com/forge/forge/internal/security"
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
	LastUsedTS     string   `json:"last_used_ts,omitempty"`
	UseCount       int      `json:"use_count,omitempty"`
	Scope          string   `json:"scope,omitempty"` // "project" (default) or "global" (recallable everywhere)
	Signature      string   `json:"-"`               // HMAC-SHA256(title+narrative); verified on read, never exposed
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

// normalizeScope maps an empty or unknown scope to the safe per-project
// default. Only "global" widens recall beyond the principle's own project.
func normalizeScope(scope string) string {
	if strings.EqualFold(strings.TrimSpace(scope), "global") {
		return "global"
	}
	return "project"
}

func signedPayload(title, narrative string) string {
	return title + "\x00" + narrative
}

// sealPrincipleContent signs title+narrative and encrypts the narrative for
// storage. Every write path that stores a principle's text goes through here,
// so a rewritten row keeps a signature scanPrinciples can verify. Returns a
// nil-safe result when the key feature is disabled.
func sealPrincipleContent(title, narrative string) (storedNarrative, signature string) {
	key := hmacKey()
	if key == nil {
		return narrative, ""
	}
	signature = security.Sign(key, signedPayload(title, narrative))
	enc, err := security.Encrypt(key, narrative)
	if err != nil {
		log.Printf("forge: encrypting principle narrative failed, storing plaintext: %v", err)
		return narrative, signature
	}
	return enc, signature
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
	return d.insertPrinciple(nil, p)
}

// InsertPrincipleTx is InsertPrinciple inside an existing transaction.
func (d *DB) InsertPrincipleTx(tx *sql.Tx, p *Principle) (inserted bool, err error) {
	if tx == nil {
		return false, ErrTxRequired
	}
	var c connOrTx = tx
	return d.insertPrinciple(&c, p)
}

func (d *DB) insertPrinciple(c *connOrTx, p *Principle) (inserted bool, err error) {
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

	// The signature is computed over the plaintext narrative; only the stored
	// column is encrypted, so verification on read still checks the real
	// content (decrypt happens before verify in scanPrinciples).
	storedNarrative, signature := sealPrincipleContent(p.Title, p.Narrative)
	p.Signature = signature

	outcome := p.Outcome
	if outcome == "" {
		outcome = "unknown"
	}
	scope := normalizeScope(p.Scope)
	p.Scope = scope
	result, err := execOn(d.conn, c)(
		`INSERT OR IGNORE INTO principles
		 (id, ts, type, title, narrative, impact_score, project_id, source_event, fingerprint, concepts, files_modified, outcome, impl_hint, last_used_ts, use_count, signature, scope)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.TS, p.Type, p.Title, storedNarrative, p.ImpactScore,
		p.ProjectID, p.SourceEvent, p.Fingerprint, conceptsJSON, filesJSON, outcome, p.ImplHint, p.LastUsedTS, p.UseCount, p.Signature, scope,
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
		        COALESCE(last_used_ts,''), COALESCE(use_count,0), COALESCE(signature,''), COALESCE(scope,'project')
		 FROM principles
		 WHERE status = 'active' OR status IS NULL OR status = ''
		 ORDER BY ts DESC LIMIT ?`, limit,
	)
	if err != nil {
		return nil, err
	}
	return scanPrinciples(rows)
}

// RecentGlobalPrinciples returns the most recent active principles scoped
// global — the only rows a cross-project recall path may surface.
func (d *DB) RecentGlobalPrinciples(limit int) ([]Principle, error) {
	rows, err := d.conn.Query(
		`SELECT id, ts, type, title, narrative, impact_score, project_id, source_event, fingerprint,
		        COALESCE(concepts,''), COALESCE(files_modified,''),
		        COALESCE(status,''), COALESCE(conflict_peer_id,''),
		        COALESCE(outcome,'unknown'), COALESCE(impl_hint,''),
		        COALESCE(last_used_ts,''), COALESCE(use_count,0), COALESCE(signature,''), COALESCE(scope,'project')
		 FROM principles
		 WHERE (status = 'active' OR status IS NULL OR status = '')
		   AND scope = 'global'
		 ORDER BY ts DESC LIMIT ?`, limit,
	)
	if err != nil {
		return nil, err
	}
	return scanPrinciples(rows)
}

// RecentPrinciplesVisibleTo returns the active principles a project may see:
// its own rows plus globally scoped rows. An empty projectID returns every
// active principle for callers that explicitly mean "all" (admin surfaces).
func (d *DB) RecentPrinciplesVisibleTo(projectID string, limit int) ([]Principle, error) {
	if projectID == "" {
		return d.RecentActivePrinciples(limit)
	}
	exact, unixLike, windowsLike := projectIDSelectors(projectID)
	rows, err := d.conn.Query(
		`SELECT id, ts, type, title, narrative, impact_score, project_id, source_event, fingerprint,
		        COALESCE(concepts,''), COALESCE(files_modified,''),
		        COALESCE(status,''), COALESCE(conflict_peer_id,''),
		        COALESCE(outcome,'unknown'), COALESCE(impl_hint,''),
		        COALESCE(last_used_ts,''), COALESCE(use_count,0), COALESCE(signature,''), COALESCE(scope,'project')
		 FROM principles
		 WHERE (project_id = ? OR project_id LIKE ? OR project_id LIKE ? OR scope = 'global')
		   AND (status = 'active' OR status IS NULL OR status = '')
		 ORDER BY ts DESC LIMIT ?`, exact, unixLike, windowsLike, limit,
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
		        COALESCE(last_used_ts,''), COALESCE(use_count,0), COALESCE(signature,''), COALESCE(scope,'project')
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
		        COALESCE(last_used_ts,''), COALESCE(use_count,0), COALESCE(signature,''), COALESCE(scope,'project')
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
		        COALESCE(last_used_ts,''), COALESCE(use_count,0), COALESCE(signature,''), COALESCE(scope,'project')
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
			&p.Outcome, &p.ImplHint, &p.LastUsedTS, &p.UseCount, &p.Signature, &p.Scope); err != nil {
			return nil, err
		}
		if key != nil {
			if plain, err := security.Decrypt(key, p.Narrative); err == nil {
				p.Narrative = plain
			} else {
				log.Printf("forge: dropping principle %s — narrative decryption failed (tampered ciphertext or wrong key): %v", p.ID, err)
				continue
			}
		} else if strings.HasPrefix(p.Narrative, security.EncPrefix) {
			// Key feature disabled (FORGE_DISABLE_KEY) — encrypted rows are
			// unreadable without the key, so drop them rather than leak ciphertext.
			log.Printf("forge: dropping principle %s — encrypted but signing key feature disabled", p.ID)
			continue
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

// SearchPrinciplesVisibleTo finds active principles visible from projectID —
// that project's own rows plus globally scoped rows — matching any of the
// provided terms. Uses FTS5 for sub-millisecond lookup at scale.
// withinDays=0 disables the time filter.
func (d *DB) SearchPrinciplesVisibleTo(projectID string, terms []string, minImpact float64, withinDays int, limit int) ([]Principle, error) {
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

	clauses := []string{
		`p.rowid IN (SELECT rowid FROM principles_fts WHERE principles_fts MATCH ?)`,
		`p.impact_score >= ?`,
		`(p.status = 'active' OR p.status IS NULL OR p.status = '')`,
	}
	args := []any{ftsQuery, minImpact}

	if projectID == "" {
		clauses = append(clauses, `p.scope = 'global'`)
	} else {
		exact, unixLike, windowsLike := projectIDSelectors(projectID)
		clauses = append(clauses, `(p.scope = 'global' OR p.project_id = ? OR p.project_id LIKE ? OR p.project_id LIKE ?)`)
		args = append(args, exact, unixLike, windowsLike)
	}
	if withinDays > 0 {
		cutoff := time.Now().UTC().AddDate(0, 0, -withinDays).Format(time.RFC3339)
		clauses = append(clauses, `p.ts >= ?`)
		args = append(args, cutoff)
	}
	args = append(args, limit)

	q := `SELECT p.id, p.ts, p.type, p.title, p.narrative, p.impact_score, p.project_id,
	             p.source_event, p.fingerprint,
	             COALESCE(p.concepts,''), COALESCE(p.files_modified,''),
	             COALESCE(p.status,''), COALESCE(p.conflict_peer_id,''),
	             COALESCE(p.outcome,'unknown'), COALESCE(p.impl_hint,''),
	             COALESCE(p.last_used_ts,''), COALESCE(p.use_count,0), COALESCE(p.signature,''), COALESCE(p.scope,'project')
	      FROM principles p
	      WHERE ` + strings.Join(clauses, " AND ") + `
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
	return d.getPrincipleByID(nil, id)
}

// GetPrincipleByIDTx is GetPrincipleByID inside an existing transaction.
// Required for reads that happen while a write transaction holds the single
// SQLite connection; a pool query there would deadlock.
func (d *DB) GetPrincipleByIDTx(tx *sql.Tx, id string) (*Principle, error) {
	if tx == nil {
		return nil, ErrTxRequired
	}
	var c connOrTx = tx
	return d.getPrincipleByID(&c, id)
}

func (d *DB) getPrincipleByID(c *connOrTx, id string) (*Principle, error) {
	rows, err := queryOn(d.conn, c)(
		`SELECT id, ts, type, title, narrative, impact_score, project_id, source_event, fingerprint,
		        COALESCE(concepts,''), COALESCE(files_modified,''),
		        COALESCE(status,''), COALESCE(conflict_peer_id,''),
		        COALESCE(outcome,'unknown'), COALESCE(impl_hint,''),
		        COALESCE(last_used_ts,''), COALESCE(use_count,0), COALESCE(signature,''), COALESCE(scope,'project')
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
		        COALESCE(last_used_ts,''), COALESCE(use_count,0), COALESCE(signature,''), COALESCE(scope,'project')
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

// InsertMemoryOp appends an audit row to memory_ops for consolidation decisions.
func (d *DB) InsertMemoryOp(id, ts, op, targetID, beforeJSON, afterJSON, reason, model, batchID string) error {
	return d.insertMemoryOp(nil, id, ts, op, targetID, beforeJSON, afterJSON, reason, model, batchID)
}

// InsertMemoryOpTx is InsertMemoryOp inside an existing transaction.
func (d *DB) InsertMemoryOpTx(tx *sql.Tx, id, ts, op, targetID, beforeJSON, afterJSON, reason, model, batchID string) error {
	if tx == nil {
		return ErrTxRequired
	}
	var c connOrTx = tx
	return d.insertMemoryOp(&c, id, ts, op, targetID, beforeJSON, afterJSON, reason, model, batchID)
}

func (d *DB) insertMemoryOp(c *connOrTx, id, ts, op, targetID, beforeJSON, afterJSON, reason, model, batchID string) error {
	_, err := execOn(d.conn, c)(
		`INSERT INTO memory_ops (id, ts, op, target_id, before_json, after_json, reason, model, batch_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, ts, op, targetID, beforeJSON, afterJSON, reason, model, batchID,
	)
	return err
}

// ExpirePrinciple revokes a principle and sets valid_until / superseded_by (temporal audit trail).
//
// It also unlinks the row from any conflict pair: a revoked row must not leave
// its peer stuck in status='conflicting', where every read path excludes it and
// no conflict pair remains on screen to resolve.
func (d *DB) ExpirePrinciple(id, expiredAt, supersededBy string) error {
	return d.expirePrinciple(nil, id, expiredAt, supersededBy)
}

// ExpirePrincipleTx is ExpirePrinciple inside an existing transaction.
func (d *DB) ExpirePrincipleTx(tx *sql.Tx, id, expiredAt, supersededBy string) error {
	if tx == nil {
		return ErrTxRequired
	}
	var c connOrTx = tx
	return d.expirePrinciple(&c, id, expiredAt, supersededBy)
}

func (d *DB) expirePrinciple(c *connOrTx, id, expiredAt, supersededBy string) error {
	if _, err := execOn(d.conn, c)(
		`UPDATE principles SET status='revoked', expired_at=?, superseded_by=?, valid_until=? WHERE id = ?`,
		expiredAt, supersededBy, expiredAt, id,
	); err != nil {
		return err
	}
	// Any peer still pointing at this row is orphaned now: clear its conflict
	// state so it returns to the active set instead of staying quarantined.
	_, err := execOn(d.conn, c)(
		`UPDATE principles
		    SET status = CASE WHEN status = 'conflicting' THEN 'active' ELSE status END,
		        conflict_peer_id = ''
		  WHERE conflict_peer_id = ?`, id,
	)
	return err
}

// UpdatePrincipleContent rewrites a principle's text through the same
// signing/encryption path as InsertPrinciple, and recomputes the fingerprint
// from the new title and the row's project — the one fingerprint definition
// distillation also uses. Callers that need pre-change values for an audit row
// must fetch them first.
func (d *DB) UpdatePrincipleContent(id, title, narrative string, impactScore float64, implHint, scope string) error {
	return d.updatePrincipleContent(nil, id, title, narrative, impactScore, implHint, scope)
}

// UpdatePrincipleContentTx is UpdatePrincipleContent inside an existing transaction.
func (d *DB) UpdatePrincipleContentTx(tx *sql.Tx, id, title, narrative string, impactScore float64, implHint, scope string) error {
	if tx == nil {
		return ErrTxRequired
	}
	var c connOrTx = tx
	return d.updatePrincipleContent(&c, id, title, narrative, impactScore, implHint, scope)
}

func (d *DB) updatePrincipleContent(c *connOrTx, id, title, narrative string, impactScore float64, implHint, scope string) error {
	var projectID string
	err := queryRowOn(d.conn, c)(`SELECT project_id FROM principles WHERE id = ?`, id).Scan(&projectID)
	if err == sql.ErrNoRows {
		return fmt.Errorf("principle %s not found", id)
	}
	if err != nil {
		return err
	}

	storedNarrative, signature := sealPrincipleContent(title, narrative)
	result, err := execOn(d.conn, c)(
		`UPDATE principles
		    SET title = ?, narrative = ?, impact_score = ?, impl_hint = ?,
		        fingerprint = ?, signature = ?, scope = ?
		  WHERE id = ?`,
		title, storedNarrative, impactScore, implHint, fingerprint(title, projectID), signature, normalizeScope(scope), id,
	)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("principle %s not found", id)
	}
	return nil
}

// RevivePrincipleIfExpired re-activates a principle that a previous
// consolidation expired, storing p's text through the signing path. It
// deliberately refuses rows with expired_at == "": those are either active
// duplicates or explicit harden revocations, and an LLM ADD decision must not
// undo a manual revocation. On success p.ID is set to the existing row's ID so
// callers audit the right target. existingID is returned even when nothing is
// revived, so the caller can record what the ADD collided with.
func (d *DB) RevivePrincipleIfExpired(p *Principle) (revived bool, existingID string, err error) {
	return d.revivePrincipleIfExpired(nil, p)
}

// RevivePrincipleIfExpiredTx is RevivePrincipleIfExpired inside an existing transaction.
func (d *DB) RevivePrincipleIfExpiredTx(tx *sql.Tx, p *Principle) (revived bool, existingID string, err error) {
	if tx == nil {
		return false, "", ErrTxRequired
	}
	var c connOrTx = tx
	return d.revivePrincipleIfExpired(&c, p)
}

func (d *DB) revivePrincipleIfExpired(c *connOrTx, p *Principle) (revived bool, existingID string, err error) {
	var expiredAt, existingType, existingConcepts, existingScope string
	err = queryRowOn(d.conn, c)(
		`SELECT id, COALESCE(expired_at,''), COALESCE(type,''), COALESCE(concepts,''), COALESCE(scope,'project')
		   FROM principles WHERE fingerprint = ?`,
		p.Fingerprint,
	).Scan(&existingID, &expiredAt, &existingType, &existingConcepts, &existingScope)
	if err == sql.ErrNoRows {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	if expiredAt == "" {
		return false, existingID, nil
	}

	// A replacement ADD may omit fields the expired row already had; keep the
	// old values rather than blanking them.
	principleType := p.Type
	if principleType == "" {
		principleType = existingType
	}
	concepts := p.Concepts
	if len(concepts) == 0 {
		concepts = decodeStringSlice(existingConcepts)
	}
	scope := p.Scope
	if strings.TrimSpace(scope) == "" {
		scope = existingScope
	}

	storedNarrative, signature := sealPrincipleContent(p.Title, p.Narrative)
	result, err := execOn(d.conn, c)(
		`UPDATE principles
		    SET title = ?, narrative = ?, impact_score = ?, type = ?, source_event = ?,
		        concepts = ?, files_modified = ?, impl_hint = ?, signature = ?, scope = ?,
		        status = 'active', expired_at = '', valid_until = '', superseded_by = '',
		        valid_from = ?
		  WHERE id = ?`,
		p.Title, storedNarrative, p.ImpactScore, principleType, p.SourceEvent,
		encodeStringSlice(concepts), encodeStringSlice(p.FilesModified), p.ImplHint, signature, normalizeScope(scope),
		time.Now().UTC().Format(time.RFC3339), existingID,
	)
	if err != nil {
		return false, existingID, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, existingID, err
	}
	if n == 0 {
		return false, existingID, nil
	}
	p.ID = existingID
	return true, existingID, nil
}
