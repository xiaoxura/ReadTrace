package store

import (
	"errors"
	"testing"
)

func TestPoolStartsEmptyWithoutBuiltins(t *testing.T) {
	// 内置 12 本书/16 章节只做引擎兜底，不再入库；新库书池必须为空。
	st := newTestStore(t)

	if books, err := st.PoolItems(KindBook); err != nil || len(books) != 0 {
		t.Fatalf("fresh store books = %d, err = %v, want empty", len(books), err)
	}
	if chapters, err := st.PoolItems(KindChapter); err != nil || len(chapters) != 0 {
		t.Fatalf("fresh store chapters = %d, err = %v, want empty", len(chapters), err)
	}
}

// seedAccount 建一个测试账号；共享池移除后，书池条目必须归属账号。
func seedAccount(t *testing.T, st *Store, name string) int64 {
	t.Helper()
	id, err := st.CreateAccount(Account{Name: name}, nil, nil)
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	return id
}

func TestPoolAccountScope(t *testing.T) {
	st := newTestStore(t)
	acc1 := seedAccount(t, st, "A")
	acc2 := seedAccount(t, st, "B")

	if _, err := st.AddPoolItem(KindBook, "a1", "账号1的书", acc1); err != nil {
		t.Fatalf("add owned: %v", err)
	}
	if _, err := st.AddPoolItem(KindBook, "b1", "账号2的书", acc2); err != nil {
		t.Fatalf("add owned other: %v", err)
	}

	// 视角回落：0 = 第一个账号。
	fallback, err := st.PoolItemsForAccount(KindBook, 0)
	if err != nil {
		t.Fatalf("fallback view: %v", err)
	}
	if len(fallback) != 1 || fallback[0].Value != "a1" {
		t.Fatalf("fallback view rows = %+v", fallback)
	}

	// 同一本书可以分属不同账号（唯一键含 account_id）。
	if _, err := st.AddPoolItem(KindBook, "a1", "账号2的同名书", acc2); err != nil {
		t.Fatalf("same value different account: %v", err)
	}

	// 同 (kind,value,account) 重复添加被拒。
	if _, err := st.AddPoolItem(KindBook, "a1", "x", acc1); !errors.Is(err, ErrPoolItemExists) {
		t.Fatalf("dup err = %v, want ErrPoolItemExists", err)
	}

	// 无账号的全新库：查询返回空，写入被拒。
	empty := newTestStore(t)
	if items, err := empty.PoolItemsForAccount(KindBook, 0); err != nil || len(items) != 0 {
		t.Fatalf("no-account view = %v, err = %v", items, err)
	}
	if _, err := empty.AddPoolItem(KindBook, "x", "", 0); err == nil {
		t.Fatal("without account, adding should fail")
	}
}

func TestPoolPoolForAccountOrder(t *testing.T) {
	st := newTestStore(t)
	acc := seedAccount(t, st, "A")
	if _, err := st.AddPoolItem(KindBook, "b1", "", acc); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := st.AddPoolItem(KindBook, "b2", "", acc); err != nil {
		t.Fatalf("add: %v", err)
	}

	owned, shared, err := st.PoolPoolForAccount(KindBook, acc)
	if err != nil {
		t.Fatalf("pool for account: %v", err)
	}
	if len(owned) != 2 || owned[0] != "b1" || owned[1] != "b2" {
		t.Fatalf("owned = %v, want [b1 b2]", owned)
	}
	if shared != nil {
		t.Fatalf("shared should be nil without shared pool, got %v", shared)
	}
}

func TestAddUpdateDelete(t *testing.T) {
	st := newTestStore(t)
	seedAccount(t, st, "A")

	id, err := st.AddPoolItem(KindBook, "abc123", "测试书", 0)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	title := "改名"
	enabled := false
	if err := st.UpdatePoolItem(id, nil, &title, &enabled); err != nil {
		t.Fatalf("update: %v", err)
	}
	items, _ := st.PoolItems(KindBook)
	var found *PoolItem
	for i := range items {
		if items[i].ID == id {
			found = &items[i]
		}
	}
	if found == nil || found.Title != "改名" || found.Enabled {
		t.Fatalf("update not applied: %+v", found)
	}

	vals, _ := st.PoolEnabledValues(KindBook)
	for _, v := range vals {
		if v == "abc123" {
			t.Fatal("disabled item still enabled")
		}
	}

	if err := st.DeletePoolItem(id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := st.DeletePoolItem(id); err == nil {
		t.Fatal("deleting twice should fail")
	}
}

func TestDeleteBookCascadesChaptersInScope(t *testing.T) {
	st := newTestStore(t)
	bookID, err := st.AddPoolItem(KindBook, "book1", "书", 1)
	if err != nil {
		t.Fatalf("add book: %v", err)
	}
	id, err := st.AddChapterItem(1, "book1", "chap1", "书 · 第一章", 3000)
	if err != nil {
		t.Fatalf("add chapter: %v", err)
	}
	chs, _ := st.PoolChaptersOf("book1", 1)
	if len(chs) != 1 {
		t.Fatalf("chapters of book1 = %d, want 1", len(chs))
	}
	if err := st.DeletePoolItem(bookID); err != nil {
		t.Fatalf("delete book: %v", err)
	}
	chs, _ = st.PoolChaptersOf("book1", 1)
	if len(chs) != 0 {
		t.Fatalf("chapters survived book delete: %d", len(chs))
	}
	_ = id
}

func TestPoolChaptersPage(t *testing.T) {
	st := newTestStore(t)
	if _, err := st.AddPoolItem(KindBook, "book1", "书", 1); err != nil {
		t.Fatalf("add book: %v", err)
	}
	for i := 0; i < 5; i++ {
		if _, err := st.AddChapterItem(1, "book1", "ch"+string(rune('a'+i)), "章", 1000); err != nil {
			t.Fatalf("add chapter: %v", err)
		}
	}
	items, total, enabled, err := st.PoolChaptersPage("book1", 1, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || total != 5 || enabled != 5 {
		t.Fatalf("page1 len=%d total=%d enabled=%d", len(items), total, enabled)
	}
	items, total, enabled, err = st.PoolChaptersPage("book1", 1, 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || total != 5 || enabled != 5 {
		t.Fatalf("page3 len=%d total=%d enabled=%d", len(items), total, enabled)
	}
}

func TestClearPoolPerAccount(t *testing.T) {
	st := newTestStore(t)
	acc1 := seedAccount(t, st, "A")
	acc2 := seedAccount(t, st, "B")
	if _, err := st.AddPoolItem(KindBook, "a1", "", acc1); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := st.AddPoolItem(KindBook, "a1b", "", acc1); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := st.AddPoolItem(KindBook, "a2", "", acc2); err != nil {
		t.Fatalf("add: %v", err)
	}

	// 只清空账号 1，账号 2 不受影响。
	if err := st.ClearPoolFor(KindBook, acc1); err != nil {
		t.Fatalf("clear: %v", err)
	}
	items, _ := st.PoolItemsForAccount(KindBook, acc1)
	if len(items) != 0 {
		t.Fatalf("after account clear, account view = %+v", items)
	}
	all, _ := st.PoolItems(KindBook)
	if len(all) != 1 {
		t.Fatalf("rows after clear = %d, want 1", len(all))
	}

	// accountID=0 回落到第一个账号（已空），账号 2 保持。
	if err := st.ClearPoolFor(KindBook, 0); err != nil {
		t.Fatalf("clear fallback: %v", err)
	}
	all, _ = st.PoolItems(KindBook)
	if len(all) != 1 {
		t.Fatalf("rows after fallback clear = %d, want 1", len(all))
	}
}

func TestDefaultTemplateRoundTrip(t *testing.T) {
	st := newTestStore(t)
	v, err := st.GetDefaultTemplate()
	if err != nil || v != "" {
		t.Fatalf("fresh store template = %q, err = %v, want empty", v, err)
	}
	if err := st.SetDefaultTemplate(`{"b":"x","c":"y"}`); err != nil {
		t.Fatalf("set: %v", err)
	}
	v, _ = st.GetDefaultTemplate()
	if v == "" {
		t.Fatal("template not persisted")
	}
	if err := st.SetDefaultTemplate(""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	v, _ = st.GetDefaultTemplate()
	if v != "" {
		t.Fatal("template not cleared")
	}
}
