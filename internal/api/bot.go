// 微信机器人管理 API（面板管理员专用）。
package api

import (
	"net/http"
	"strconv"
	"strings"

	"readtrace/internal/wxbot"
)

// wxBotReady 在渠道未接线时短路（测试或二次开发可能省略 bot）。
func (s *Server) wxBotReady(w http.ResponseWriter) bool {
	if s.wxBot == nil {
		s.writeErr(w, http.StatusServiceUnavailable, "微信机器人渠道未启用")
		return false
	}
	return true
}

// handleWxBotStatus returns the channel summary.
func (s *Server) handleWxBotStatus(w http.ResponseWriter, r *http.Request) {
	if !s.wxBotReady(w) {
		return
	}
	s.writeJSON(w, http.StatusOK, s.wxBot.Status())
}

// handleWxBotLogin starts the bot binding QR flow.
func (s *Server) handleWxBotLogin(w http.ResponseWriter, r *http.Request) {
	if !s.wxBotReady(w) {
		return
	}
	state, err := s.wxBot.StartBotLogin()
	if err != nil && state.Status == "" {
		s.writeErr(w, http.StatusBadGateway, "获取机器人二维码失败："+err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, state)
}

// handleWxBotLoginStatus returns the current binding flow snapshot.
func (s *Server) handleWxBotLoginStatus(w http.ResponseWriter, r *http.Request) {
	if !s.wxBotReady(w) {
		return
	}
	s.writeJSON(w, http.StatusOK, s.wxBot.BotLoginSnapshot())
}

// handleWxBotLoginOTP submits the phone-displayed pairing code.
func (s *Server) handleWxBotLoginOTP(w http.ResponseWriter, r *http.Request) {
	if !s.wxBotReady(w) {
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	code := strings.TrimSpace(body.Code)
	if code == "" {
		s.writeErr(w, http.StatusBadRequest, "请输入手机微信上显示的数字")
		return
	}
	s.writeJSON(w, http.StatusOK, s.wxBot.SubmitBotOTP(code))
}

// handleWxBotEnabled flips the channel switch.
func (s *Server) handleWxBotEnabled(w http.ResponseWriter, r *http.Request) {
	if !s.wxBotReady(w) {
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	if body.Enabled == nil {
		s.writeErr(w, http.StatusBadRequest, "缺少 enabled 字段")
		return
	}
	if err := wxbot.SetEnabled(s.st, *body.Enabled); err != nil {
		s.writeErr(w, http.StatusInternalServerError, "保存开关失败："+err.Error())
		return
	}
	// 进程启动时渠道可能是关闭的，此时 Start 还没有执行过；启用后必须
	// 补一次（幂等），否则面板显示运行中但没有任何长轮询。
	if *body.Enabled {
		s.wxBot.Start()
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": *body.Enabled})
}

// handleWxBotPush toggles run-end push to bound users.
func (s *Server) handleWxBotPush(w http.ResponseWriter, r *http.Request) {
	if !s.wxBotReady(w) {
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	if body.Enabled == nil {
		s.writeErr(w, http.StatusBadRequest, "缺少 enabled 字段")
		return
	}
	if err := wxbot.SetPushRuns(s.st, *body.Enabled); err != nil {
		s.writeErr(w, http.StatusInternalServerError, "保存推送开关失败："+err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": *body.Enabled})
}

// handleWxBotUsers returns users plus bindings for management.
func (s *Server) handleWxBotUsers(w http.ResponseWriter, r *http.Request) {
	if !s.wxBotReady(w) {
		return
	}
	users, err := s.st.ListBotUsers()
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, "读取用户失败："+err.Error())
		return
	}
	binds, err := s.st.ListBotBindings()
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, "读取绑定失败："+err.Error())
		return
	}
	accounts, err := s.st.ListAccounts()
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, "读取账号失败："+err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"users":    users,
		"bindings": binds,
		"accounts": accounts,
	})
}

// handleWxBotUserAdmin grants/revokes admin.
func (s *Server) handleWxBotUserAdmin(w http.ResponseWriter, r *http.Request) {
	if !s.wxBotReady(w) {
		return
	}
	var body struct {
		UserID string `json:"user_id"`
		Admin  bool   `json:"admin"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	body.UserID = strings.TrimSpace(body.UserID)
	if body.UserID == "" {
		s.writeErr(w, http.StatusBadRequest, "缺少 user_id")
		return
	}
	if err := s.st.SetBotUserAdmin(body.UserID, body.Admin); err != nil {
		s.writeErr(w, http.StatusInternalServerError, "更新权限失败："+err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleWxBotUserDelete removes a user and their bindings.
func (s *Server) handleWxBotUserDelete(w http.ResponseWriter, r *http.Request) {
	if !s.wxBotReady(w) {
		return
	}
	uid := strings.TrimSpace(r.PathValue("uid"))
	if uid == "" {
		s.writeErr(w, http.StatusBadRequest, "缺少用户 ID")
		return
	}
	if err := s.st.DeleteBotUser(uid); err != nil {
		s.writeErr(w, http.StatusInternalServerError, "删除失败："+err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleWxBotBind binds an existing panel account to a WeChat user.
func (s *Server) handleWxBotBind(w http.ResponseWriter, r *http.Request) {
	if !s.wxBotReady(w) {
		return
	}
	var body struct {
		UserID    string `json:"user_id"`
		AccountID int64  `json:"account_id"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	body.UserID = strings.TrimSpace(body.UserID)
	if body.UserID == "" || body.AccountID <= 0 {
		s.writeErr(w, http.StatusBadRequest, "需要 user_id 与 account_id")
		return
	}
	acc, err := s.st.GetAccount(body.AccountID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, "读取账号失败："+err.Error())
		return
	}
	if acc == nil {
		s.writeErr(w, http.StatusNotFound, "账号不存在")
		return
	}
	if _, err := s.st.BindUserAccount(body.UserID, body.AccountID); err != nil {
		s.writeErr(w, http.StatusInternalServerError, "绑定失败："+err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "account": acc})
}

// handleWxBotUnbind removes binding rows. With user_id deletes that binding,
// otherwise clears every binding of the account.
func (s *Server) handleWxBotUnbind(w http.ResponseWriter, r *http.Request) {
	if !s.wxBotReady(w) {
		return
	}
	var body struct {
		UserID    string `json:"user_id"`
		AccountID int64  `json:"account_id"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	body.UserID = strings.TrimSpace(body.UserID)
	if body.AccountID <= 0 {
		s.writeErr(w, http.StatusBadRequest, "需要 account_id")
		return
	}
	if body.UserID != "" {
		ok, err := s.st.UnbindUserAccount(body.UserID, body.AccountID)
		if err != nil {
			s.writeErr(w, http.StatusInternalServerError, "解绑失败："+err.Error())
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]any{"ok": ok})
		return
	}
	if err := s.st.UnbindAccount(body.AccountID); err != nil {
		s.writeErr(w, http.StatusInternalServerError, "解绑失败："+err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleWxBotMessages lists chat history of one WeChat user (对话记录).
func (s *Server) handleWxBotMessages(w http.ResponseWriter, r *http.Request) {
	if !s.wxBotReady(w) {
		return
	}
	uid := strings.TrimSpace(r.URL.Query().Get("user_id"))
	if uid == "" {
		s.writeErr(w, http.StatusBadRequest, "缺少 user_id")
		return
	}
	before := int64(0)
	if v := strings.TrimSpace(r.URL.Query().Get("before_id")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			before = n
		}
	}
	limit := 50
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	msgs, err := s.st.ListBotMessages(uid, before, limit)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, "读取对话记录失败："+err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"user_id":  uid,
		"messages": msgs,
		"has_more": len(msgs) == limit,
	})
}

// handleWxBotLogout disconnects the bot credential（绑定关系保留）.
func (s *Server) handleWxBotLogout(w http.ResponseWriter, r *http.Request) {
	if !s.wxBotReady(w) {
		return
	}
	if err := s.st.ClearBotLogin(); err != nil {
		s.writeErr(w, http.StatusInternalServerError, "清除凭据失败："+err.Error())
		return
	}
	_ = s.st.SaveBotUpdatesBuf("")
	_ = wxbot.SetNeedsRebind(s.st, false)
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
