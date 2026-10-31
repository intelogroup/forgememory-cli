package db

import (
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/forge/forge/internal/security"
)

// Kinds of document recall indexes: distilled, human-scale text. Raw tool events
// are left out on purpose: they embed at about 6/s and bury the signal.
const (
	RecallKindPrinciple = "principle"
	RecallKindSummary   = "summary"
	RecallKindNote      = "note"
)

const activePrincipleSQL = `(status = 'active' OR status IS NULL OR status = '')`

// maxEmbedText bounds what one document sends to the model.
const maxEmbedText = 6000

// Principle narratives stay out of recall_fts: indexing them would write the
// plaintext that sealPrincipleContent encrypts. Vectors cover narrative meaning.
func migrateRecall(tx *sql.Tx) error {
	const summaryBody = `COALESCE(new.request,'')||' '||COALESCE(new.summary,'')||' '||COALESCE(new.investigation,'')||' '||COALESCE(new.learnings,'')||' '||COALESCE(new.next_steps,'')||' '||COALESCE(new.keywords,'')`
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS embeddings (
			source_kind TEXT NOT NULL,
			source_id   TEXT NOT NULL,
			model       TEXT NOT NULL,
			vec         BLOB NOT NULL,
			partial     INTEGER NOT NULL DEFAULT 0,
			embedded_at TEXT NOT NULL,
			PRIMARY KEY (source_kind, source_id)
		)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS recall_fts USING fts5(
			source_kind UNINDEXED, source_id UNINDEXED, body
		)`,
		`CREATE INDEX IF NOT EXISTS idx_events_manual_save ON events(id) WHERE event_type = 'ManualSave'`,

		`CREATE TRIGGER IF NOT EXISTS principles_recall_ai AFTER INSERT ON principles BEGIN
			INSERT INTO recall_fts(source_kind, source_id, body)
			VALUES ('principle', new.id, new.title || ' ' || COALESCE(new.concepts,''));
		END`,
		`CREATE TRIGGER IF NOT EXISTS principles_recall_au AFTER UPDATE OF title, concepts ON principles BEGIN
			DELETE FROM recall_fts WHERE source_kind = 'principle' AND source_id = old.id;
			INSERT INTO recall_fts(source_kind, source_id, body)
			VALUES ('principle', new.id, new.title || ' ' || COALESCE(new.concepts,''));
		END`,
		`CREATE TRIGGER IF NOT EXISTS principles_embed_au AFTER UPDATE OF title, narrative, concepts ON principles BEGIN
			DELETE FROM embeddings WHERE source_kind = 'principle' AND source_id = old.id;
		END`,
		`CREATE TRIGGER IF NOT EXISTS principles_recall_ad AFTER DELETE ON principles BEGIN
			DELETE FROM recall_fts WHERE source_kind = 'principle' AND source_id = old.id;
			DELETE FROM embeddings WHERE source_kind = 'principle' AND source_id = old.id;
		END`,

		`CREATE TRIGGER IF NOT EXISTS summaries_recall_ai AFTER INSERT ON session_summaries BEGIN
			INSERT INTO recall_fts(source_kind, source_id, body) VALUES ('summary', new.id, ` + summaryBody + `);
		END`,
		`CREATE TRIGGER IF NOT EXISTS summaries_recall_au AFTER UPDATE OF summary, request, investigation, learnings, next_steps, keywords ON session_summaries BEGIN
			DELETE FROM recall_fts WHERE source_kind = 'summary' AND source_id = old.id;
			DELETE FROM embeddings WHERE source_kind = 'summary' AND source_id = old.id;
			INSERT INTO recall_fts(source_kind, source_id, body) VALUES ('summary', new.id, ` + summaryBody + `);
		END`,
		`CREATE TRIGGER IF NOT EXISTS summaries_recall_ad AFTER DELETE ON session_summaries BEGIN
			DELETE FROM recall_fts WHERE source_kind = 'summary' AND source_id = old.id;
			DELETE FROM embeddings WHERE source_kind = 'summary' AND source_id = old.id;
		END`,

		// Raw payload, never json_extract: payloads can be truncated, and a JSON
		// error in this trigger would abort the event insert on the hook path.
		`CREATE TRIGGER IF NOT EXISTS events_recall_ai AFTER INSERT ON events WHEN new.event_type = 'ManualSave' BEGIN
			INSERT INTO recall_fts(source_kind, source_id, body) VALUES ('note', new.id, new.payload);
		END`,
	}
	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("exec recall migration: %w", err)
		}
	}

	var done string
	_ = tx.QueryRow(`SELECT done_at FROM _fts_rebuild_log WHERE name = 'recall_fts_v1'`).Scan(&done)
	if done != "" {
		return nil
	}
	backfill := []string{
		`INSERT INTO recall_fts(source_kind, source_id, body)
		 SELECT 'principle', id, title || ' ' || COALESCE(concepts,'') FROM principles`,
		`INSERT INTO recall_fts(source_kind, source_id, body)
		 SELECT 'summary', id, ` + strings.ReplaceAll(summaryBody, "new.", "") + ` FROM session_summaries`,
		`INSERT INTO recall_fts(source_kind, source_id, body)
		 SELECT 'note', id, payload FROM events WHERE event_type = 'ManualSave'`,
	}
	for _, stmt := range backfill {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("backfill recall_fts: %w", err)
		}
	}
	_, err := tx.Exec(
		`INSERT OR REPLACE INTO _fts_rebuild_log(name, done_at) VALUES('recall_fts_v1', ?)`,
		time.Now().UTC().Format(time.RFC3339),
	)
	return err
}

// EncodeVector packs a vector into the little-endian float32 BLOB stored in
// embeddings.vec.
func EncodeVector(v []float32) []byte {
	out := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(out[i*4:], math.Float32bits(x))
	}
	return out
}

// DecodeVector reverses EncodeVector.
func DecodeVector(b []byte) []float32 {
	out := make([]float32, len(b)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return out
}

// RecallScope selects which documents a recall may see. It applies the same
// rule as RecentPrinciplesVisibleTo: a project sees its own rows plus global
// principles. All is the explicit cross-project opt-in.
type RecallScope struct {
	ProjectID  string
	ProjectKey string
	All        bool
}

func (s RecallScope) visibleClause(kindCol, idCol string) (string, []any) {
	principles := `SELECT id FROM principles WHERE ` + activePrincipleSQL
	summaries := `SELECT id FROM session_summaries`
	notes := `SELECT id FROM events WHERE event_type = 'ManualSave'`
	var args []any
	switch {
	case s.All:
	case s.ProjectID == "":
		principles += ` AND scope = 'global'`
		summaries += ` WHERE 0`
		notes += ` AND 0`
	default:
		pClause, pArgs := projectScopePredicate("", s.ProjectID, s.ProjectKey)
		sClause, sArgs := projectScopePredicate("", s.ProjectID, "")
		principles += ` AND (scope = 'global' OR ` + pClause + `)`
		summaries += ` WHERE ` + sClause
		notes += ` AND ` + sClause
		args = append(append(append(args, pArgs...), sArgs...), sArgs...)
	}
	clause := fmt.Sprintf(`((%[1]s = 'principle' AND %[2]s IN (%[3]s))
		OR (%[1]s = 'summary' AND %[2]s IN (%[4]s))
		OR (%[1]s = 'note' AND %[2]s IN (%[5]s)))`, kindCol, idCol, principles, summaries, notes)
	return clause, args
}

// EmbedDoc is a document waiting for a vector. Partial means its narrative
// could not be read or verified, so Text holds only the title.
type EmbedDoc struct {
	Kind    string
	ID      string
	Text    string
	Partial bool
}

// Embedding is a computed vector ready to store.
type Embedding struct {
	Kind    string
	ID      string
	Model   string
	Vec     []float32
	Partial bool
}

// VectorRow is a stored vector visible to one recall.
type VectorRow struct {
	Kind    string
	ID      string
	Vec     []float32
	Partial bool
}

// RecallKey identifies a recall document.
type RecallKey struct {
	Kind string
	ID   string
}

// RecallDoc is a recall document ready to show. Partial means the title could
// not be verified and there is no narrative to show.
type RecallDoc struct {
	Kind      string
	ID        string
	TS        string
	ProjectID string
	Title     string
	Body      string
	Partial   bool
}

// readNarrative decrypts and verifies a stored narrative. false means it is
// unreadable or fails its signature, and must not be shown or embedded.
func readNarrative(title, stored, signature string) (string, bool) {
	key := hmacKey()
	if key == nil {
		if strings.HasPrefix(stored, security.EncPrefix) {
			return "", false
		}
		return stored, true
	}
	plain, err := security.Decrypt(key, stored)
	if err != nil {
		return "", false
	}
	if signature != "" && !security.Verify(key, signedPayload(title, plain), signature) {
		return "", false
	}
	return plain, true
}

// principleEmbedDoc leaves concept tags out: generic tags like "gotcha" pull
// unrelated principles together, and removing them raised recall in testing.
func principleEmbedDoc(id, title, stored, signature string) EmbedDoc {
	text := title
	narrative, ok := readNarrative(title, stored, signature)
	if ok {
		text += ". " + narrative
	}
	return EmbedDoc{Kind: RecallKindPrinciple, ID: id, Text: clampText(text), Partial: !ok}
}

func clampText(s string) string {
	s = strings.ToValidUTF8(strings.TrimSpace(s), "")
	if len(s) > maxEmbedText {
		s = strings.ToValidUTF8(s[:maxEmbedText], "")
	}
	return s
}

func joinNonEmpty(parts ...string) string {
	seen := map[string]bool{}
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return strings.Join(out, "\n")
}

func noteText(payload string) string {
	var saved struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(payload), &saved); err == nil && strings.TrimSpace(saved.Content) != "" {
		return saved.Content
	}
	return payload
}

// EmbeddingBacklog returns documents with no vector for model, newest first.
// A row missing here is the whole contract: triggers delete a vector when its
// source changes, so no write path needs to know about embeddings.
func (d *DB) EmbeddingBacklog(model string, limit int) ([]EmbedDoc, error) {
	var docs []EmbedDoc

	rows, err := d.conn.Query(
		`SELECT p.id, p.title, p.narrative, COALESCE(p.signature,'')
		 FROM principles p
		 LEFT JOIN embeddings e ON e.source_kind = 'principle' AND e.source_id = p.id AND e.model = ?
		 WHERE e.source_id IS NULL AND (p.status = 'active' OR p.status IS NULL OR p.status = '')
		 ORDER BY p.ts DESC LIMIT ?`, model, limit)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, title, narrative, signature string
		if err := rows.Scan(&id, &title, &narrative, &signature); err != nil {
			rows.Close()
			return nil, err
		}
		docs = append(docs, principleEmbedDoc(id, title, narrative, signature))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = d.conn.Query(
		`SELECT s.id, COALESCE(s.request,''), COALESCE(s.summary,''), COALESCE(s.learnings,''),
		        COALESCE(s.investigation,''), COALESCE(s.next_steps,'')
		 FROM session_summaries s
		 LEFT JOIN embeddings e ON e.source_kind = 'summary' AND e.source_id = s.id AND e.model = ?
		 WHERE e.source_id IS NULL
		 ORDER BY s.ts DESC LIMIT ?`, model, limit)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, request, summary, learnings, investigation, nextSteps string
		if err := rows.Scan(&id, &request, &summary, &learnings, &investigation, &nextSteps); err != nil {
			rows.Close()
			return nil, err
		}
		text := clampText(joinNonEmpty(request, summary, learnings, investigation, nextSteps))
		if text == "" {
			// Store something so an empty summary doesn't stay in the backlog forever.
			text = "empty session summary"
		}
		docs = append(docs, EmbedDoc{Kind: RecallKindSummary, ID: id, Text: text})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = d.conn.Query(
		`SELECT ev.id, ev.payload
		 FROM events ev
		 LEFT JOIN embeddings e ON e.source_kind = 'note' AND e.source_id = ev.id AND e.model = ?
		 WHERE ev.event_type = 'ManualSave' AND e.source_id IS NULL
		 ORDER BY ev.ts DESC LIMIT ?`, model, limit)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, payload string
		if err := rows.Scan(&id, &payload); err != nil {
			rows.Close()
			return nil, err
		}
		text := clampText(noteText(payload))
		if text == "" {
			text = "empty note"
		}
		docs = append(docs, EmbedDoc{Kind: RecallKindNote, ID: id, Text: text})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(docs) > limit {
		docs = docs[:limit]
	}
	return docs, nil
}

// SaveEmbeddings stores vectors in one short transaction.
//
// shortcut: a source edited while its vector is being computed keeps the stale
// vector until its next edit. Upgrade by comparing a content hash on save if
// principle edits ever become frequent.
func (d *DB) SaveEmbeddings(items []Embedding) error {
	if len(items) == 0 {
		return nil
	}
	tx, err := d.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339)
	for _, it := range items {
		partial := 0
		if it.Partial {
			partial = 1
		}
		if _, err := tx.Exec(
			`INSERT OR REPLACE INTO embeddings(source_kind, source_id, model, vec, partial, embedded_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			it.Kind, it.ID, it.Model, EncodeVector(it.Vec), partial, now,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// HealPartialEmbeddings drops title-only vectors whose narrative is readable
// now, so the backlog re-embeds them in full. A process that fell back to the
// wrong key would otherwise leave them title-only for good.
func (d *DB) HealPartialEmbeddings() (int, error) {
	rows, err := d.conn.Query(
		`SELECT p.id, p.title, p.narrative, COALESCE(p.signature,'')
		 FROM embeddings e JOIN principles p ON e.source_id = p.id
		 WHERE e.source_kind = 'principle' AND e.partial = 1`)
	if err != nil {
		return 0, err
	}
	var healed []string
	for rows.Next() {
		var id, title, narrative, signature string
		if err := rows.Scan(&id, &title, &narrative, &signature); err != nil {
			rows.Close()
			return 0, err
		}
		if _, ok := readNarrative(title, narrative, signature); ok {
			healed = append(healed, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, id := range healed {
		if _, err := d.conn.Exec(`DELETE FROM embeddings WHERE source_kind = 'principle' AND source_id = ?`, id); err != nil {
			return 0, err
		}
	}
	return len(healed), nil
}

// LoadVectors returns every vector for model visible in scope.
func (d *DB) LoadVectors(model string, scope RecallScope) ([]VectorRow, error) {
	clause, args := scope.visibleClause("source_kind", "source_id")
	rows, err := d.conn.Query(
		`SELECT source_kind, source_id, vec, partial FROM embeddings WHERE model = ? AND `+clause,
		append([]any{model}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VectorRow
	for rows.Next() {
		var r VectorRow
		var blob []byte
		var partial int
		if err := rows.Scan(&r.Kind, &r.ID, &blob, &partial); err != nil {
			return nil, err
		}
		r.Vec = DecodeVector(blob)
		r.Partial = partial == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

// RecallTerms splits a query into lowercase search terms of three or more
// letters or digits, dropping repeats.
func RecallTerms(query string) []string {
	seen := map[string]bool{}
	var terms []string
	for _, t := range strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !(r == '_' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r > 127)
	}) {
		if len(t) < 3 || seen[t] {
			continue
		}
		seen[t] = true
		terms = append(terms, t)
	}
	return terms
}

// RecallFTS ranks visible documents by BM25 per kind and matches any query term. Any, not
// all: requiring every term found 5 of 40 known documents in testing, against
// 38 of 40 for any.
func (d *DB) RecallFTS(query string, scope RecallScope, limit int) ([]RecallKey, error) {
	match := buildFTSQuery(RecallTerms(query))
	if match == "" {
		return nil, nil
	}
	clause, args := scope.visibleClause("source_kind", "source_id")
	var out []RecallKey
	// Limit per kind: BM25 favours long summaries, which would otherwise use up
	// every slot before a short principle is considered.
	for _, kind := range []string{RecallKindPrinciple, RecallKindNote, RecallKindSummary} {
		queryArgs := append(append([]any{match, kind}, args...), limit)
		rows, err := d.conn.Query(
			`SELECT source_kind, source_id FROM recall_fts
			 WHERE recall_fts MATCH ? AND source_kind = ? AND `+clause+` ORDER BY rank LIMIT ?`,
			queryArgs...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var k RecallKey
			if err := rows.Scan(&k.Kind, &k.ID); err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, k)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// RecallDocs loads documents for display, in the order given. Keys that no
// longer resolve are skipped.
func (d *DB) RecallDocs(keys []RecallKey) ([]RecallDoc, error) {
	docs := make([]RecallDoc, 0, len(keys))
	for _, k := range keys {
		doc, err := d.recallDoc(k)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, err
		}
		docs = append(docs, doc)
	}
	return docs, nil
}

func (d *DB) recallDoc(k RecallKey) (RecallDoc, error) {
	doc := RecallDoc{Kind: k.Kind, ID: k.ID}
	switch k.Kind {
	case RecallKindPrinciple:
		var narrative, signature string
		if err := d.conn.QueryRow(
			`SELECT ts, project_id, title, narrative, COALESCE(signature,'') FROM principles WHERE id = ?`, k.ID,
		).Scan(&doc.TS, &doc.ProjectID, &doc.Title, &narrative, &signature); err != nil {
			return doc, err
		}
		body, ok := readNarrative(doc.Title, narrative, signature)
		doc.Body, doc.Partial = body, !ok
	case RecallKindSummary:
		var request, summary, learnings, investigation, nextSteps string
		if err := d.conn.QueryRow(
			`SELECT ts, project_id, COALESCE(request,''), COALESCE(summary,''), COALESCE(learnings,''),
			        COALESCE(investigation,''), COALESCE(next_steps,'')
			 FROM session_summaries WHERE id = ?`, k.ID,
		).Scan(&doc.TS, &doc.ProjectID, &request, &summary, &learnings, &investigation, &nextSteps); err != nil {
			return doc, err
		}
		doc.Title = "Session summary"
		doc.Body = joinNonEmpty(request, summary, learnings, investigation, nextSteps)
	case RecallKindNote:
		var payload string
		if err := d.conn.QueryRow(
			`SELECT ts, project_id, payload FROM events WHERE id = ? AND event_type = 'ManualSave'`, k.ID,
		).Scan(&doc.TS, &doc.ProjectID, &payload); err != nil {
			return doc, err
		}
		doc.Title = "Saved note"
		doc.Body = noteText(payload)
	default:
		return doc, sql.ErrNoRows
	}
	return doc, nil
}

// EmbeddingCoverage reports how much of the recall corpus has a vector for model.
func (d *DB) EmbeddingCoverage(model string) (embedded, total, partial int, err error) {
	err = d.conn.QueryRow(
		`SELECT
		   (SELECT COUNT(*) FROM principles WHERE ` + activePrincipleSQL + `)
		 + (SELECT COUNT(*) FROM session_summaries)
		 + (SELECT COUNT(*) FROM events WHERE event_type = 'ManualSave')`,
	).Scan(&total)
	if err != nil {
		return
	}
	clause, args := RecallScope{All: true}.visibleClause("source_kind", "source_id")
	err = d.conn.QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(partial),0) FROM embeddings WHERE model = ? AND `+clause,
		append([]any{model}, args...)...,
	).Scan(&embedded, &partial)
	return
}
