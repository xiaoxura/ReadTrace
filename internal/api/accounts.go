package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"readtrace/internal/checker"
	"readtrace/internal/engine"
	"readtrace/internal/store"
	"readtrace/internal/weread"
)

func (s *Server) runningAccounts() map[int64]bool {
	out := map[int64]bool{}
	for _, p := range s.eng.Snapshot() {
		out[p.AccountID] = true
	}
	return out
}

func (s *Server) fillDailyAccounts(accounts []store.Account) error {
	seconds, err := s.st.DailySecondsByAccount(time.Now(), s.timezone())
	if err != nil {
		return err
	}
	for i := range accounts {
		accounts[i].TodaySeconds = seconds[accounts[i].ID]
		if accounts[i].DailyGoalMinutes > 0 {
			accounts[i].DailyRemainingSeconds = max(0, accounts[i].DailyGoalMinutes*60-accounts[i].TodaySeconds)
		}
	}
	return nil
}

func (s *Server) handleAccounts(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.ListAccounts()
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, "读取账号列表失败")
		return
	}
	if err := s.fillDailyAccounts(list); err != nil {
		s.writeErr(w, http.StatusInternalServerError, "读取今日阅读统计失败")
		return
	}
	running := s.runningAccounts()
	for i := range list {
		list[i].Running = running[list[i].ID]
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"accounts": list, "active": s.eng.Snapshot()})
}

func (s *Server) handleAccountGet(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		s.writeErr(w, http.StatusBadRequest, "无效的账号 ID")
		return
	}
	acc, ok := s.accountOrErr(w, id)
	if !ok {
		return
	}
	acc.Running = s.eng.IsRunning(acc.ID)
	accountList := []store.Account{*acc}
	if err := s.fillDailyAccounts(accountList); err != nil {
		s.writeErr(w, http.StatusInternalServerError, "读取今日阅读统计失败")
		return
	}
	s.writeJSON(w, http.StatusOK, accountList[0])
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
		Name      string `json:"name"`
		Curl      string `json:"curl"`
		Cookie    string `json:"cookie"`
		Payload   string `json:"payload"`
		Force     bool   `json:"force"`
		ReadSpeed *int   `json:"readSpeed"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	// 可选的初始阅读速度：缺省（0/未填）走 store 层默认 300。
	createReadSpeed := 0
	if body.ReadSpeed != nil {
		if msg := invalidReadSpeed(*body.ReadSpeed); msg != "" {
			s.writeErr(w, http.StatusBadRequest, msg)
			return
		}
		createReadSpeed = *body.ReadSpeed
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
		err := s.eng.WithIdleAccount(existing.ID, func() error {
			if err := s.st.SaveCookies(existing.ID, map[string]string(ck)); err != nil {
				return err
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
			return nil
		})
		if errors.Is(err, engine.ErrAccountRunning) {
			s.writeErr(w, http.StatusConflict, "该账号正在阅读中，请先停止任务再更新登录凭据")
			return
		}
		if err != nil {
			s.writeErr(w, http.StatusInternalServerError, "保存 Cookie 失败")
			return
		}
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
	name = truncateRunes(name, 60)
	id, err := s.st.CreateAccount(store.Account{
		Name: name, Vid: vid, Nickname: nickname, Avatar: avatar,
		Status: status, LastError: validateErrString(validateErr), LastCheckAt: time.Now().Unix(),
		DefaultMinutes: 30, DailyGoalMinutes: 30, TickSeconds: 30, Mode: "sequence",
		ReadSpeed: createReadSpeed,
		// 新账号默认：阅读失败自动重试 5 次，间隔 30 秒。
		MaxRetries: 5, RetryDelay: 30,
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
	id, ok := pathID(r)
	if !ok {
		s.writeErr(w, http.StatusBadRequest, "无效的账号 ID")
		return
	}
	acc, ok := s.accountOrErr(w, id)
	if !ok {
		return
	}
	var body struct {
		Name             *string `json:"name"`
		DefaultMinutes   *int    `json:"default_minutes"`
		DailyGoalMinutes *int    `json:"daily_goal_minutes"`
		TickSeconds      *int    `json:"tick_seconds"`
		ReadSpeed        *int    `json:"readSpeed"`
		MaxRetries       *int    `json:"max_retries"`
		RetryDelay       *int    `json:"retry_delay"`
		BookID           *string `json:"book_id"`
		BookTitle        *string `json:"book_title"`
		ChapterID        *string `json:"chapter_id"`
		ChapterTitle     *string `json:"chapter_title"`
		Mode             *string `json:"mode"`
		PushChannelID    *int64  `json:"push_channel_id"`
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
		acc.Name = truncateRunes(name, 60)
	}
	if body.DefaultMinutes != nil {
		if *body.DefaultMinutes < 1 || *body.DefaultMinutes > 24*60 {
			s.writeErr(w, http.StatusBadRequest, "默认时长需在 1~1440 分钟之间")
			return
		}
		acc.DefaultMinutes = *body.DefaultMinutes
	}
	if body.DailyGoalMinutes != nil {
		if *body.DailyGoalMinutes < 0 || *body.DailyGoalMinutes > 24*60 {
			s.writeErr(w, http.StatusBadRequest, "每日目标需在 0~1440 分钟之间，0 表示关闭")
			return
		}
		acc.DailyGoalMinutes = *body.DailyGoalMinutes
	}
	if body.TickSeconds != nil {
		if *body.TickSeconds < 5 || *body.TickSeconds > 300 {
			s.writeErr(w, http.StatusBadRequest, "心跳间隔需在 5~300 秒之间")
			return
		}
		acc.TickSeconds = *body.TickSeconds
	}
	if body.ReadSpeed != nil {
		if msg := invalidReadSpeed(*body.ReadSpeed); msg != "" {
			s.writeErr(w, http.StatusBadRequest, msg)
			return
		}
		// 0 表示恢复默认：store 层 clampReadSpeed 会把它折算成 300。
		acc.ReadSpeed = *body.ReadSpeed
	}
	if body.MaxRetries != nil {
		if *body.MaxRetries < 0 || *body.MaxRetries > 10 {
			s.writeErr(w, http.StatusBadRequest, "自动重试次数需在 0~10 之间，0 表示不重试")
			return
		}
		acc.MaxRetries = *body.MaxRetries
	}
	if body.RetryDelay != nil {
		if *body.RetryDelay <= 0 || *body.RetryDelay > 600 {
			s.writeErr(w, http.StatusBadRequest, "重试间隔需在 1~600 秒之间")
			return
		}
		acc.RetryDelay = *body.RetryDelay
	}
	if body.BookID != nil {
		// 自定义 book_id 的入口在形态与长度上都和手动书池同一标准
		// （书池按 64 rune 上限拒绝超长串）：空值合法，表示回落书池。
		id := strings.TrimSpace(*body.BookID)
		if msg := invalidCustomBookID(id); msg != "" {
			s.writeErr(w, http.StatusBadRequest, msg)
			return
		}
		acc.BookID = id
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
		case "sequence", "rotate", "custom":
			acc.Mode = mode
		default:
			s.writeErr(w, http.StatusBadRequest, "模式只能是 sequence / rotate / custom")
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
	accountList := []store.Account{*acc}
	if err := s.fillDailyAccounts(accountList); err != nil {
		s.writeErr(w, http.StatusInternalServerError, "读取今日阅读统计失败")
		return
	}
	s.writeJSON(w, http.StatusOK, accountList[0])
}

// invalidReadSpeed 校验「每分钟阅读字数」：0 表示恢复默认（未填不进这条
// 路径 = 不改），其余必须是 10~3000 之间的整数，负数一律拒绝。
func invalidReadSpeed(v int) string {
	if v == 0 {
		return ""
	}
	if v < 0 || v >= 1 && v < 10 || v > 3000 {
		return "每分钟阅读字数需为 0（默认）或 10~3000 之间的整数"
	}
	return ""
}

func (s *Server) handleAccountDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		s.writeErr(w, http.StatusBadRequest, "无效的账号 ID")
		return
	}
	schedules, err := s.st.ListSchedules()
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, "读取账号定时任务失败")
		return
	}
	// Serialize deletion with run startup. Commit the database first: a failed
	// transaction must leave the matching live schedules registered. Release
	// the account lock before Remove, since fire holds the scheduler lock while
	// acquiring the account lock in Engine.Start.
	err = s.eng.WithIdleAccount(id, func() error { return s.st.DeleteAccount(id) })
	if errors.Is(err, engine.ErrAccountRunning) {
		s.writeErr(w, http.StatusConflict, "该账号正在阅读，请先停止")
		return
	}
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, "删除账号失败")
		return
	}
	for _, v := range schedules {
		if v.AccountID == id {
			s.sch.Remove(v.ID)
		}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// checkLoginClassified validates the cookie, renewing exactly once on a true
// auth failure. It returns a three-way outcome; the shared implementation
// (also used by the background auto-checker) lives in internal/checker.
//   - "active":  session valid (possibly after an in-place renewal)
//   - "expired": WeRead explicitly rejected the session after a renewal retry
//   - "error":   transient failure (network, proxy, unexpected API code or
//     risk control HTML); the caller must NOT expire the account
//
// /web/user can reject non-dead sessions with odd codes (e.g. -2003
// 参数格式错误 after recent API changes), so an unknown failure on a not-running
// account gets one renewal probe: a live session can always renew, a dead one
// is refused — that decides liveness more reliably than /web/user itself.
func (s *Server) checkLoginClassified(ctx context.Context, accountID int64, ck weread.Cookies) (string, error) {
	return checker.CheckLoginWithEngine(ctx, s.st, s.wc, s.eng, accountID, ck)
}

func (s *Server) handleAccountRefresh(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		s.writeErr(w, http.StatusBadRequest, "无效的账号 ID")
		return
	}
	acc, ok := s.accountOrErr(w, id)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()

	var updated *store.Account
	status, err, opErr := checker.CheckLoginAndUpdate(ctx, s.st, s.wc, s.eng, acc.ID,
		func(ck weread.Cookies, outcome string, checkErr error) error {
			current, loadErr := s.st.GetAccount(acc.ID)
			if loadErr != nil {
				return loadErr
			}
			if current == nil {
				return errors.New("账号不存在")
			}
			switch outcome {
			case "active":
				// Renewal mutates ck in place. Use that fresh key for UserInfo and
				// preserve existing identity fields if the lookup has no data.
				nickname, avatar := s.wc.UserInfo(ctx, ck, ck["wr_vid"])
				vid := ck["wr_vid"]
				if vid == "" {
					vid = current.Vid
				}
				if nickname == "" {
					nickname = current.Nickname
				}
				if avatar == "" {
					avatar = current.Avatar
				}
				if err := s.st.UpdateIdentity(acc.ID, vid, nickname, avatar); err != nil {
					return err
				}
				if err := s.st.SetAccountStatus(acc.ID, "active", "", time.Now().Unix()); err != nil {
					return err
				}
			case "error":
				if err := s.st.SetAccountStatus(acc.ID, current.Status, "刷新遇到临时问题："+validateErrString(checkErr), time.Now().Unix()); err != nil {
					return err
				}
			default: // "expired"
				if err := s.st.SetAccountStatus(acc.ID, "expired", "续期失败："+validateErrString(checkErr), time.Now().Unix()); err != nil {
					return err
				}
			}
			updated, loadErr = s.st.GetAccount(acc.ID)
			return loadErr
		})
	if opErr != nil {
		s.writeAccountCheckOpError(w, opErr)
		return
	}
	switch status {
	case "active":
		s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "account": updated})
	case "error":
		// Transient failure: keep the account's current status and ask the
		// user to retry instead of killing a possibly healthy session.
		s.writeErr(w, http.StatusBadGateway, "刷新未完成（可能是网络或接口波动），账号状态未改动，请稍后重试："+validateErrString(err))
	default: // "expired"
		s.writeErr(w, http.StatusBadGateway, "登录凭据已确认失效，请重新扫码登录："+validateErrString(err))
	}
}

func (s *Server) writeAccountCheckOpError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, engine.ErrAccountRunning):
		s.writeErr(w, http.StatusConflict, "账号正在阅读中，请先停止任务再检查或刷新")
	case errors.Is(err, checker.ErrNoCookies):
		s.writeErr(w, http.StatusBadRequest, "账号没有可用的 Cookie")
	default:
		s.writeErr(w, http.StatusInternalServerError, "更新账号检查结果失败："+err.Error())
	}
}

func (s *Server) handleAccountCheck(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		s.writeErr(w, http.StatusBadRequest, "无效的账号 ID")
		return
	}
	acc, ok := s.accountOrErr(w, id)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()

	var updated *store.Account
	outcome, verifyErr, opErr := checker.CheckLoginAndUpdate(ctx, s.st, s.wc, s.eng, acc.ID,
		func(_ weread.Cookies, outcome string, checkErr error) error {
			current, loadErr := s.st.GetAccount(acc.ID)
			if loadErr != nil {
				return loadErr
			}
			if current == nil {
				return errors.New("账号不存在")
			}
			switch outcome {
			case "active":
				loadErr = s.st.SetAccountStatus(acc.ID, "active", "", time.Now().Unix())
			case "expired":
				loadErr = s.st.SetAccountStatus(acc.ID, "expired", validateErrString(checkErr), time.Now().Unix())
			default: // transient failure retains the current status.
				loadErr = s.st.SetAccountStatus(acc.ID, current.Status, "检查遇到临时问题："+validateErrString(checkErr), time.Now().Unix())
			}
			if loadErr != nil {
				return loadErr
			}
			updated, loadErr = s.st.GetAccount(acc.ID)
			return loadErr
		})
	if opErr != nil {
		s.writeAccountCheckOpError(w, opErr)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"ok": outcome == "active", "status": outcome, "error": validateErrString(verifyErr), "account": updated,
	})
}

func (s *Server) handleAccountPayload(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		s.writeErr(w, http.StatusBadRequest, "无效的账号 ID")
		return
	}
	acc, ok := s.accountOrErr(w, id)
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
	raw := strings.TrimSpace(body.Payload)
	if raw == "" {
		raw = body.Curl
	}
	pl, err := weread.ParseReadTemplate(raw)
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

// handleAccountPayloadGet 返回该账号当前保存的请求模板（免抓包生成或粘贴
// 所得的 JSON）。供账号设置里的「查看当前模板」使用；Cookie 不随行返回。
func (s *Server) handleAccountPayloadGet(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		s.writeErr(w, http.StatusBadRequest, "无效的账号 ID")
		return
	}
	acc, ok := s.accountOrErr(w, id)
	if !ok {
		return
	}
	_, payload, err := s.st.AccountSecrets(acc.ID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, "读取模板失败")
		return
	}
	out := map[string]any{"has_payload": len(payload) > 0}
	if len(payload) > 0 {
		out["payload"] = payload
	}
	s.writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAccountPayloadDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		s.writeErr(w, http.StatusBadRequest, "无效的账号 ID")
		return
	}
	acc, ok := s.accountOrErr(w, id)
	if !ok {
		return
	}
	if err := s.st.DeletePayload(acc.ID); err != nil {
		s.writeErr(w, http.StatusInternalServerError, "删除模板失败")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "has_payload": false})
}

// handleAccountGeneratePayload 一键生成该账号的专属 read 模板（免抓包）。
// 组装细节见 generateAccountTemplate；签名 s/sg 由引擎每次心跳现算，模板里
// 不需要保存。生成结果直接覆盖该账号的专属模板（清除后可回到全局回落）。
func (s *Server) handleAccountGeneratePayload(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		s.writeErr(w, http.StatusBadRequest, "无效的账号 ID")
		return
	}
	acc, ok := s.accountOrErr(w, id)
	if !ok {
		return
	}
	var body struct {
		BookID string `json:"book_id"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	pl, bookID, chapter, err := s.generateAccountTemplate(ctx, acc, strings.TrimSpace(body.BookID))
	if err != nil {
		s.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.st.SavePayload(acc.ID, pl); err != nil {
		s.writeErr(w, http.StatusInternalServerError, "保存模板失败")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "has_payload": true,
		"book_id": bookID, "chapter_id": chapter.ID, "ci": chapter.Index,
		"book_title": truncateRunes(chapter.Title, 60),
	})
}

// generateAccountTemplate 不抓包生成一份 read 请求基底：
// 1) bookID 为空时回落书池第一本书；2) 拉官方目录取首个实章（chapterUid/
// 章节下标）；3) GET 阅读页从 window.__INITIAL_STATE__ 解析 psvts/pclts；
// 4) appId 按官方公式由 User-Agent 推导；pclts 缺失时用 _e(ct) 兜底。
// 登录失效时先续期再试一次（与书架/导入通道同语义）。自定义 bookID 的
// 长度/形态校验与手动书池一致（invalidCustomBookID）。
func (s *Server) generateAccountTemplate(ctx context.Context, acc *store.Account, bookID string) (map[string]any, string, *weread.Chapter, error) {
	bookID = strings.TrimSpace(bookID)
	// 入口与手动书池同一标准：超长或非法形态（空白字符）拒绝。
	if msg := invalidCustomBookID(bookID); msg != "" {
		return nil, "", nil, errors.New(msg)
	}
	if bookID == "" {
		books, err := s.st.PoolItemsForAccount(store.KindBook, acc.ID)
		if err != nil {
			return nil, "", nil, fmt.Errorf("读取书池失败：%v", err)
		}
		for _, b := range books {
			if b.Value != "" {
				bookID = b.Value
				break
			}
		}
	}
	if bookID == "" {
		return nil, "", nil, errors.New("书池还是空的：请先在「书池 · 书籍导入」导入书籍，再生成模板")
	}
	ck, err := s.accountCookies(acc)
	if err != nil {
		return nil, "", nil, err
	}

	var chs []weread.Chapter
	err = s.authedCall(ctx, acc, ck, func() error {
		toc, tocErr := s.chapterInfos(ctx, acc.ID, ck, bookID)
		if tocErr != nil {
			return tocErr
		}
		chs = toc
		return nil
	})
	if err != nil {
		return nil, "", nil, fmt.Errorf("读取目录失败：%v", err)
	}
	chapter := poolChapterFromTOC(chs)
	if chapter == nil {
		return nil, "", nil, errors.New("这本书在官方目录里没有可读章节，换一本书试试")
	}

	var state *weread.ReaderState
	err = s.authedCall(ctx, acc, ck, func() error {
		st, stErr := s.wc.FetchReaderState(ctx, ck, bookID)
		if stErr != nil {
			return stErr
		}
		state = st
		return nil
	})
	if err != nil {
		return nil, "", nil, err
	}
	pc := state.PCLTS
	if pc == "" || pc == "0" {
		// 官方前端的兜底：pc = _e(ct)。
		pc = weread.NormalizePayloadID(strconv.FormatInt(time.Now().Unix(), 10))
	}
	pl := map[string]any{
		"appId": weread.DeriveAppID(s.wc.UA),
		"b":     weread.NormalizePayloadID(bookID),
		"c":     chapter.ID,
		"ci":    chapter.Index,
		"co":    100 + int(time.Now().UnixNano()%300),
		"sm":    truncateRunes(chapter.Title, 24),
		"pr":    0,
		"ps":    state.PSVTS,
		"pc":    pc,
	}
	return pl, bookID, chapter, nil
}

// poolChapterFromTOC 从章节目录中挑选生成模板所用章节：优先取有正文的实章
// （wordCount 足够大），把封面页/版权页过滤掉。
func poolChapterFromTOC(chs []weread.Chapter) *weread.Chapter {
	candidate := -1
	for i, ch := range chs {
		if ch.UID == 0 || ch.ID == "" {
			continue
		}
		if candidate < 0 {
			candidate = i // 兜底用第一个有效章节
		}
		if ch.WordCount >= 5 {
			return &chs[i]
		}
	}
	if candidate < 0 {
		return nil
	}
	return &chs[candidate]
}

// invalidCustomBookID 校验账号自定义 book_id 的形态与长度，和手动书池入口
// 同一标准（书池按 TrimSpace 判空 + 64 rune 上限拒绝，见 pool.go）：
// 非空时不得超过 64 rune、不得含空白字符；空串合法，表示回落书池，由引擎
// 用 NormalizePayloadID 决定最终形态，这里不做合法 ID 形态的白名单。
func invalidCustomBookID(id string) string {
	if id == "" {
		return ""
	}
	if utf8.RuneCountInString(id) > 64 {
		return "书籍 ID 过长（上限 64 字符）"
	}
	if strings.ContainsFunc(id, unicode.IsSpace) {
		return "书籍 ID 不能包含空白字符"
	}
	return ""
}
