// Package store persists accounts, runs, schedules and settings in SQLite.
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"readtrace/internal/secure"
)

// Store wraps the SQLite database and the at-rest encryption box.
type Store struct {
	db  *sql.DB
	box *secure.Box
}

const schema = `
CREATE TABLE IF NOT EXISTS settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
	id         TEXT PRIMARY KEY,
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL,
	ip         TEXT NOT NULL DEFAULT '',
	ua         TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS accounts (
	id               INTEGER PRIMARY KEY AUTOINCREMENT,
	name             TEXT NOT NULL,
	vid              TEXT NOT NULL DEFAULT '',
	nickname         TEXT NOT NULL DEFAULT '',
	avatar           TEXT NOT NULL DEFAULT '',
	cookie_enc       BLOB,
	payload_enc      BLOB,
	status           TEXT NOT NULL DEFAULT 'active',
	last_error       TEXT NOT NULL DEFAULT '',
	last_check_at    INTEGER NOT NULL DEFAULT 0,
	default_minutes  INTEGER NOT NULL DEFAULT 30,
	tick_seconds     INTEGER NOT NULL DEFAULT 30,
	book_id          TEXT NOT NULL DEFAULT '',
	book_title       TEXT NOT NULL DEFAULT '',
	chapter_id       TEXT NOT NULL DEFAULT '',
	chapter_title    TEXT NOT NULL DEFAULT '',
	mode             TEXT NOT NULL DEFAULT 'stable',
	push_channel_id  INTEGER NOT NULL DEFAULT 0,
	created_at       INTEGER NOT NULL,
	updated_at       INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS runs (
	id               INTEGER PRIMARY KEY AUTOINCREMENT,
	account_id       INTEGER NOT NULL,
	trigger          TEXT NOT NULL DEFAULT 'manual',
	plan_seconds     INTEGER NOT NULL,
	elapsed_seconds  INTEGER NOT NULL DEFAULT 0,
	success_ticks    INTEGER NOT NULL DEFAULT 0,
	failed_ticks     INTEGER NOT NULL DEFAULT 0,
	status           TEXT NOT NULL DEFAULT 'running',
	error            TEXT NOT NULL DEFAULT '',
	started_at       INTEGER NOT NULL,
	finished_at      INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS run_logs (
	id      INTEGER PRIMARY KEY AUTOINCREMENT,
	run_id  INTEGER NOT NULL,
	ts      INTEGER NOT NULL,
	level   TEXT NOT NULL,
	message TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS schedules (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	account_id  INTEGER NOT NULL,
	cron        TEXT NOT NULL,
	minutes     INTEGER NOT NULL DEFAULT 30,
	enabled     INTEGER NOT NULL DEFAULT 1,
	last_run_at INTEGER NOT NULL DEFAULT 0,
	next_run_at INTEGER NOT NULL DEFAULT 0,
	created_at  INTEGER NOT NULL,
	updated_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS push_channels (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	name       TEXT NOT NULL,
	type       TEXT NOT NULL,
	config_enc BLOB,
	enabled    INTEGER NOT NULL DEFAULT 1,
	created_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_runs_account_started ON runs(account_id, started_at);
CREATE INDEX IF NOT EXISTS idx_runs_started ON runs(started_at);
CREATE INDEX IF NOT EXISTS idx_run_logs_run ON run_logs(run_id, id);
CREATE INDEX IF NOT EXISTS idx_schedules_account ON schedules(account_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at);
`

// Open creates/opens the database and applies the schema.
func Open(dbPath string, box *secure.Box) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.ToSlash(dbPath) +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// The database holds encrypted credentials; keep it owner-only on POSIX.
	_ = os.Chmod(dbPath, 0o600)
	// A single connection keeps writes serialized and avoids SQLITE_BUSY on
	// platforms where WAL locking is less forgiving.
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(0)

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("初始化数据库失败: %w", err)
	}
	return &Store{db: db, box: box}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func now() int64 { return time.Now().Unix() }

func (s *Store) sealJSON(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return s.box.Seal(b)
}

func (s *Store) openJSON(data []byte, v any) error {
	if len(data) == 0 {
		return nil
	}
	plain, err := s.box.Open(data)
	if err != nil {
		return err
	}
	return json.Unmarshal(plain, v)
}
