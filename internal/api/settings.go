package api

import (
	"net/http"
	"runtime"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func (s *Server) handleSettingsGet(w http.ResponseWriter, r *http.Request) {
	hash, _ := s.st.GetSetting(keyPasswordHash)
	all, _ := s.st.AllSettings()
	delete(all, keyPasswordHash)
	s.writeJSON(w, http.StatusOK, map[string]any{
		"site_name":      s.siteName(),
		"timezone":       s.timezone().String(),
		"password_set":   hash != "",
		"version":        s.version,
		"go_version":     runtime.Version(),
		"uptime_seconds": int(time.Since(s.started).Seconds()),
		"started_at":     s.started.Unix(),
		"settings":       all,
	})
}

func (s *Server) handleSettingsUpdate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SiteName string `json:"site_name"`
		Timezone string `json:"timezone"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	if name := strings.TrimSpace(body.SiteName); name != "" {
		if len(name) > 40 {
			s.writeErr(w, http.StatusBadRequest, "站点名称过长")
			return
		}
		_ = s.st.SetSetting(keySiteName, name)
	}
	if tz := strings.TrimSpace(body.Timezone); tz != "" {
		loc, err := time.LoadLocation(tz)
		if err != nil {
			s.writeErr(w, http.StatusBadRequest, "时区无效："+tz)
			return
		}
		if err := s.st.SetSetting(keyTimezone, tz); err != nil {
			s.writeErr(w, http.StatusInternalServerError, "保存时区失败")
			return
		}
		if err := s.sch.Start(loc); err != nil {
			s.writeErr(w, http.StatusInternalServerError, "重建定时任务失败："+err.Error())
			return
		}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"site_name": s.siteName(),
		"timezone":  s.timezone().String(),
	})
}

func (s *Server) handlePasswordChange(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	hash, _ := s.st.GetSetting(keyPasswordHash)
	if hash == "" {
		s.writeErr(w, http.StatusBadRequest, "面板尚未初始化")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(body.Current)); err != nil {
		s.writeErr(w, http.StatusUnauthorized, "当前密码不正确")
		return
	}
	if len(body.New) < 6 {
		s.writeErr(w, http.StatusBadRequest, "新密码至少需要 6 位")
		return
	}
	b, err := bcrypt.GenerateFromPassword([]byte(body.New), 12)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, "生成密码失败")
		return
	}
	if err := s.st.SetSetting(keyPasswordHash, string(b)); err != nil {
		s.writeErr(w, http.StatusInternalServerError, "保存密码失败")
		return
	}
	if id := sessionID(r); id != "" {
		_ = s.st.DeleteOtherSessions(id)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
