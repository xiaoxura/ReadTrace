package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	"readtrace/internal/secure"
)

func legacyPoolDB(t *testing.T, uniqueConstraint string) (string, *secure.Box) {
	t.Helper()
	dir := t.TempDir()
	box, err := secure.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE pool_items`); err != nil {
		t.Fatal(err)
	}
	legacy := `CREATE TABLE pool_items (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		kind TEXT NOT NULL CHECK (kind IN ('book','chapter')),
		value TEXT NOT NULL,
		title TEXT NOT NULL DEFAULT '',
		author TEXT NOT NULL DEFAULT '',
		cover TEXT NOT NULL DEFAULT '',
		book_id TEXT NOT NULL DEFAULT '',
		account_id INTEGER NOT NULL DEFAULT 0,
		enabled INTEGER NOT NULL DEFAULT 1,
		sort_order INTEGER NOT NULL DEFAULT 0,
		word_count INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL` + uniqueConstraint + `)`
	if _, err := db.Exec(legacy); err != nil {
		t.Fatal(err)
	}
	return path, box
}

func TestMigrateLegacyPoolUniqueKeyPreservesMetadata(t *testing.T) {
	for _, coexist := range []bool{false, true} {
		name := "legacy_only"
		if coexist {
			name = "legacy_and_new_indexes"
		}
		t.Run(name, func(t *testing.T) {
			path, box := legacyPoolDB(t, `, UNIQUE(kind, value)`)
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			if coexist {
				if _, err := db.Exec(`CREATE UNIQUE INDEX idx_pool_new ON pool_items(kind, value, account_id)`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.Exec(`INSERT INTO pool_items
				(id, kind, value, title, author, cover, book_id, account_id, enabled, sort_order, word_count, created_at)
				VALUES (7, 'book', 'same-book', 'old title', 'old author', 'old cover', 'owner-book', 1, 0, 9, 2345, 123)`); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			st, err := Open(path, box)
			if err != nil {
				t.Fatalf("migrate: %v", err)
			}
			defer st.Close()
			first, err := st.CreateAccount(Account{Name: "first"}, nil, nil)
			if err != nil || first != 1 {
				t.Fatalf("create first account = %d, err = %v", first, err)
			}
			second, err := st.CreateAccount(Account{Name: "second"}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.AddPoolItem(KindBook, "same-book", "second copy", second); err != nil {
				t.Fatalf("same book for another account after migration: %v", err)
			}
			var title, author, cover, bookID string
			var accountID, enabled, order, words, created int64
			err = st.db.QueryRow(`SELECT title, author, cover, book_id, account_id, enabled, sort_order, word_count, created_at
				FROM pool_items WHERE id = 7`).Scan(&title, &author, &cover, &bookID, &accountID, &enabled, &order, &words, &created)
			if err != nil || title != "old title" || author != "old author" || cover != "old cover" ||
				bookID != "owner-book" || accountID != first || enabled != 0 || order != 9 || words != 2345 || created != 123 {
				t.Fatalf("metadata after migration: title=%q author=%q cover=%q bookID=%q account=%d enabled=%d order=%d words=%d created=%d err=%v",
					title, author, cover, bookID, accountID, enabled, order, words, created, err)
			}
		})
	}
}

func TestMigratePoolConflictFailsStartupWithoutDroppingRows(t *testing.T) {
	path, box := legacyPoolDB(t, "")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO pool_items (kind, value, account_id, created_at)
		VALUES ('book', 'duplicate', 1, 1), ('book', 'duplicate', 1, 2)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if st, err := Open(path, box); err == nil {
		_ = st.Close()
		t.Fatal("duplicate rows should fail migration instead of being ignored")
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pool_items WHERE value = 'duplicate'`).Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("legacy rows after failed migration = %d, err = %v", rows, err)
	}
	var rebuilt int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'pool_items_new'`).Scan(&rebuilt); err != nil || rebuilt != 0 {
		t.Fatalf("partial rebuild table remained = %d, err = %v", rebuilt, err)
	}
}

// 缺陷 02 转正测试：老库（3 列唯一键 UNIQUE(kind,value,account_id)）里跨书
// 同章节 ID 的共享行与专属行共存，迁移必须保留双方各自的上 book_id 归属与
// 元数据，绝不能把它们当成重复合并删除。
func TestMigrateSharedPoolKeepsCrossBookChapterRows(t *testing.T) {
	path, box := legacyPoolDB(t, `, UNIQUE(kind, value, account_id)`)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO accounts (id, seq, name, created_at, updated_at)
		VALUES (1, 0, 'first', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	// BOOK_A 的章节留在共享池（老共享数据），BOOK_B 已经归属账号 1；book_x1
	// 的 value 相同但 book_id 不同，book_x3 则是共享行非空、专属行空。
	if _, err := db.Exec(`INSERT INTO pool_items
		(id, kind, value, title, book_id, account_id, enabled, sort_order, word_count, created_at)
		VALUES
		(1, 'chapter', 'book_x1', 'A 的第一章', 'BOOK_A', 0, 1, 3, 1500, 11),
		(2, 'chapter', 'book_x1', 'B 的第一章', 'BOOK_B', 1, 0, 9, 2000, 12),
		(3, 'chapter', 'book_x2', 'A 的第二章', 'BOOK_A', 0, 1, 4, 1600, 13),
		(4, 'chapter', 'book_x2', '手动导入的', '', 1, 1, 5, 0, 14)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO pool_items (kind, value, title, account_id, created_at)
		VALUES ('book', 'BOOK_A', '甲书', 0, 15), ('book', 'BOOK_B', '乙书', 1, 16)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := Open(path, box)
	if err != nil {
		t.Fatalf("migrate cross-book pool: %v", err)
	}
	defer st.Close()
	var count, shared int
	if err := st.db.QueryRow(`SELECT COUNT(*), SUM(CASE WHEN account_id = 0 THEN 1 ELSE 0 END)
		FROM pool_items`).Scan(&count, &shared); err != nil || count != 6 || shared != 0 {
		t.Fatalf("rows after migration: count=%d shared=%d err=%v", count, shared, err)
	}
	rows := []struct {
		value, title, bookID string
		accountID, words     int64
	}{
		{"book_x1", "A 的第一章", "BOOK_A", 1, 1500},
		{"book_x1", "B 的第一章", "BOOK_B", 1, 2000},
		{"book_x2", "A 的第二章", "BOOK_A", 1, 1600},
		{"book_x2", "手动导入的", "", 1, 0},
	}
	for _, want := range rows {
		var got struct {
			title, bookID    string
			accountID, words int64
		}
		err := st.db.QueryRow(`SELECT title, book_id, account_id, word_count FROM pool_items
			WHERE kind = 'chapter' AND value = ? AND book_id = ?`, want.value, want.bookID).
			Scan(&got.title, &got.bookID, &got.accountID, &got.words)
		if err != nil || got.title != want.title || got.accountID != want.accountID || got.words != want.words {
			t.Fatalf("chapter %s/%s after migration: title=%q book=%q account=%d words=%d err=%v, want %+v",
				want.value, want.bookID, got.title, got.bookID, got.accountID, got.words, err, want)
		}
	}
	// 账号视角：两本书的目录都在，共享书的元数据也迁到了账号名下。
	for _, book := range []string{"BOOK_A", "BOOK_B"} {
		chs, err := st.PoolChaptersOf(book, 1)
		if err != nil {
			t.Fatalf("chapters of %s: %v", book, err)
		}
		if len(chs) == 0 {
			t.Fatalf("book %s lost its chapters after migration", book)
		}
		books, err := st.PoolItemsForAccount(KindBook, 1)
		if err != nil {
			t.Fatal(err)
		}
		var found bool
		for _, it := range books {
			if it.Value == book {
				found = true
			}
		}
		if !found {
			t.Fatalf("book %s missing from account view %v", book, books)
		}
	}
}

// 缺陷 02 的 book_id=” 配对语义：共享行与专属行同为无归属遗留行（book_id ”
// 全等）时仍是同一条目，走合并：专属行保留 enabled/sort，空元数据由共享行
// 补齐，共享行删除。
func TestMigrateSharedPoolMergesUnlinkedRows(t *testing.T) {
	path, box := legacyPoolDB(t, `, UNIQUE(kind, value, account_id)`)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO accounts (id, seq, name, created_at, updated_at)
		VALUES (1, 0, 'first', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO pool_items
		(id, kind, value, title, book_id, account_id, enabled, sort_order, word_count, created_at)
		VALUES
		(1, 'chapter', 'shared_ch1', '共享标题', '', 0, 1, 2, 900, 11),
		(2, 'chapter', 'shared_ch1', '', '', 1, 0, 7, 0, 12)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := Open(path, box)
	if err != nil {
		t.Fatalf("migrate unlinked pool rows: %v", err)
	}
	defer st.Close()
	var id, accountID, enabled, order, words int64
	var title string
	if err := st.db.QueryRow(`SELECT id, account_id, enabled, sort_order, word_count, title FROM pool_items
		WHERE kind = 'chapter' AND value = 'shared_ch1'`).
		Scan(&id, &accountID, &enabled, &order, &words, &title); err != nil {
		t.Fatalf("unlinked chapter after merge: %v", err)
	}
	// 老的 id=2 专属行保留，enabled=0 / sort=7 不变，空字段由共享行补齐。
	if id != 2 || accountID != 1 || enabled != 0 || order != 7 || words != 900 || title != "共享标题" {
		t.Fatalf("unlinked merge result: id=%d account=%d enabled=%d order=%d words=%d title=%q",
			id, accountID, enabled, order, words, title)
	}
}

// 缺陷 24 转正测试：无账号的旧库升级时跳过共享池迁移，遗留 account_id=0 行
// 在创建首账号的同一事务里接管：PoolItemsForAccount 立即可见，不需要重启。
func TestCreateFirstAccountAdoptsSharedPoolImmediately(t *testing.T) {
	path, box := legacyPoolDB(t, `, UNIQUE(kind, value, account_id)`)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO pool_items
		(id, kind, value, title, book_id, account_id, enabled, sort_order, word_count, created_at)
		VALUES
		(1, 'book', 'BOOK_A', '甲书', '', 0, 1, 2, 0, 11),
		(2, 'chapter', 'ch1', '第一章', 'BOOK_A', 0, 1, 3, 1500, 12)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := Open(path, box)
	if err != nil {
		t.Fatalf("migrate without accounts: %v", err)
	}
	defer st.Close()
	// 升级后还没有账号：共享行原地不动（此时账号视角依旧是空的）。
	if books, err := st.PoolItemsForAccount(KindBook, 0); err != nil || len(books) != 0 {
		t.Fatalf("no-account view books = %v, err = %v, want empty", books, err)
	}
	first, err := st.CreateAccount(Account{Name: "first"}, nil, nil)
	if err != nil {
		t.Fatalf("create first account: %v", err)
	}
	books, err := st.PoolItemsForAccount(KindBook, first)
	if err != nil || len(books) != 1 || books[0].Value != "BOOK_A" {
		t.Fatalf("books after first create = %+v, err = %v, want shared book adopted", books, err)
	}
	chs, err := st.PoolItemsForAccount(KindChapter, first)
	if err != nil || len(chs) != 1 || chs[0].Value != "ch1" || chs[0].BookID != "BOOK_A" || chs[0].WordCount != 1500 {
		t.Fatalf("chapters after first create = %+v, err = %v, want adopted with metadata", chs, err)
	}
	// 共享层全部清空，且不会影响既有账号视角的重开读取。
	var left int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM pool_items WHERE account_id = 0`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("leftover shared rows = %d, err = %v", left, err)
	}
}

// 缺陷 20 转正测试：老库升级重建书池表后，查询索引必须已经在同一次 Open 里
// 恢复（表 DROP 会连带删除建在其之前的索引），不能等下次启动。
func TestMigrateCreatesPoolQueryIndexAfterRebuild(t *testing.T) {
	for _, name := range []string{"legacy_rebuild", "fresh"} {
		t.Run(name, func(t *testing.T) {
			var st *Store
			if name == "legacy_rebuild" {
				path, box := legacyPoolDB(t, `, UNIQUE(kind, value, account_id)`)
				var err error
				st, err = Open(path, box)
				if err != nil {
					t.Fatal(err)
				}
				defer st.Close()
			} else {
				st = newTestStore(t)
			}
			var n int
			if err := st.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master
				WHERE type = 'index' AND name = 'idx_pool_account_kind_book'`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 1 {
				t.Fatalf("idx_pool_account_kind_book count after migration = %d, want 1", n)
			}
		})
	}
}

func TestMigrateSharedPoolOverlapMergesWithoutLosingAccountEntries(t *testing.T) {
	path, box := legacyPoolDB(t, `, UNIQUE(kind, value, account_id)`)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO accounts (id, seq, name, created_at, updated_at)
		VALUES (1, 0, 'first', 1, 1), (2, 1, 'second', 2, 2)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO pool_items
		(id, kind, value, title, author, cover, book_id, account_id, enabled, sort_order, word_count, created_at)
		VALUES
		(1, 'book', 'book-x', 'shared title', 'shared author', 'shared cover', '', 0, 1, 2, 456, 11),
		(2, 'book', 'book-x', '', 'owned author', '', '', 1, 0, 8, 0, 12),
		(3, 'chapter', 'chapter-x1', 'shared chapter', '', '', 'book-x', 0, 1, 3, 1500, 13),
		(4, 'chapter', 'chapter-x1', 'owned chapter', '', '', '', 1, 0, 9, 0, 14),
		(5, 'chapter', 'chapter-x2', 'second chapter', '', '', 'book-x', 0, 1, 4, 2000, 15),
		(6, 'book', 'book-y', 'other shared book', '', '', '', 0, 1, 5, 0, 16),
		(7, 'book', 'book-x', 'second account book', '', '', '', 2, 1, 6, 300, 17)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	verify := func(st *Store) {
		t.Helper()
		// 迁移键含 book_id 后（缺陷 02）：(book,'book-x','') 共享行并入专属
		// 行 2（book_id 全等）；(chapter,'chapter-x1','book-x') 共享行 3 与
		// book_id='' 的专属行 4 不是同键，不再删除老章节，而是整行归属到
		// 账号 1（改 account_id 保留 book_x 关联）——旧语义会把它当成重复
		// 删掉共享行并把专属行 4 的 book_id 改写为 'book-x'，两书共用章节
		// ID 时另一本书的章节会被误删。
		var count, shared int
		if err := st.db.QueryRow(`SELECT COUNT(*), SUM(CASE WHEN account_id = 0 THEN 1 ELSE 0 END)
			FROM pool_items`).Scan(&count, &shared); err != nil || count != 6 || shared != 0 {
			t.Fatalf("rows after shared merge: count=%d shared=%d err=%v", count, shared, err)
		}
		var id, accountID, enabled, order, words, created int64
		var title, author, cover, bookID string
		err := st.db.QueryRow(`SELECT id, account_id, enabled, sort_order, word_count, created_at,
			title, author, cover, book_id FROM pool_items WHERE kind = 'book' AND value = 'book-x' AND account_id = 1`).
			Scan(&id, &accountID, &enabled, &order, &words, &created, &title, &author, &cover, &bookID)
		if err != nil || id != 2 || accountID != 1 || enabled != 0 || order != 8 || words != 456 ||
			created != 12 || title != "shared title" || author != "owned author" || cover != "shared cover" || bookID != "" {
			t.Fatalf("owned book changed incorrectly: id=%d account=%d enabled=%d order=%d words=%d created=%d title=%q author=%q cover=%q bookID=%q err=%v",
				id, accountID, enabled, order, words, created, title, author, cover, bookID, err)
		}
		err = st.db.QueryRow(`SELECT id, enabled, sort_order, word_count, title, book_id
			FROM pool_items WHERE kind = 'chapter' AND value = 'chapter-x1' AND account_id = 1 AND book_id = ''`).
			Scan(&id, &enabled, &order, &words, &title, &bookID)
		if err != nil || id != 4 || enabled != 0 || order != 9 || words != 0 || title != "owned chapter" || bookID != "" {
			t.Fatalf("owned chapter changed incorrectly: id=%d enabled=%d order=%d words=%d title=%q bookID=%q err=%v",
				id, enabled, order, words, title, bookID, err)
		}
		// 同键不同 book_id 的共享行不改归属期间的数据，整行归属到账号 1。
		err = st.db.QueryRow(`SELECT id, account_id, book_id, title, word_count
			FROM pool_items WHERE kind = 'chapter' AND value = 'chapter-x1' AND account_id = 1 AND book_id <> ''`).
			Scan(&id, &accountID, &bookID, &title, &words)
		if err != nil || id != 3 || accountID != 1 || bookID != "book-x" || title != "shared chapter" || words != 1500 {
			t.Fatalf("cross-book shared chapter mishandled: id=%d account=%d bookID=%q title=%q words=%d err=%v",
				id, accountID, bookID, title, words, err)
		}
		chapters, err := st.PoolChaptersOf("book-x", 1)
		if err != nil || len(chapters) != 2 {
			t.Fatalf("associated chapters after merge = %+v, err=%v", chapters, err)
		}
		if err := st.db.QueryRow(`SELECT id, account_id FROM pool_items WHERE value = 'book-y'`).Scan(&id, &accountID); err != nil || id != 6 || accountID != 1 {
			t.Fatalf("unmatched shared book: id=%d account=%d err=%v", id, accountID, err)
		}
		if err := st.db.QueryRow(`SELECT id, title, sort_order, word_count FROM pool_items
			WHERE kind = 'book' AND value = 'book-x' AND account_id = 2`).Scan(&id, &title, &order, &words); err != nil ||
			id != 7 || title != "second account book" || order != 6 || words != 300 {
			t.Fatalf("other account book changed: id=%d title=%q order=%d words=%d err=%v", id, title, order, words, err)
		}
	}
	st, err := Open(path, box)
	if err != nil {
		t.Fatalf("open overlapping legacy pool: %v", err)
	}
	verify(st)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path, box)
	if err != nil {
		t.Fatalf("reopen after shared merge: %v", err)
	}
	defer st.Close()
	verify(st)
}
