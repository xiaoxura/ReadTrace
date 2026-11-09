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
	daily_goal_minutes INTEGER NOT NULL DEFAULT 0,
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
	-- last_progress_at：最后一次心跳进度落库时刻。跨日停机时用它结算被
	-- 打断的运行（finished_at），避免把整段停机时间算进阅读时段。
	last_progress_at INTEGER NOT NULL DEFAULT 0,
	finished_at      INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS run_logs (
	id      INTEGER PRIMARY KEY AUTOINCREMENT,
	run_id  INTEGER NOT NULL,
	ts      INTEGER NOT NULL,
	level   TEXT NOT NULL,
	message TEXT NOT NULL
);

-- 清理运行详情后保留统计事实。run_id 与仍在 runs 中的记录互斥。
CREATE TABLE IF NOT EXISTS run_statistics (
	run_id           INTEGER PRIMARY KEY,
	account_id       INTEGER NOT NULL,
	started_at       INTEGER NOT NULL,
	finished_at      INTEGER NOT NULL,
	elapsed_seconds  INTEGER NOT NULL
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
	chapter_index  INTEGER NOT NULL DEFAULT 0,
	created_at  INTEGER NOT NULL,
	-- 章节唯一键含 book_id：上传书（CB_）的 chapterUid 是每书从 1 开始的
	-- 小序号（官方心跳用 b+c 双键区分书），跨书同值是合法数据。
	UNIQUE(kind, value, book_id, account_id)
);

-- 微信机器人渠道已移除：旧库中的遗留表在此清理。
DROP TABLE IF EXISTS bot_logins;
DROP TABLE IF EXISTS bot_users;
DROP TABLE IF EXISTS bot_bindings;
DROP TABLE IF EXISTS bot_messages;

CREATE INDEX IF NOT EXISTS idx_runs_account_started ON runs(account_id, started_at);
CREATE INDEX IF NOT EXISTS idx_runs_started ON runs(started_at);
CREATE INDEX IF NOT EXISTS idx_run_statistics_account_started ON run_statistics(account_id, started_at);
CREATE INDEX IF NOT EXISTS idx_run_logs_run ON run_logs(run_id, id);
CREATE INDEX IF NOT EXISTS idx_schedules_account ON schedules(account_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at);
`

// Open creates/opens the database and applies the schema.
func Open(dbPath string, box *secure.Box) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return nil, err
	}
	// sql.Open does not create the file. Create it with private permissions
	// before SQLite opens it, including WAL side files in an existing data dir.
	for _, path := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if err := ensurePrivateDBFile(path); err != nil {
			return nil, fmt.Errorf("设置数据库文件权限失败 (%s): %w", path, err)
		}
	}
	dsn := "file:" + filepath.ToSlash(dbPath) +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// A single connection keeps writes serialized and avoids SQLITE_BUSY on
	// platforms where WAL locking is less forgiving.
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(0)

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("初始化数据库失败: %w", err)
	}
	// SQLite may replace/remove and recreate its side files while switching to
	// WAL. Check permissions again after the first real connection.
	for _, path := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if err := chmodExistingDBFile(path); err != nil {
			db.Close()
			return nil, fmt.Errorf("设置数据库文件权限失败 (%s): %w", path, err)
		}
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("迁移数据库失败: %w", err)
	}
	s := &Store{db: db, box: box}
	return s, nil
}

func ensurePrivateDBFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func chmodExistingDBFile(path string) error {
	err := os.Chmod(path, 0o600)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// migrate adjusts databases created by older versions in one transaction.
// Columns are added only when absent, and unexpected errors stop startup.
func migrate(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	goalAdded, err := addColumnIfMissing(tx, "accounts", "daily_goal_minutes", `INTEGER NOT NULL DEFAULT 0`)
	if err != nil {
		return err
	}
	if goalAdded {
		if _, err := tx.Exec(`UPDATE accounts SET daily_goal_minutes = CASE
			WHEN default_minutes BETWEEN 1 AND 1440 THEN default_minutes ELSE 30 END`); err != nil {
			return err
		}
	}
	// 每个账号的阅读重试配置。retry_delay 列先建立，backfill 才能生效。
	if _, err := addColumnIfMissing(tx, "accounts", "retry_delay", `INTEGER NOT NULL DEFAULT 15`); err != nil {
		return err
	}
	maxRetriesAdded, err := addColumnIfMissing(tx, "accounts", "max_retries", `INTEGER NOT NULL DEFAULT 0`)
	if err != nil {
		return err
	}
	if maxRetriesAdded {
		// 老库迁移：保证已有账号的重试间隔不为 0（避免失败后立即重试）。
		if _, err := tx.Exec(`UPDATE accounts SET retry_delay = 15`); err != nil {
			return err
		}
	}
	// 本项目生成的展示 ID：按创建顺序从 0 开始。-1 表示「尚未分配」，让
	// 列默认值不会把 MAX(seq)+1 的首个分配从 0 提前到 1。
	seqAdded, err := addColumnIfMissing(tx, "accounts", "seq", `INTEGER NOT NULL DEFAULT -1`)
	if err != nil {
		return err
	}
	if seqAdded {
		if _, err := tx.Exec(`UPDATE accounts SET seq = CAST(id - 1 AS INTEGER)`); err != nil {
			return err
		}
	}
	// 稳定模板模式已移除：旧账号统一归入顺序书单。
	if _, err := tx.Exec(`UPDATE accounts SET mode = 'sequence' WHERE mode = 'stable' OR mode = ''`); err != nil {
		return err
	}
	// 每个推送渠道可自定义各事件类型的消息模板。
	if _, err := addColumnIfMissing(tx, "push_channels", "templates_enc", `BLOB`); err != nil {
		return err
	}
	// 书池的展示元数据与章节归属：kind=book 行存 author/cover；kind=chapter
	// 行存 book_id 指向所属书籍的 value，供「点进书籍看章节」与按书抽取。
	for _, col := range []struct{ name, definition string }{
		{"author", `TEXT NOT NULL DEFAULT ''`},
		{"cover", `TEXT NOT NULL DEFAULT ''`},
		{"book_id", `TEXT NOT NULL DEFAULT ''`},
		{"sort_order", `INTEGER NOT NULL DEFAULT 0`},
		{"word_count", `INTEGER NOT NULL DEFAULT 0`},
	} {
		if _, err := addColumnIfMissing(tx, "pool_items", col.name, col.definition); err != nil {
			return err
		}
	}
	// 内置 12 本书/16 章节已彻底移除：书池不再由程序播种，这里不再有兜底
	// 常量需要清理。
	// "mpbook" 是微信读书的「文章收藏」合集虚拟书，没法按书阅读，迁移时清理。
	if _, err := tx.Exec(`DELETE FROM pool_items WHERE value = 'mpbook'`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM settings WHERE key = 'pool_seeded_v1'`); err != nil {
		return err
	}
	// 书池多账号化：account_id（0=共享）。老库还有 UNIQUE(kind, value) 约束，
	// 同一本书无法在不同账号各有一条，需要重建表改唯一键。
	if _, err := addColumnIfMissing(tx, "pool_items", "account_id", `INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	// 心跳 payload 的 ci（章节下标）：导入目录时随 word_count 一起保存。
	// 必须在唯一键重建之前补列，重建 INSERT 才能带上它。
	if _, err := addColumnIfMissing(tx, "pool_items", "chapter_index", `INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	// 跨日停机结算（缺陷 07）：补 runs.last_progress_at，供重启打断旧运行时
	// 按最后活动时刻写入 finished_at。老库用 ensure 列模式补齐。
	if _, err := addColumnIfMissing(tx, "runs", "last_progress_at", `INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if err := rebuildPoolUniqueKey(tx); err != nil {
		return err
	}
	// 书池查询的主力索引：PoolItemsForAccount / PoolChaptersPage（COUNT+LIMIT）
	// / PoolChapterCountsForAccount / AddChapters 去重都按 (account_id, kind) 起
	// 步，章节还带 book_id 过滤。必须建在表重建之后——上面的 DROP TABLE 会把
	// 建在它之前（schema 常量里）的同名索引一并删掉，导致升级后的本次进程
	// 缺索引（缺陷 20）。IF NOT EXISTS 幂等：未重建的库已有时零开销。
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_pool_account_kind_book ON pool_items(account_id, kind, book_id)`); err != nil {
		return err
	}
	// 共享池已移除：account_id=0 的历史条目归属到第一个已绑定账号。
	if err := migrateSharedPoolOwnership(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// migrateSharedPoolOwnership 把 account_id=0 的共享池遗留条目归属到第一个已
// 绑定账号。无账号的老库在这里跳过，首账号创建时由 CreateAccount 再次调用
// adoptSharedPoolOwnership 补做（缺陷 24），见 adoptSharedPoolOwnership 的注释。
func migrateSharedPoolOwnership(tx *sql.Tx) error {
	var firstAccount sql.NullInt64
	if err := tx.QueryRow(`SELECT MIN(id) FROM accounts`).Scan(&firstAccount); err != nil {
		return err
	}
	if !firstAccount.Valid {
		return nil
	}
	return adoptSharedPoolOwnership(tx, firstAccount.Int64)
}

// adoptSharedPoolOwnership 逐行处置 target 账号名下的共享池遗留行
// （pool_items.account_id=0），合并与改归属都在本事务内完成：
//
//  1. 与 target 已有专属行 (kind, value, book_id) 三键全等 → 视为同一章节/
//     条目的重复：专属行保留 ID/enabled/sort 等参与状态，空元数据由共享
//     行补齐，随后删除共享行。注意 book_id 必须全等：老共享行与某账号专属
//     行 value 相同但 book_id 不同（两本书复用同一章节 ID，唯一键含 book_id
//     的合法数据）不是重复，绝不能在此合并删除（缺陷 02 曾导致另一本书的
//     章节被误删）。
//  2. 没有同键专属行 → 只改归属（UPDATE account_id）。
//
// 逐行「先查后改」而不是一条 `UPDATE ... WHERE account_id=0` 全量翻转：
// 全量翻转在唯一键 UNIQUE(kind, value, book_id, account_id) 下可能撞键，让
// 迁移启动失败；逐行路径保证任何一步都不会撞键，绝不因撞键让迁移失败。
func adoptSharedPoolOwnership(tx *sql.Tx, target int64) error {
	if target <= 0 {
		return nil
	}
	for {
		// 每次取最早的一条共享行处置完再取下一条；共享行被合并删除或改
		// 归属后便退出候选，循环必然收敛。
		var id int64
		var kind, value, bookID, title, author, cover string
		var wordCount int64
		err := tx.QueryRow(`SELECT id, kind, value, book_id, title, author, cover, word_count
			FROM pool_items WHERE account_id = 0 ORDER BY id LIMIT 1`).
			Scan(&id, &kind, &value, &bookID, &title, &author, &cover, &wordCount)
		if err == sql.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		var ownedID int64
		err = tx.QueryRow(`SELECT id FROM pool_items
			WHERE kind = ? AND value = ? AND book_id = ? AND account_id = ?`,
			kind, value, bookID, target).Scan(&ownedID)
		switch {
		case err == sql.ErrNoRows:
			// 没有同键专属行：整体改归属安置这条共享行。
			res, uerr := tx.Exec(`UPDATE pool_items SET account_id = ? WHERE id = ? AND account_id = 0`,
				target, id)
			if uerr != nil {
				return fmt.Errorf("迁移共享书池条目 %d 归属失败: %w", id, uerr)
			}
			if n, nerr := res.RowsAffected(); nerr == nil && n == 0 {
				// 选中的行刚被消失属于异常状态，报告而不是空转。
				return fmt.Errorf("迁移共享书池条目 %d 归属失败: 条目消失", id)
			}
		case err != nil:
			return err
		default:
			// 有同键专属行：合并元数据（只填空字段）后删除共享行，专属行
			// 的参与状态与排序保持不动。
			if _, uerr := tx.Exec(`UPDATE pool_items SET
				title = CASE WHEN title = '' THEN ? ELSE title END,
				author = CASE WHEN author = '' THEN ? ELSE author END,
				cover = CASE WHEN cover = '' THEN ? ELSE cover END,
				book_id = CASE WHEN book_id = '' THEN ? ELSE book_id END,
				word_count = CASE WHEN word_count <= 0 THEN ? ELSE word_count END
				WHERE id = ?`, title, author, cover, bookID, wordCount, ownedID); uerr != nil {
				return fmt.Errorf("合并共享书池条目 %d 失败: %w", id, uerr)
			}
			if _, derr := tx.Exec(`DELETE FROM pool_items WHERE id = ? AND account_id = 0`, id); derr != nil {
				return fmt.Errorf("删除已合并共享书池条目 %d 失败: %w", id, derr)
			}
		}
	}
}

func addColumnIfMissing(tx *sql.Tx, table, column, definition string) (bool, error) {
	rows, err := tx.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false, fmt.Errorf("检查 %s.%s 列失败: %w", table, column, err)
	}
	found, count := false, 0
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			_ = rows.Close()
			return false, err
		}
		count++
		found = found || strings.EqualFold(name, column)
	}
	err = rows.Err()
	if closeErr := rows.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return false, fmt.Errorf("检查 %s.%s 列失败: %w", table, column, err)
	}
	if count == 0 {
		return false, fmt.Errorf("迁移缺少表 %s", table)
	}
	if found {
		return false, nil
	}
	if _, err := tx.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + column + ` ` + definition); err != nil {
		return false, fmt.Errorf("添加 %s.%s 列失败: %w", table, column, err)
	}
	return true, nil
}

// rebuildPoolUniqueKey replaces legacy unique constraints with the account-
// scoped key. Inspect actual indexes because the old table can already have an
// account_id column while still enforcing UNIQUE(kind, value).
func rebuildPoolUniqueKey(tx *sql.Tx) error {
	indexes, err := tx.Query(`PRAGMA index_list(pool_items)`)
	if err != nil {
		return err
	}
	type uniqueIndex struct {
		name    string
		partial bool
	}
	unique := []uniqueIndex{}
	for indexes.Next() {
		var seq, isUnique, partial int
		var name, origin string
		if err := indexes.Scan(&seq, &name, &isUnique, &origin, &partial); err != nil {
			_ = indexes.Close()
			return err
		}
		if isUnique != 0 {
			unique = append(unique, uniqueIndex{name: name, partial: partial != 0})
		}
	}
	err = indexes.Err()
	if closeErr := indexes.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	hasExpected, hasOther := false, false
	for _, index := range unique {
		quoted := `"` + strings.ReplaceAll(index.name, `"`, `""`) + `"`
		rows, err := tx.Query(`PRAGMA index_info(` + quoted + `)`)
		if err != nil {
			return err
		}
		columns := []string{}
		for rows.Next() {
			var seq, cid int
			var name sql.NullString
			if err := rows.Scan(&seq, &cid, &name); err != nil {
				_ = rows.Close()
				return err
			}
			columns = append(columns, name.String)
		}
		err = rows.Err()
		if closeErr := rows.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
		if !index.partial && len(columns) == 4 && columns[0] == "kind" && columns[1] == "value" && columns[2] == "book_id" && columns[3] == "account_id" {
			hasExpected = true
		} else {
			hasOther = true
		}
	}
	if hasExpected && !hasOther {
		return nil
	}
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
			chapter_index INTEGER NOT NULL DEFAULT 0,
			created_at  INTEGER NOT NULL,
			UNIQUE(kind, value, book_id, account_id)
		)`,
		`INSERT INTO pool_items_new
			(id, kind, value, title, author, cover, book_id, account_id, enabled, sort_order, word_count, chapter_index, created_at)
		 SELECT id, kind, value, title, author, cover, book_id, account_id, enabled, sort_order, word_count, chapter_index, created_at
		 FROM pool_items`,
		`DROP TABLE pool_items`,
		`ALTER TABLE pool_items_new RENAME TO pool_items`,
	}
	for _, stmt := range steps {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("重建书池唯一键失败: %w", err)
		}
	}
	return nil
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
