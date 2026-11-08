// Package store persists accounts, runs, schedules and settings in SQLite.
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

-- 随机轮换用的书池与章节池（多账号：account_id=0 表示全账号共享，
-- account_id=N 表示该账号专属）。pool_items 只保存用户条目，内置 12 本书/
-- 16 章节 16 作为引擎兜底由 weread 包级常量承担。
-- sort_order：书籍在书池中的显示与「顺序书单」阅读顺序（0 = 按 id 兜底）。
CREATE TABLE IF NOT EXISTS pool_items (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	kind        TEXT NOT NULL CHECK (kind IN ('book','chapter')),
	value       TEXT NOT NULL,
	title       TEXT NOT NULL DEFAULT '',
	author      TEXT NOT NULL DEFAULT '',
	cover       TEXT NOT NULL DEFAULT '',
	book_id     TEXT NOT NULL DEFAULT '',
	account_id  INTEGER NOT NULL DEFAULT 0,
	enabled     INTEGER NOT NULL DEFAULT 1,
	sort_order  INTEGER NOT NULL DEFAULT 0,
	word_count  INTEGER NOT NULL DEFAULT 0,
	created_at  INTEGER NOT NULL,
	UNIQUE(kind, value, account_id)
);

-- 微信机器人渠道已移除：旧库中的遗留表在此清理。
DROP TABLE IF EXISTS bot_logins;
DROP TABLE IF EXISTS bot_users;
DROP TABLE IF EXISTS bot_bindings;
DROP TABLE IF EXISTS bot_messages;

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
	s := &Store{db: db, box: box}
	return s, nil
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
	// 稳定模板模式已移除：旧账号统一归入顺序书单。
	_, _ = db.Exec(`UPDATE accounts SET mode = 'sequence' WHERE mode = 'stable' OR mode = ''`)
	// 每个推送渠道可自定义各事件类型的消息模板。
	if _, err := db.Exec(`ALTER TABLE push_channels ADD COLUMN templates_enc BLOB`); err != nil {
		_ = err
	}
	// 书池的展示元数据与章节归属：kind=book 行存 author/cover；kind=chapter
	// 行存 book_id 指向所属书籍的 value，供「点进书籍看章节」与按书抽取。
	for _, col := range []string{
		`ALTER TABLE pool_items ADD COLUMN author TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE pool_items ADD COLUMN cover TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE pool_items ADD COLUMN book_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE pool_items ADD COLUMN sort_order INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE pool_items ADD COLUMN word_count INTEGER NOT NULL DEFAULT 0`,
	} {
		if _, err := db.Exec(col); err != nil {
			_ = err
		}
	}
	// 内置 12 本书/16 章节已彻底移除：书池不再由程序播种，这里不再有兜底
	// 常量需要清理。
	// "mpbook" 是微信读书的「文章收藏」合集虚拟书，没法按书阅读，迁移时清理。
	_, _ = db.Exec(`DELETE FROM pool_items WHERE value = 'mpbook'`)
	_, _ = db.Exec(`DELETE FROM settings WHERE key = 'pool_seeded_v1'`)
	// 书池多账号化：account_id（0=共享）。老库还有 UNIQUE(kind, value) 约束，
	// 同一本书无法在不同账号各有一条，需要重建表改唯一键。
	if _, err := db.Exec(`ALTER TABLE pool_items ADD COLUMN account_id INTEGER NOT NULL DEFAULT 0`); err != nil {
		_ = err
	}
	rebuildPoolUniqueKey(db)
	// 共享池已移除：account_id=0 的历史条目归属到第一个已绑定账号。
	_, _ = db.Exec(`UPDATE pool_items SET account_id = (SELECT MIN(id) FROM accounts)
		WHERE account_id = 0 AND EXISTS (SELECT 1 FROM accounts)`)
}

// rebuildPoolUniqueKey 把 pool_items 的 UNIQUE(kind, value) 升级为
// UNION(kind, value, account_id)。通过检查旧表 DDL 判断是否已经迁移过，
// 避免每次重启都要重建表。
func rebuildPoolUniqueKey(db *sql.DB) {
	var ddl string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='pool_items'`).Scan(&ddl); err != nil {
		return
	}
	if strings.Contains(strings.ToLower(ddl), "account_id") {
		return // 已经是多账号版本
	}
	tx, err := db.Begin()
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback() }()
	steps := []string{
		`CREATE TABLE pool_items_new (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			kind        TEXT NOT NULL CHECK (kind IN ('book','chapter')),
			value       TEXT NOT NULL,
			title       TEXT NOT NULL DEFAULT '',
			author      TEXT NOT NULL DEFAULT '',
			cover       TEXT NOT NULL DEFAULT '',
			book_id     TEXT NOT NULL DEFAULT '',
			account_id  INTEGER NOT NULL DEFAULT 0,
			enabled     INTEGER NOT NULL DEFAULT 1,
			sort_order  INTEGER NOT NULL DEFAULT 0,
			word_count  INTEGER NOT NULL DEFAULT 0,
			created_at  INTEGER NOT NULL,
			UNIQUE(kind, value, account_id)
		)`,
		`INSERT OR IGNORE INTO pool_items_new
			(id, kind, value, title, author, cover, book_id, account_id, enabled, sort_order, word_count, created_at)
		 SELECT id, kind, value, title, author, cover, book_id, COALESCE(account_id, 0), enabled, 0, 0, created_at
		 FROM pool_items`,
		`DROP TABLE pool_items`,
		`ALTER TABLE pool_items_new RENAME TO pool_items`,
	}
	for _, stmt := range steps {
		if _, err := tx.Exec(stmt); err != nil {
			return
		}
	}
	_ = tx.Commit()
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
