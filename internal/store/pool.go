package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// pool_items 只保存用户自己管理（书架导入/手动添加）的书与章节，按 account_id
// 归属到某个账号（共享池已移除）。引擎抽取从该账号的条目出发；池为空时启动
// 直接报错提示导入。

// fallbackAccountID 返回第一个已绑定账号的 ID（旧“共享池”条目的落点）。
func (s *Store) fallbackAccountID() int64 {
	var first int64
	_ = s.db.QueryRow(`SELECT MIN(id) FROM accounts`).Scan(&first)
	return first
}

// 书池/章节池条目的两类 kind。
const (
	KindBook    = "book"
	KindChapter = "chapter"
)

// KeyDefaultReadPayload 是历史遗留的全局默认 read 模板设置键（缺陷 13）：
// 模板契约已收敛为「每个账号保存自己的专属模板」，引擎不再回落该全局
// 设置，读写路径（Get/SetDefaultTemplate 与 PUT /api/pool/template）均已
// 删除。旧库 settings 表里已有的 read_default_payload 行按约定保留不读不
// 写（不迁移也不删除），既避免旧数据以额外来源复活，也给回滚留出路径。
const KeyDefaultReadPayload = "read_default_payload"

// SharedAccountID 保留为 0 的历史值：共享池已移除，所有条目都归属某个账号，
// account_id<=0 的写入会回落到第一个已绑定账号。
const SharedAccountID int64 = 0

// PoolItem is one configurable entry of the user's reading pool.
// AccountID=0 表示全账号共享，>0 表示该账号专属。
// kind=book 行的 Author/Cover 用于书架式展示；kind=chapter 行的 BookID 记录
// 所属书籍的 value（bookId），供按书查看与按书抽取。
type PoolItem struct {
	ID           int64  `json:"id"`
	Kind         string `json:"kind"`
	Value        string `json:"value"`
	Title        string `json:"title"`
	Author       string `json:"author,omitempty"`
	Cover        string `json:"cover,omitempty"`
	BookID       string `json:"book_id,omitempty"`
	AccountID    int64  `json:"account_id"`
	Enabled      bool   `json:"enabled"`
	SortOrder    int    `json:"sort_order"`
	WordCount    int    `json:"word_count"`
	ChapterIndex int    `json:"chapter_index"`
}

func scanPoolItems(rows *sql.Rows) ([]PoolItem, error) {
	out := []PoolItem{}
	for rows.Next() {
		var it PoolItem
		var enabled, sortOrder, wordCount, chapterIndex int
		if err := rows.Scan(&it.ID, &it.Kind, &it.Value, &it.Title, &it.Author, &it.Cover, &it.BookID, &it.AccountID, &enabled, &sortOrder, &wordCount, &chapterIndex); err != nil {
			return nil, err
		}
		it.Enabled = enabled != 0
		it.SortOrder = sortOrder
		it.WordCount = wordCount
		it.ChapterIndex = chapterIndex
		out = append(out, it)
	}
	return out, rows.Err()
}

const poolSelectCols = `SELECT id, kind, value, title, author, cover, book_id, account_id, enabled, sort_order, word_count, chapter_index FROM pool_items`

// PoolItems lists every user entry of the given kind (all accounts), oldest first.
func (s *Store) PoolItems(kind string) ([]PoolItem, error) {
	if kind != KindBook && kind != KindChapter {
		return nil, fmt.Errorf("未知池类型 %q", kind)
	}
	rows, err := s.db.Query(poolSelectCols+` WHERE kind = ? ORDER BY id`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPoolItems(rows)
}

// PoolItemsForAccount 视角查询：accountID <= 0 时回落到第一个账号；返回的
// 都是「该账号专属」的条目（共享池已移除），按 sort_order、id 排序。
func (s *Store) PoolItemsForAccount(kind string, accountID int64) ([]PoolItem, error) {
	if kind != KindBook && kind != KindChapter {
		return nil, fmt.Errorf("未知池类型 %q", kind)
	}
	if accountID <= 0 {
		accountID = s.fallbackAccountID()
		if accountID <= 0 {
			return []PoolItem{}, nil // 尚无账号：书池必空
		}
	}
	rows, err := s.db.Query(poolSelectCols+` WHERE kind = ? AND account_id = ? ORDER BY sort_order, id`, kind, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPoolItems(rows)
}

// PoolChapterCountsForAccount 统计某账号书池的章节数（只统计 enabled=1 的行）。
func (s *Store) PoolChapterCountsForAccount(accountID int64) (map[string]int, error) {
	if accountID <= 0 {
		accountID = s.fallbackAccountID()
	}
	if accountID <= 0 {
		return map[string]int{}, nil
	}
	q := `SELECT book_id, COUNT(*) FROM pool_items
		WHERE kind = '` + KindChapter + `' AND enabled = 1 AND account_id = ? GROUP BY book_id`
	args := []any{accountID}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var bookID string
		var n int
		if err := rows.Scan(&bookID, &n); err != nil {
			return nil, err
		}
		out[bookID] = n
	}
	return out, rows.Err()
}

// PoolChaptersOf returns the pool chapters belonging to one book
// （accountID<=0 回落到第一个账号）。
func (s *Store) PoolChaptersOf(bookValue string, accountID int64) ([]PoolItem, error) {
	if accountID <= 0 {
		accountID = s.fallbackAccountID()
	}
	if accountID <= 0 {
		return []PoolItem{}, nil
	}
	rows, err := s.db.Query(poolSelectCols+` WHERE kind = ? AND book_id = ? AND account_id = ? ORDER BY sort_order, id`,
		KindChapter, bookValue, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPoolItems(rows)
}

// PoolChaptersPage returns one page of a book's chapter pool plus total and
// enabled counts. Pagination keeps multi-thousand-chapter books fast to open.
func (s *Store) PoolChaptersPage(bookValue string, accountID int64, limit, offset int) ([]PoolItem, int, int, error) {
	if accountID <= 0 {
		accountID = s.fallbackAccountID()
	}
	if accountID <= 0 {
		return []PoolItem{}, 0, 0, nil
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	var total, enabled int
	if err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(enabled), 0)
		FROM pool_items WHERE kind = ? AND book_id = ? AND account_id = ?`,
		KindChapter, bookValue, accountID).Scan(&total, &enabled); err != nil {
		return nil, 0, 0, err
	}
	rows, err := s.db.Query(poolSelectCols+` WHERE kind = ? AND book_id = ? AND account_id = ?
		ORDER BY sort_order, id LIMIT ? OFFSET ?`,
		KindChapter, bookValue, accountID, limit, offset)
	if err != nil {
		return nil, 0, 0, err
	}
	defer rows.Close()
	items, err := scanPoolItems(rows)
	if err != nil {
		return nil, 0, 0, err
	}
	return items, total, enabled, nil
}

// ErrPoolItemExists is returned when the same value is added twice (same
// kind/value/account tuple).
var ErrPoolItemExists = errors.New("该 ID 已在池中")

// ErrChapterConflict 历史遗留：章节唯一键放宽到 (kind, value, book_id,
// account) 之后，跨书同章节 ID 属于合法数据（上传书 CB_ 的 chapterUid 是
// 每书小编号），这类行不会再被误判；保留错误常量以兼容既有调用方。
var ErrChapterConflict = errors.New("章节 ID 已归属其他书籍")

// AddPoolItem inserts a new pool entry and returns its row ID. accountID<=0 → shared.
func (s *Store) AddPoolItem(kind, value, title string, accountID int64) (int64, error) {
	switch {
	case kind != KindBook && kind != KindChapter:
		return 0, fmt.Errorf("未知池类型 %q", kind)
	case strings.TrimSpace(value) == "":
		return 0, errors.New("ID 不能为空")
	}
	value, title = strings.TrimSpace(value), strings.TrimSpace(title)
	if accountID <= 0 {
		accountID = s.fallbackAccountID()
	}
	if accountID <= 0 {
		return 0, errors.New("请先绑定微信读书账号，再导入书籍")
	}
	var exist int64
	err := s.db.QueryRow(`SELECT id FROM pool_items WHERE kind = ? AND value = ? AND account_id = ?`,
		kind, value, accountID).Scan(&exist)
	if err == nil {
		return 0, ErrPoolItemExists
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	res, err := s.db.Exec(`INSERT INTO pool_items (kind, value, title, account_id, enabled, created_at)
		VALUES (?, ?, ?, ?, 1, ?)`, kind, value, title, accountID, now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// AddChapterItem inserts a chapter row linked to its book's value in the
// account scope. wordCount 记录章节字数，顺序书单用它折算阅读进度。
func (s *Store) AddChapterItem(accountID int64, bookValue, value, title string, wordCount int) (int64, error) {
	if strings.TrimSpace(bookValue) == "" || strings.TrimSpace(value) == "" {
		return 0, errors.New("章节与所属书籍 ID 不能为空")
	}
	if accountID <= 0 {
		accountID = s.fallbackAccountID()
	}
	if accountID <= 0 {
		return 0, errors.New("请先绑定微信读书账号，再导入章节")
	}
	value, title, bookValue = strings.TrimSpace(value), strings.TrimSpace(title), strings.TrimSpace(bookValue)
	// 查重分两步，语义不同、顺序固定：
	//  1. 先按目标书键 (kind, value, book_id, account) 查既有行——命中说明
	//     本书已有这条章节，属于重复导入，直接拒绝。
	//  2. 目标书键没有时，再查同 (kind, value, account) 但 book_id='' 的
	//     无归属遗留行（老的手动导入章节没有 book 关联）。存在则就地挂到
	//     本书：UPDATE 补 book_id，只回填空的 title/word_count，enabled 与
	//     sort_order 原样保留（缺陷 12：旧实现查重键含 book_id 导致该分支
	//     不可达，老行成了孤儿，再 INSERT 一行默认启用的副本，丢了用户的
	//     禁用状态）。注意不能与第 1 步合并成一个查询：book_id='' 的行不
	//     能算「本书已有」，否则显式导入会被误判重复而永远挂不上。
	var exist int64
	err := s.db.QueryRow(`SELECT id FROM pool_items WHERE kind = ? AND value = ? AND book_id = ? AND account_id = ?`,
		KindChapter, value, bookValue, accountID).Scan(&exist)
	if err == nil {
		return 0, ErrPoolItemExists
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	var legacyID int64
	err = s.db.QueryRow(`SELECT id FROM pool_items WHERE kind = ? AND value = ? AND book_id = '' AND account_id = ?`,
		KindChapter, value, accountID).Scan(&legacyID)
	if err == nil {
		res, err := s.db.Exec(`UPDATE pool_items SET book_id = ?,
			title = CASE WHEN title = '' THEN ? ELSE title END,
			word_count = CASE WHEN word_count <= 0 THEN ? ELSE word_count END
			WHERE id = ? AND book_id = ''`, bookValue, title, wordCount, legacyID)
		if err != nil {
			return 0, err
		}
		if n, err := res.RowsAffected(); err != nil {
			return 0, err
		} else if n > 0 {
			return legacyID, nil
		}
		return 0, ErrPoolItemExists
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	res, err := s.db.Exec(`INSERT INTO pool_items (kind, value, title, book_id, account_id, enabled, word_count, created_at)
		VALUES (?, ?, ?, ?, ?, 1, ?, ?)`, KindChapter, value, title, bookValue, accountID, wordCount, now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// NewChapter is one filtered TOC chapter handed to AddChapters.
type NewChapter struct {
	Value        string // 章节 ID（编码后的 chapterId）
	Title        string // 完整标题（「书名 · 章节名」由调用方拼好）
	WordCount    int
	ChapterIndex int // 官方目录的章节下标（chapterIdx），心跳 payload 的 ci 用
}

// AddChapters writes many chapters of one book into the account's chapter
// pool within a SINGLE transaction. 批量导入一本几千章的书时逐条 autocommit
// 会把写入放大成几千个写事务（单连接 SQLite 上还会堵住心跳与日志写入）；
// 这里用预编译语句 + 单事务，一次提交完成整本。返回值：
//   - added：新插入（含旧数据修复）的行数；
//   - existed：已存在且本就归属本书的行数（真正的重复导入）；
//   - conflicts：章节 ID 已存在但归属其他书的行数——这不是「已在池」，
//     上传书（CB_）目录复用时会大量出现，调用方必须单独呈现，否则用户
//     会误以为导入成功而书籍 0 章。
func (s *Store) AddChapters(accountID int64, bookValue string, chs []NewChapter) (added, existed, conflicts int, err error) {
	if strings.TrimSpace(bookValue) == "" {
		return 0, 0, 0, errors.New("所属书籍 ID 不能为空")
	}
	if accountID <= 0 {
		accountID = s.fallbackAccountID()
	}
	if accountID <= 0 {
		return 0, 0, 0, errors.New("请先绑定微信读书账号，再导入章节")
	}
	bookValue = strings.TrimSpace(bookValue)
	if len(chs) == 0 {
		return 0, 0, 0, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, 0, 0, err
	}
	defer tx.Rollback()
	sel, err := tx.Prepare(`SELECT id, book_id FROM pool_items WHERE kind = 'chapter' AND value = ? AND book_id = ? AND account_id = ?`)
	if err != nil {
		return 0, 0, 0, err
	}
	defer sel.Close()
	// 无归属遗留行的查重语句：与逐条入口同构——先查目标书键（sel），再查
	// book_id='' 的老行（selLegacy）。老行是在本书键没有时的下一步，不能
	// 归入「本书已有」（缺陷 12：否则显式导入被误判重复、老行永远挂不上，
	// 还会新插一行默认启用的副本丢掉禁用状态）。
	selLegacy, err := tx.Prepare(`SELECT id FROM pool_items WHERE kind = 'chapter' AND value = ? AND book_id = '' AND account_id = ?`)
	if err != nil {
		return 0, 0, 0, err
	}
	defer selLegacy.Close()
	repair, err := tx.Prepare(`UPDATE pool_items SET book_id = ?,
		title = CASE WHEN title = '' THEN ? ELSE title END,
		word_count = CASE WHEN word_count <= 0 THEN ? ELSE word_count END,
		chapter_index = CASE WHEN chapter_index = 0 THEN ? ELSE chapter_index END
		WHERE id = ? AND book_id = ''`)
	if err != nil {
		return 0, 0, 0, err
	}
	defer repair.Close()
	ins, err := tx.Prepare(`INSERT INTO pool_items (kind, value, title, book_id, account_id, enabled, word_count, chapter_index, created_at)
		VALUES ('chapter', ?, ?, ?, ?, 1, ?, ?, ?)`)
	if err != nil {
		return 0, 0, 0, err
	}
	defer ins.Close()

	for _, ch := range chs {
		value := strings.TrimSpace(ch.Value)
		if value == "" {
			return 0, 0, 0, errors.New("章节 ID 不能为空")
		}
		var exist int64
		var existingBook string
		err := sel.QueryRow(value, bookValue, accountID).Scan(&exist, &existingBook)
		if err == nil {
			// 前一步确认本书已登记该章节（existingBook 必为 bookValue）：
			// 真正的重复导入。
			existed++
			continue
		}
		if err != sql.ErrNoRows {
			return 0, 0, 0, err
		}
		// 本书键没有 → 找无归属遗留行就地挂上，保留 enabled/title/sort 等
		// 元数据与参与状态。
		var legacyID int64
		legacyErr := selLegacy.QueryRow(value, accountID).Scan(&legacyID)
		if legacyErr == sql.ErrNoRows {
			legacyErr = nil
		} else if legacyErr != nil {
			return 0, 0, 0, legacyErr
		}
		if legacyErr == nil {
			res, rerr := repair.Exec(bookValue, ch.Title, ch.WordCount, ch.ChapterIndex, legacyID)
			if rerr != nil {
				return 0, 0, 0, rerr
			}
			n, rerr := res.RowsAffected()
			if rerr != nil {
				return 0, 0, 0, rerr
			}
			if n > 0 {
				added++
				continue
			}
		}
		if _, err := ins.Exec(value, ch.Title, bookValue, accountID, ch.WordCount, ch.ChapterIndex, now()); err != nil {
			return 0, 0, 0, err
		}
		added++
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, 0, err
	}
	return added, existed, conflicts, nil
}

// UpsertBookMeta adds a book row when missing (in the given account scope) and
// refreshes its display metadata either way; metadata fill only targets empty
// fields so user-edited titles survive. Returns the row ID and whether the row
// was newly created.
func (s *Store) UpsertBookMeta(accountID int64, value, title, author, cover string) (int64, bool, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false, errors.New("书籍 ID 不能为空")
	}
	if accountID <= 0 {
		accountID = s.fallbackAccountID()
	}
	if accountID <= 0 {
		return 0, false, errors.New("请先绑定微信读书账号，再导入书籍")
	}
	var exist int64
	err := s.db.QueryRow(`SELECT id FROM pool_items WHERE kind = ? AND value = ? AND account_id = ?`,
		KindBook, value, accountID).Scan(&exist)
	if err == sql.ErrNoRows {
		res, err := s.db.Exec(`INSERT INTO pool_items (kind, value, title, author, cover, account_id, enabled, sort_order, created_at)
			VALUES (?,?,?,?,?,?,1,
				COALESCE((SELECT MAX(sort_order)+1 FROM pool_items WHERE kind=? AND account_id=?), 0), ?)`,
			KindBook, value, strings.TrimSpace(title), strings.TrimSpace(author),
			strings.TrimSpace(cover), accountID, KindBook, accountID, now())
		if err != nil {
			return 0, false, err
		}
		id, _ := res.LastInsertId()
		return id, true, nil
	}
	if err != nil {
		return 0, false, err
	}
	_, err = s.db.Exec(`UPDATE pool_items SET
		title = CASE WHEN title = '' THEN ? ELSE title END,
		author = CASE WHEN author = '' THEN ? ELSE author END,
		cover = CASE WHEN cover = '' THEN ? ELSE cover END
		WHERE id = ?`, strings.TrimSpace(title), strings.TrimSpace(author), strings.TrimSpace(cover), exist)
	if err != nil {
		return 0, false, err
	}
	return exist, false, nil
}

// PoolEnabledValues returns the enabled values of a kind across accounts (kept
// for diagnostics); the engine uses PoolPoolForAccount instead.
func (s *Store) PoolEnabledValues(kind string) ([]string, error) {
	items, err := s.PoolItems(kind)
	if err != nil {
		return nil, err
	}
	vals := make([]string, 0, len(items))
	for _, it := range items {
		if it.Enabled && it.Value != "" {
			vals = append(vals, it.Value)
		}
	}
	return vals, nil
}

// PoolPoolForAccount packs ALL enabled values of a kind for one account into
// the first slice（共享层已移除，只有一个池）。kept as-is
// for call sites that expect the two-slice signature.
func (s *Store) PoolPoolForAccount(kind string, accountID int64) (owned, shared []string, err error) {
	items, err := s.PoolItemsForAccount(kind, accountID)
	if err != nil {
		return nil, nil, err
	}
	for _, it := range items {
		if !it.Enabled || it.Value == "" {
			continue
		}
		owned = append(owned, it.Value)
	}
	return owned, nil, nil
}

// PoolChapterMapForAccount 读出「书 → 章节」映射（该账号自己的池；hook 无归属
// 章节同样返回在 ownedGlobal）。共享层已移除，shared 字段返回空以兼容签名。
func (s *Store) PoolChapterMapForAccount(accountID int64) (ownedByBook, sharedByBook map[string][]string, ownedGlobal, sharedGlobal []string, err error) {
	items, err := s.PoolItemsForAccount(KindChapter, accountID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	ownedByBook, sharedByBook = map[string][]string{}, map[string][]string{}
	for _, it := range items {
		if !it.Enabled || it.Value == "" {
			continue
		}
		if it.BookID != "" {
			ownedByBook[it.BookID] = append(ownedByBook[it.BookID], it.Value)
		} else {
			ownedGlobal = append(ownedGlobal, it.Value)
		}
	}
	return ownedByBook, sharedByBook, ownedGlobal, sharedGlobal, nil
}

// UpdatePoolItem edits value/title/enabled of one entry. Passing nil keeps
// the current value of that field. 书（kind=book）改 value 时在同一事务内把
// 其章节行的 book_id 一起UPDATE 到新值（缺陷 10：旧行为只改书行，章节保留
// 旧 book_id，书变成 0 章、之后删书也找不到旧章节，留下孤儿数据）。
func (s *Store) UpdatePoolItem(id int64, value, title *string, enabled *bool) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var cur PoolItem
	var curEnabled int
	err = tx.QueryRow(`SELECT id, kind, value, title, account_id, enabled FROM pool_items WHERE id = ?`, id).
		Scan(&cur.ID, &cur.Kind, &cur.Value, &cur.Title, &cur.AccountID, &curEnabled)
	if err == sql.ErrNoRows {
		return errors.New("条目不存在")
	}
	if err != nil {
		return err
	}
	updates := map[string]any{}
	if value != nil {
		v := strings.TrimSpace(*value)
		if v == "" {
			return errors.New("ID 不能为空")
		}
		updates["value"] = v
	}
	if title != nil {
		updates["title"] = strings.TrimSpace(*title)
	}
	if enabled != nil {
		updates["enabled"] = *enabled
	}
	// Guard the unique key: the new value must not collide with another row in
	// the same (kind, account) scope.
	var newValue string
	var rebindChapters bool
	if v, has := updates["value"]; has && toPoolString(v) != cur.Value {
		newValue = toPoolString(v)
		var other int64
		err := tx.QueryRow(`SELECT id FROM pool_items WHERE kind = ? AND value = ? AND account_id = ? AND id <> ?`,
			cur.Kind, newValue, cur.AccountID, id).Scan(&other)
		if err == nil {
			return ErrPoolItemExists
		}
		if err != sql.ErrNoRows {
			return err
		}
		// 书改 ID 才需要同步章节外键；章节改 value 不存在归属问题。
		rebindChapters = cur.Kind == KindBook
	}
	if len(updates) == 0 {
		return nil
	}
	var cols []string
	var args []any
	for k, v := range updates {
		cols = append(cols, k+" = ?")
		args = append(args, v)
	}
	args = append(args, id)
	if _, err := tx.Exec(`UPDATE pool_items SET `+strings.Join(cols, ", ")+` WHERE id = ?`, args...); err != nil {
		return err
	}
	if rebindChapters {
		// 章节跟随换书：同账号 (book_id=旧值) 的章节全部指向新书 value。
		// 与书行的更新同一事务——任一步失败整体回滚，不会出现「书改了、
		// 章节还挂在旧书」的中间态。
		if _, err := tx.Exec(`UPDATE pool_items SET book_id = ?
			WHERE kind = ? AND book_id = ? AND account_id = ?`,
			newValue, KindChapter, cur.Value, cur.AccountID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func toPoolString(v any) string {
	s, _ := v.(string)
	return s
}

// DeletePoolItem removes one entry. Deleting a BOOK also cascades its linked
// chapters in the same account scope; deleting a chapter is standalone.
// 读目标、删章节、删书三步放进单条事务（缺陷 11：旧实现三步各自 autocommit，
// 删书失败时章节已被永久删除而书还在；并发导入也可能在两步之间加入孤儿章
// 节），失败整体回滚，参考 ClearPoolFor 的既有事务模式。
func (s *Store) DeletePoolItem(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var kind, value string
	var accountID int64
	err = tx.QueryRow(`SELECT kind, value, account_id FROM pool_items WHERE id = ?`, id).
		Scan(&kind, &value, &accountID)
	if err == sql.ErrNoRows {
		return errors.New("条目不存在")
	}
	if err != nil {
		return err
	}
	if kind == KindBook {
		if _, err := tx.Exec(`DELETE FROM pool_items WHERE kind = ? AND book_id = ? AND account_id = ?`,
			KindChapter, value, accountID); err != nil {
			return err
		}
	}
	res, err := tx.Exec(`DELETE FROM pool_items WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("条目不存在")
	}
	return tx.Commit()
}

// ClearPoolFor empties the pool within the account scope. accountID<=0 只删
// 共享条目；accountID>0 只删该账号的专属条目（不动共享）。清空书籍顺带级联
// 其章节——与逐本删除的语义一致，否则会留下看不见却仍在引擎兜底里被引用的
// 孤儿章节。整个清空在一条事务内完成：单连接 SQLite 下逐条 autocommit DELETE
// 会把一次清空放大成 N 次写事务。
func (s *Store) ClearPoolFor(kind string, accountID int64) error {
	if kind != KindBook && kind != KindChapter {
		return fmt.Errorf("未知池类型 %q", kind)
	}
	if accountID <= 0 {
		accountID = s.fallbackAccountID()
	}
	if accountID <= 0 {
		return nil // 尚无账号，池必为空
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if kind == KindBook {
		if _, err := tx.Exec(`DELETE FROM pool_items WHERE kind = ? AND account_id = ? AND book_id IN
			(SELECT value FROM pool_items WHERE kind = ? AND account_id = ?)`, KindChapter, accountID, KindBook, accountID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM pool_items WHERE kind = ? AND account_id = ?`, kind, accountID); err != nil {
		return err
	}
	return tx.Commit()
}

// MovePoolItem 把一个条目在它所在视角的同类列表里上移/下移 delta 位
// （±1；越界为无操作）。顺序用于「顺序书单」逐本阅读与列表展示。
func (s *Store) MovePoolItem(id int64, delta int) error {
	if delta == 0 {
		return nil
	}
	var kind string
	var accountID int64
	err := s.db.QueryRow(`SELECT kind, account_id FROM pool_items WHERE id = ?`, id).
		Scan(&kind, &accountID)
	if err == sql.ErrNoRows {
		return errors.New("条目不存在")
	}
	if err != nil {
		return err
	}
	items, err := s.PoolItemsForAccount(kind, accountID)
	if err != nil {
		return err
	}
	idx := -1
	for i, it := range items {
		if it.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return errors.New("条目不存在")
	}
	j := idx + delta
	if j < 0 || j >= len(items) {
		return nil // 已在边界，无操作
	}
	swapped := append([]PoolItem(nil), items...)
	swapped[idx], swapped[j] = swapped[j], swapped[idx]
	// 全视角的 sort_order 重写在一条事务内完成：逐条 autocommit 会让一次
	// 上移/下移动作放大成 N 个写事务，并阻塞单连接上的其他请求。
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	for i, it := range swapped {
		if _, err := tx.Exec(`UPDATE pool_items SET sort_order = ? WHERE id = ?`, i, it.ID); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// PoolTitleMapForAccount 返回某类条目的「value → 标题」映射（账号视角），
// 引擎拿来在运行日志里展示正在读的书名/章节名。
func (s *Store) PoolTitleMapForAccount(kind string, accountID int64) (map[string]string, error) {
	if kind != KindBook && kind != KindChapter {
		return nil, fmt.Errorf("未知池类型 %q", kind)
	}
	items, err := s.PoolItemsForAccount(kind, accountID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(items))
	for _, it := range items {
		if it.Title != "" {
			out[it.Value] = it.Title
		}
	}
	return out, nil
}

// PoolWordCountMapForAccount 返回某类条目的「value → 字数」映射（账号视角）。
func (s *Store) PoolWordCountMapForAccount(kind string, accountID int64) (map[string]int, error) {
	if kind != KindBook && kind != KindChapter {
		return nil, fmt.Errorf("未知池类型 %q", kind)
	}
	items, err := s.PoolItemsForAccount(kind, accountID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]int, len(items))
	for _, it := range items {
		if it.WordCount > 0 {
			out[it.Value] = it.WordCount
		}
	}
	return out, nil
}
