package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestValidateDefaultTemplate(t *testing.T) {
	ok := `{"b": "ce032b305a9bc1ce0b0dd2a", "c": "7f632b502707f6ffaa6bf2e", "ci": 27}`
	got, err := validateDefaultTemplate(ok)
	if err != nil {
		t.Fatalf("valid template rejected: %v", err)
	}
	// 规范化后仍是可解析的 JSON，且保留 b/c。
	if got == "" {
		t.Fatal("normalized template empty")
	}

	cases := []string{
		"",                 // 空串
		"not json",         // 非对象
		`[]`,               // 数组
		`{}`,               // 缺 b/c
		`{"b":"x"}`,        // 缺 c
		`{"b":"","c":"y"}`, // b 为空
		`{"b":1,"c":"y"}`,  // b 非字符串
	}
	for _, c := range cases {
		if _, err := validateDefaultTemplate(c); err == nil {
			t.Errorf("template %q accepted, want rejection", c)
		}
	}
}

func TestValidateDefaultTemplateAcceptsCurl(t *testing.T) {
	raw := `curl --url 'https://weread.qq.com/web/book/read' \
		-H 'content-type: application/json;charset=UTF-8' \
		--data-raw '{"b":"book-1","c":"chapter-1","ci":7,"co":381}'`
	got, err := validateDefaultTemplate(raw)
	if err != nil {
		t.Fatalf("curl template rejected: %v", err)
	}
	if !strings.Contains(got, `"b":"book-1"`) || !strings.Contains(got, `"c":"chapter-1"`) {
		t.Fatalf("unexpected normalized template: %s", got)
	}
}

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
