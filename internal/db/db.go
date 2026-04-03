package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}
	conn, err := sql.Open("sqlite", path+"?_pragma=journal_mode(wal)&_pragma=synchronous(normal)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	conn.SetMaxOpenConns(1) // SQLite is single-writer
	db := &DB{conn: conn, Path: path}
	if err := db.migrate(); err != nil {
		conn.Close()
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
		`CREATE INDEX IF NOT EXISTS idx_principles_project ON principles(project_id)`,
		`CREATE TABLE IF NOT EXISTS session_summaries (
			id         TEXT PRIMARY KEY,
			ts         TEXT NOT NULL,
			session_id TEXT NOT NULL,
			project_id TEXT NOT NULL,
			summary    TEXT NOT NULL,
			tokens     INTEGER DEFAULT 0
		)`,
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
		{"events", "git_root", "TEXT DEFAULT ''"},
	}
	migrations = append(migrations,
		`CREATE TABLE IF NOT EXISTS projects (
			id           TEXT PRIMARY KEY,
			git_root     TEXT NOT NULL,
			name         TEXT NOT NULL,
			last_active  TEXT NOT NULL,
			agents       TEXT DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_projects_git_root ON projects(git_root)`,
	)
	for _, cm := range colMigrations {
		if err := d.addColumnIfMissing(cm.table, cm.col, cm.def); err != nil {
			return fmt.Errorf("add column %s.%s: %w", cm.table, cm.col, err)
		}
	}
	return nil
}

// addColumnIfMissing runs ALTER TABLE ADD COLUMN, ignoring duplicate-column errors.
func (d *DB) addColumnIfMissing(table, column, colDef string) error {
	_, err := d.conn.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, colDef))
	if err != nil && strings.Contains(err.Error(), "duplicate column name") {
		return nil
	}
	return err
}
