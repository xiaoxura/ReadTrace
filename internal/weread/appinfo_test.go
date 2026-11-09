package weread

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const sampleChromeLinuxUA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/152.0.0.0 Safari/537.36"

// 该样例与一次真实 read 心跳的 appId 相互验证（2026-11 捕获）。
func TestDeriveAppIDMatchesLiveCapture(t *testing.T) {
	got := DeriveAppID(sampleChromeLinuxUA)
	if got != "wb1557874663h565891605" {
		t.Fatalf("DeriveAppID = %s, want wb1557874663h565891605", got)
	}
}

func TestDeriveAppIDPanelUAShape(t *testing.T) {
	// 默认 UA 是 12 token 的标准 Chrome 串：前缀 12 位数字，且整体形如 wb…h…。
	got := DeriveAppID("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	if len(got) < 8 || got[:2] != "wb" {
		t.Fatalf("unexpected appid shape: %s", got)
	}
	if i := indexByte(got, 'h'); i < 4 || i == len(got)-1 {
		t.Fatalf("unexpected appid %q, want wb<prefix>h<hash>", got)
	}
}

func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

// reader 页：psvts 来自 SSR 的 __INITIAL_STATE__；pclts 缺失时返回空，由调用
// 方用 _e(ct) 兜底，与官方前端语义一致。
func TestFetchReaderStateParsesInitialAppState(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/web/reader/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<!DOCTYPE html><html>
		<script>window.__INITIAL_STATE__={"reader":{"psvts":"psv123","bookInfo":{"bookId":"CB_test","title":"个人书"}},"other":1};(function(){})();</script>
		</html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient()
	c.Base = srv.URL
	st, err := c.FetchReaderState(context.Background(), Cookies{"wr_skey": "x", "wr_vid": "1"}, "CB_test")
	if err != nil {
		t.Fatalf("FetchReaderState: %v", err)
	}
	if st.PSVTS != "psv123" || st.BookID != "CB_test" || st.Title != "个人书" {
		t.Fatalf("reader state = %+v", st)
	}
	if st.PCLTS != "" {
		t.Fatalf("pclts should be empty when missing, got %q", st.PCLTS)
	}
}

func TestFetchReaderStateParsesPCLTS(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/web/reader/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<script>window.__INITIAL_STATE__={"reader":{"psvts":"psvA","pclts":"pcltsB"}};
		</script>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient()
	c.Base = srv.URL
	st, err := c.FetchReaderState(context.Background(), Cookies{}, "somebook")
	if err != nil {
		t.Fatalf("FetchReaderState: %v", err)
	}
	if st.PCLTS != "pcltsB" || st.PSVTS != "psvA" {
		t.Fatalf("reader state = %+v", st)
	}
}

func TestFetchReaderStateRejectsLoginPage(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/web/reader/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body>请先登录微信读书</body></html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient()
	c.Base = srv.URL
	if _, err := c.FetchReaderState(context.Background(), Cookies{}, "book"); err == nil {
		t.Fatal("want failure when __INITIAL_STATE__ missing")
	}
}
