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

// KeyDefaultReadPayload 保存稳定模板缺失账号抓包数据时使用的默认书模板
// （完整 read 请求 payload 的 JSON）。为空时引擎回落到 weread.DefaultTemplate。
const KeyDefaultReadPayload = "read_default_payload"

// SharedAccountID 保留为 0 的历史值：共享池已移除，所有条目都归属某个账号，
// account_id<=0 的写入会回落到第一个已绑定账号。
const SharedAccountID int64 = 0

// PoolItem is one configurable entry of the user's reading pool.
// AccountID=0 表示全账号共享，>0 表示该账号专属。
// kind=book 行的 Author/Cover 用于书架式展示；kind=chapter 行的 BookID 记录
// 所属书籍的 value（bookId），供按书查看与按书抽取。
type PoolItem struct {
	ID        int64  `json:"id"`
	Kind      string `json:"kind"`
	Value     string `json:"value"`
	Title     string `json:"title"`
	Author    string `json:"author,omitempty"`
	Cover     string `json:"cover,omitempty"`
	BookID    string `json:"book_id,omitempty"`
	AccountID int64  `json:"account_id"`
	Enabled   bool   `json:"enabled"`
	SortOrder int    `json:"sort_order"`
	WordCount int    `json:"word_count"`
}

func scanPoolItems(rows *sql.Rows) ([]PoolItem, error) {
	out := []PoolItem{}
	for rows.Next() {
		var it PoolItem
		var enabled, sortOrder, wordCount int
		if err := rows.Scan(&it.ID, &it.Kind, &it.Value, &it.Title, &it.Author, &it.Cover, &it.BookID, &it.AccountID, &enabled, &sortOrder, &wordCount); err != nil {
			return nil, err
		}
		it.Enabled = enabled != 0
		it.SortOrder = sortOrder
		it.WordCount = wordCount
		out = append(out, it)
	}
	return out, rows.Err()
}

const poolSelectCols = `SELECT id, kind, value, title, author, cover, book_id, account_id, enabled, sort_order, word_count FROM pool_items`

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
	var exist int64
	var existingBook string
	err := s.db.QueryRow(`SELECT id, book_id FROM pool_items WHERE kind = ? AND value = ? AND account_id = ?`,
		KindChapter, value, accountID).Scan(&exist, &existingBook)
	if err == nil {
		// Old manual imports lacked a book association. An explicit import
		// from this book can repair it without guessing or deleting the row.
		if existingBook == "" {
			res, err := s.db.Exec(`UPDATE pool_items SET book_id = ?,
				title = CASE WHEN title = '' THEN ? ELSE title END,
				word_count = CASE WHEN word_count <= 0 THEN ? ELSE word_count END
				WHERE id = ? AND book_id = ''`, bookValue, title, wordCount, exist)
			if err != nil {
				return 0, err
			}
			if n, err := res.RowsAffected(); err != nil {
				return 0, err
			} else if n > 0 {
				return exist, nil
			}
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
// the current value of that field.
func (s *Store) UpdatePoolItem(id int64, value, title *string, enabled *bool) error {
	var cur PoolItem
	var curEnabled int
	err := s.db.QueryRow(`SELECT id, kind, value, title, account_id, enabled FROM pool_items WHERE id = ?`, id).
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
	if v, has := updates["value"]; has && toPoolString(v) != cur.Value {
		var other int64
		err := s.db.QueryRow(`SELECT id FROM pool_items WHERE kind = ? AND value = ? AND account_id = ? AND id <> ?`,
			cur.Kind, v, cur.AccountID, id).Scan(&other)
		if err == nil {
			return ErrPoolItemExists
		}
		if err != sql.ErrNoRows {
			return err
		}
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
	_, err = s.db.Exec(`UPDATE pool_items SET `+strings.Join(cols, ", ")+` WHERE id = ?`, args...)
	return err
}

func toPoolString(v any) string {
	s, _ := v.(string)
	return s
}

// DeletePoolItem removes one entry. Deleting a BOOK also cascades its linked
// chapters in the same account scope; deleting a chapter is standalone.
func (s *Store) DeletePoolItem(id int64) error {
	var kind, value string
	var accountID int64
	err := s.db.QueryRow(`SELECT kind, value, account_id FROM pool_items WHERE id = ?`, id).
		Scan(&kind, &value, &accountID)
	if err == sql.ErrNoRows {
		return errors.New("条目不存在")
	}
	if err != nil {
		return err
	}
	if kind == KindBook {
		if _, err := s.db.Exec(`DELETE FROM pool_items WHERE kind = ? AND book_id = ? AND account_id = ?`,
			KindChapter, value, accountID); err != nil {
			return err
		}
	}
	res, err := s.db.Exec(`DELETE FROM pool_items WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("条目不存在")
	}
	return nil
}

// ClearPoolFor empties the pool within the account scope. accountID<=0 只删
// 共享条目；accountID>0 只删该账号的专属条目（不动共享）。书籍删除顺带级联
// 其章节。引擎兜底不经此路，池清空后回落内置常量。
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
	rows, err := s.PoolItems(kind)
	if err != nil {
		return err
	}
	for _, it := range rows {
		if it.Kind != kind || it.AccountID != accountID {
			continue
		}
		if err := s.DeletePoolItem(it.ID); err != nil && !strings.Contains(err.Error(), "不存在") {
			return err
		}
	}
	return nil
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
	for i, it := range swapped {
		if _, err := s.db.Exec(`UPDATE pool_items SET sort_order = ? WHERE id = ?`, i, it.ID); err != nil {
			return err
		}
	}
	return nil
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

// GetDefaultTemplate returns the stored default read template, or "" when unset.
func (s *Store) GetDefaultTemplate() (string, error) {
	return s.GetSetting(KeyDefaultReadPayload)
}

// SetDefaultTemplate stores (or clears with "") the default read template JSON.
func (s *Store) SetDefaultTemplate(raw string) error {
	return s.SetSetting(KeyDefaultReadPayload, strings.TrimSpace(raw))
}
