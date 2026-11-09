package api

import (
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"readtrace/internal/checker"
	"readtrace/internal/store"
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
		"default_template": mustSetting(s.st, store.KeyDefaultReadPayload),
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
	values := map[string]string{}
	if name := strings.TrimSpace(body.SiteName); name != "" {
		if len(name) > 40 {
			s.writeErr(w, http.StatusBadRequest, "站点名称过长")
			return
		}
		values[keySiteName] = name
	}
	var newLocation *time.Location
	if tz := strings.TrimSpace(body.Timezone); tz != "" {
		loc, err := time.LoadLocation(tz)
		if err != nil {
			s.writeErr(w, http.StatusBadRequest, "时区无效："+tz)
			return
		}
		values[keyTimezone] = tz
		newLocation = loc
	}
	// 0 disables the background account check; the checker picks the new
	// value up on its next poll without a restart.
	if body.CheckIntervalMinutes != nil {
		v := *body.CheckIntervalMinutes
		if v < 0 || v > 24*60 {
			s.writeErr(w, http.StatusBadRequest, "自动检测间隔需在 0~1440 分钟之间，0 表示关闭")
			return
		}
		values[checker.SettingInterval] = strconv.Itoa(v)
	}
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	previous, err := s.st.AllSettings()
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, "读取设置失败")
		return
	}
	if err := s.st.SetSettings(values); err != nil {
		s.writeErr(w, http.StatusInternalServerError, "保存设置失败")
		return
	}
	if newLocation != nil && newLocation.String() != s.timezone().String() {
		if err := s.sch.Start(newLocation); err != nil {
			// Scheduler.Start leaves its previous generation running when the
			// configuration cannot be read. Restore the matching stored values.
			rollback := make(map[string]string, len(values))
			for key := range values {
				rollback[key] = previous[key]
			}
			message := "重建定时任务失败：" + err.Error()
			if rollbackErr := s.st.SetSettings(rollback); rollbackErr != nil {
				s.log.Error("恢复设置失败", "err", rollbackErr)
				message += "；恢复设置失败，请重新保存设置"
			}
			s.writeErr(w, http.StatusInternalServerError, message)
			return
		}
		s.setTimezone(newLocation)
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
	if len(body.New) < 6 {
		s.writeErr(w, http.StatusBadRequest, "新密码至少需要 6 位")
		return
	}
	if len(body.New) > 72 {
		s.writeErr(w, http.StatusBadRequest, "新密码最多 72 字节（中文等字符会占多个字节）")
		return
	}
	s.authMu.Lock()
	defer s.authMu.Unlock()
	hash, err := s.st.GetSetting(keyPasswordHash)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, "读取密码失败")
		return
	}
	if hash == "" {
		s.writeErr(w, http.StatusBadRequest, "面板尚未初始化")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(body.Current)); err != nil {
		s.writeErr(w, http.StatusBadRequest, "当前密码不正确")
		return
	}
	b, err := bcrypt.GenerateFromPassword([]byte(body.New), 12)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, "生成密码失败")
		return
	}
	changed, err := s.st.ChangePassword(hash, string(b), sessionID(r))
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, "保存密码失败")
		return
	}
	if !changed {
		s.writeErr(w, http.StatusConflict, "密码已发生变化，请重新登录")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
