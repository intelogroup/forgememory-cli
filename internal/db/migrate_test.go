package db

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// v7PrinciplesSchema is the principles table exactly as schema version 7 left
// it — before the temporal columns (expired_at and friends) existed. A real v7
// database has more tables; migrate() creates the rest with IF NOT EXISTS, so
// this fixture is enough to exercise the upgrade path.
const v7PrinciplesSchema = `CREATE TABLE principles (
	id              TEXT PRIMARY KEY,
	ts              TEXT NOT NULL,
	type            TEXT NOT NULL,
	title           TEXT NOT NULL,
	narrative       TEXT NOT NULL,
	impact_score    REAL DEFAULT 0.5,
	project_id      TEXT NOT NULL,
	source_event    TEXT,
	fingerprint     TEXT UNIQUE,
	concepts        TEXT DEFAULT '',
	files_modified  TEXT DEFAULT '',
	status          TEXT DEFAULT '',
	conflict_peer_id TEXT DEFAULT '',
	outcome         TEXT NOT NULL DEFAULT 'unknown',
	impl_hint       TEXT NOT NULL DEFAULT '',
	last_used_ts    TEXT DEFAULT '',
	use_count       INTEGER DEFAULT 0,
	signature       TEXT DEFAULT ''
)`

// TestOpenMigratesV7Database is the regression test for the schema-8 ordering
// bug: the partial index on fingerprint referenced expired_at before
// colMigrations added the column, so every Open() on a v7 (or fresh) database
// failed with "no such column".
func TestOpenMigratesV7Database(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v7.db")

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	if _, err := raw.Exec(v7PrinciplesSchema); err != nil {
		raw.Close()
		t.Fatalf("create v7 principles table: %v", err)
	}
	if _, err := raw.Exec(
		`INSERT INTO principles (id, ts, type, title, narrative, project_id, source_event, fingerprint)
		 VALUES ('p1', '2026-01-01T00:00:00Z', 'pattern', 'old lesson', 'kept', 'proj-a', '', 'fp-old')`,
	); err != nil {
		raw.Close()
		t.Fatalf("seed v7 row: %v", err)
	}
	if _, err := raw.Exec(`PRAGMA user_version = 7`); err != nil {
		raw.Close()
		t.Fatalf("stamp user_version: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close fixture: %v", err)
	}

	database, err := Open(path)
	if err != nil {
		t.Fatalf("Open v7 database: %v", err)
	}
	defer database.Close()

	var version int
	if err := database.conn.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != schemaVersion {
		t.Errorf("user_version = %d, want %d", version, schemaVersion)
	}

	got, err := database.GetPrincipleByID("p1")
	if err != nil {
		t.Fatalf("GetPrincipleByID: %v", err)
	}
	if got == nil {
		t.Fatal("pre-migration principle lost")
	}
	if got.Narrative != "kept" {
		t.Errorf("narrative = %q, want kept", got.Narrative)
	}

	// The temporal columns must exist and default to empty for upgraded rows.
	var expiredAt, validFrom, sourceEpisodes string
	if err := database.conn.QueryRow(
		`SELECT expired_at, valid_from, source_episodes FROM principles WHERE id = 'p1'`,
	).Scan(&expiredAt, &validFrom, &sourceEpisodes); err != nil {
		t.Fatalf("temporal columns missing after migration: %v", err)
	}
	if expiredAt != "" || validFrom != "" || sourceEpisodes != "[]" {
		t.Errorf("unexpected defaults: expired_at=%q valid_from=%q source_episodes=%q",
			expiredAt, validFrom, sourceEpisodes)
	}
}
