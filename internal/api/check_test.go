package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"readtrace/internal/engine"
	"readtrace/internal/secure"
	"readtrace/internal/store"
	"readtrace/internal/weread"
)

// newCheckTestServer wires a Store + Engine + weread.Client pointing at a live
// httptest server acting as weread.qq.com, so checkLoginClassified can be
// exercised end to end.
func newCheckTestServer(t *testing.T, mux *http.ServeMux) *Server {
	t.Helper()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	box, err := secure.New(dir)
	if err != nil {
		t.Fatalf("secure.New: %v", err)
	}
	st, err := store.Open(filepath.Join(dir, "api-test.db"), box)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := weread.NewClient()
	client.Base = srv.URL
	eng := engine.New(st, client, nil, engine.NewHub(), log)
	return &Server{st: st, eng: eng, wc: client, log: log}
}

func startTestAccount(t *testing.T, s *Server) int64 {
	t.Helper()
	id, err := s.st.CreateAccount(store.Account{Name: "t"}, map[string]string{"wr_vid": "1", "wr_skey": "OLD"}, nil)
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	return id
}

// /web/user refuses with -2003 while renewal still hands out a fresh skey:
// the session is alive, so the outcome must be "active".
func TestCheckLoginProbeRenewal(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/web/user", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"errCode":-2003}`))
	})
	mux.HandleFunc("/web/login/renewal", func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "wr_skey", Value: "NEW"})
		_, _ = w.Write([]byte(`{"succ":1}`))
	})
	s := newCheckTestServer(t, mux)
	id := startTestAccount(t, s)

	outcome, err := s.checkLoginClassified(context.Background(), id, weread.Cookies{"wr_vid": "1", "wr_skey": "OLD"})
	if outcome != "active" || err != nil {
		t.Fatalf("want active/nil, got %s/%v", outcome, err)
	}

	secrets, _, err := s.st.AccountSecrets(id)
	if err != nil {
		t.Fatalf("AccountSecrets: %v", err)
	}
	if secrets["wr_skey"] != "NEW" {
		t.Fatalf("renewed cookie not saved: %v", secrets)
	}
}

// A real auth failure the renewal cannot fix must come back as expired.
func TestCheckLoginAuthFailureExpired(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/web/user", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"errCode":-2012}`))
	})
	mux.HandleFunc("/web/login/renewal", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"errCode":-2012}`)) // no Set-Cookie → renewal failed
	})
	s := newCheckTestServer(t, mux)
	id := startTestAccount(t, s)

	outcome, _ := s.checkLoginClassified(context.Background(), id, weread.Cookies{"wr_vid": "1", "wr_skey": "OLD"})
	if outcome != "expired" {
		t.Fatalf("want expired, got %s", outcome)
	}
}

// While the account is running, unknown failures must not rotate the skey.
func TestCheckLoginRunningNoProbe(t *testing.T) {
	var renews atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/web/user", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"errCode":-2003}`))
	})
	mux.HandleFunc("/web/login/renewal", func(w http.ResponseWriter, _ *http.Request) {
		renews.Add(1)
		http.SetCookie(w, &http.Cookie{Name: "wr_skey", Value: "NEW"})
		_, _ = w.Write([]byte(`{"succ":1}`))
	})
	mux.HandleFunc("/web/book/read", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"succ":1,"synckey":1}`))
	})
	s := newCheckTestServer(t, mux)
	id := startTestAccount(t, s)
	// 新版移除了内置模板兜底：所有模式都需要一份阅读模板作为请求基底。
	if err := s.st.SetSetting(store.KeyDefaultReadPayload, `{"b":"ce032b305a9bc1ce0b0dd2a","c":"7f632b502707f6ffaa6bf2e"}`); err != nil {
		t.Fatalf("set default template: %v", err)
	}
	// 顺序书单（默认模式）需要书池里有带章节的书。
	if _, err := s.st.AddPoolItem(store.KindBook, "book1", "测试书", id); err != nil {
		t.Fatalf("add pool book: %v", err)
	}
	if _, err := s.st.AddChapterItem(id, "book1", "chap1", "测试书 · 第一章", 3000); err != nil {
		t.Fatalf("add pool chapter: %v", err)
	}

	acc, err := s.st.GetAccount(id)
	if err != nil || acc == nil {
		t.Fatalf("GetAccount: %v %v", acc, err)
	}
	if _, err := s.eng.Start(acc, 1, "manual"); err != nil {
		t.Fatalf("engine start: %v", err)
	}
	t.Cleanup(s.eng.StopAll)

	// The engine's own upfront Renew runs asynchronously; wait for it so the
	// baseline below does not race with it.
	deadline := time.Now().Add(2 * time.Second)
	for renews.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	before := renews.Load()
	outcome, checkErr := s.checkLoginClassified(context.Background(), id, weread.Cookies{"wr_vid": "1", "wr_skey": "OLD"})
	if outcome != "error" || checkErr == nil {
		t.Fatalf("want error outcome while running, got %s/%v", outcome, checkErr)
	}
	if renews.Load() != before {
		t.Fatalf("renewal must not be probed while the account is running (%d calls)", renews.Load()-before)
	}
}

func TestRefreshUsesRenewedCookieAndPreservesIdentityOnUserInfoFailure(t *testing.T) {
	seenUserInfoKey := make(chan string, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/web/user", func(w http.ResponseWriter, r *http.Request) {
		cookie, _ := r.Cookie("wr_skey")
		if cookie == nil || cookie.Value != "NEW" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"errCode":0}`))
	})
	mux.HandleFunc("/web/login/renewal", func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "wr_skey", Value: "NEW"})
		_, _ = w.Write([]byte(`{"succ":1}`))
	})
	mux.HandleFunc("/api/userInfo", func(w http.ResponseWriter, r *http.Request) {
		cookie, _ := r.Cookie("wr_skey")
		if cookie != nil {
			seenUserInfoKey <- cookie.Value
		}
		_, _ = w.Write([]byte(`{}`)) // lookup has no identity fields
	})
	s := newCheckTestServer(t, mux)
	id := startTestAccount(t, s)
	if err := s.st.UpdateIdentity(id, "1", "Saved Nick", "saved-avatar"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/1/refresh", nil)
	req.SetPathValue("id", strconv.FormatInt(id, 10))
	rec := httptest.NewRecorder()
	s.handleAccountRefresh(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh status=%d body=%s", rec.Code, rec.Body.String())
	}
	select {
	case key := <-seenUserInfoKey:
		if key != "NEW" {
			t.Fatalf("UserInfo used %q, want NEW", key)
		}
	default:
		t.Fatal("UserInfo not requested")
	}
	acc, err := s.st.GetAccount(id)
	if err != nil {
		t.Fatal(err)
	}
	if acc.Nickname != "Saved Nick" || acc.Avatar != "saved-avatar" || acc.Status != "active" {
		t.Fatalf("refresh erased identity or status: %+v", acc)
	}
	ck, _, err := s.st.AccountSecrets(id)
	if err != nil || ck["wr_skey"] != "NEW" {
		t.Fatalf("saved cookies=%v err=%v", ck, err)
	}
}
