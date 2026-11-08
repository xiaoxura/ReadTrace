package api

import (
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"readtrace/internal/checker"
)

func (s *Server) handleSettingsGet(w http.ResponseWriter, r *http.Request) {
	hash, _ := s.st.GetSetting(keyPasswordHash)
	all, _ := s.st.AllSettings()
	delete(all, keyPasswordHash)
	interval := checker.DefaultInterval
	if raw, ok := all[checker.SettingInterval]; ok && raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 0 && n <= 24*60 {
			interval = n
		}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"site_name":        s.siteName(),
		"timezone":         s.timezone().String(),
		"password_set":     hash != "",
		"version":          s.version,
		"go_version":       runtime.Version(),
		"uptime_seconds":   int(time.Since(s.started).Seconds()),
		"started_at":       s.started.Unix(),
		"check_interval":   interval,
		"settings":         all,
	})
}

func (s *Server) handleSettingsUpdate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SiteName             string `json:"site_name"`
		Timezone             string `json:"timezone"`
		CheckIntervalMinutes *int   `json:"check_interval_minutes"`
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
		s.setTimezone(loc)
		if err := s.sch.Start(loc); err != nil {
			s.writeErr(w, http.StatusInternalServerError, "重建定时任务失败："+err.Error())
			return
		}
	}
	// 0 disables the background account check; the checker picks the new
	// value up on its next poll without a restart.
	if body.CheckIntervalMinutes != nil {
		v := *body.CheckIntervalMinutes
		if v < 0 || v > 24*60 {
			s.writeErr(w, http.StatusBadRequest, "自动检测间隔需在 0~1440 分钟之间，0 表示关闭")
			return
		}
		if err := s.st.SetSetting(checker.SettingInterval, strconv.Itoa(v)); err != nil {
			s.writeErr(w, http.StatusInternalServerError, "保存检测设置失败")
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
