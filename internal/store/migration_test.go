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
		var count, shared int
		if err := st.db.QueryRow(`SELECT COUNT(*), SUM(CASE WHEN account_id = 0 THEN 1 ELSE 0 END)
			FROM pool_items`).Scan(&count, &shared); err != nil || count != 5 || shared != 0 {
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
			FROM pool_items WHERE kind = 'chapter' AND value = 'chapter-x1' AND account_id = 1`).
			Scan(&id, &enabled, &order, &words, &title, &bookID)
		if err != nil || id != 4 || enabled != 0 || order != 9 || words != 1500 || title != "owned chapter" || bookID != "book-x" {
			t.Fatalf("owned chapter changed incorrectly: id=%d enabled=%d order=%d words=%d title=%q bookID=%q err=%v",
				id, enabled, order, words, title, bookID, err)
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
