package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"readtrace/internal/weread"
)

// 一键生成模板：目录（chapterInfos）给章节下标，阅读页给 psvts/pclts，
// appId 由构造的 UA 推导；最终 SavePayload 存入账号专属模板。
func TestGenerateAccountPayloadWithoutCapture(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/web/book/chapterInfos", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"bookId":"43208843","updated":[
			{"chapterUid":1,"chapterIdx":1,"title":"封面","wordCount":2},
			{"chapterUid":2,"chapterIdx":2,"title":"第一章 出发","wordCount":3000}]}}`))
	})
	mux.HandleFunc("/web/reader/c9c321c07293508bc9c79df", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<script>window.__INITIAL_STATE__={"reader":{"psvts":"psv-live","pclts":"pc-live"}}
		</script>`))
	})
	s := newCheckTestServer(t, mux)
	s.wc.UA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/152.0.0.0 Safari/537.36"
	id := startTestAccount(t, s)
	if _, err := s.st.AddPoolItem("book", "43208843", "测试书", id); err != nil {
		t.Fatalf("add book: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/x/payload/generate", strings.NewReader("{}"))
	req.SetPathValue("id", strconv.FormatInt(id, 10))
	s.handleAccountGeneratePayload(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("generate status=%d body=%s", rec.Code, rec.Body.String())
	}

	ck, payload, err := s.st.AccountSecrets(id)
	if err != nil || len(payload) == 0 {
		t.Fatalf("payload saved=%v err=%v", payload, err)
	}
	if got := payload["appId"]; got != "wb1557874663h565891605" {
		cj, _ := json.Marshal(payload["appId"])
		t.Fatalf("appId = %s (cookies %v), want UA-derived value", cj, ck)
	}
	if wantB := weread.NormalizePayloadID("43208843"); payload["b"] != wantB {
		t.Fatalf("b = %v, want %s", payload["b"], wantB)
	}
	if wantC := weread.ChapterIDFromUID(2); payload["c"] != wantC {
		t.Fatalf("c = %v, want %s", payload["c"], wantC)
	}
	if v, ok := payload["ci"].(float64); !ok || int(v) != 2 {
		t.Fatalf("ci = %v, want chapterIdx 2 (封面被过滤)", payload["ci"])
	}
	if payload["ps"] != "psv-live" || payload["pc"] != "pc-live" {
		t.Fatalf("ps/pc = %v/%v, want reader page values", payload["ps"], payload["pc"])
	}

	// 书池为空时报错而非造模板。
	empty := startTestAccount(t, s)
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/api/accounts/x/payload/generate", strings.NewReader("{}"))
	req2.SetPathValue("id", strconv.FormatInt(empty, 10))
	s.handleAccountGeneratePayload(rec2, req2)
	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("generate with empty pool status=%d, want 400", rec2.Code)
	}
}
