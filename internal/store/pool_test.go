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

	added, existed, conflicts, err := st.AddChapters(acc, "book1", chs)
	if err != nil {
		t.Fatalf("add chapters: %v", err)
	}
	if added != 3 || existed != 0 || conflicts != 0 {
		t.Fatalf("first run added=%d existed=%d conflicts=%d, want 3/0/0", added, existed, conflicts)
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

	added, existed, conflicts, err = st.AddChapters(acc, "book1", chs)
	if err != nil {
		t.Fatalf("re-add chapters: %v", err)
	}
	if added != 0 || existed != 3 || conflicts != 0 {
		t.Fatalf("second run added=%d existed=%d conflicts=%d, want 0/3/0", added, existed, conflicts)
	}

	// 空输入直接返回，正常账号下不报错。
	if added, existed, conflicts, err := st.AddChapters(acc, "book1", nil); err != nil || added != 0 || existed != 0 || conflicts != 0 {
		t.Fatalf("empty batch = %d/%d/%d, %v", added, existed, conflicts, err)
	}

	// 跨书同章节 ID：同账号另一本书登记相同章节 ID 属于合法数据
	// （上传书 CB_ 的 chapterUid 是每书小编号），各自挂各自的 book_id。
	if added, existed, conflicts, err := st.AddChapters(acc, "book2", chs); err != nil || added != 3 || existed != 0 || conflicts != 0 {
		t.Fatalf("cross-book run added=%d existed=%d conflicts=%d err=%v, want 3/0/0", added, existed, conflicts, err)
	}
	b1, _ := st.PoolChaptersOf("book1", acc)
	b2, _ := st.PoolChaptersOf("book2", acc)
	if len(b1) != 3 || len(b2) != 3 {
		t.Fatalf("chapters per book: book1=%d book2=%d, want 3/3", len(b1), len(b2))
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
	if _, _, _, err := st.AddChapters(acc, "book1", []NewChapter{{Value: "c1", Title: "书 · c1", WordCount: 100}}); err != nil {
		t.Fatalf("add chapters: %v", err)
	}
	if _, _, _, err := st.AddChapters(other, "book1", []NewChapter{{Value: "c2", Title: "B · c2", WordCount: 100}}); err != nil {
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

// 缺陷 10：书改 value 时其章节的 book_id 必须在同一事务里跟着改到新值，
// 否则书变成零章、之后删书留下孤儿章节。
func TestUpdateBookValueSyncsChapters(t *testing.T) {
	st := newTestStore(t)
	acc := seedAccount(t, st, "A")
	if _, err := st.AddPoolItem(KindBook, "BOOK_A", "甲", acc); err != nil {
		t.Fatalf("add book: %v", err)
	}
	if _, _, _, err := st.AddChapters(acc, "BOOK_A", []NewChapter{
		{Value: "c1", Title: "第一章", WordCount: 100},
		{Value: "c2", Title: "第二章", WordCount: 200},
	}); err != nil {
		t.Fatalf("add chapters: %v", err)
	}
	newValue := "BOOK_B"
	if err := st.UpdatePoolItem(1, &newValue, nil, nil); err != nil {
		t.Fatalf("rename book: %v", err)
	}
	old, err := st.PoolChaptersOf("BOOK_A", acc)
	if err != nil || len(old) != 0 {
		t.Fatalf("chapters still under old value BOOK_A = %+v, err = %v", old, err)
	}
	chs, err := st.PoolChaptersOf("BOOK_B", acc)
	if err != nil || len(chs) != 2 {
		t.Fatalf("chapters under new value BOOK_B = %+v, err = %v, want 2", chs, err)
	}
	// 无孤儿：所有章节行都归属于改名后的书。此时删书也一并清掉章节。
	var orphans int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM pool_items WHERE kind = 'chapter' AND book_id <> 'BOOK_B'`).Scan(&orphans); err != nil || orphans != 0 {
		t.Fatalf("orphan chapters = %d, err = %v", orphans, err)
	}
	if err := st.DeletePoolItem(1); err != nil {
		t.Fatalf("delete renamed book: %v", err)
	}
	var left int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM pool_items WHERE kind = 'chapter'`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("chapters after deleting renamed book = %d, want 0", left)
	}
}

// 缺陷 10 的回滚面：目标 value 已撞名时直接拒绝；级联章节更新中途失败时，
// 事务回滚把书行的改名一并撤销，不留「书换了名、章节还挂在旧书」的中间态。
func TestUpdateBookValueFailureRollsBackWholeChange(t *testing.T) {
	st := newTestStore(t)
	acc := seedAccount(t, st, "A")
	var bookA, bookC int64
	var err error
	if bookA, err = st.AddPoolItem(KindBook, "BOOK_A", "甲", acc); err != nil {
		t.Fatalf("add book A: %v", err)
	}
	if bookC, err = st.AddPoolItem(KindBook, "BOOK_C", "丙", acc); err != nil {
		t.Fatalf("add book C: %v", err)
	}
	if _, _, _, err := st.AddChapters(acc, "BOOK_A", []NewChapter{{Value: "c1", Title: "第一章", WordCount: 100}}); err != nil {
		t.Fatalf("add chapters: %v", err)
	}
	// 目标 value 已有同名书：撞唯一键保护，直接拒绝。
	conflict := "BOOK_C"
	if err := st.UpdatePoolItem(bookA, &conflict, nil, nil); !errors.Is(err, ErrPoolItemExists) {
		t.Fatalf("rename onto existing value: err = %v, want ErrPoolItemExists", err)
	}
	// 让章节级联 UPDATE 失败（触发器 RAISE FAIL 只拒章节行），书行已经
	// 改名的语句也要被事务回滚。
	if _, err := st.db.Exec(`CREATE TRIGGER fail_chapter_rebind BEFORE UPDATE ON pool_items
		WHEN NEW.kind = 'chapter' BEGIN SELECT RAISE(FAIL, 'probe'); END`); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdatePoolItem(bookA, &conflict, nil, nil); err == nil {
		t.Fatal("chapter rebind failure should fail the rename")
	}
	var bookValue, title string
	if err := st.db.QueryRow(`SELECT value, title FROM pool_items WHERE id = ?`, bookA).Scan(&bookValue, &title); err != nil ||
		bookValue != "BOOK_A" || title != "甲" {
		t.Fatalf("book row after failed rename: value=%q title=%q err=%v", bookValue, title, err)
	}
	chs, err := st.PoolChaptersOf("BOOK_A", acc)
	if err != nil || len(chs) != 1 {
		t.Fatalf("chapters after failed rename = %+v, err = %v, want unchanged", chs, err)
	}
	var otherAccountsBooks int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM pool_items WHERE kind = 'book'`).Scan(&otherAccountsBooks); err != nil || otherAccountsBooks != 2 {
		t.Fatalf("book rows after failure = %d, want 2", otherAccountsBooks)
	}
	_ = bookC
}

// 缺陷 11：删书的三步（读目标、删章节、删书）在同一事务内，第二步失败时
// 章节的删除随事务回滚，书与章节全部保留（旧实现两个 autocommit，书还在
// 而章节已永久丢失）。
func TestDeleteBookFailureRollsBackChapters(t *testing.T) {
	st := newTestStore(t)
	acc := seedAccount(t, st, "A")
	book, err := st.AddPoolItem(KindBook, "BOOK_A", "甲", acc)
	if err != nil {
		t.Fatalf("add book: %v", err)
	}
	if _, _, _, err := st.AddChapters(acc, "BOOK_A", []NewChapter{
		{Value: "c1", Title: "第一章", WordCount: 100},
		{Value: "c2", Title: "第二章", WordCount: 200},
	}); err != nil {
		t.Fatalf("add chapters: %v", err)
	}
	// 触发器只拒绝删书行：让「删书」这一步失败，前面的「删章节」必须随
	// 事务回滚。
	if _, err := st.db.Exec(`CREATE TRIGGER fail_book_delete BEFORE DELETE ON pool_items
		WHEN OLD.kind = 'book' BEGIN SELECT RAISE(FAIL, 'probe'); END`); err != nil {
		t.Fatal(err)
	}
	if err := st.DeletePoolItem(book); err == nil {
		t.Fatal("delete book should fail via trigger")
	}
	var books, chapters int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM pool_items WHERE kind = 'book'`).Scan(&books); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM pool_items WHERE kind = 'chapter'`).Scan(&chapters); err != nil {
		t.Fatal(err)
	}
	if books != 1 || chapters != 2 {
		t.Fatalf("after failed delete: books=%d chapters=%d, want 1/2 (nothing committed)", books, chapters)
	}
	// 撤掉触发器后删除恢复正常级联。
	if _, err := st.db.Exec(`DROP TRIGGER fail_book_delete`); err != nil {
		t.Fatal(err)
	}
	if err := st.DeletePoolItem(book); err != nil {
		t.Fatalf("delete after removing trigger: %v", err)
	}
	if chapters, err := st.PoolChaptersOf("BOOK_A", acc); err != nil || len(chapters) != 0 {
		t.Fatalf("chapters after real delete = %+v, err = %v", chapters, err)
	}
}

// 缺陷 12（单条入口）：已禁用的无归属遗留章节被显式导入时应就地挂上目标
// 书，保留 enabled=0 与旧 title/sort，而不是新插一行默认启用的副本。
func TestAddChapterItemAdoptsDisabledLegacyChapter(t *testing.T) {
	st := newTestStore(t)
	acc := seedAccount(t, st, "A")
	// 老的手动导入：无 book_id、已禁用、带标题和排序。
	if _, err := st.db.Exec(`INSERT INTO pool_items
		(kind, value, title, book_id, account_id, enabled, sort_order, created_at)
		VALUES ('chapter', 'c1', '旧标题', '', ?, 0, 6, ?)`, acc, now()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddChapterItem(acc, "BOOK_A", "c1", "新标题", 200); err != nil {
		t.Fatalf("explicit import: %v", err)
	}
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM pool_items WHERE kind = 'chapter' AND value = 'c1'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("chapter rows after import = %d, err = %v, want 1", n, err)
	}
	var item *PoolItem
	items, err := st.PoolItemsForAccount(KindChapter, acc)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("account chapters = %+v, want 1", items)
	}
	item = &items[0]
	if item.Enabled {
		t.Fatal("adopted chapter lost its disabled state")
	}
	if item.BookID != "BOOK_A" || item.Title != "旧标题" || item.SortOrder != 6 || item.WordCount != 200 {
		t.Fatalf("adopted chapter = %+v, want book_id=BOOK_A, title kept, sort 6, word_count 200", item)
	}
	// 再导入一次：现在已归属本书，属于真正的重复。
	if _, err := st.AddChapterItem(acc, "BOOK_A", "c1", "again", 0); !errors.Is(err, ErrPoolItemExists) {
		t.Fatalf("second import err = %v, want ErrPoolItemExists", err)
	}
}

// 缺陷 12（批量入口）：与单条入口同样的两步查重，禁用的无归属老行保留
// 元数据与参与状态挂上新书，不产生第二行。
func TestAddChaptersAdoptsDisabledLegacyChapter(t *testing.T) {
	st := newTestStore(t)
	acc := seedAccount(t, st, "A")
	if _, err := st.db.Exec(`INSERT INTO pool_items
		(kind, value, title, book_id, account_id, enabled, sort_order, word_count, chapter_index, created_at)
		VALUES ('chapter', 'c2', '旧的标题', '', ?, 0, 4, 1800, 2, ?)`, acc, now()); err != nil {
		t.Fatal(err)
	}
	added, existed, conflicts, err := st.AddChapters(acc, "BOOK_B", []NewChapter{
		{Value: "c1", Title: "新的一章", WordCount: 100, ChapterIndex: 1},
		{Value: "c2", Title: "新的标题", WordCount: 2000, ChapterIndex: 2},
	})
	if err != nil {
		t.Fatalf("batch import: %v", err)
	}
	if added != 2 || existed != 0 || conflicts != 0 {
		t.Fatalf("batch import counts = %d/%d/%d, want 2/0/0", added, existed, conflicts)
	}
	items, err := st.PoolChaptersOf("BOOK_B", acc)
	if err != nil || len(items) != 2 {
		t.Fatalf("chapters of BOOK_B = %+v, err = %v, want 2", items, err)
	}
	var total int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM pool_items WHERE kind = 'chapter'`).Scan(&total); err != nil || total != 2 {
		t.Fatalf("chapter rows in store = %d, err = %v, want 2 (no duplicate inserted)", total, err)
	}
	for _, it := range items {
		if it.Value != "c2" {
			continue
		}
		if it.Enabled || it.Title != "旧的标题" || it.SortOrder != 4 || it.WordCount != 1800 || it.ChapterIndex != 2 {
			t.Fatalf("legacy chapter after adopt = %+v, want disabled with old title/sort, filled word_count", it)
		}
	}
}
