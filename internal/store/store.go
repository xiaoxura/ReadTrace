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
	seq              INTEGER NOT NULL DEFAULT -1,
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
	max_retries      INTEGER NOT NULL DEFAULT 0,
	retry_delay      INTEGER NOT NULL DEFAULT 15,
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
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	name          TEXT NOT NULL,
	type          TEXT NOT NULL,
	config_enc    BLOB,
	templates_enc BLOB,
	enabled       INTEGER NOT NULL DEFAULT 1,
	created_at    INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS bot_logins (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	bot_id          TEXT NOT NULL DEFAULT '',
	user_id         TEXT NOT NULL DEFAULT '',
	token_enc       BLOB,
	base_url        TEXT NOT NULL DEFAULT 'https://ilinkai.weixin.qq.com',
	get_updates_buf BLOB,
	updated_at      INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS bot_users (
	user_id    TEXT PRIMARY KEY,
	is_admin   INTEGER NOT NULL DEFAULT 0,
	first_seen INTEGER NOT NULL,
	last_seen  INTEGER NOT NULL
);

-- One account has exactly one owner WeChat user; a user may own many.
CREATE TABLE IF NOT EXISTS bot_bindings (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id    TEXT NOT NULL,
	account_id INTEGER NOT NULL UNIQUE,
	created_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_bot_bindings_user ON bot_bindings(user_id);

-- 用户与机器人之间的往来消息（对话记录面板用）。
CREATE TABLE IF NOT EXISTS bot_messages (
	id        INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id   TEXT NOT NULL,
	direction TEXT NOT NULL DEFAULT 'in', -- in = 用户发出, out = 机器人回复
	kind      TEXT NOT NULL DEFAULT 'text',
	content   TEXT NOT NULL DEFAULT '',
	msg_ref   TEXT NOT NULL DEFAULT '',
	ts        INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_bot_messages_user ON bot_messages(user_id, id);

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
	migrate(db)
	return &Store{db: db, box: box}, nil
}

// migrate adjusts databases created by older versions in place. Each ALTER
// only succeeds once; on a fresh database the columns already exist in the
// schema and the "duplicate column" error is ignored. Backfills rely on the
// ALTER being the moment the column appears, so old data gets sane values.
func migrate(db *sql.DB) {
	// 每个账号的阅读重试配置。retry_delay 列先建立，backfill 才能生效。
	if _, err := db.Exec(`ALTER TABLE accounts ADD COLUMN retry_delay INTEGER NOT NULL DEFAULT 15`); err != nil {
		_ = err
	}
	if _, err := db.Exec(`ALTER TABLE accounts ADD COLUMN max_retries INTEGER NOT NULL DEFAULT 0`); err == nil {
		// 老库迁移：保证已有账号的重试间隔不为 0（避免失败后立即重试）。
		_, _ = db.Exec(`UPDATE accounts SET retry_delay = 15`)
	}
	// 本项目生成的展示 ID：按创建顺序从 0 开始。-1 表示「尚未分配」，让
	// 列默认值不会把 MAX(seq)+1 的首个分配从 0 提前到 1。
	if _, err := db.Exec(`ALTER TABLE accounts ADD COLUMN seq INTEGER NOT NULL DEFAULT -1`); err == nil {
		_, _ = db.Exec(`UPDATE accounts SET seq = CAST(id - 1 AS INTEGER)`)
	}
	// 每个推送渠道可自定义各事件类型的消息模板。
	if _, err := db.Exec(`ALTER TABLE push_channels ADD COLUMN templates_enc BLOB`); err != nil {
		_ = err
	}
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
