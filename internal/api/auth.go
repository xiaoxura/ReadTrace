package api

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"readtrace/internal/secure"
)

const (
	keyPasswordHash = "password_hash"
	keySiteName     = "site_name"
	keyTimezone     = "timezone"

	sessionCookieName = "readtrace_session"
	sessionTTL        = 30 * 24 * time.Hour
)

func sessionID(r *http.Request) string {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, id string) {
	secureCookie := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    id,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secureCookie,
		MaxAge:   int(sessionTTL.Seconds()),
		Expires:  time.Now().Add(sessionTTL),
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: "", Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, MaxAge: -1, Expires: time.Unix(0, 0),
	})
}

func (s *Server) newSession(w http.ResponseWriter, r *http.Request) error {
	id := secure.Token(32)
	if err := s.st.CreateSession(id, clientIP(r), r.UserAgent(), sessionTTL); err != nil {
		return err
	}
	s.setSessionCookie(w, r, id)
	return nil
}

func (s *Server) siteName() string {
	v, _ := s.st.GetSetting(keySiteName)
	if strings.TrimSpace(v) == "" {
		return "阅迹"
	}
	return v
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	hash, _ := s.st.GetSetting(keyPasswordHash)
	loggedIn := false
	if id := sessionID(r); id != "" {
		if ses, err := s.st.GetSession(id); err == nil && ses != nil {
			loggedIn = true
		}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"needs_setup": hash == "",
		"logged_in":   loggedIn,
		"site_name":   s.siteName(),
		"version":     s.version,
	})
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	hash, _ := s.st.GetSetting(keyPasswordHash)
	if hash != "" {
		s.writeErr(w, http.StatusConflict, "已完成初始化，请直接登录")
		return
	}
	var body struct {
		Password string `json:"password"`
		SiteName string `json:"site_name"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	if len(body.Password) < 6 {
		s.writeErr(w, http.StatusBadRequest, "密码至少需要 6 位")
		return
	}
	if len(body.Password) > 72 {
		s.writeErr(w, http.StatusBadRequest, "密码最多 72 字节（中文等字符会占多个字节）")
		return
	}
	b, err := bcrypt.GenerateFromPassword([]byte(body.Password), 12)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, "生成密码失败")
		return
	}
	won, problem := s.claimSetupSession(w, r, string(b), body.SiteName, nil)
	if problem != "" {
		s.writeErr(w, http.StatusInternalServerError, problem)
		return
	}
	if !won {
		s.writeErr(w, http.StatusConflict, "已完成初始化，请直接登录")
		return
	}
	s.log.Info("面板初始化完成")
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "site_name": s.siteName()})
}

// claimSetupSession keeps the winning setup's password write and session
// issuance in one auth critical section. afterClaim is nil in the handler;
// tests pass a local barrier to check the interleaving deterministically.
func (s *Server) claimSetupSession(w http.ResponseWriter, r *http.Request, hash, siteName string, afterClaim func()) (bool, string) {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	won, err := s.st.SetSettingIfEmpty(keyPasswordHash, hash)
	if err != nil {
		return false, "保存密码失败"
	}
	if !won {
		return false, ""
	}
	if afterClaim != nil {
		afterClaim()
	}
	if name := strings.TrimSpace(siteName); name != "" {
		_ = s.st.SetSetting(keySiteName, name)
	}
	if err := s.newSession(w, r); err != nil {
		return true, "创建会话失败"
	}
	return true, ""
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	hash, err := s.st.GetSetting(keyPasswordHash)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, "读取密码失败")
		return
	}
	if hash == "" {
		s.writeErr(w, http.StatusBadRequest, "面板尚未初始化")
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	attempt, wait := s.limiter.Reserve(ip)
	if attempt == nil {
		s.writeErr(w, http.StatusTooManyRequests, "尝试次数过多，请在 "+wait.Round(time.Second).String()+" 后重试")
		return
	}
	s.authMu.Lock()
	defer s.authMu.Unlock()
	// Re-read under the password lock: a concurrent password change can
	// complete while this request waits, and no old hash may mint a session.
	hash, err = s.st.GetSetting(keyPasswordHash)
	if err != nil {
		s.limiter.Cancel(attempt)
		s.writeErr(w, http.StatusInternalServerError, "读取密码失败")
		return
	}
	if hash == "" {
		s.limiter.Cancel(attempt)
		s.writeErr(w, http.StatusBadRequest, "面板尚未初始化")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(body.Password)); err != nil {
		s.limiter.Settle(attempt, false)
		s.writeErr(w, http.StatusUnauthorized, "密码错误")
		return
	}
	s.limiter.Settle(attempt, true)
	if err := s.newSession(w, r); err != nil {
		s.writeErr(w, http.StatusInternalServerError, "创建会话失败")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "site_name": s.siteName()})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if id := sessionID(r); id != "" {
		_ = s.st.DeleteSession(id)
	}
	s.clearSessionCookie(w)
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]any{
		"site_name": s.siteName(),
		"timezone":  s.timezone().String(),
		"version":   s.version,
		"now":       time.Now().Unix(),
	})
}

// loginLimiter throttles failed password attempts per IP address.
type loginLimiter struct {
	mu        sync.Mutex
	entries   map[string]*limitEntry
	lastSweep time.Time
}

type limitEntry struct {
	fails    int
	inFlight int
	reset    time.Time
}

type loginAttempt struct {
	ip    string
	entry *limitEntry
}

const (
	maxLoginFails = 6
	loginWindow   = 10 * time.Minute
)

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{entries: map[string]*limitEntry{}}
}

// Reserve atomically occupies one verification slot before bcrypt runs.
func (l *loginLimiter) Reserve(ip string) (*loginAttempt, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if now.Sub(l.lastSweep) >= time.Minute {
		for key, entry := range l.entries {
			if !now.Before(entry.reset) && entry.inFlight == 0 {
				delete(l.entries, key)
			}
		}
		l.lastSweep = now
	}
	e, ok := l.entries[ip]
	if !ok {
		e = &limitEntry{reset: now.Add(loginWindow)}
		l.entries[ip] = e
	} else if !now.Before(e.reset) {
		// Keep outstanding reservations across the window boundary.
		e.fails = 0
		e.reset = now.Add(loginWindow)
	}
	if e.fails+e.inFlight >= maxLoginFails {
		return nil, time.Until(e.reset)
	}
	e.inFlight++
	return &loginAttempt{ip: ip, entry: e}, 0
}

// Settle charges a failed attempt or clears failures after a valid password.
func (l *loginLimiter) Settle(a *loginAttempt, success bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[a.ip]
	if e != a.entry {
		return
	}
	e.inFlight--
	if success {
		e.fails = 0
	} else {
		e.fails++
	}
	if e.fails == 0 && e.inFlight == 0 {
		delete(l.entries, a.ip)
	}
}

// Cancel refunds a slot if verification never ran (for example DB failure).
func (l *loginLimiter) Cancel(a *loginAttempt) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[a.ip]
	if e != a.entry {
		return
	}
	e.inFlight--
	if e.fails == 0 && e.inFlight == 0 {
		delete(l.entries, a.ip)
	}
}
