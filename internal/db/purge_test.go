package db

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Assembled from pieces so secret scanners ignore this file.
var fakeToken = "sbp_" + strings.Repeat("0a1b2c3d", 5)

func purgeTestDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "forge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// rawEvent bypasses InsertEvent, which scrubs: old rows predate the current scrubber.
func rawEvent(t *testing.T, d *DB, id, eventType, payload string) {
	t.Helper()
	if _, err := d.conn.Exec(`INSERT INTO events(id, ts, session_id, project_id, source_tool, event_type, payload)
		VALUES(?, '2026-08-21T00:00:00Z', 's', 'p', 'claude', ?, ?)`, id, eventType, payload); err != nil {
		t.Fatal(err)
	}
}

func rawSummary(t *testing.T, d *DB, id, text string) {
	t.Helper()
	if _, err := d.conn.Exec(`INSERT INTO session_summaries(id, ts, session_id, project_id, summary, learnings)
		VALUES(?, '2026-08-21T00:00:00Z', 's', 'p', ?, 'kept learning')`, id, text); err != nil {
		t.Fatal(err)
	}
}

func count(t *testing.T, d *DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := d.conn.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func ftsHits(t *testing.T, d *DB, table, term string) int {
	t.Helper()
	return count(t, d, fmt.Sprintf(`SELECT count(*) FROM %s WHERE %s MATCH ?`, table, table), `"`+term+`"`)
}

func vocabHits(t *testing.T, d *DB, table, term string) int {
	t.Helper()
	if _, err := d.conn.Exec(fmt.Sprintf(`CREATE VIRTUAL TABLE IF NOT EXISTS temp.v_%s USING fts5vocab(main, %s, row)`, table, table)); err != nil {
		t.Fatal(err)
	}
	return count(t, d, fmt.Sprintf(`SELECT count(*) FROM temp.v_%s WHERE term = ?`, table), strings.ToLower(term))
}

func TestPurgeColumnsCoverEveryTextColumn(t *testing.T) {
	d := purgeTestDB(t)
	listed := map[string]bool{}
	for _, c := range purgeColumns {
		listed[c.table+"."+c.column] = true
	}
	// IDs, timestamps, enums, hashes and paths the code relies on. Rewriting these breaks lookups.
	structural := regexp.MustCompile(`^(id|ts|key|sha|sha256|path|fingerprint|signature|nonce|model|status|kind|role|source|op|type|action|author|cwd|files|git_root|git_branch|git_commit|transcript_path|tool_name|source_tool|media_type|severity|state|evaluator|relation|outcome|sensitivity|project_key|scope|agents|last_active|files_modified|dedupe_key|library_name|library_version|error_kind|command_family|delivery_mode|expected_tests|baseline_commit|extractor_version|definition_version|ranking_version|rubric_version|summary_kind|relevance_tags|cache_ref|last_status|failure_category|checkpoint_kind|checkpoint_key|reason_code|source_ref|source_actor|skill_key|pattern_type|alert_type|event_type|source_type|source_kind|source_event|trigger_type|scope_type|.*_id|.*_ids|.*_ts|.*_at|.*_digest|valid_from|valid_until|effective_from|effective_until|superseded_by|source_episodes)$`)
	handledElsewhere := map[string]bool{
		"principles.title": true, "principles.narrative": true, // signed: redactPrinciples and Forget
		"projects.name": true, "skill_definitions.name": true, "skill_definitions.description": true,
	}
	rows, err := d.conn.Query(`SELECT m.name, c.name FROM sqlite_master m, pragma_table_info(m.name) c
		WHERE m.type = 'table' AND m.name NOT LIKE 'sqlite_%' AND m.name NOT LIKE '%_fts%' AND upper(c.type) LIKE '%TEXT%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			t.Fatal(err)
		}
		if !listed[table+"."+column] && !structural.MatchString(column) && !handledElsewhere[table+"."+column] {
			t.Errorf("%s.%s: add it to purgeColumns, or to the structural list if it holds no free text", table, column)
		}
	}
	for _, c := range purgeColumns[:len(purgeColumns)-1] {
		if n := count(t, d, `SELECT count(*) FROM pragma_table_info(?) WHERE name = ?`, c.table, c.column); n != 1 {
			t.Errorf("purgeColumns names a missing column: %s.%s", c.table, c.column)
		}
	}
}

func TestRedactSecretsDryRunChangesNothing(t *testing.T) {
	d := purgeTestDB(t)
	rawEvent(t, d, "e1", "PostToolUse", `{"text":"`+fakeToken+`"}`)

	counts, err := d.RedactSecrets(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(counts) != 1 || counts[0].Target != "events.payload" || counts[0].Rows != 1 {
		t.Fatalf("counts = %+v", counts)
	}
	if ftsHits(t, d, "events_fts", fakeToken) != 1 {
		t.Fatal("dry run changed the index")
	}
	if n := count(t, d, `SELECT count(*) FROM events WHERE instr(payload, ?) > 0`, fakeToken); n != 1 {
		t.Fatal("dry run changed the row")
	}
}

func TestRedactSecretsReachesEveryCopy(t *testing.T) {
	d := purgeTestDB(t)
	rawEvent(t, d, "e1", "PostToolUse", `{"text":"`+fakeToken+` plainneighbor"}`)
	rawEvent(t, d, "e2", "PostToolUse", `{"text":"untouchedword stays"}`)
	rawEvent(t, d, "n1", "ManualSave", "note with "+fakeToken)
	rawSummary(t, d, "s1", "summary with "+fakeToken)
	rawSummary(t, d, "s2", "clean summary")
	for _, e := range []Embedding{
		{Kind: RecallKindNote, ID: "n1", Model: "m", Vec: []float32{1}},
		{Kind: RecallKindSummary, ID: "s1", Model: "m", Vec: []float32{1}},
		{Kind: RecallKindSummary, ID: "s2", Model: "m", Vec: []float32{1}},
	} {
		if err := d.SaveEmbeddings([]Embedding{e}); err != nil {
			t.Fatal(err)
		}
	}
	if ftsHits(t, d, "recall_fts", fakeToken) != 2 {
		t.Fatal("setup: expected the token in recall_fts")
	}

	if _, err := d.RedactSecrets(true); err != nil {
		t.Fatal(err)
	}
	if err := d.CompactAfterPurge(); err != nil {
		t.Fatal(err)
	}

	for _, table := range []string{"events_fts", "recall_fts"} {
		if n := ftsHits(t, d, table, fakeToken); n != 0 {
			t.Errorf("%s still matches the token: %d", table, n)
		}
		if n := vocabHits(t, d, table, fakeToken); n != 0 {
			t.Errorf("%s vocabulary still holds the token: %d", table, n)
		}
	}
	var payload string
	if err := d.conn.QueryRow(`SELECT payload FROM events WHERE id = 'e1'`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload, fakeToken) || !json.Valid([]byte(payload)) {
		t.Errorf("payload = %q", payload)
	}
	// VACUUM may renumber rowids: the index must still point at the right rows.
	var id string
	if err := d.conn.QueryRow(`SELECT e.id FROM events_fts f JOIN events e ON e.rowid = f.rowid WHERE events_fts MATCH 'untouchedword'`).Scan(&id); err != nil || id != "e2" {
		t.Errorf("untouched event via FTS: id=%q err=%v", id, err)
	}
	if ftsHits(t, d, "events_fts", "plainneighbor") != 1 {
		t.Error("redacted event lost the rest of its text from the index")
	}
	for kind, want := range map[string]int{"note/n1": 0, "summary/s1": 0, "summary/s2": 1} {
		parts := strings.Split(kind, "/")
		if n := count(t, d, `SELECT count(*) FROM embeddings WHERE source_kind = ? AND source_id = ?`, parts[0], parts[1]); n != want {
			t.Errorf("embedding %s: %d, want %d", kind, n, want)
		}
	}
	for _, name := range []string{d.Path, d.Path + "-wal"} {
		raw, err := os.ReadFile(name)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), fakeToken) {
			t.Errorf("token bytes remain in %s", filepath.Base(name))
		}
	}
}

func TestRedactSecretsKeepsPrincipleSignaturesValid(t *testing.T) {
	d := purgeTestDB(t)
	p := &Principle{ID: "p1", Title: "Clean title", Narrative: "uses " + fakeToken, Type: "pattern", ProjectID: "p", TS: "2026-08-21T00:00:00Z"}
	if _, err := d.InsertPrinciple(p); err != nil {
		t.Fatal(err)
	}
	if _, err := d.RedactSecrets(true); err != nil {
		t.Fatal(err)
	}
	var title, stored, signature string
	if err := d.conn.QueryRow(`SELECT title, narrative, signature FROM principles WHERE id = 'p1'`).Scan(&title, &stored, &signature); err != nil {
		t.Fatal(err)
	}
	narrative, ok := readNarrative(title, stored, signature)
	if !ok || strings.Contains(narrative, fakeToken) || !strings.Contains(narrative, "uses") {
		t.Fatalf("narrative = %q readable = %v", narrative, ok)
	}
}

func TestForgetRemovesTextEverywhereAndLeavesTheRest(t *testing.T) {
	d := purgeTestDB(t)
	rawEvent(t, d, "e1", "PostToolUse", `{"text":"codename zebra-7731 here"}`)
	rawEvent(t, d, "e3", "PostToolUse", `{"text":"unrelated words"}`)
	rawEvent(t, d, "n1", "ManualSave", "note about ZEBRA-7731")
	rawSummary(t, d, "s1", "summary about Zebra-7731")
	match := &Principle{ID: "p1", Title: "Avoid ZEBRA-7731", Narrative: "n", Type: "pattern", ProjectID: "p", TS: "2026-08-21T00:00:00Z"}
	secretBody := &Principle{ID: "p2", Title: "Clean title", Narrative: "mentions zebra-7731", Type: "pattern", ProjectID: "p", TS: "2026-08-21T00:00:00Z"}
	keep := &Principle{ID: "p3", Title: "Keep this lesson", Narrative: "fine", Type: "pattern", ProjectID: "p", TS: "2026-08-21T00:00:00Z"}
	for _, p := range []*Principle{match, secretBody, keep} {
		if _, err := d.InsertPrinciple(p); err != nil {
			t.Fatal(err)
		}
	}
	pattern, err := ForgetPattern("ZEBRA-7731")
	if err != nil {
		t.Fatal(err)
	}

	counts, _, err := d.Forget(pattern, false)
	if err != nil || len(counts) == 0 {
		t.Fatalf("dry run: %+v err=%v", counts, err)
	}
	if n := count(t, d, `SELECT count(*) FROM principles`); n != 3 {
		t.Fatalf("dry run deleted principles: %d left", n)
	}
	if n := count(t, d, `SELECT count(*) FROM events WHERE instr(lower(payload), 'zebra-7731') > 0`); n != 2 {
		t.Fatalf("dry run changed events: %d", n)
	}

	if _, _, err := d.Forget(pattern, true); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`SELECT count(*) FROM events WHERE instr(lower(payload), 'zebra-7731') > 0`,
		`SELECT count(*) FROM session_summaries WHERE instr(lower(summary), 'zebra-7731') > 0`,
		`SELECT count(*) FROM recall_fts WHERE instr(lower(body), 'zebra-7731') > 0`,
	} {
		if n := count(t, d, q); n != 0 {
			t.Errorf("%s = %d", q, n)
		}
	}
	for _, table := range []string{"events_fts", "recall_fts"} {
		if n := ftsHits(t, d, table, "7731"); n != 0 {
			t.Errorf("%s still matches: %d", table, n)
		}
		if n := vocabHits(t, d, table, "7731"); n != 0 {
			t.Errorf("%s vocabulary still holds the term: %d", table, n)
		}
	}
	if n := count(t, d, `SELECT count(*) FROM principles`); n != 1 {
		t.Errorf("principles left: %d, want only the unrelated one", n)
	}
	if ftsHits(t, d, "events_fts", "unrelated") != 1 || ftsHits(t, d, "recall_fts", "keep") != 1 {
		t.Error("forget damaged unrelated rows")
	}
	var payload string
	if err := d.conn.QueryRow(`SELECT payload FROM events WHERE id = 'e1'`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if !json.Valid([]byte(payload)) || !strings.Contains(payload, ForgottenMarker) {
		t.Errorf("payload = %q", payload)
	}
}

func TestForgetMatchesJSONEscapedText(t *testing.T) {
	d := purgeTestDB(t)
	rawEvent(t, d, "e1", "PostToolUse", `{"text":"she said \"open sesame\" twice"}`)
	rawEvent(t, d, "e2", "PostToolUse", `{"cmd":"{\"text\":\"she said \\\"open sesame\\\" twice\"}"}`)
	pattern, err := ForgetPattern(`said "open sesame"`)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.Forget(pattern, true); err != nil {
		t.Fatal(err)
	}
	if n := count(t, d, `SELECT count(*) FROM events WHERE instr(payload, 'sesame') > 0`); n != 0 {
		t.Errorf("escaped forms survived: %d", n)
	}
	if n := count(t, d, `SELECT count(*) FROM events WHERE json_valid(payload)`); n != 2 {
		t.Errorf("JSON broken: %d valid", n)
	}
}

func TestForgetRefusesShortText(t *testing.T) {
	for _, short := range []string{"", "   ", "abc", " ab "} {
		if _, err := ForgetPattern(short); err == nil {
			t.Errorf("ForgetPattern(%q) accepted", short)
		}
	}
}
