// Package api exposes the dashboard's HTTP API and serves the embedded UI.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"readtrace/internal/engine"
	"readtrace/internal/notify"
	"readtrace/internal/sched"
	"readtrace/internal/store"
	"readtrace/internal/weread"
)

// Server bundles the HTTP dependencies.
type Server struct {
	st      *store.Store
	eng     *engine.Engine
	sch     *sched.Scheduler
	nt      *notify.Sender
	wc      *weread.Client
	qr      *qrManager
	log     *slog.Logger
	assets  fs.FS
	version string
	started time.Time
	limiter *loginLimiter
}

// New creates the API server.
func New(st *store.Store, eng *engine.Engine, sch *sched.Scheduler, nt *notify.Sender,
	wc *weread.Client, assets fs.FS, version string, log *slog.Logger) *Server {
	s := &Server{
		st: st, eng: eng, sch: sch, nt: nt, wc: wc, log: log,
		assets: assets, version: version, started: time.Now(),
		limiter: newLoginLimiter(),
	}
	s.qr = newQRManager(st, wc, log)
	return s
}

// Handler builds the router with middleware.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Public endpoints.
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("POST /api/setup", s.handleSetup)
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)

	// Authenticated API.
	mux.HandleFunc("GET /api/me", s.auth(s.handleMe))
	mux.HandleFunc("GET /api/overview", s.auth(s.handleOverview))

	mux.HandleFunc("GET /api/accounts", s.auth(s.handleAccounts))
	mux.HandleFunc("POST /api/accounts", s.auth(s.handleAccountImport))
	mux.HandleFunc("GET /api/accounts/{id}", s.auth(s.handleAccountGet))
	mux.HandleFunc("PATCH /api/accounts/{id}", s.auth(s.handleAccountUpdate))
	mux.HandleFunc("DELETE /api/accounts/{id}", s.auth(s.handleAccountDelete))
	mux.HandleFunc("POST /api/accounts/{id}/refresh", s.auth(s.handleAccountRefresh))
	mux.HandleFunc("POST /api/accounts/{id}/check", s.auth(s.handleAccountCheck))
	mux.HandleFunc("POST /api/accounts/{id}/payload", s.auth(s.handleAccountPayload))
	mux.HandleFunc("DELETE /api/accounts/{id}/payload", s.auth(s.handleAccountPayloadDelete))

	mux.HandleFunc("POST /api/qr", s.auth(s.handleQRStart))
	mux.HandleFunc("GET /api/qr/{id}", s.auth(s.handleQRPoll))
	mux.HandleFunc("POST /api/qr/{id}/otp", s.auth(s.handleQROTP))

	mux.HandleFunc("GET /api/books/search", s.auth(s.handleBookSearch))
	mux.HandleFunc("GET /api/books/{id}", s.auth(s.handleBookDetail))

	mux.HandleFunc("POST /api/runs", s.auth(s.handleRunStart))
	mux.HandleFunc("GET /api/runs", s.auth(s.handleRunList))
	mux.HandleFunc("GET /api/runs/{id}", s.auth(s.handleRunGet))
	mux.HandleFunc("GET /api/runs/{id}/logs", s.auth(s.handleRunLogs))
	mux.HandleFunc("POST /api/runs/{id}/stop", s.auth(s.handleRunStop))
	mux.HandleFunc("GET /api/events", s.auth(s.handleEvents))

	mux.HandleFunc("GET /api/schedules", s.auth(s.handleScheduleList))
	mux.HandleFunc("POST /api/schedules", s.auth(s.handleScheduleCreate))
	mux.HandleFunc("GET /api/schedules/preview", s.auth(s.handleSchedulePreview))
	mux.HandleFunc("PATCH /api/schedules/{id}", s.auth(s.handleScheduleUpdate))
	mux.HandleFunc("DELETE /api/schedules/{id}", s.auth(s.handleScheduleDelete))

	mux.HandleFunc("GET /api/push/channels", s.auth(s.handlePushList))
	mux.HandleFunc("POST /api/push/channels", s.auth(s.handlePushCreate))
	mux.HandleFunc("PATCH /api/push/channels/{id}", s.auth(s.handlePushUpdate))
	mux.HandleFunc("DELETE /api/push/channels/{id}", s.auth(s.handlePushDelete))
	mux.HandleFunc("POST /api/push/channels/{id}/test", s.auth(s.handlePushTest))

	mux.HandleFunc("GET /api/settings", s.auth(s.handleSettingsGet))
	mux.HandleFunc("PUT /api/settings", s.auth(s.handleSettingsUpdate))
	mux.HandleFunc("POST /api/settings/password", s.auth(s.handlePasswordChange))

	// Static UI.
	fileServer := http.FileServerFS(s.assets)
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", cacheControl(fileServer)))
	mux.HandleFunc("GET /", s.handleIndex)

	return s.recoverMW(s.securityMW(s.logMW(mux)))
}

// ---------- middleware ----------

func (s *Server) recoverMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				s.log.Error("请求处理 panic", "err", v, "path", r.URL.Path)
				s.writeErr(w, http.StatusInternalServerError, "服务器内部错误")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) securityMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data: https:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'")
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (s *Server) logMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/events" {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		if r.URL.Path != "/api/health" {
			s.log.Debug("http", "method", r.Method, "path", r.URL.Path, "status", sw.status,
				"ms", time.Since(start).Milliseconds())
		}
	})
}

func cacheControl(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := sessionID(r)
		if id == "" {
			s.writeErr(w, http.StatusUnauthorized, "请先登录")
			return
		}
		ses, err := s.st.GetSession(id)
		if err != nil || ses == nil {
			s.clearSessionCookie(w)
			s.writeErr(w, http.StatusUnauthorized, "登录已过期，请重新登录")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Requested-With")), []byte("readtrace")) != 1 {
				s.writeErr(w, http.StatusForbidden, "非法请求")
				return
			}
		}
		ctx := context.WithValue(r.Context(), sessionCtxKey{}, ses)
		next(w, r.WithContext(ctx))
	}
}

type sessionCtxKey struct{}

func sessionFrom(r *http.Request) *store.Session {
	v, _ := r.Context().Value(sessionCtxKey{}).(*store.Session)
	return v
}

// ---------- helpers ----------

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		s.writeErr(w, http.StatusNotFound, "接口不存在")
		return
	}
	data, err := fs.ReadFile(s.assets, "index.html")
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, "界面资源缺失")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

func (s *Server) writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) writeErr(w http.ResponseWriter, code int, msg string) {
	s.writeJSON(w, code, map[string]string{"error": msg})
}

func (s *Server) decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			s.writeErr(w, http.StatusBadRequest, "请求体为空")
			return false
		}
		s.writeErr(w, http.StatusBadRequest, "请求格式错误："+err.Error())
		return false
	}
	return true
}

func pathID(r *http.Request) int64 {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id
}

func (s *Server) accountOrErr(w http.ResponseWriter, id int64) (*store.Account, bool) {
	acc, err := s.st.GetAccount(id)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, "读取账号失败")
		return nil, false
	}
	if acc == nil {
		s.writeErr(w, http.StatusNotFound, "账号不存在")
		return nil, false
	}
	return acc, true
}

func (s *Server) timezone() *time.Location {
	tz, _ := s.st.GetSetting("timezone")
	if tz == "" {
		return time.Local
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.Local
	}
	return loc
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "version": s.version, "uptime_seconds": int(time.Since(s.started).Seconds()),
	})
}

func minutesLabel(sec int) string {
	m := float64(sec) / 60
	return fmt.Sprintf("%.1f", m)
}
