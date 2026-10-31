package db

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/forge/forge/internal/sanitize"
)

const (
	// ForgottenMarker replaces forgotten text. It holds no quote or backslash, so JSON stays valid.
	ForgottenMarker = "[FORGOTTEN]"
	minForgetRunes  = 4
	purgeBatchSize  = 500
)

// PurgeCount says how many rows in one place were, or would be, changed.
type PurgeCount struct {
	Target string
	Rows   int
}

type textColumn struct{ table, column string }

// purgeColumns lists every stored column holding free text. TestPurgeColumnsCoverEveryTextColumn
// fails when a new text column is added without deciding here whether it belongs.
// Principle titles and narratives are signed, so they go through their own paths.
var purgeColumns = []textColumn{
	{"events", "payload"},
	{"session_summaries", "summary"},
	{"session_summaries", "request"},
	{"session_summaries", "investigation"},
	{"session_summaries", "learnings"},
	{"session_summaries", "next_steps"},
	{"session_summaries", "keywords"},
	{"alerts", "title"},
	{"alerts", "narrative"},
	{"coaching_items", "question"},
	{"coaching_items", "next_action"},
	{"coaching_items", "lesson"},
	{"coaching_items", "resolution"},
	{"cross_session_patterns", "pattern"},
	{"distillation_health", "last_error_message"},
	{"evaluation_artifacts", "metadata"},
	{"evaluation_tasks", "name"},
	{"evaluation_tasks", "prompt"},
	{"evaluation_tasks", "metadata"},
	{"external_context_summaries", "query"},
	{"external_context_summaries", "title"},
	{"external_context_summaries", "narrative"},
	{"external_context_summaries", "hint"},
	{"failure_signatures", "normalized_message"},
	{"memory_corrections", "explanation"},
	{"memory_ops", "before_json"},
	{"memory_ops", "after_json"},
	{"memory_ops", "reason"},
	{"observation_evidence", "excerpt"},
	{"observations", "summary"},
	{"principles", "concepts"},
	{"principles", "impl_hint"},
	{"retrieval_jobs", "query"},
	{"retrieval_jobs", "last_error"},
	{"session_commits", "subject"},
	{"trace_evaluations", "rationale"},
	{"trace_evaluations", "evidence_spans"},
	// Last: triggers above refill it, so this only catches what they do not, such as note bodies.
	{"recall_fts", "body"},
}

type purgeChange struct {
	rowid    int64
	old, new string
}

func (d *DB) rewriteColumn(c textColumn, rewrite func(string) string, apply bool) (int, error) {
	total := 0
	var last int64
	for {
		changes, next, done, err := d.scanColumn(c, last, rewrite)
		if err != nil {
			return total, fmt.Errorf("%s.%s: %w", c.table, c.column, err)
		}
		total += len(changes)
		if apply && len(changes) > 0 {
			if err := d.applyChanges(c, changes); err != nil {
				return total, fmt.Errorf("%s.%s: %w", c.table, c.column, err)
			}
		}
		if done {
			return total, nil
		}
		last = next
	}
}

func (d *DB) scanColumn(c textColumn, after int64, rewrite func(string) string) (changes []purgeChange, last int64, done bool, err error) {
	// Rows close before any write: the pool has one connection.
	rows, err := d.conn.Query(fmt.Sprintf(
		`SELECT rowid, %s FROM %s WHERE rowid > ? ORDER BY rowid LIMIT ?`, c.column, c.table), after, purgeBatchSize)
	if err != nil {
		return nil, 0, false, err
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var value sql.NullString
		if err := rows.Scan(&last, &value); err != nil {
			return nil, 0, false, err
		}
		seen++
		if rewritten := rewrite(value.String); value.Valid && rewritten != value.String {
			changes = append(changes, purgeChange{last, value.String, rewritten})
		}
	}
	return changes, last, seen < purgeBatchSize, rows.Err()
}

func (d *DB) applyChanges(c textColumn, changes []purgeChange) error {
	if err := d.enableSecureDelete(); err != nil {
		return err
	}
	tx, err := d.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, ch := range changes {
		if err := applyChange(tx, c, ch); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func applyChange(tx *sql.Tx, c textColumn, ch purgeChange) error {
	switch c.table {
	case "events":
		// events_fts has no update trigger; it must be told the exact old text.
		if _, err := tx.Exec(`INSERT INTO events_fts(events_fts, rowid, payload) VALUES('delete', ?, ?)`, ch.rowid, ch.old); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO events_fts(rowid, payload) VALUES(?, ?)`, ch.rowid, ch.new); err != nil {
			return err
		}
	case "recall_fts":
		if _, err := tx.Exec(`DELETE FROM embeddings WHERE (source_kind, source_id) IN
			(SELECT source_kind, source_id FROM recall_fts WHERE rowid = ?)`, ch.rowid); err != nil {
			return err
		}
	}
	// OR REPLACE: two rows can become identical once redacted, and keeping either is fine.
	_, err := tx.Exec(fmt.Sprintf(`UPDATE OR REPLACE %s SET %s = ? WHERE rowid = ?`, c.table, c.column), ch.new, ch.rowid)
	return err
}

// enableSecureDelete makes SQLite and FTS5 overwrite removed text instead of only unlinking it.
func (d *DB) enableSecureDelete() error {
	if _, err := d.conn.Exec(`PRAGMA secure_delete = ON`); err != nil {
		return err
	}
	for _, fts := range []string{"events_fts", "principles_fts", "recall_fts"} {
		if _, err := d.conn.Exec(fmt.Sprintf(`INSERT INTO %s(%s, rank) VALUES('secure-delete', 1)`, fts, fts)); err != nil {
			return fmt.Errorf("%s secure-delete: %w", fts, err)
		}
	}
	return nil
}

// RedactSecrets runs the secret scrubber over everything already stored. Without
// apply it only counts. Follow an applied run with CompactAfterPurge.
func (d *DB) RedactSecrets(apply bool) ([]PurgeCount, error) {
	principles, err := d.redactPrinciples(apply)
	if err != nil {
		return nil, err
	}
	return d.rewriteAll(sanitize.ScrubSecrets, apply, principles)
}

func (d *DB) rewriteAll(rewrite func(string) string, apply bool, first []PurgeCount) ([]PurgeCount, error) {
	counts := first
	for _, c := range purgeColumns {
		n, err := d.rewriteColumn(c, rewrite, apply)
		if err != nil {
			return counts, err
		}
		if n > 0 {
			counts = append(counts, PurgeCount{c.table + "." + c.column, n})
		}
	}
	return counts, nil
}

type storedPrinciple struct {
	id, title, narrative, signature, concepts, hint, scope string
	impact                                                 float64
}

func (d *DB) storedPrinciples() ([]storedPrinciple, error) {
	rows, err := d.conn.Query(`SELECT id, title, narrative, COALESCE(signature,''), COALESCE(concepts,''),
		COALESCE(impl_hint,''), COALESCE(scope,''), COALESCE(impact_score,0) FROM principles`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []storedPrinciple
	for rows.Next() {
		var p storedPrinciple
		if err := rows.Scan(&p.id, &p.title, &p.narrative, &p.signature, &p.concepts, &p.hint, &p.scope, &p.impact); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// redactPrinciples re-seals readable principles so their signatures stay valid.
// An unreadable narrative cannot be re-signed, so a title needing redaction there is only counted.
func (d *DB) redactPrinciples(apply bool) ([]PurgeCount, error) {
	all, err := d.storedPrinciples()
	if err != nil {
		return nil, err
	}
	changed, skipped := 0, 0
	for _, p := range all {
		title := sanitize.ScrubSecrets(p.title)
		narrative, readable := readNarrative(p.title, p.narrative, p.signature)
		if !readable {
			if title != p.title {
				skipped++
			}
			continue
		}
		clean := sanitize.ScrubSecrets(narrative)
		if title == p.title && clean == narrative {
			continue
		}
		changed++
		if apply {
			if err := d.UpdatePrincipleContent(p.id, title, clean, p.impact, p.hint, p.scope); err != nil {
				return nil, err
			}
		}
	}
	var counts []PurgeCount
	if changed > 0 {
		counts = append(counts, PurgeCount{"principles.title+narrative", changed})
	}
	if skipped > 0 {
		counts = append(counts, PurgeCount{"principles.title NOT redacted, narrative unreadable", skipped})
	}
	return counts, nil
}

// ForgetPattern matches needle literally and case-insensitively, also where it was
// stored JSON-escaped: payloads nest tool input as strings up to two levels deep.
func ForgetPattern(needle string) (*regexp.Regexp, error) {
	needle = strings.TrimSpace(needle)
	if utf8.RuneCountInString(needle) < minForgetRunes {
		return nil, fmt.Errorf("text to forget must be at least %d characters", minForgetRunes)
	}
	once := jsonInner(needle)
	twice := jsonInner(once)
	return regexp.Compile("(?i)" + regexp.QuoteMeta(twice) + "|" + regexp.QuoteMeta(once) + "|" + regexp.QuoteMeta(needle))
}

func jsonInner(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s) // encoding a string cannot fail
	out := strings.TrimSuffix(buf.String(), "\n")
	return out[1 : len(out)-1]
}

// Forget removes literal text from everything stored. Matching principles are deleted,
// because their signed text cannot be edited in place. unreadable counts narratives
// that could not be searched. Without apply it only counts.
func (d *DB) Forget(pattern *regexp.Regexp, apply bool) (counts []PurgeCount, unreadable int, err error) {
	all, err := d.storedPrinciples()
	if err != nil {
		return nil, 0, err
	}
	deleted := 0
	for _, p := range all {
		narrative, readable := readNarrative(p.title, p.narrative, p.signature)
		if !readable {
			unreadable++
		}
		if !pattern.MatchString(p.title) && !pattern.MatchString(p.concepts) && !pattern.MatchString(p.hint) && !(readable && pattern.MatchString(narrative)) {
			continue
		}
		deleted++
		if apply {
			if err := d.enableSecureDelete(); err != nil {
				return nil, unreadable, err
			}
			if err := d.DeletePrinciple(p.id); err != nil {
				return nil, unreadable, err
			}
		}
	}
	if deleted > 0 {
		counts = append(counts, PurgeCount{"principles deleted", deleted})
	}
	counts, err = d.rewriteAll(func(s string) string { return pattern.ReplaceAllLiteralString(s, ForgottenMarker) }, apply, counts)
	if err != nil {
		return counts, unreadable, err
	}
	if apply {
		err = d.checkpointTruncate()
	}
	return counts, unreadable, err
}

// CompactAfterPurge removes freed pages that may still hold old text, and needs no other
// process on the database. VACUUM may renumber implicit rowids, which external-content
// FTS tables point at, so those are rebuilt afterwards.
func (d *DB) CompactAfterPurge() error {
	if _, err := d.conn.Exec(`VACUUM`); err != nil {
		return fmt.Errorf("vacuum: %w", err)
	}
	for _, fts := range []string{"events_fts", "principles_fts"} {
		if _, err := d.conn.Exec(fmt.Sprintf(`INSERT INTO %s(%s) VALUES('rebuild')`, fts, fts)); err != nil {
			return fmt.Errorf("rebuild %s: %w", fts, err)
		}
	}
	for _, fts := range []string{"events_fts", "principles_fts", "recall_fts"} {
		if _, err := d.conn.Exec(fmt.Sprintf(`INSERT INTO %s(%s) VALUES('integrity-check')`, fts, fts)); err != nil {
			return fmt.Errorf("integrity-check %s: %w", fts, err)
		}
	}
	return d.checkpointTruncate()
}

// checkpointTruncate empties the WAL, which otherwise keeps old page images on disk.
func (d *DB) checkpointTruncate() error {
	var busy, logged, moved int
	if err := d.conn.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logged, &moved); err != nil {
		return err
	}
	if busy != 0 {
		return fmt.Errorf("WAL still in use by another process: old text may remain until it closes")
	}
	return nil
}

// ArtifactFile is one stored evidence file and the media type it was stored under.
type ArtifactFile struct{ Path, MediaType string }

// ArtifactFiles lists each stored evidence file once.
func (d *DB) ArtifactFiles() ([]ArtifactFile, error) {
	rows, err := d.conn.Query(`SELECT path, MIN(media_type) FROM evaluation_artifacts GROUP BY path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ArtifactFile
	for rows.Next() {
		var f ArtifactFile
		if err := rows.Scan(&f.Path, &f.MediaType); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// RepointArtifacts moves every row using oldPath to rewritten content.
func (d *DB) RepointArtifacts(oldPath, newPath, sha string, size int64) error {
	_, err := d.conn.Exec(`UPDATE evaluation_artifacts SET path = ?, sha256 = ?, byte_size = ? WHERE path = ?`, newPath, sha, size, oldPath)
	return err
}
