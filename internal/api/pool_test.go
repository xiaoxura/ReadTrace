package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"readtrace/internal/weread"
)

// 书架接口必须保持“秒回”：不随车审计（目录接口对整架书意味着几百次请求）。
// 审计由 POST /api/pool/audit 按页提供，失败的书带逐本错误明细。
func TestShelfNoAuditAndAuditEndpointReturnsPaidBreakdown(t *testing.T) {
	var tocHits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/web/shelf/sync", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"bookCount":2,"books":[{"bookId":"b1","title":"B1"},{"bookId":"b2","title":"B2"}]}`))
	})
	mux.HandleFunc("/web/book/info", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("bookId")
		_, _ = w.Write([]byte(fmt.Sprintf(`{"bookId":%q,"title":%q}`, id, id)))
	})
	mux.HandleFunc("/web/book/chapterInfos", func(w http.ResponseWriter, r *http.Request) {
		tocHits.Add(1)
		var body struct {
			BookIDs []string `json:"bookIds"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body.BookIDs) == 1 && body.BookIDs[0] == "boom" {
			_, _ = w.Write([]byte(`{"errCode":-1}`))
			return
		}
		// 免费一章（price=0）+ 付费未购一章（price=-1）+ 已购一章（price=10, paid=1）。
		_, _ = w.Write([]byte(`{"data":{"bookId":` + fmt.Sprintf("%q", body.BookIDs[0]) + `,"updated":[
			{"chapterUid":1,"chapterIdx":1,"title":"免费","wordCount":300,"price":0},
			{"chapterUid":2,"chapterIdx":2,"title":"未购","wordCount":300,"price":-1},
			{"chapterUid":3,"chapterIdx":3,"title":"已购","wordCount":300,"price":10,"paid":1}]}}`))
	})
	s := newCheckTestServer(t, mux)
	id := startTestAccount(t, s)

	w := httptest.NewRecorder()
	s.handlePoolShelf(w, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/pool/shelf?account_id=%d", id), nil))
	if w.Code != http.StatusOK {
		t.Fatalf("shelf status=%d body=%s", w.Code, w.Body.String())
	}
	var shelf struct {
		Books []struct {
			ID string `json:"id"`
		} `json:"books"`
		Audit map[string]BookAudit `json:"audit"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &shelf); err != nil {
		t.Fatal(err)
	}
	if len(shelf.Books) != 2 {
		t.Fatalf("shelf books = %+v", shelf.Books)
	}
	if len(shelf.Audit) != 0 {
		t.Fatalf("shelf returned audit %+v, want none", shelf.Audit)
	}
	if tocHits.Load() != 0 {
		t.Fatalf("shelf pull hit chapterInfos %d times, want 0", tocHits.Load())
	}

	post := func(bodyString string) (map[string]BookAudit, map[string]string) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/pool/audit", strings.NewReader(bodyString))
		s.handlePoolAudit(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("audit status=%d body=%s", rec.Code, rec.Body.String())
		}
		var out struct {
			Audit  map[string]BookAudit `json:"audit"`
			Errors map[string]string    `json:"errors"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Audit, out.Errors
	}

	audit, errs := post(fmt.Sprintf(`{"account_id":%d,"books":["b1","boom","b2"]}`, id))
	if got := audit["b1"]; got.Total != 3 || got.Priced != 1 || got.Owned != 1 {
		t.Fatalf("audit[b1] = %+v, want total=3 priced=1 owned=1", got)
	}
	if a, ok := audit["boom"]; ok {
		t.Fatalf("failed book audited anyway: %+v", a)
	}
	if errs["boom"] == "" {
		t.Fatalf("errors missing boom detail: %v", errs)
	}

	// 校验失败用例：一次超过 100 本、空列表、非法 ID。
	for _, c := range []struct {
		body string
		code int
	}{
		{fmt.Sprintf(`{"account_id":%d,"books":[]}`, id), http.StatusBadRequest},
		{fmt.Sprintf(`{"account_id":%d,"books":["%s"]}`, id, strings.Repeat("x", 65)), http.StatusBadRequest},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/pool/audit", strings.NewReader(c.body))
		s.handlePoolAudit(rec, req)
		if rec.Code != c.code {
			t.Fatalf("audit %s status=%d, want %d", c.body, rec.Code, c.code)
		}
	}
}

// 登录已失效（-2012 登录超时）的书架拉取应当先自动续期再重试，成功后
// 正常返回；这与阅读引擎的自愈语义一致。
func TestShelfRenewsExpiredLoginAutomatically(t *testing.T) {
	var renewed atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/web/login/renewal", func(w http.ResponseWriter, r *http.Request) {
		renewed.Store(true)
		http.SetCookie(w, &http.Cookie{Name: "wr_skey", Value: "NEW"})
		_, _ = w.Write([]byte(`{"succ":1}`))
	})
	mux.HandleFunc("/web/shelf/sync", func(w http.ResponseWriter, r *http.Request) {
		cookie, _ := r.Cookie("wr_skey")
		if cookie == nil || cookie.Value != "NEW" {
			_, _ = w.Write([]byte(`{"errCode":-2012,"errMsg":"登录超时"}`))
			return
		}
		_, _ = w.Write([]byte(`{"bookCount":1,"books":[{"bookId":"b1","title":"B1"}]}`))
	})
	s := newCheckTestServer(t, mux)
	id := startTestAccount(t, s)

	w := httptest.NewRecorder()
	s.handlePoolShelf(w, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/pool/shelf?account_id=%d", id), nil))
	if w.Code != http.StatusOK {
		t.Fatalf("shelf after renewal status=%d body=%s", w.Code, w.Body.String())
	}
	if !renewed.Load() {
		t.Fatal("renewal was not attempted")
	}
	ck, _, err := s.st.AccountSecrets(id)
	if err != nil || ck["wr_skey"] != "NEW" {
		t.Fatalf("renewed skey not persisted: %v err=%v", ck, err)
	}

	// 非鉴权失败（如 HTTP 500 风控页）不应白白续期。
	var renewedNonAuth atomic.Bool
	mux2 := http.NewServeMux()
	mux2.HandleFunc("/web/login/renewal", func(w http.ResponseWriter, r *http.Request) {
		renewedNonAuth.Store(true)
		http.SetCookie(w, &http.Cookie{Name: "wr_skey", Value: "NEW"})
		_, _ = w.Write([]byte(`{"succ":1}`))
	})
	mux2.HandleFunc("/web/shelf/sync", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bot check", http.StatusInternalServerError)
	})
	s2 := newCheckTestServer(t, mux2)
	id2 := startTestAccount(t, s2)
	w2 := httptest.NewRecorder()
	s2.handlePoolShelf(w2, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/pool/shelf?account_id=%d", id2), nil))
	if w2.Code != http.StatusBadGateway {
		t.Fatalf("non-auth failure status=%d, want 502", w2.Code)
	}
	if renewedNonAuth.Load() {
		t.Fatal("renewal should not be attempted on non-auth failures")
	}
}

// TestAuthedCallRenewalCASDoesNotOverwriteRebind 复现探针
// TestReviewProbeStaleRenewalOverwritesRebind：阻塞旧续期响应，等待期间用
// WithIdleAccount 换绑新登录，释放响应后——旧写回必须被 CAS 跳过，库中的
// wr_skey 仍是 NEW_LOGIN。
func TestAuthedCallRenewalCASDoesNotOverwriteRebind(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/web/login/renewal", func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		http.SetCookie(w, &http.Cookie{Name: "wr_skey", Value: "OLD_SESSION_RENEWED"})
		_, _ = w.Write([]byte(`{"succ":1}`))
	})
	s := newCheckTestServer(t, mux)
	id := startTestAccount(t, s)
	acc, err := s.st.GetAccount(id)
	if err != nil || acc == nil {
		t.Fatalf("GetAccount: %v %v", acc, err)
	}
	ck, err := s.accountCookies(acc)
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		first := true
		finished <- s.authedCall(context.Background(), acc, ck, func() error {
			if first {
				first = false
				return weread.ErrUnauthorized
			}
			return nil
		})
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("renewal not reached")
	}
	if err := s.eng.WithIdleAccount(id, func() error {
		return s.st.SaveCookies(id, map[string]string{"wr_vid": "1", "wr_skey": "NEW_LOGIN"})
	}); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	saved, _, err := s.st.AccountSecrets(id)
	if err != nil {
		t.Fatal(err)
	}
	if saved["wr_skey"] != "NEW_LOGIN" {
		t.Fatalf("stale renewal overwrote the rebind, skey=%q", saved["wr_skey"])
	}
}

// TestAuthedCallRenewalCASWritesBackWhenUnchanged 正常续期路径：库未被第三方
// 改动时，CAS 通过，续期结果正常写回。
func TestAuthedCallRenewalCASWritesBackWhenUnchanged(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/web/login/renewal", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "wr_skey", Value: "NEW"})
		_, _ = w.Write([]byte(`{"succ":1}`))
	})
	s := newCheckTestServer(t, mux)
	id := startTestAccount(t, s)
	acc, err := s.st.GetAccount(id)
	if err != nil || acc == nil {
		t.Fatalf("GetAccount: %v %v", acc, err)
	}
	ck, err := s.accountCookies(acc)
	if err != nil {
		t.Fatal(err)
	}
	callErr := s.authedCall(context.Background(), acc, ck, func() error { return weread.ErrUnauthorized })
	if callErr == nil {
		t.Fatal("auth failure must still propagate")
	}
	saved, _, err := s.st.AccountSecrets(id)
	if err != nil {
		t.Fatal(err)
	}
	if saved["wr_skey"] != "NEW" {
		t.Fatalf("normal renewal write-back missing: %q", saved["wr_skey"])
	}
}
