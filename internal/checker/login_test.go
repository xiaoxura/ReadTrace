package checker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"readtrace/internal/engine"
	"readtrace/internal/secure"
	"readtrace/internal/store"
	"readtrace/internal/weread"
)

type checkerRoundTripFunc func(*http.Request) (*http.Response, error)

func (f checkerRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func loginTestStore(t *testing.T) (*store.Store, int64) {
	t.Helper()
	dir := t.TempDir()
	box, err := secure.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "checker.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	id, err := st.CreateAccount(store.Account{Name: "test"}, map[string]string{"wr_skey": "old"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return st, id
}

func TestCheckLoginRenewalClassification(t *testing.T) {
	for _, tc := range []struct {
		name        string
		userStatus  int
		userBody    string
		renewStatus int
		renewBody   string
		outcome     string
		authError   bool
	}{
		{"riskThen503", 200, "<html>risk</html>", 503, "server unavailable", "error", false},
		{"http401Then503", 401, "unauthorized", 503, "server unavailable", "error", false},
		{"http401Then401", 401, "unauthorized", 401, "unauthorized", "expired", true},
		{"riskThenAuthCode", 200, "<html>risk</html>", 200, `{"errCode":-2012}`, "expired", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/web/user":
					w.WriteHeader(tc.userStatus)
					_, _ = w.Write([]byte(tc.userBody))
				case "/web/login/renewal":
					w.WriteHeader(tc.renewStatus)
					_, _ = w.Write([]byte(tc.renewBody))
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			st, id := loginTestStore(t)
			wc := weread.NewClient()
			wc.Base = srv.URL
			log := slog.New(slog.NewTextHandler(io.Discard, nil))
			eng := engine.New(st, wc, nil, engine.NewHub(), log)
			outcome, err := CheckLoginWithEngine(context.Background(), st, wc, eng, id, weread.Cookies{"wr_skey": "old"})
			if outcome != tc.outcome || err == nil || errors.Is(err, weread.ErrUnauthorized) != tc.authError {
				t.Fatalf("outcome=%q err=%v, want %q auth=%v", outcome, err, tc.outcome, tc.authError)
			}
		})
	}
}

func TestCheckLoginReadsLatestCookiesUnderAccountLock(t *testing.T) {
	seen := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cookie, err := r.Cookie("wr_skey"); err == nil {
			seen <- cookie.Value
		}
		_, _ = w.Write([]byte(`{"errCode":0}`))
	}))
	defer srv.Close()
	st, id := loginTestStore(t)
	if err := st.SaveCookies(id, map[string]string{"wr_skey": "new"}); err != nil {
		t.Fatal(err)
	}
	wc := weread.NewClient()
	wc.Base = srv.URL
	eng := engine.New(st, wc, nil, engine.NewHub(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	outcome, err := CheckLoginWithEngine(context.Background(), st, wc, eng, id, weread.Cookies{"wr_skey": "old"})
	if outcome != "active" || err != nil {
		t.Fatalf("outcome=%q err=%v", outcome, err)
	}
	select {
	case key := <-seen:
		if key != "new" {
			t.Fatalf("validated stale cookie %q", key)
		}
	default:
		t.Fatal("no validation request")
	}
}

func TestRiskControlThenRenewalTimeoutStaysUncertain(t *testing.T) {
	st, id := loginTestStore(t)
	wc := weread.NewClient()
	wc.HTTP.Transport = checkerRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/web/login/renewal" {
			return nil, context.DeadlineExceeded
		}
		return &http.Response{StatusCode: 200, Header: http.Header{},
			Body: io.NopCloser(strings.NewReader("<html>risk</html>"))}, nil
	})
	eng := engine.New(st, wc, nil, engine.NewHub(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	outcome, err := CheckLoginWithEngine(context.Background(), st, wc, eng, id, weread.Cookies{"wr_skey": "old"})
	if outcome != "error" || err == nil || errors.Is(err, weread.ErrUnauthorized) {
		t.Fatalf("risk control + timeout = %q/%v", outcome, err)
	}
}

func TestCheckAndStatusCommitHoldAccountLock(t *testing.T) {
	st, id := loginTestStore(t)
	wc := weread.NewClient()
	wc.HTTP.Transport = checkerRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusUnauthorized, Header: http.Header{},
			Body: io.NopCloser(strings.NewReader("unauthorized"))}, nil
	})
	eng := engine.New(st, wc, nil, engine.NewHub(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	inUpdate := make(chan struct{})
	release := make(chan struct{})
	checked := make(chan error, 1)
	go func() {
		outcome, checkErr, opErr := CheckLoginAndUpdate(context.Background(), st, wc, eng, id,
			func(_ weread.Cookies, outcome string, _ error) error {
				if outcome != "expired" {
					return errors.New("expected explicit expiry")
				}
				if err := st.SetAccountStatus(id, "expired", "old credentials", time.Now().Unix()); err != nil {
					return err
				}
				close(inUpdate)
				<-release
				return nil
			})
		if outcome != "expired" || checkErr == nil {
			checked <- errors.New("expected expired check")
			return
		}
		checked <- opErr
	}()
	select {
	case <-inUpdate:
	case <-time.After(2 * time.Second):
		close(release)
		<-checked
		t.Fatal("check did not reach status commit")
	}
	rebound := make(chan error, 1)
	go func() {
		rebound <- eng.WithIdleAccount(id, func() error {
			if err := st.SaveCookies(id, map[string]string{"wr_skey": "new"}); err != nil {
				return err
			}
			return st.SetAccountStatus(id, "active", "", time.Now().Unix())
		})
	}()
	var premature bool
	select {
	case <-rebound:
		premature = true
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-checked; err != nil {
		t.Fatal(err)
	}
	if premature {
		t.Fatal("rebind crossed in-progress status commit")
	}
	if err := <-rebound; err != nil {
		t.Fatal(err)
	}
	acc, err := st.GetAccount(id)
	if err != nil {
		t.Fatal(err)
	}
	if acc.Status != "active" {
		t.Fatalf("old check overwrote rebind status: %+v", acc)
	}
}
