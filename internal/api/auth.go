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
	if len(body.Password) > 128 {
		s.writeErr(w, http.StatusBadRequest, "密码过长")
		return
	}
	b, err := bcrypt.GenerateFromPassword([]byte(body.Password), 12)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, "生成密码失败")
		return
	}
	if err := s.st.SetSetting(keyPasswordHash, string(b)); err != nil {
		s.writeErr(w, http.StatusInternalServerError, "保存密码失败")
		return
	}
	if name := strings.TrimSpace(body.SiteName); name != "" {
		_ = s.st.SetSetting(keySiteName, name)
	}
	if err := s.newSession(w, r); err != nil {
		s.writeErr(w, http.StatusInternalServerError, "创建会话失败")
		return
	}
	s.log.Info("面板初始化完成")
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "site_name": s.siteName()})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if ok, wait := s.limiter.Allow(ip); !ok {
		s.writeErr(w, http.StatusTooManyRequests, "尝试次数过多，请在 "+wait.Round(time.Second).String()+" 后重试")
		return
	}
	hash, _ := s.st.GetSetting(keyPasswordHash)
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
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(body.Password)); err != nil {
		s.limiter.Fail(ip)
		s.writeErr(w, http.StatusUnauthorized, "密码错误")
		return
	}
	s.limiter.Reset(ip)
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
	mu      sync.Mutex
	entries map[string]*limitEntry
}

type limitEntry struct {
	fails int
	reset time.Time
}

const (
	maxLoginFails = 6
	loginWindow   = 10 * time.Minute
)

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{entries: map[string]*limitEntry{}}
}

func (l *loginLimiter) Allow(ip string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[ip]
	if !ok || time.Now().After(e.reset) {
		return true, 0
	}
	if e.fails < maxLoginFails {
		return true, 0
	}
	return false, time.Until(e.reset)
}

func (l *loginLimiter) Fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[ip]
	if !ok || time.Now().After(e.reset) {
		l.entries[ip] = &limitEntry{fails: 1, reset: time.Now().Add(loginWindow)}
		return
	}
	e.fails++
}

func (l *loginLimiter) Reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, ip)
}
