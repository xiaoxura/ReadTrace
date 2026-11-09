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

// 「每分钟阅读字数」入口校验：缺省不改动、0 → 默认 300、10~3000 生效、
// 1~9 / >3000 / 负数拒绝。
func TestAccountReadSpeedValidation(t *testing.T) {
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
	id, err := st.CreateAccount(store.Account{Name: "speed"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	eng := engine.New(st, nil, nil, engine.NewHub(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	s := &Server{st: st, eng: eng}
	update := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(body))
		r.SetPathValue("id", fmt.Sprint(id))
		w := httptest.NewRecorder()
		s.handleAccountUpdate(w, r)
		return w
	}

	// 缺省不改：保持库里的当前值。
	if w := update(`{"name":"speed"}`); w.Code != http.StatusOK {
		t.Fatalf("缺省 readSpeed 被拒: %d %s", w.Code, w.Body.String())
	}
	acc, _ := st.GetAccount(id)
	if acc.ReadSpeed != 300 {
		t.Fatalf("缺省更新后 read_speed = %d, want 300", acc.ReadSpeed)
	}

	// 0 → 恢复默认 300；正常值直接落库。
	if w := update(`{"readSpeed":25}`); w.Code != http.StatusOK {
		t.Fatalf("25 被拒: %d %s", w.Code, w.Body.String())
	}
	if acc, _ = st.GetAccount(id); acc.ReadSpeed != 25 {
		t.Fatalf("read_speed = %d, want 25", acc.ReadSpeed)
	}
	if w := update(`{"readSpeed":0}`); w.Code != http.StatusOK {
		t.Fatalf("0 被拒: %d %s", w.Code, w.Body.String())
	}
	if acc, _ = st.GetAccount(id); acc.ReadSpeed != 300 {
		t.Fatalf("0 更新后 read_speed = %d, want 默认 300", acc.ReadSpeed)
	}
	if w := update(`{"readSpeed":3000}`); w.Code != http.StatusOK {
		t.Fatalf("3000 被拒: %d %s", w.Code, w.Body.String())
	}

	// 拒绝 1~9、>3000、负数。
	for _, bad := range []int{1, 5, 9, 3001, 99999, -1} {
		body := fmt.Sprintf(`{"readSpeed":%d}`, bad)
		w := update(body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("readSpeed=%d 未被拒: %d %s", bad, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "每分钟阅读字数") {
			t.Fatalf("readSpeed=%d 错误文案不像字段提示: %s", bad, w.Body.String())
		}
	}
	acc, _ = st.GetAccount(id)
	if acc.ReadSpeed != 3000 {
		t.Fatalf("被拒的请求不应写库: read_speed = %d", acc.ReadSpeed)
	}

	// 响应 JSON 带出 read_speed 字段。
	if w := update(`{"readSpeed":600}`); !strings.Contains(w.Body.String(), `"read_speed":600`) {
		t.Fatalf("响应缺少 read_speed: %s", w.Body.String())
	}
}

// 创建（导入账号）请求同样接受可选 readSpeed。
func TestAccountImportReadSpeed(t *testing.T) {
	// parseImport 需要 Cookie 内容，走最短路径：只读 body 的 readSpeed 校验。
	// 这里直接复用 handleAccountImport 的入参校验分支，不联网。
	s := &Server{}
	handled := false
	for _, body := range []string{`{"readSpeed":3}`, `{"readSpeed":4000}`} {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		w := httptest.NewRecorder()
		s.handleAccountImport(w, r)
		handled = handled || (w.Code == http.StatusBadRequest && strings.Contains(w.Body.String(), "每分钟阅读字数"))
	}
	if !handled {
		t.Fatal("导入请求的 readSpeed 越界值未被拒绝")
	}
}
