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

// AddChapters 是批量导入的单事务入口：新增/已在池/修复空 book_id 的计数语义
// 必须与逐条的 AddChapterItem 一致，且同样的列表重复执行应当全部变成“已在池”。
func TestAddChaptersBatch(t *testing.T) {
	st := newTestStore(t)
	acc := seedAccount(t, st, "A")
	chs := []NewChapter{
		{Value: "ch1", Title: "书 · 第一章", WordCount: 3000, ChapterIndex: 1},
		{Value: "ch2", Title: "书 · 第二章", WordCount: 3200, ChapterIndex: 2},
		// 修复路径：这条先手动加成无归属章节，再由整本导入补 book_id。
	}
	if _, err := st.AddPoolItem(KindBook, "book1", "测试书", acc); err != nil {
		t.Fatalf("add book: %v", err)
	}
	// 老的手动导入章节没有 book 关联（AddPoolItem 加的 chapter 行 book_id 空）。
	if _, err := st.AddPoolItem(KindChapter, "ch3", "", acc); err != nil {
		t.Fatalf("seed unlinked chapter: %v", err)
	}
	chs = append(chs, NewChapter{Value: "ch3", Title: "书 · 第三章", WordCount: 2800, ChapterIndex: 3})

	added, existed, err := st.AddChapters(acc, "book1", chs)
	if err != nil {
		t.Fatalf("add chapters: %v", err)
	}
	if added != 3 || existed != 0 {
		t.Fatalf("first run added=%d existed=%d, want 3/0", added, existed)
	}
	items, err := st.PoolChaptersOf("book1", acc)
	if err != nil || len(items) != 3 {
		t.Fatalf("chapters of book1 = %d, err = %v, want 3", len(items), err)
	}
	for _, it := range items {
		if it.BookID != "book1" {
			t.Fatalf("chapter %s book_id = %q, want book1", it.Value, it.BookID)
		}
	}
	for _, it := range items {
		want := map[string]int{"ch1": 1, "ch2": 2, "ch3": 3}[it.Value]
		if it.ChapterIndex != want {
			t.Fatalf("chapter %s chapter_index = %d, want %d", it.Value, it.ChapterIndex, want)
		}
	}
	var ch1 *PoolItem
	for i := range items {
		if items[i].Value == "ch1" {
			ch1 = &items[i]
		}
	}
	if ch1 == nil || ch1.Title != "书 · 第一章" || ch1.WordCount != 3000 {
		t.Fatalf("chapter ch1 meta wrong: %+v", ch1)
	}

	added, existed, err = st.AddChapters(acc, "book1", chs)
	if err != nil {
		t.Fatalf("re-add chapters: %v", err)
	}
	if added != 0 || existed != 3 {
		t.Fatalf("second run added=%d existed=%d, want 0/3", added, existed)
	}

	// 空输入直接返回，正常账号下不报错。
	if added, existed, err := st.AddChapters(acc, "book1", nil); err != nil || added != 0 || existed != 0 {
		t.Fatalf("empty batch = %d/%d, %v", added, existed, err)
	}
}

// 清空书籍池会级联删除这些书的章节（与逐本删除的语义一致）。
func TestClearBooksCascadesChapters(t *testing.T) {
	st := newTestStore(t)
	acc := seedAccount(t, st, "A")
	other := seedAccount(t, st, "B")
	if _, err := st.AddPoolItem(KindBook, "book1", "书", acc); err != nil {
		t.Fatalf("add book: %v", err)
	}
	if _, _, err := st.AddChapters(acc, "book1", []NewChapter{{Value: "c1", Title: "书 · c1", WordCount: 100}}); err != nil {
		t.Fatalf("add chapters: %v", err)
	}
	if _, _, err := st.AddChapters(other, "book1", []NewChapter{{Value: "c2", Title: "B · c2", WordCount: 100}}); err != nil {
		t.Fatalf("add chapters other: %v", err)
	}
	if err := st.ClearPoolFor(KindBook, acc); err != nil {
		t.Fatalf("clear: %v", err)
	}
	for _, it := range []struct {
		kind string
		n    int
		msg  string
	}{
		{KindBook, 0, "account books"},
		{KindChapter, 0, "account chapters"},
	} {
		items, err := st.PoolItemsForAccount(it.kind, acc)
		if err != nil || len(items) != it.n {
			t.Fatalf("after clear %s = %d, err = %v, want 0", it.msg, len(items), err)
		}
	}
	// 其他账号的同类条目必须保留。
	if items, err := st.PoolItemsForAccount(KindChapter, other); err != nil || len(items) != 1 {
		t.Fatalf("other account chapters = %d, err = %v, want 1", len(items), err)
	}
}
