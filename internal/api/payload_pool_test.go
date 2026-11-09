package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"readtrace/internal/secure"
	"readtrace/internal/store"
)

func TestAccountTemplateWithoutCredentials(t *testing.T) {
	s := newAuthSecurityServer(t)
	id, err := s.st.CreateAccount(store.Account{Name: "template"}, map[string]string{"wr_skey": "keep"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []map[string]string{
		{"payload": `{"b":"book","c":"chapter","s":"old"}`},
		{"curl": `curl 'https://weread.qq.com/web/book/read' --data-raw '{"b":"book","c":"chapter","sg":"old"}'`},
		{"curl": `{"b":"book","c":"chapter"}`},
	} {
		data, _ := json.Marshal(body)
		r := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(data))
		r.SetPathValue("id", fmt.Sprint(id))
		w := httptest.NewRecorder()
		s.handleAccountPayload(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("save %v: %d %s", body, w.Code, w.Body.String())
		}
		cookies, payload, err := s.st.AccountSecrets(id)
		if err != nil || cookies["wr_skey"] != "keep" || payload["b"] != "book" || payload["c"] != "chapter" || payload["s"] != nil || payload["sg"] != nil {
			t.Fatalf("saved payload=%v cookies=%v err=%v", payload, cookies, err)
		}
	}
	data, _ := json.Marshal(map[string]string{"payload": `{"b":"book"}`})
	r := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(data))
	r.SetPathValue("id", fmt.Sprint(id))
	w := httptest.NewRecorder()
	s.handleAccountPayload(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid template: %d", w.Code)
	}
}

func TestManualChaptersKeepBookAndAccount(t *testing.T) {
	s := newAuthSecurityServer(t)
	a, err := s.st.CreateAccount(store.Account{Name: "A"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.st.CreateAccount(store.Account{Name: "B"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.st.AddPoolItem(store.KindBook, "book-a", "A", a)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.st.AddPoolItem(store.KindBook, "book-b", "B", b)
	if err != nil {
		t.Fatal(err)
	}
	// Explicit re-import also repairs a legacy unassociated manual chapter.
	_, err = s.st.AddPoolItem(store.KindChapter, "one", "", b)
	if err != nil {
		t.Fatal(err)
	}
	call := func(body map[string]any, single bool) *httptest.ResponseRecorder {
		data, _ := json.Marshal(body)
		r := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(data))
		w := httptest.NewRecorder()
		if single {
			s.handlePoolAdd(w, r)
		} else {
			s.handlePoolImport(w, r)
		}
		return w
	}
	w := call(map[string]any{"kind": "chapter", "text": "one two", "account_id": b, "book_id": "book-b"}, false)
	if w.Code != http.StatusOK {
		t.Fatalf("bulk: %d %s", w.Code, w.Body.String())
	}
	w = call(map[string]any{"kind": "chapter", "value": "three", "account_id": b, "book_id": "book-b"}, true)
	if w.Code != http.StatusOK {
		t.Fatalf("single: %d %s", w.Code, w.Body.String())
	}
	chapters, err := s.st.PoolChaptersOf("book-b", b)
	if err != nil || len(chapters) != 3 {
		t.Fatalf("linked chapters=%v err=%v", chapters, err)
	}
	for _, ch := range chapters {
		if ch.BookID != "book-b" || ch.AccountID != b {
			t.Fatalf("bad association: %+v", ch)
		}
	}
	for _, parent := range []string{"", "book-a"} {
		w = call(map[string]any{"kind": "chapter", "text": "unlinked", "account_id": b, "book_id": parent}, false)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("parent %q: %d %s", parent, w.Code, w.Body.String())
		}
	}
	other, err := s.st.PoolChaptersOf("book-b", a)
	if err != nil || len(other) != 0 {
		t.Fatalf("other account chapters=%v err=%v", other, err)
	}
}

func TestShelfImportValidatesAccountAndCookiesBeforeWriting(t *testing.T) {
	s := newAuthSecurityServer(t)
	id, err := s.st.CreateAccount(store.Account{Name: "no cookies"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	call := func(accountID int64, withChapters bool) *httptest.ResponseRecorder {
		data, _ := json.Marshal(map[string]any{
			"account_id": accountID, "with_chapters": withChapters,
			"books": []map[string]string{{"book_id": "imported-book", "title": "test"}},
		})
		w := httptest.NewRecorder()
		s.handlePoolShelfImport(w, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(data)))
		return w
	}
	for _, tc := range []struct {
		account  int64
		chapters bool
	}{{id + 100, false}, {-1, false}, {id, true}} {
		w := call(tc.account, tc.chapters)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid import %+v: %d %s", tc, w.Code, w.Body.String())
		}
		items, err := s.st.PoolItemsForAccount(store.KindBook, id)
		if err != nil || len(items) != 0 {
			t.Fatalf("rejected import persisted book: %v err=%v", items, err)
		}
	}
	// The default account resolves to the actual owner. Book-only imports do
	// not need remote credentials and never create shared/unowned entries.
	w := call(0, false)
	if w.Code != http.StatusOK {
		t.Fatalf("local import: %d %s", w.Code, w.Body.String())
	}
	items, err := s.st.PoolItemsForAccount(store.KindBook, id)
	if err != nil || len(items) != 1 || items[0].AccountID != id {
		t.Fatalf("book owner=%v err=%v", items, err)
	}
}

func TestShelfImportUnreadableCookiesDoesNotPersistBooks(t *testing.T) {
	dir := t.TempDir()
	box, err := secure.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "test.db")
	st, err := store.Open(path, box)
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateAccount(store.Account{Name: "encrypted"}, map[string]string{"wr_skey": "secret"}, nil)
	if err != nil {
		st.Close()
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	wrongBox, err := secure.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(path, wrongBox)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := &Server{st: st}
	body := fmt.Sprintf(`{"account_id":%d,"with_chapters":true,"books":[{"book_id":"no-write","title":"test"}]}`, id)
	w := httptest.NewRecorder()
	s.handlePoolShelfImport(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "解密") {
		t.Fatalf("unreadable credentials: %d %s", w.Code, w.Body.String())
	}
	items, err := st.PoolItemsForAccount(store.KindBook, id)
	if err != nil || len(items) != 0 {
		t.Fatalf("rejected import persisted book: %v err=%v", items, err)
	}
}
