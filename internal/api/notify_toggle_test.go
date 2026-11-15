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

// TestAccountNotifyOnSuccessToggle 账号更新接口能读写「阅读完成推送」开关：
// 默认开启（建号时即可查询到 true），PATCH false/true 都可落库，响应里也
// 带上该字段供前端渲染勾选框。
func TestAccountNotifyOnSuccessToggle(t *testing.T) {
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
	id, err := st.CreateAccount(store.Account{Name: "notify", NotifyOnSuccess: true}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	eng := engine.New(st, nil, nil, engine.NewHub(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	s := &Server{st: st, eng: eng}
	patch := func(on bool) *httptest.ResponseRecorder {
		t.Helper()
		body := fmt.Sprintf(`{"notify_on_success":%t}`, on)
		r := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(body))
		r.SetPathValue("id", fmt.Sprint(id))
		w := httptest.NewRecorder()
		s.handleAccountUpdate(w, r)
		return w
	}

	if w := patch(false); w.Code != http.StatusOK {
		t.Fatalf("关闭完成推送失败: %d %s", w.Code, w.Body.String())
	}
	acc, err := st.GetAccount(id)
	if err != nil || acc == nil || acc.NotifyOnSuccess {
		t.Fatalf("关闭未落库: %+v err=%v", acc, err)
	}
	w := patch(true)
	if w.Code != http.StatusOK {
		t.Fatalf("重新开启失败: %d %s", w.Code, w.Body.String())
	}
	acc, err = st.GetAccount(id)
	if err != nil || acc == nil || !acc.NotifyOnSuccess {
		t.Fatalf("开启未落库: %+v err=%v", acc, err)
	}
	if !strings.Contains(w.Body.String(), `"notify_on_success":true`) {
		t.Fatalf("响应缺少 notify_on_success 字段: %s", w.Body.String())
	}
}
