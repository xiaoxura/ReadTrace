package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"readtrace/internal/secure"
	"readtrace/internal/store"
)

func newAuthSecurityServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	box, err := secure.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "readtrace.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &Server{st: st, limiter: newLoginLimiter(), log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func TestConcurrentSetupOnlyWinnerGetsSession(t *testing.T) {
	s := newAuthSecurityServer(t)
	type result struct {
		password string
		code     int
		cookies  []*http.Cookie
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	for _, password := range []string{"first-password", "second-password"} {
		go func(password string) {
			<-start
			r := httptest.NewRequest(http.MethodPost, "/api/setup", strings.NewReader(`{"password":"`+password+`"}`))
			w := httptest.NewRecorder()
			s.handleSetup(w, r)
			results <- result{password, w.Code, w.Result().Cookies()}
		}(password)
	}
	close(start)
	var winner string
	for i := 0; i < 2; i++ {
		res := <-results
		switch res.code {
		case http.StatusOK:
			if winner != "" {
				t.Fatal("both setup requests succeeded")
			}
			winner = res.password
			if len(res.cookies) != 1 {
				t.Fatalf("winner cookies = %d", len(res.cookies))
			}
			ses, err := s.st.GetSession(res.cookies[0].Value)
			if err != nil || ses == nil {
				t.Fatalf("winner session missing: %v", err)
			}
		case http.StatusConflict:
			if len(res.cookies) != 0 {
				t.Fatal("loser received session")
			}
		default:
			t.Fatalf("setup code = %d", res.code)
		}
	}
	if winner == "" {
		t.Fatal("no setup winner")
	}
	hash, err := s.st.GetSetting(keyPasswordHash)
	if err != nil || bcrypt.CompareHashAndPassword([]byte(hash), []byte(winner)) != nil {
		t.Fatalf("winning password not stored: %v", err)
	}
}

func TestSetupClaimHoldsAuthLockUntilSessionIssued(t *testing.T) {
	s := newAuthSecurityServer(t)
	oldHash, err := bcrypt.GenerateFromPassword([]byte("old-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	claimed := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseSetup := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseSetup()
	type setupResult struct {
		won     bool
		problem string
		w       *httptest.ResponseRecorder
	}
	setupDone := make(chan setupResult, 1)
	go func() {
		r := httptest.NewRequest(http.MethodPost, "/api/setup", nil)
		w := httptest.NewRecorder()
		won, problem := s.claimSetupSession(w, r, string(oldHash), "", func() {
			close(claimed)
			<-release
		})
		setupDone <- setupResult{won: won, problem: problem, w: w}
	}()
	select {
	case <-claimed:
	case <-time.After(3 * time.Second):
		t.Fatal("setup did not claim password")
	}
	if s.authMu.TryLock() {
		s.authMu.Unlock()
		t.Fatal("setup released auth lock before issuing session")
	}

	loginDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"password":"old-password"}`))
		r.RemoteAddr = "127.0.0.1:12345"
		w := httptest.NewRecorder()
		s.handleLogin(w, r)
		loginDone <- w
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		s.limiter.mu.Lock()
		entry := s.limiter.entries["127.0.0.1"]
		reserved := entry != nil && entry.inFlight == 1
		s.limiter.mu.Unlock()
		if reserved {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("login did not reach auth lock")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-loginDone:
		t.Fatal("login issued session during setup gap")
	default:
	}
	releaseSetup()
	setup := <-setupDone
	if !setup.won || setup.problem != "" || len(setup.w.Result().Cookies()) != 1 {
		t.Fatalf("setup result: won=%v problem=%q cookies=%d", setup.won, setup.problem, len(setup.w.Result().Cookies()))
	}
	setupSession := setup.w.Result().Cookies()[0].Value
	loginW := <-loginDone
	if loginW.Code != http.StatusOK || len(loginW.Result().Cookies()) != 1 {
		t.Fatalf("login response: code=%d cookies=%d", loginW.Code, len(loginW.Result().Cookies()))
	}
	changeReq := httptest.NewRequest(http.MethodPost, "/api/settings/password",
		strings.NewReader(`{"current":"old-password","new":"new-password"}`))
	changeReq.AddCookie(loginW.Result().Cookies()[0])
	changeW := httptest.NewRecorder()
	s.handlePasswordChange(changeW, changeReq)
	if changeW.Code != http.StatusOK {
		t.Fatalf("password change response: %d %s", changeW.Code, changeW.Body.String())
	}
	stale, err := s.st.GetSession(setupSession)
	if err != nil || stale != nil {
		t.Fatalf("setup session survived later password change: session=%v err=%v", stale, err)
	}
}

func TestLoginLimiterReservesConcurrentAttempts(t *testing.T) {
	l := newLoginLimiter()
	const requests = 32
	var wg sync.WaitGroup
	start := make(chan struct{})
	attempts := make(chan *loginAttempt, requests)
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			a, _ := l.Reserve("127.0.0.1")
			attempts <- a
		}()
	}
	close(start)
	wg.Wait()
	close(attempts)
	var reserved []*loginAttempt
	for a := range attempts {
		if a != nil {
			reserved = append(reserved, a)
		}
	}
	if len(reserved) != maxLoginFails {
		t.Fatalf("reserved = %d, want %d", len(reserved), maxLoginFails)
	}
	l.mu.Lock()
	l.entries["127.0.0.1"].reset = time.Now().Add(-time.Second)
	l.mu.Unlock()
	if a, _ := l.Reserve("127.0.0.1"); a != nil {
		t.Fatal("expired window discarded in-flight reservations")
	}
	for _, a := range reserved {
		l.Settle(a, false)
	}
	if a, _ := l.Reserve("127.0.0.1"); a != nil {
		t.Fatal("failed attempts did not consume quota")
	}
	l.mu.Lock()
	l.entries["127.0.0.1"].reset = time.Now().Add(-time.Second)
	l.mu.Unlock()
	if a, _ := l.Reserve("127.0.0.1"); a == nil {
		t.Fatal("expired quota was not cleared")
	} else {
		l.Cancel(a)
	}
}

func TestOldPasswordWaitingForChangeCannotGetSession(t *testing.T) {
	s := newAuthSecurityServer(t)
	oldHash, err := bcrypt.GenerateFromPassword([]byte("old-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte("new-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.st.SetSetting(keyPasswordHash, string(oldHash)); err != nil {
		t.Fatal(err)
	}
	// Hold the API coordination lock until the login has read the old hash
	// and reserved its verification slot. Commit a password change before it
	// can verify and issue a session.
	s.authMu.Lock()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"password":"old-password"}`))
		r.RemoteAddr = "127.0.0.1:12345"
		w := httptest.NewRecorder()
		s.handleLogin(w, r)
		done <- w
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		s.limiter.mu.Lock()
		entry := s.limiter.entries["127.0.0.1"]
		reserved := entry != nil && entry.inFlight == 1
		s.limiter.mu.Unlock()
		if reserved {
			break
		}
		if time.Now().After(deadline) {
			s.authMu.Unlock()
			t.Fatal("login did not reach password lock")
		}
		time.Sleep(time.Millisecond)
	}
	changed, err := s.st.ChangePassword(string(oldHash), string(newHash), "")
	if err != nil || !changed {
		s.authMu.Unlock()
		t.Fatalf("password change: changed=%v err=%v", changed, err)
	}
	s.authMu.Unlock()
	select {
	case w := <-done:
		if w.Code != http.StatusUnauthorized || len(w.Result().Cookies()) != 0 {
			t.Fatalf("old password response = %d, cookies = %d", w.Code, len(w.Result().Cookies()))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("login did not complete")
	}
}
