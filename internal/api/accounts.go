package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"wxread-panel/internal/store"
	"wxread-panel/internal/weread"
)

func (s *Server) runningAccounts() map[int64]bool {
	out := map[int64]bool{}
	for _, p := range s.eng.Snapshot() {
		out[p.AccountID] = true
	}
	return out
}

func (s *Server) handleAccounts(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.ListAccounts()
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, "读取账号列表失败")
		return
	}
	running := s.runningAccounts()
	for i := range list {
		list[i].Running = running[list[i].ID]
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"accounts": list, "active": s.eng.Snapshot()})
}

func (s *Server) handleAccountGet(w http.ResponseWriter, r *http.Request) {
	acc, ok := s.accountOrErr(w, pathID(r))
	if !ok {
		return
	}
	acc.Running = s.eng.IsRunning(acc.ID)
	s.writeJSON(w, http.StatusOK, acc)
}

func parseImport(curl, cookie, payload string) (weread.Cookies, map[string]any, error) {
	ck := weread.Cookies{}
	var pl map[string]any

	if strings.TrimSpace(curl) != "" {
		imp, err := weread.ParseCurl(curl)
		if err != nil {
			return nil, nil, err
		}
		ck = imp.Cookies
		pl = imp.Payload
	}
	if strings.TrimSpace(cookie) != "" {
		explicit := weread.ParseCookieString(cookie)
		if len(explicit) == 0 {
			return nil, nil, errors.New("无法解析 Cookie 内容")
		}
		ck = explicit
	}
	if strings.TrimSpace(payload) != "" {
		p, err := weread.DecodePayload(payload)
		if err != nil {
			return nil, nil, errors.New("模板 JSON 解析失败：" + err.Error())
		}
		pl = p
	}
	if len(ck) == 0 {
		return nil, nil, errors.New("未找到 Cookie，请粘贴完整的 curl 命令或 Cookie 字符串")
	}
	if ck["wr_skey"] == "" && ck["wr_vid"] == "" {
		return nil, nil, errors.New("Cookie 中缺少 wr_skey / wr_vid，可能不是微信读书的登录凭据")
	}
	return ck, pl, nil
}

func (s *Server) handleAccountImport(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name    string `json:"name"`
		Curl    string `json:"curl"`
		Cookie  string `json:"cookie"`
		Payload string `json:"payload"`
		Force   bool   `json:"force"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	ck, payload, err := parseImport(body.Curl, body.Cookie, body.Payload)
	if err != nil {
		s.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()

	validateErr := s.wc.Validate(ctx, ck)
	nickname, avatar := s.wc.UserInfo(ctx, ck, ck["wr_vid"])
	status, warning := "active", ""
	if validateErr != nil {
		if !body.Force {
			s.writeErr(w, http.StatusBadRequest, "Cookie 校验失败："+validateErr.Error()+"。如确认有效可勾选「强制导入」")
			return
		}
		status = "unverified"
		warning = "未能在线验证 Cookie：" + validateErr.Error()
	}

	vid := ck["wr_vid"]
	if existing, _ := s.st.FindAccountByVid(vid); existing != nil {
		if err := s.st.SaveCookies(existing.ID, map[string]string(ck)); err != nil {
			s.writeErr(w, http.StatusInternalServerError, "保存 Cookie 失败")
			return
		}
		_ = s.st.UpdateIdentity(existing.ID, vid, nickname, avatar)
		if payload != nil {
			_ = s.st.SavePayload(existing.ID, payload)
		}
		if name := strings.TrimSpace(body.Name); name != "" {
			existing.Name = name
			_ = s.st.UpdateAccount(*existing)
		}
		_ = s.st.SetAccountStatus(existing.ID, status, validateErrString(validateErr), time.Now().Unix())
		if warning == "" {
			warning = "该微信号已存在，已更新登录凭据"
		}
		acc, _ := s.st.GetAccount(existing.ID)
		s.writeJSON(w, http.StatusOK, map[string]any{"account": acc, "warning": warning, "updated": true})
		return
	}

	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = strings.TrimSpace(nickname)
	}
	if name == "" {
		name = "微信读书账号"
	}
	id, err := s.st.CreateAccount(store.Account{
		Name: name, Vid: vid, Nickname: nickname, Avatar: avatar,
		Status: status, LastError: validateErrString(validateErr), LastCheckAt: time.Now().Unix(),
		DefaultMinutes: 30, TickSeconds: 30, Mode: "stable",
	}, map[string]string(ck), payload)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, "创建账号失败："+err.Error())
		return
	}
	acc, _ := s.st.GetAccount(id)
	s.writeJSON(w, http.StatusOK, map[string]any{"account": acc, "warning": warning})
}

func validateErrString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (s *Server) handleAccountUpdate(w http.ResponseWriter, r *http.Request) {
	acc, ok := s.accountOrErr(w, pathID(r))
	if !ok {
		return
	}
	var body struct {
		Name           *string `json:"name"`
		DefaultMinutes *int    `json:"default_minutes"`
		TickSeconds    *int    `json:"tick_seconds"`
		BookID         *string `json:"book_id"`
		BookTitle      *string `json:"book_title"`
		ChapterID      *string `json:"chapter_id"`
		ChapterTitle   *string `json:"chapter_title"`
		Mode           *string `json:"mode"`
		PushChannelID  *int64  `json:"push_channel_id"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	if body.Name != nil {
		name := strings.TrimSpace(*body.Name)
		if name == "" {
			s.writeErr(w, http.StatusBadRequest, "账号名称不能为空")
			return
		}
		acc.Name = name
	}
	if body.DefaultMinutes != nil {
		if *body.DefaultMinutes < 1 || *body.DefaultMinutes > 24*60 {
			s.writeErr(w, http.StatusBadRequest, "默认时长需在 1~1440 分钟之间")
			return
		}
		acc.DefaultMinutes = *body.DefaultMinutes
	}
	if body.TickSeconds != nil {
		if *body.TickSeconds < 5 || *body.TickSeconds > 300 {
			s.writeErr(w, http.StatusBadRequest, "心跳间隔需在 5~300 秒之间")
			return
		}
		acc.TickSeconds = *body.TickSeconds
	}
	if body.BookID != nil {
		acc.BookID = strings.TrimSpace(*body.BookID)
	}
	if body.BookTitle != nil {
		acc.BookTitle = strings.TrimSpace(*body.BookTitle)
	}
	if body.ChapterID != nil {
		acc.ChapterID = strings.TrimSpace(*body.ChapterID)
	}
	if body.ChapterTitle != nil {
		acc.ChapterTitle = strings.TrimSpace(*body.ChapterTitle)
	}
	if body.Mode != nil {
		mode := strings.ToLower(strings.TrimSpace(*body.Mode))
		switch mode {
		case "stable", "rotate", "custom":
			acc.Mode = mode
		default:
			s.writeErr(w, http.StatusBadRequest, "模式只能是 stable / rotate / custom")
			return
		}
	}
	if body.PushChannelID != nil {
		if *body.PushChannelID > 0 {
			ch, err := s.st.GetPushChannel(*body.PushChannelID)
			if err != nil || ch == nil {
				s.writeErr(w, http.StatusBadRequest, "推送渠道不存在")
				return
			}
		}
		acc.PushChannelID = *body.PushChannelID
	}
	if err := s.st.UpdateAccount(*acc); err != nil {
		s.writeErr(w, http.StatusInternalServerError, "保存账号失败")
		return
	}
	acc.Running = s.eng.IsRunning(acc.ID)
	s.writeJSON(w, http.StatusOK, acc)
}

func (s *Server) handleAccountDelete(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	if s.eng.IsRunning(id) {
		s.writeErr(w, http.StatusConflict, "该账号正在阅读，请先停止")
		return
	}
	if err := s.st.DeleteAccount(id); err != nil {
		s.writeErr(w, http.StatusInternalServerError, "删除账号失败")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAccountRefresh(w http.ResponseWriter, r *http.Request) {
	acc, ok := s.accountOrErr(w, pathID(r))
	if !ok {
		return
	}
	cookies, _, err := s.st.AccountSecrets(acc.ID)
	if err != nil || len(cookies) == 0 {
		s.writeErr(w, http.StatusBadRequest, "账号没有可用的 Cookie")
		return
	}
	ck := weread.Cookies(cookies)
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()

	key, err := s.wc.Renew(ctx, ck)
	if err != nil || key == "" {
		_ = s.st.SetAccountStatus(acc.ID, "expired", "续期失败："+validateErrString(err), time.Now().Unix())
		s.writeErr(w, http.StatusBadGateway, "续期失败，请重新扫码登录："+validateErrString(err))
		return
	}
	_ = s.st.SaveCookies(acc.ID, map[string]string(ck))

	if err := s.wc.Validate(ctx, ck); err != nil {
		_ = s.st.SetAccountStatus(acc.ID, "expired", err.Error(), time.Now().Unix())
		s.writeErr(w, http.StatusBadGateway, "Cookie 已失效，请重新扫码登录："+err.Error())
		return
	}
	nickname, avatar := s.wc.UserInfo(ctx, ck, ck["wr_vid"])
	_ = s.st.UpdateIdentity(acc.ID, ck["wr_vid"], nickname, avatar)
	_ = s.st.SetAccountStatus(acc.ID, "active", "", time.Now().Unix())
	acc, _ = s.st.GetAccount(acc.ID)
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "account": acc})
}

func (s *Server) handleAccountCheck(w http.ResponseWriter, r *http.Request) {
	acc, ok := s.accountOrErr(w, pathID(r))
	if !ok {
		return
	}
	cookies, _, err := s.st.AccountSecrets(acc.ID)
	if err != nil || len(cookies) == 0 {
		s.writeErr(w, http.StatusBadRequest, "账号没有可用的 Cookie")
		return
	}
	ck := weread.Cookies(cookies)
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()

	checkErr := s.wc.Validate(ctx, ck)
	if checkErr != nil {
		if key, renewErr := s.wc.Renew(ctx, ck); renewErr == nil && key != "" {
			_ = s.st.SaveCookies(acc.ID, map[string]string(ck))
			checkErr = s.wc.Validate(ctx, ck)
		}
	}
	status := "active"
	if checkErr != nil {
		status = "expired"
	}
	_ = s.st.SetAccountStatus(acc.ID, status, validateErrString(checkErr), time.Now().Unix())
	acc, _ = s.st.GetAccount(acc.ID)
	s.writeJSON(w, http.StatusOK, map[string]any{
		"ok": checkErr == nil, "status": status, "error": validateErrString(checkErr), "account": acc,
	})
}

func (s *Server) handleAccountPayload(w http.ResponseWriter, r *http.Request) {
	acc, ok := s.accountOrErr(w, pathID(r))
	if !ok {
		return
	}
	var body struct {
		Curl    string `json:"curl"`
		Payload string `json:"payload"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	_, pl, err := parseImport(body.Curl, "", body.Payload)
	if err != nil {
		s.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(pl) == 0 {
		s.writeErr(w, http.StatusBadRequest, "未找到请求体模板，请粘贴包含 --data-raw 的 read 请求")
		return
	}
	if err := s.st.SavePayload(acc.ID, pl); err != nil {
		s.writeErr(w, http.StatusInternalServerError, "保存模板失败")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "has_payload": true})
}

func (s *Server) handleAccountPayloadDelete(w http.ResponseWriter, r *http.Request) {
	acc, ok := s.accountOrErr(w, pathID(r))
	if !ok {
		return
	}
	if err := s.st.DeletePayload(acc.ID); err != nil {
		s.writeErr(w, http.StatusInternalServerError, "删除模板失败")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "has_payload": false})
}
