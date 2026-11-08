// 微信机器人主体：长轮询收消息、会话路由、账号/定时命令、运行结束推送。
package wxbot

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"readtrace/internal/engine"
	"readtrace/internal/sched"
	"readtrace/internal/store"
	"readtrace/internal/weread"
)

// 设置项（存在 settings 表）。
const (
	SettingEnabled     = "wxbot_enabled"      // "0" 关闭，空/"1" 开启
	SettingPushRuns    = "wxbot_push_run_end" // "1" 运行结束直推绑定用户（默认）
	SettingNeedsRebind = "wxbot_needs_rebind" // "1" 凭据失效，需要重新扫码
	bindingsMax        = 64
)

// Bot owns the WeChat bot channel: credential, poll loop and conversations.
type Bot struct {
	st  *store.Store
	wc  *weread.Client
	eng *engine.Engine
	sch *sched.Scheduler
	hub *engine.Hub
	log *slog.Logger

	mu        sync.Mutex
	dedup     map[string]struct{}
	wrLogins  map[string]*wrLogin
	botFlow   *botLoginFlow
	ctxTokens map[string]string
	lastPoll  int64
	lastErr   string

	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	stopping bool
}

// New creates the bot; call Start to activate.
func New(st *store.Store, wc *weread.Client, eng *engine.Engine, sch *sched.Scheduler, hub *engine.Hub, log *slog.Logger) *Bot {
	return &Bot{
		st: st, wc: wc, eng: eng, sch: sch, hub: hub, log: log,
		dedup:     map[string]struct{}{},
		wrLogins:  map[string]*wrLogin{},
		ctxTokens: map[string]string{},
	}
}

// EnsureOwnerAdmin retroactively grants admin to the WeChat user who bound the
// bot, so installs created before the auto-admin fix also get an operator.
func (b *Bot) EnsureOwnerAdmin() {
	login, err := b.st.GetBotLogin()
	if err != nil || login == nil || strings.TrimSpace(login.UserID) == "" {
		return
	}
	users, err := b.st.ListBotUsers()
	if err == nil {
		for _, u := range users {
			if u.IsAdmin {
				return
			}
		}
	}
	_ = b.st.EnsureBotUser(login.UserID)
	_ = b.st.SetBotUserAdmin(login.UserID, true)
}

// Start launches the monitor and the run-end push pump. It is idempotent.
func (b *Bot) Start() {
	b.mu.Lock()
	if b.stopping {
		b.mu.Unlock()
		return
	}
	if b.ctx != nil && b.ctx.Err() == nil {
		b.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	b.ctx, b.cancel = ctx, cancel
	b.mu.Unlock()

	b.wg.Add(2)
	go func() {
		defer b.wg.Done()
		b.runLoop(ctx)
	}()
	go func() {
		defer b.wg.Done()
		b.eventPump(ctx)
	}()
	b.log.Info("微信机器人渠道已启动")
}

// Stop cancels the loops and waits for them to exit.
func (b *Bot) Stop() {
	b.mu.Lock()
	b.stopping = true
	cancel := b.cancel
	cf := b.botFlow
	// 扫码登录会话持有自己的 10 分钟 deadline；不主动取消会让 Stop 等到超时。
	for _, sess := range b.wrLogins {
		if sess.cancel != nil {
			sess.cancel()
		}
	}
	b.mu.Unlock()
	if cf != nil {
		cf.stop()
	}
	if cancel != nil {
		cancel()
	}
	b.wg.Wait()
	b.mu.Lock()
	b.ctx, b.cancel = nil, nil
	b.mu.Unlock()
}

// Enabled reads the channel switch.
func Enabled(st *store.Store) bool {
	v, _ := st.GetSetting(SettingEnabled)
	return v != "0"
}

// SetEnabled flips the channel switch.
func SetEnabled(st *store.Store, on bool) error {
	v := "1"
	if !on {
		v = "0"
	}
	return st.SetSetting(SettingEnabled, v)
}

// PushRuns reports whether run-end messages are pushed to bound users.
func PushRuns(st *store.Store) bool {
	v, _ := st.GetSetting(SettingPushRuns)
	return v == "" || v == "1"
}

// SetPushRuns toggles run-end push.
func SetPushRuns(st *store.Store, on bool) error {
	v := "1"
	if !on {
		v = "0"
	}
	return st.SetSetting(SettingPushRuns, v)
}

// NeedsRebind reports whether the stored credential was rejected (-14) and the
// channel is waiting for a new QR bind.
func NeedsRebind(st *store.Store) bool {
	v, _ := st.GetSetting(SettingNeedsRebind)
	return v == "1"
}

// SetNeedsRebind persists the "credential stale, rescan needed" state.
func SetNeedsRebind(st *store.Store, on bool) error {
	v := "0"
	if on {
		v = "1"
	}
	return st.SetSetting(SettingNeedsRebind, v)
}

// markPoll records the last successful/failed long-poll for the panel health.
func (b *Bot) markPoll(err error) {
	b.mu.Lock()
	b.lastPoll = time.Now().Unix()
	if err != nil {
		b.lastErr = err.Error()
	} else {
		b.lastErr = ""
	}
	b.mu.Unlock()
}

// rememberContextToken keeps the latest inbound context token per user so
// delayed replies (QR result, run-end push) can carry it like the official
// plugin does.
func (b *Bot) rememberContextToken(userID, token string) {
	if userID == "" || token == "" {
		return
	}
	b.mu.Lock()
	if len(b.ctxTokens) > 2048 {
		b.ctxTokens = map[string]string{}
	}
	b.ctxTokens[userID] = token
	b.mu.Unlock()
}

// contextToken returns the latest inbound token for a user, or "".
func (b *Bot) contextToken(userID string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ctxTokens[userID]
}

// sendText sends a message using the remembered context token, retrying
// without it when the token has gone stale.
func (b *Bot) sendText(ctx context.Context, cl *Client, to, text string) error {
	if text == "" {
		return nil
	}
	token := b.contextToken(to)
	err := cl.SendText(ctx, to, token, text)
	if err != nil && token != "" {
		if err2 := cl.SendText(ctx, to, "", text); err2 == nil {
			return nil
		}
	}
	return err
}

// ---- monitor ----

func (b *Bot) runLoop(ctx context.Context) {
	for ctx.Err() == nil {
		if !Enabled(b.st) {
			sleepCtx(ctx, 15*time.Second)
			continue
		}
		if NeedsRebind(b.st) {
			// 凭据已被服务端拒绝，重试没有意义；等面板重新扫码绑定。
			sleepCtx(ctx, 30*time.Second)
			continue
		}
		login, err := b.st.GetBotLogin()
		if err != nil || login == nil || !login.HasToken {
			sleepCtx(ctx, 20*time.Second)
			continue
		}
		buf, _ := b.st.BotUpdatesBuf()
		b.sessionOnce(ctx, login, buf)
		// sessionOnce returns when credential stale (-14) or channel disabled;
		// pause a bit so a rebind (or re-enable) gets its chance.
		sleepCtx(ctx, 10*time.Second)
	}
}

// sessionOnce serves one credential for its full lifetime.
func (b *Bot) sessionOnce(ctx context.Context, login *store.BotLogin, buf string) {
	cl := b.client(login.BaseURL)
	b.log.Info("微信机器人连接", "bot_id", login.BotID, "base", login.BaseURL)
	_ = cl.NotifyStart(ctx)
	defer func() { _ = cl.NotifyStop(context.Background()) }()

	lastBuf := buf
	consecFail := 0
	for {
		if ctx.Err() != nil || !Enabled(b.st) {
			return
		}
		resp, err := cl.GetUpdates(ctx, lastBuf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			consecFail++
			b.markPoll(err)
			b.log.Warn("微信长轮询失败", "err", err, "n", consecFail)
			if !sleepCtx(ctx, backoffSeconds(consecFail)) {
				return
			}
			continue
		}
		consecFail = 0
		if resp.ErrCode == -14 {
			_ = SetNeedsRebind(b.st, true)
			b.markPoll(fmt.Errorf("bot_token 已失效（-14），请重新扫码绑定"))
			b.log.Warn("机器人登录凭据已失效，请在面板重新扫码绑定", "bot_id", login.BotID)
			return
		}
		if resp.Ret != 0 {
			b.markPoll(fmt.Errorf("微信服务返回错误 ret=%d %s", resp.Ret, resp.ErrMsg))
			b.log.Warn("微信服务返回错误", "ret", resp.Ret, "errmsg", resp.ErrMsg)
			if !sleepCtx(ctx, 10*time.Second) {
				return
			}
			continue
		}
		b.markPoll(nil)
		for i := range resp.Msgs {
			b.dispatch(ctx, cl, &resp.Msgs[i])
		}
		if nb := resp.GetUpdatesBuf; nb != "" && nb != lastBuf {
			lastBuf = nb
			_ = b.st.SaveBotUpdatesBuf(lastBuf)
		}
	}
}

func backoffSeconds(n int) time.Duration {
	d := time.Duration(n*5) * time.Second
	if d > time.Minute {
		d = time.Minute
	}
	return d
}

// ---- inbound dispatch ----

func (b *Bot) dispatch(ctx context.Context, cl *Client, m *Message) {
	if m.MessageType == msgTypeBot {
		return
	}
	from := m.FromUserID
	if from == "" || from == m.ToUserID {
		return
	}
	key := string(m.MessageID) + "|" + m.ClientID
	if key != "|" {
		b.mu.Lock()
		if _, seen := b.dedup[key]; seen {
			b.mu.Unlock()
			return
		}
		b.dedup[key] = struct{}{}
		if len(b.dedup) > 4096 {
			b.dedup = map[string]struct{}{}
		}
		b.mu.Unlock()
	}

	_ = b.st.EnsureBotUser(from)
	b.rememberContextToken(from, m.ContextToken)
	text := strings.TrimSpace(m.Text())
	if text == "" {
		// 非文本内容也要在对话记录里留痕（kind 标注媒体类型）。
		if kind, label := inboundMedia(m); kind != "" {
			b.record(from, "in", kind, label, key)
		}
		return
	}
	b.record(from, "in", "text", text, key)

	bc := &botCtx{bot: b, cl: cl, ctx: ctx, userID: from, contextToken: m.ContextToken}
	if u, err := b.st.GetBotUser(from); err == nil && u != nil {
		bc.admin = u.IsAdmin
	}

	// 未绑定的用户只允许菜单/登录绑定类命令，避免未授权者探测数据。
	binds, err := b.st.UserBoundAccounts(from)
	if err != nil {
		b.log.Error("读取微信绑定失败", "user", from, "err", err)
	}
	bc.binds = binds

	reply := func(msg string) { b.reply(ctx, cl, from, m.ContextToken, msg) }

	// 登录等待验证码的数字输入优先于命令路由。
	if otp := otpOnly(text); otp != "" {
		if ok := b.submitWeradOTP(bc, otp); ok {
			return
		}
	}

	b.route(bc, text, reply)
}

// client builds a fresh iLink client with the current stored token.
func (b *Bot) client(baseURL string) *Client {
	token, _ := b.st.BotToken()
	return NewClient(baseURL, token)
}

// record writes one chat-log row (面板「微信」页对话视图) and notifies SSE.
func (b *Bot) record(userID, direction, kind, content, ref string) {
	ts := time.Now().Unix()
	id, err := b.st.RecordBotMessage(userID, direction, kind, content, ref)
	if err != nil {
		b.log.Warn("写入微信对话记录失败", "err", err)
		return
	}
	if id == 0 {
		return
	}
	b.hub.Publish(engine.Event{Type: "bot.message", TS: ts, Data: map[string]any{
		"id": id, "user_id": userID, "direction": direction,
		"kind": kind, "content": content, "ts": ts,
	}})
}

// inboundMedia maps a non-text inbound message to a chat-log label.
func inboundMedia(m *Message) (kind, label string) {
	for _, it := range m.ItemList {
		switch it.Type {
		case 2:
			return "media", "[图片]"
		case 3:
			return "media", "[语音]"
		case 4:
			return "media", "[文件]"
		case 5:
			return "media", "[视频]"
		}
	}
	return "", ""
}

// reply sends one text reply; failures are logged, never fatal.
func (b *Bot) reply(ctx context.Context, cl *Client, to, contextToken, text string) {
	if text == "" {
		return
	}
	if err := cl.SendText(ctx, to, contextToken, text); err != nil {
		b.log.Warn("微信回复失败", "to", to, "err", err)
		return
	}
	b.record(to, "out", "text", text, "")
}

// replyImage uploads and sends a picture (QR 等)，成功后同样入对话记录。
func (b *Bot) replyImage(ctx context.Context, cl *Client, to, contextToken, caption string, png []byte) error {
	err := cl.SendImage(ctx, to, contextToken, caption, png)
	if err == nil {
		label := caption
		if label == "" {
			label = "[图片]"
		}
		b.record(to, "out", "image", label, "")
	}
	return err
}

// ---- run-end push pump ----

func (b *Bot) eventPump(ctx context.Context) {
	id, ch := b.hub.Subscribe()
	defer b.hub.Unsubscribe(id)
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if ev.Type != "run.end" {
				continue
			}
			data, _ := ev.Data.(map[string]any)
			accountID, _ := num64(data["account_id"])
			if accountID <= 0 {
				continue
			}
			if !PushRuns(b.st) {
				continue
			}
			owner, err := b.st.AccountOwner(accountID)
			if err != nil || owner == "" {
				continue
			}
			text := runEndText(data)
			login, err := b.st.GetBotLogin()
			if err != nil || login == nil || !login.HasToken {
				continue
			}
			cl := b.client(login.BaseURL)
			// 推送不携带 context_token（无对应入站消息）。
			go func(owner string) {
				pushCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				if err := b.sendText(pushCtx, cl, owner, text); err != nil {
					b.log.Warn("运行结果微信推送失败", "to", owner, "err", err)
					return
				}
				b.record(owner, "out", "text", text, "")
			}(owner)
		}
	}
}

func num64(v any) (int64, bool) {
	switch x := v.(type) {
	case int:
		return int64(x), true
	case int64:
		return x, true
	case float64:
		return int64(x), true
	case string:
		if n, err := strconv.ParseInt(x, 10, 64); err == nil {
			return n, true
		}
	}
	return 0, false
}

func runEndText(data map[string]any) string {
	name, _ := data["account_name"].(string)
	status, _ := data["status"].(string)
	label := "失败"
	switch status {
	case "success":
		label = "✅ 阅读"
	case "stopped":
		label = "⏹ 已停止"
	default:
		label = "❌ 阅读失败"
	}
	errMsg, _ := data["error"].(string)
	success, _ := num64(data["success"])
	failed, _ := num64(data["failed"])
	plan, _ := num64(data["target"])
	elapsed, _ := num64(data["elapsed"])
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s ｜ %s\n", label, name))
	b.WriteString(fmt.Sprintf("本次已读：%s（计划 %s）\n", humanMinutes(int(elapsed)), humanMinutes(int(plan))))
	b.WriteString(fmt.Sprintf("心跳：成功 %d 次，失败 %d 次", success, failed))
	if errMsg != "" {
		b.WriteString("\n原因：" + errMsg)
	}
	return b.String()
}

// ---- shared helpers ----

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (b *Bot) siteTZ() *time.Location {
	if name, _ := b.st.GetSetting("timezone"); strings.TrimSpace(name) != "" {
		if loc, err := time.LoadLocation(strings.TrimSpace(name)); err == nil {
			return loc
		}
	}
	return time.Local
}

func humanMinutes(sec int) string {
	m := float64(sec) / 60
	if m >= 10 {
		return fmt.Sprintf("%.0f 分钟", m)
	}
	return fmt.Sprintf("%.1f 分钟", m)
}
