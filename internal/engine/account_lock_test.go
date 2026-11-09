package engine

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"readtrace/internal/secure"
	"readtrace/internal/store"
	"readtrace/internal/weread"
)

func TestStartReadsCredentialSavedUnderAccountLock(t *testing.T) {
	dir := t.TempDir()
	box, err := secure.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "readtrace.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	id, err := st.CreateAccount(store.Account{Name: "test", Mode: "custom", BookID: "book"},
		map[string]string{"wr_vid": "1", "wr_skey": "OLD"}, map[string]any{"b": "book", "c": "chapter"})
	if err != nil {
		t.Fatal(err)
	}
	// 缺陷 05：custom 指定书籍且章节为空需要书池可用章节。
	addReadablePoolChapter(t, st, id, "book", "chapter")
	acc, err := st.GetAccount(id)
	if err != nil || acc == nil {
		t.Fatalf("GetAccount: %v", err)
	}
	seenCookie := make(chan string, 1)
	releaseHTTP := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("wr_skey"); err == nil {
			once.Do(func() { seenCookie <- c.Value })
		}
		select {
		case <-releaseHTTP:
		case <-r.Context().Done():
		}
		_, _ = w.Write([]byte(`{"errCode":-2012}`))
	}))
	defer srv.Close()
	wc := weread.NewClient()
	wc.Base = srv.URL
	e := New(st, wc, nil, NewHub(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer e.StopAll()

	entered := make(chan struct{})
	release := make(chan struct{})
	saved := make(chan error, 1)
	go func() {
		saved <- e.WithIdleAccount(id, func() error {
			if err := st.SaveCookies(id, map[string]string{"wr_vid": "1", "wr_skey": "NEW"}); err != nil {
				return err
			}
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	started := make(chan error, 1)
	go func() {
		_, err := e.Start(acc, 1, "manual")
		started <- err
	}()
	select {
	case err := <-started:
		close(release)
		t.Fatalf("Start returned while credential replacement held lock: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-saved; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-started:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not complete")
	}
	select {
	case cookie := <-seenCookie:
		if cookie != "NEW" {
			t.Fatalf("run used %q cookie, want NEW", cookie)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run made no request")
	}
	if err := e.WithIdleAccount(id, func() error { return nil }); err != ErrAccountRunning {
		t.Fatalf("credential replacement while active: %v", err)
	}
	close(releaseHTTP)
}
