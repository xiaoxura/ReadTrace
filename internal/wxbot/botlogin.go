// 机器人自身的微信绑定流程（面板触发，管理员扫码）。
package wxbot

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/skip2/go-qrcode"
)

// BotLoginState 仅暴露给面板 HTTP API 的快照。
type BotLoginState struct {
	Status  string `json:"status"` // idle | pending | need_otp | confirmed | failed
	QR      string `json:"qr,omitempty"`
	Error   string `json:"error,omitempty"`
	BotID   string `json:"bot_id,omitempty"`
	UserID  string `json:"user_id,omitempty"`
	BaseURL string `json:"base_url,omitempty"`
}

type botLoginFlow struct {
	mu         sync.Mutex
	status     string
	qrContent  string
	png        []byte
	errMsg     string
	qrcodeID   string
	botID      string
	userID     string
	baseURL    string
	verifyCode string
	cancel     context.CancelFunc
	done       chan struct{}
}

func (f *botLoginFlow) stop() {
	if f == nil {
		return
	}
	f.mu.Lock()
	cancel := f.cancel
	f.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (f *botLoginFlow) snapshot(st *BotLoginState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st.Status = f.status
	st.Error = f.errMsg
	st.BotID = f.botID
	st.UserID = f.userID
	st.BaseURL = f.baseURL
	if len(f.png) > 0 {
		st.QR = "data:image/png;base64," + toBase64(f.png)
	}
}

func (f *botLoginFlow) set(status, errMsg string) {
	f.mu.Lock()
	f.status, f.errMsg = status, errMsg
	f.mu.Unlock()
}

// StartBotLogin launches a fresh QR binding flow (cancels any previous one).
func (b *Bot) StartBotLogin() (BotLoginState, error) {
	ctx, cancel := context.WithCancel(context.Background())
	flow := &botLoginFlow{status: "pending", cancel: cancel, done: make(chan struct{})}
	b.mu.Lock()
	old := b.botFlow
	if old != nil {
		old.stop()
	}
	b.botFlow = flow
	b.mu.Unlock()

	cl := NewClient(DefaultILinkBaseURL, "")
	qrc, err := cl.GetQRCode(ctx)
	if err != nil {
		cancel()
		close(flow.done)
		return BotLoginState{Status: "failed", Error: err.Error()}, err
	}
	png, err := qrcode.Encode(qrc.QRCodeImgContent, qrcode.Medium, 320)
	if err != nil {
		cancel()
		close(flow.done)
		return BotLoginState{Status: "failed", Error: err.Error()}, err
	}
	flow.mu.Lock()
	flow.qrContent = qrc.QRCodeImgContent
	flow.png = png
	flow.qrcodeID = qrc.QRCode
	flow.status = "pending"
	flow.mu.Unlock()

	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		defer close(flow.done)
		defer cancel()
		b.runBotLoginFlow(flow, cl, ctx)
	}()
	var st BotLoginState
	flow.snapshot(&st)
	return st, nil
}

// runBotLoginFlow mirrors the official plugin's QR state machine.
func (b *Bot) runBotLoginFlow(flow *botLoginFlow, cl *Client, ctx context.Context) {
	deadline := time.Now().Add(8 * time.Minute)
	refreshCount := 0
	verifyCode := ""
	for {
		if ctx.Err() != nil {
			return
		}
		if time.Now().After(deadline) {
			flow.set("failed", "二维码超时，请重新获取")
			return
		}
		// 只读取二维码 ID，长轮询必须在锁外进行，否则面板每 2 秒一次的状态
		// 轮询和 OTP 提交都会被 35 秒的长轮询阻塞。
		flow.mu.Lock()
		qrcodeID := flow.qrcodeID
		flow.mu.Unlock()

		state, err := cl.PollQRStatus(ctx, qrcodeID, verifyCode)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			b.log.Warn("机器人扫码状态轮询失败", "err", err)
			if !sleepCtx(ctx, 2*time.Second) {
				return
			}
			continue
		}

		switch state.Status {
		case QRWait, QRScaned:
			verifyCode = ""
			flow.set("pending", "")
		case QRExpired:
			verifyCode = ""
			refreshCount++
			if refreshCount > 3 {
				flow.set("failed", "二维码多次失效，请重新获取")
				return
			}
			if err := b.refreshBotQR(flow, cl, ctx); err != nil {
				flow.set("failed", "刷新二维码失败："+err.Error())
				return
			}
		case QRVerifyBlocked:
			flow.set("failed", "手机显示的数字多次输错，请重新扫码")
			return
		case QRNeedVerifyCode:
			msg := "请在面板输入手机微信上显示的数字"
			if verifyCode != "" {
				msg = "数字验证码不正确，请重新输入"
			}
			verifyCode = ""
			flow.set("need_otp", msg)
			// 等待面板提交验证码；面板会 POST /api/wxbot/login/otp。
			code := b.waitVerifyCode(flow, ctx)
			if code == "" {
				return
			}
			// 下一次轮询必须带上验证码（官方插件的 pendingVerifyCode 语义）。
			verifyCode = code
			flow.set("pending", "")
			continue
		case QRRedirect:
			// IDC 调度：此后改用 redirect_host 轮询（与官方插件一致）。
			if state.RedirectHost != "" {
				cl = NewClient("https://"+strings.TrimPrefix(state.RedirectHost, "https://"), "")
				flow.mu.Lock()
				flow.baseURL = cl.BaseURL()
				flow.mu.Unlock()
			}
			flow.set("pending", "")
		case QRBindedRedirect:
			// 服务端识别出该机器人已绑定；需要本地已存有 token。
			if t, _ := b.st.BotToken(); strings.TrimSpace(t) != "" {
				_ = SetNeedsRebind(b.st, false)
				b.Start()
				flow.set("confirmed", "")
				return
			}
			flow.set("failed", "服务端提示已绑定过，但本机没有凭据，请清空后重扫")
			return
		case QRConfirmed:
			if state.ILinkBotID == "" {
				flow.set("failed", "登录成功但服务端未返回 bot_id")
				return
			}
			if err := b.st.SaveBotLogin(state.ILinkBotID, state.ILinkUserID, state.BotToken, state.BaseURL); err != nil {
				flow.set("failed", "保存机器人凭据失败："+err.Error())
				return
			}
			_ = b.st.SaveBotUpdatesBuf("")
			_ = SetNeedsRebind(b.st, false)
			_ = SetEnabled(b.st, true)
			// 扫码人自动成为管理员（面板据此判断谁可以操作全部账号）。
			if uid := strings.TrimSpace(state.ILinkUserID); uid != "" {
				if err := b.st.EnsureBotUser(uid); err == nil {
					_ = b.st.SetBotUserAdmin(uid, true)
				}
			}
			flow.mu.Lock()
			flow.botID, flow.userID, flow.baseURL = state.ILinkBotID, state.ILinkUserID, state.BaseURL
			flow.mu.Unlock()
			flow.set("confirmed", "")
			b.log.Info("微信机器人绑定成功", "bot_id", state.ILinkBotID, "admin_user", state.ILinkUserID)
			// 如果启动时渠道是关闭的，Start 还没有跑过；这里补启动（幂等）。
			b.Start()
			return
		}
		if !sleepCtx(ctx, time.Second) {
			return
		}
	}
}

func (b *Bot) refreshBotQR(flow *botLoginFlow, cl *Client, ctx context.Context) error {
	qrc, err := cl.GetQRCode(ctx)
	if err != nil {
		return err
	}
	png, err := qrcode.Encode(qrc.QRCodeImgContent, qrcode.Medium, 320)
	if err != nil {
		return err
	}
	flow.mu.Lock()
	flow.qrContent = qrc.QRCodeImgContent
	flow.qrcodeID = qrc.QRCode
	flow.png = png
	flow.mu.Unlock()
	flow.set("pending", "")
	return nil
}

// waitVerifyCode blocks until SubmitBotOTP supplies the pairing digits.
func (b *Bot) waitVerifyCode(flow *botLoginFlow, ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				flow.set("failed", "等待验证码超时，请重新获取二维码")
			}
			return ""
		case <-t.C:
			flow.mu.Lock()
			code := flow.verifyCode
			if code != "" {
				flow.verifyCode = ""
			}
			flow.mu.Unlock()
			if code != "" {
				return code
			}
		}
	}
}

// SubmitBotOTP submits the phone-displayed digits for the binding flow.
func (b *Bot) SubmitBotOTP(code string) BotLoginState {
	b.mu.Lock()
	flow := b.botFlow
	b.mu.Unlock()
	if flow != nil {
		flow.mu.Lock()
		if flow.status == "need_otp" {
			flow.verifyCode = strings.TrimSpace(code)
		}
		flow.mu.Unlock()
	}
	var st BotLoginState
	if flow != nil {
		flow.snapshot(&st)
	}
	return st
}

// BotLoginSnapshot returns the current flow's status for polling.
func (b *Bot) BotLoginSnapshot() BotLoginState {
	b.mu.Lock()
	flow := b.botFlow
	b.mu.Unlock()
	if flow == nil {
		return BotLoginState{Status: "idle"}
	}
	var st BotLoginState
	flow.snapshot(&st)
	return st
}

// Status returns the panel-facing channel summary.
func (b *Bot) Status() map[string]any {
	login, err := b.st.GetBotLogin()
	b.mu.Lock()
	lastPoll, lastErr := b.lastPoll, b.lastErr
	b.mu.Unlock()
	out := map[string]any{
		"enabled":      Enabled(b.st),
		"push_runs":    PushRuns(b.st),
		"bound":        false,
		"needs_rebind": NeedsRebind(b.st),
		"last_poll_at": lastPoll,
		"last_error":   lastErr,
	}
	if err == nil && login != nil {
		token, _ := b.st.BotToken()
		out["bound"] = token != ""
		out["bot_id"] = login.BotID
		out["user_id"] = login.UserID
		out["base_url"] = login.BaseURL
	}
	users, _ := b.st.ListBotUsers()
	out["users"] = len(users)
	binds, _ := b.st.ListBotBindings()
	out["bindings"] = len(binds)
	return out
}
