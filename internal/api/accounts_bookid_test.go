package api

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"readtrace/internal/engine"
	"readtrace/internal/secure"
	"readtrace/internal/store"
)

// TestAccountCustomBookIDEntryLimit 转正归档缺陷 21 的入口统一部分：账号
// 自定义 book_id 的长度/形态上限与手动书池一致（64 rune、无空白字符）。
func TestAccountCustomBookIDEntryLimit(t *testing.T) {
	dir := t.TempDir()
	box, err := secure.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "api.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	id, err := st.CreateAccount(store.Account{Name: "bookid"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	eng := engine.New(st, nil, nil, engine.NewHub(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	s := &Server{st: st, eng: eng}
	update := func(bookID string) *httptest.ResponseRecorder {
		t.Helper()
		body := fmt.Sprintf(`{"name":"bookid","book_id":%s}`, mustJSONString(bookID))
		r := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(body))
		r.SetPathValue("id", fmt.Sprint(id))
		w := httptest.NewRecorder()
		s.handleAccountUpdate(w, r)
		return w
	}
	if w := update("24-bit-hex-book-id"); w.Code != http.StatusOK {
		t.Fatalf("正常 ID 被拒: %d %s", w.Code, w.Body.String())
	}
	// 66 字符的真实抓包编码形态：超长，与书池入口同语义被拒。
	long66 := "17c424b3643425f343553424447424543396a66373456373542416457376a70879"
	if w := update(long66); w.Code != http.StatusBadRequest {
		t.Fatalf("超长 ID 未被拒: %d", w.Code)
	}
	// 含空白字符。
	if w := update("book id with space"); w.Code != http.StatusBadRequest {
		t.Fatalf("空白字符 ID 未被拒: %d", w.Code)
	}
	// 空字符串合法（回落书池）。
	if w := update(""); w.Code != http.StatusOK {
		t.Fatalf("空书 ID 应回落书池: %d", w.Code)
	}
	acc, err := st.GetAccount(id)
	if err != nil {
		t.Fatal(err)
	}
	if acc.BookID != "" {
		t.Fatalf("book_id = %q", acc.BookID)
	}
	// 生效值再取一遍，确认 24 位 ID 已写入。
	if w := update("24-bit-hex-book-id"); w.Code != http.StatusOK {
		t.Fatalf("正常 ID 未生效: %d", w.Code)
	}
	acc, err = st.GetAccount(id)
	if err != nil || acc.BookID != "24-bit-hex-book-id" {
		t.Fatalf("book_id 未写入 acc=%+v err=%v", acc, err)
	}
}

func mustJSONString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString("\\\"")
		case '\\':
			b.WriteString("\\\\")
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
