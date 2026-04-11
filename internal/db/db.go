package db

import (
	"database/sql"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// DB wraps the SQLite connection.
type DB struct {
	conn *sql.DB
	Path string
}

// Open opens (or creates) the Forge database.
// Default path: ~/.forge/forge.db
func Open(path string) (*DB, error) {
	if path == "" {
		path = defaultPath()
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}
	// Best-effort: fix permission bits if they're too restrictive but we own the files.
	_ = os.Chmod(dir, 0o700)
	_ = os.Chmod(path, 0o600)

	conn, err := sql.Open("sqlite", path+"?_pragma=journal_mode(wal)&_pragma=synchronous(normal)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	conn.SetMaxOpenConns(1) // SQLite is single-writer
	db := &DB{conn: conn, Path: path}
	if err := db.migrate(); err != nil {
		conn.Close()
		if strings.Contains(err.Error(), "readonly") {
			return nil, readonlyDBError(path)
		}
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return db, nil
}

func (d *DB) Close() error {
	return d.conn.Close()
}

func (d *DB) Conn() *sql.DB {
	return d.conn
}

func readonlyDBError(path string) error {
	username := "$(whoami)"
	if u, err := user.Current(); err == nil {
		username = u.Username
	}
	dir := filepath.Dir(path)
	return fmt.Errorf(
		"database at %s is read-only\n\nThis usually means the file was created by a different user (e.g. root).\nFix: sudo chown -R %s %s",
		path, username, dir,
	)
}

func defaultPath() string {
	// Check HOME env var first so tests can override via t.Setenv("HOME", ...).
	// On Windows os.UserHomeDir() reads USERPROFILE and ignores HOME.
	home := os.Getenv("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return filepath.Join(home, ".forge", "forge.db")
}

func (d *DB) migrate() error {
	migrations := []string{
		`CREATE TABLE IF NOT EXISTS events (
			id          TEXT PRIMARY KEY,
			ts          TEXT NOT NULL,
			session_id  TEXT NOT NULL,
			project_id  TEXT NOT NULL,
			source_tool TEXT NOT NULL,
			event_type  TEXT NOT NULL,
			tool_name   TEXT,
			payload     TEXT NOT NULL,
			distilled   INTEGER DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS idx_events_distilled ON events(distilled)`,
		`CREATE INDEX IF NOT EXISTS idx_events_ts ON events(ts)`,
		`CREATE INDEX IF NOT EXISTS idx_events_session ON events(session_id)`,
		`CREATE TABLE IF NOT EXISTS principles (
			id           TEXT PRIMARY KEY,
			ts           TEXT NOT NULL,
			type         TEXT NOT NULL,
			title        TEXT NOT NULL,
			narrative    TEXT NOT NULL,
			impact_score REAL DEFAULT 0.5,
			project_id   TEXT NOT NULL,
			source_event TEXT,
			fingerprint  TEXT UNIQUE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_principles_ts ON principles(ts)`,
		`CREATE INDEX IF NOT EXISTS idx_principles_impact_ts ON principles(impact_score DESC, ts DESC)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS principles_fts USING fts5(
			title, narrative, concepts,
			content=principles,
			content_rowid=rowid
		)`,
		`CREATE TRIGGER IF NOT EXISTS principles_ai AFTER INSERT ON principles BEGIN
			INSERT INTO principles_fts(rowid, title, narrative, concepts)
			VALUES (new.rowid, new.title, new.narrative, COALESCE(new.concepts,''));
		END`,
		`CREATE TRIGGER IF NOT EXISTS principles_au AFTER UPDATE ON principles BEGIN
			INSERT INTO principles_fts(principles_fts, rowid, title, narrative, concepts)
			VALUES ('delete', old.rowid, old.title, old.narrative, COALESCE(old.concepts,''));
			INSERT INTO principles_fts(rowid, title, narrative, concepts)
			VALUES (new.rowid, new.title, new.narrative, COALESCE(new.concepts,''));
		END`,
		`CREATE TRIGGER IF NOT EXISTS principles_ad AFTER DELETE ON principles BEGIN
			INSERT INTO principles_fts(principles_fts, rowid, title, narrative, concepts)
			VALUES ('delete', old.rowid, old.title, old.narrative, COALESCE(old.concepts,''));
		END`,
		`CREATE INDEX IF NOT EXISTS idx_principles_project ON principles(project_id)`,
		`CREATE TABLE IF NOT EXISTS session_summaries (
			id         TEXT PRIMARY KEY,
			ts         TEXT NOT NULL,
			session_id TEXT NOT NULL,
			project_id TEXT NOT NULL,
			summary    TEXT NOT NULL,
			tokens     INTEGER DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS failure_signatures (
			id                 TEXT PRIMARY KEY,
			project_id         TEXT NOT NULL,
			session_id         TEXT NOT NULL,
			source_tool        TEXT NOT NULL,
			tool_name          TEXT NOT NULL,
			command_family     TEXT NOT NULL DEFAULT '',
			fingerprint        TEXT NOT NULL,
			error_kind         TEXT NOT NULL,
			normalized_message TEXT NOT NULL,
			first_seen_ts      TEXT NOT NULL,
			last_seen_ts       TEXT NOT NULL,
			repeat_count       INTEGER NOT NULL DEFAULT 1,
			resolved_ts        TEXT DEFAULT '',
			UNIQUE(project_id, session_id, fingerprint, resolved_ts)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_failure_signatures_active
		 ON failure_signatures(project_id, session_id, fingerprint, resolved_ts)`,
		`CREATE TABLE IF NOT EXISTS alerts (
			id          TEXT PRIMARY KEY,
			ts          TEXT NOT NULL,
			project_id  TEXT NOT NULL,
			session_id  TEXT NOT NULL,
			alert_type  TEXT NOT NULL,
			severity    TEXT NOT NULL,
			title       TEXT NOT NULL,
			narrative   TEXT NOT NULL,
			fingerprint TEXT DEFAULT '',
			score       REAL NOT NULL DEFAULT 0,
			source_ref  TEXT DEFAULT '',
			expires_at  TEXT DEFAULT '',
			acknowledged INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_alerts_active_unique
		 ON alerts(project_id, session_id, alert_type, fingerprint, acknowledged)`,
		`CREATE INDEX IF NOT EXISTS idx_alerts_active_lookup
		 ON alerts(project_id, ts DESC, expires_at, acknowledged)`,
		`CREATE TABLE IF NOT EXISTS retrieval_jobs (
			id              TEXT PRIMARY KEY,
			ts              TEXT NOT NULL,
			project_id      TEXT NOT NULL,
			trigger_type    TEXT NOT NULL,
			source          TEXT NOT NULL,
			query           TEXT NOT NULL,
			library_name    TEXT DEFAULT '',
			library_version TEXT DEFAULT '',
			status          TEXT NOT NULL,
			priority        INTEGER NOT NULL DEFAULT 0,
			dedupe_key      TEXT NOT NULL UNIQUE,
			last_error      TEXT DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_retrieval_jobs_lookup
		 ON retrieval_jobs(project_id, status, ts DESC)`,
		`CREATE TABLE IF NOT EXISTS external_context_summaries (
			id              TEXT PRIMARY KEY,
			ts              TEXT NOT NULL,
			source          TEXT NOT NULL,
			project_id      TEXT NOT NULL,
			query           TEXT NOT NULL,
			library_name    TEXT DEFAULT '',
			library_version TEXT DEFAULT '',
			title           TEXT NOT NULL,
			narrative       TEXT NOT NULL,
			summary_kind    TEXT DEFAULT '',
			hint            TEXT DEFAULT '',
			trust_score     REAL NOT NULL DEFAULT 0,
			relevance_tags  TEXT DEFAULT '',
			cache_ref       TEXT DEFAULT '',
			last_used_at    TEXT DEFAULT '',
			expires_at      TEXT DEFAULT ''
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_external_context_unique
		 ON external_context_summaries(project_id, source, query, library_name)`,
		`CREATE INDEX IF NOT EXISTS idx_external_context_lookup
		 ON external_context_summaries(project_id, trust_score DESC, ts DESC)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS events_fts USING fts5(
			payload, content=events, content_rowid=rowid
		)`,
		`CREATE TRIGGER IF NOT EXISTS events_ai AFTER INSERT ON events BEGIN
			INSERT INTO events_fts(rowid, payload) VALUES (new.rowid, new.payload);
		END`,
	}
	for _, m := range migrations {
		if _, err := d.conn.Exec(m); err != nil {
			return fmt.Errorf("exec migration: %w", err)
		}
	}
	// Column additions (idempotent — ignored if column already exists).
	colMigrations := []struct{ table, col, def string }{
		{"principles", "concepts", "TEXT DEFAULT ''"},
		{"principles", "files_modified", "TEXT DEFAULT ''"},
		{"session_summaries", "request", "TEXT DEFAULT ''"},
		{"session_summaries", "investigation", "TEXT DEFAULT ''"},
		{"session_summaries", "learnings", "TEXT DEFAULT ''"},
		{"session_summaries", "next_steps", "TEXT DEFAULT ''"},
		{"session_summaries", "checkpoint_kind", "TEXT DEFAULT ''"},
		{"session_summaries", "checkpoint_key", "TEXT DEFAULT ''"},
		{"events", "git_root", "TEXT DEFAULT ''"},
		{"failure_signatures", "command_family", "TEXT DEFAULT ''"},
		{"external_context_summaries", "summary_kind", "TEXT DEFAULT ''"},
		{"external_context_summaries", "hint", "TEXT DEFAULT ''"},
		{"principles", "status", "TEXT DEFAULT ''"},
		{"principles", "conflict_peer_id", "TEXT DEFAULT ''"},
		{"principles", "outcome", "TEXT NOT NULL DEFAULT 'unknown'"},
		{"principles", "impl_hint", "TEXT NOT NULL DEFAULT ''"},
	}
	extraMigrations := []string{
		`CREATE TABLE IF NOT EXISTS projects (
			id           TEXT PRIMARY KEY,
			git_root     TEXT NOT NULL,
			name         TEXT NOT NULL,
			last_active  TEXT NOT NULL,
			agents       TEXT DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_projects_git_root ON projects(git_root)`,
		`CREATE TABLE IF NOT EXISTS _fts_rebuild_log (
			name TEXT PRIMARY KEY,
			done_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS distillation_health (
			id TEXT PRIMARY KEY,
			last_run_at TEXT DEFAULT '',
			last_success_at TEXT DEFAULT '',
			last_error_at TEXT DEFAULT '',
			last_error_message TEXT DEFAULT '',
			last_status TEXT DEFAULT 'pending',
			consecutive_failures INTEGER DEFAULT 0,
			total_successes INTEGER DEFAULT 0,
			total_failures INTEGER DEFAULT 0,
			last_attempted_events INTEGER DEFAULT 0,
			last_distilled_principles INTEGER DEFAULT 0,
			last_duration_ms INTEGER DEFAULT 0,
			next_scheduled_at TEXT DEFAULT ''
		)`,
	}
	for _, m := range extraMigrations {
		if _, err := d.conn.Exec(m); err != nil {
			return fmt.Errorf("exec migration: %w", err)
		}
	}
	for _, cm := range colMigrations {
		if err := d.addColumnIfMissing(cm.table, cm.col, cm.def); err != nil {
			return fmt.Errorf("add column %s.%s: %w", cm.table, cm.col, err)
		}
	}
	// Indexes that depend on columns added above must run after colMigrations.
	if _, err := d.conn.Exec(
		`CREATE INDEX IF NOT EXISTS idx_principles_status ON principles(project_id, status)`,
	); err != nil {
		return fmt.Errorf("exec migration: %w", err)
	}
	if _, err := d.conn.Exec(`INSERT OR IGNORE INTO distillation_health(id) VALUES ('singleton')`); err != nil {
		return fmt.Errorf("init distillation_health singleton: %w", err)
	}
	if err := d.maybeRebuildPrinciplesFTS(); err != nil {
		return fmt.Errorf("principles fts rebuild: %w", err)
	}
	return nil
}

// maybeRebuildPrinciplesFTS runs a one-time FTS rebuild to backfill principles
// that existed before the FTS index was added. Runs exactly once per DB file,
// tracked in _fts_rebuild_log so it never repeats on subsequent startups.
func (d *DB) maybeRebuildPrinciplesFTS() error {
	var doneat string
	_ = d.conn.QueryRow(`SELECT done_at FROM _fts_rebuild_log WHERE name='principles_fts_v1'`).Scan(&doneat)
	if doneat != "" {
		return nil
	}
	if _, err := d.conn.Exec(`INSERT INTO principles_fts(principles_fts) VALUES('rebuild')`); err != nil {
		return err
	}
	_, err := d.conn.Exec(
		`INSERT OR REPLACE INTO _fts_rebuild_log(name, done_at) VALUES('principles_fts_v1', ?)`,
		time.Now().UTC().Format(time.RFC3339),
	)
	return err
}

// addColumnIfMissing runs ALTER TABLE ADD COLUMN, ignoring duplicate-column errors.
func (d *DB) addColumnIfMissing(table, column, colDef string) error {
	_, err := d.conn.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, colDef))
	if err != nil && strings.Contains(err.Error(), "duplicate column name") {
		return nil
	}
	return err
}
