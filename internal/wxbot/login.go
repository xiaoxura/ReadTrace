// 微信读书扫码登录会话：每个微信用户独立会话，互不串号。
package wxbot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/skip2/go-qrcode"

	"readtrace/internal/store"
	"readtrace/internal/weread"
)

// wrLogin is one user-scoped WeRead login flow.
type wrLogin struct {
	userID    string
	cl        *Client
	uid       string
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	mu        sync.Mutex
	status    string // pending | need_otp | done
	errMsg    string
	accountID int64
}

func (l *wrLogin) setStatus(st, msg string) {
	l.mu.Lock()
	l.status = st
	l.errMsg = msg
	l.mu.Unlock()
}

func (l *wrLogin) snapshot() (status, errMsg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.status, l.errMsg
}

// cmdLogin 回复「登录/绑定/扫码」——为该微信用户开启一个新的扫码会话。
func (b *Bot) cmdLogin(bc *botCtx, reply func(string)) {
	sctx, cancel := context.WithCancel(context.Background())
	sess := &wrLogin{
		cl:     bc.cl,
		ctx:    sctx,
		cancel: cancel,
		done:   make(chan struct{}),
	}
	b.mu.Lock()
	if old, ok := b.wrLogins[bc.userID]; ok && old.cancel != nil {
		old.cancel() // 一个用户同时只保留一个登录会话
	}
	b.wrLogins[bc.userID] = sess
	b.mu.Unlock()

	deadline, cancelTimeout := context.WithTimeout(sctx, 10*time.Minute)
	uid, err := b.wc.LoginUID(deadline)
	if err != nil {
		cancelTimeout()
		cancel()
		sess.setStatus("done", "获取二维码失败："+err.Error())
		reply("获取微信读书登录二维码失败，请稍后再试：" + err.Error())
		b.cleanupLogin(bc.userID, sess)
		return
	}
	png, err := qrcode.Encode(weread.ConfirmURL(uid), qrcode.Medium, 280)
	if err != nil {
		cancelTimeout()
		cancel()
		sess.setStatus("done", "生成二维码失败")
		reply("生成二维码失败：" + err.Error())
		b.cleanupLogin(bc.userID, sess)
		return
	}
	sess.mu.Lock()
	sess.uid, sess.status = uid, "pending"
	sess.mu.Unlock()

	if err := b.replyImage(bc.ctx, bc.cl, bc.userID, bc.contextToken, "", png); err != nil {
		cancelTimeout()
		cancel()
		reply("二维码图片发送失败：" + err.Error())
		b.cleanupLogin(bc.userID, sess)
		return
	}
	reply("📱 请用手机微信扫描上方二维码并确认登录。\n" +
		"若手机显示 4 位数字验证码，直接回复数字即可。\n" +
		"登录成功后该微信读书账号将绑定到你的微信，数据仅你可见。会话 10 分钟内有效。")

	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		// deadline 的所有权交给轮询 goroutine：cmdLogin 返回时不能取消它，
		// 否则 pollWeradLogin 一启动就会看到 ctx.Err() 并误报二维码过期。
		defer cancelTimeout()
		b.pollWeradLogin(sess, deadline)
	}()
}

// cleanupLogin removes the finished session for this user.
func (b *Bot) cleanupLogin(userID string, sess *wrLogin) {
	b.mu.Lock()
	current := b.wrLogins[userID]
	b.mu.Unlock()
	if current == sess {
		b.mu.Lock()
		delete(b.wrLogins, userID)
		b.mu.Unlock()
	}
}

// pollWeradLogin long-poll WeRead until scanned/confirmed/OTP/expired.
// 直接复用 panel qrManager 的状态机语义（LOGIN_TIMEOUT 属正常窗口结束）。
func (b *Bot) pollWeradLogin(sess *wrLogin, ctx context.Context) {
	defer close(sess.done)
	defer sess.cancel()
	for {
		if ctx.Err() != nil {
			status, _ := sess.snapshot()
			if status == "pending" || status == "need_otp" {
				sess.setStatus("done", "二维码已过期")
				b.safeReply(sess.cl, sess.userID, "二维码已过期，请重新发送「登录」。")
				b.cleanupLogin(sess.userID, sess)
			}
			return
		}
		res, err := b.wc.LoginPoll(ctx, sess.uid, "")
		if err != nil {
			if ctx.Err() != nil {
				continue
			}
			if errors.Is(err, weread.ErrLoginSessionExpired) {
				sess.setStatus("done", "扫码会话失效")
				b.safeReply(sess.cl, sess.userID, "扫码会话已失效，请重新发送「登录」。")
				b.cleanupLogin(sess.userID, sess)
				return
			}
			b.log.Warn("微信读书扫码轮询失败", "err", err)
			if !sleepCtx(ctx, 2*time.Second) {
				continue
			}
			continue
		}
		if res.Success {
			b.bindWeradLogin(sess, res)
			return
		}
		switch strings.ToUpper(res.LogicCode) {
		case "NEED_OTP":
			sess.setStatus("need_otp", "")
			b.safeReply(sess.cl, sess.userID, "📱 请回复手机微信上显示的数字验证码（不在这个窗口，直接在聊天框回复 4 位数字）。")
			return // 等用户把数字发进来（submitWeradOTP）
		case "LOGIN_TIMEOUT":
			continue // 单轮长轮询窗口正常结束，继续下一轮
		}
		if !sleepCtx(ctx, 900*time.Millisecond) {
			continue
		}
	}
}

// submitWeradOTP handles a numeric reply while a login waits for OTP.
func (b *Bot) submitWeradOTP(bc *botCtx, code string) bool {
	b.mu.Lock()
	sess := b.wrLogins[bc.userID]
	b.mu.Unlock()
	if sess == nil {
		return false
	}
	status, _ := sess.snapshot()
	if status != "need_otp" {
		return false
	}

	otpCtx, cancelOTP := context.WithTimeout(bc.ctx, 30*time.Second)
	res, err := b.wc.LoginPoll(otpCtx, sess.uid, code)
	cancelOTP()
	if err != nil {
		bc.reply("验证码校验失败：" + err.Error())
		return true
	}
	if res.Success {
		b.bindWeradLogin(sess, res)
		return true
	}
	switch strings.ToUpper(res.LogicCode) {
	case "OTP_NOT_MATCH":
		bc.reply("❌ 验证码不正确，请重新回复手机上显示的数字。")
	case "OTP_EXPIRED":
		sess.setStatus("done", "验证码过期")
		b.cleanupLogin(bc.userID, sess)
		bc.reply("验证码已过期，请重新发送「登录」获取新二维码。")
	case "LOGIN_TIMEOUT":
		sess.setStatus("done", "二维码过期")
		b.cleanupLogin(bc.userID, sess)
		bc.reply("二维码已过期，请重新发送「登录」。")
	default:
		bc.reply("验证失败：" + res.ErrMsg + "，请重试。")
	}
	return true
}

// bindWeradLogin binds the scanned WeRead account to the session owner.
// 逻辑与面板 qrManager.bind 一致，额外多一步「微信用户绑定」。
func (b *Bot) bindWeradLogin(sess *wrLogin, res *weread.LoginPoll) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	bind := func(ok bool, msg string, accountID int64) {
		sess.mu.Lock()
		sess.status, sess.errMsg, sess.accountID = "done", msg, accountID
		sess.mu.Unlock()
		b.safeReply(sess.cl, sess.userID, msg)
		b.cleanupLogin(sess.userID, sess)
	}

	ck := weread.Cookies{}
	for k, v := range res.Cookies {
		ck[k] = v
	}
	if res.AccessToken != "" {
		ck["wr_skey"] = res.AccessToken
	}
	vid := res.Vid
	b.wc.Warmup(ctx, ck)
	if vid == "" {
		vid = ck["wr_vid"]
	}
	if vid == "" {
		vid = b.wc.CurrentVID(ctx, ck)
	}
	if vid == "" {
		bind(false, "登录成功但未获取到用户 ID，请重新发送「登录」再扫码。", 0)
		return
	}
	ck["wr_vid"] = vid
	nickname, avatar := b.wc.UserInfo(ctx, ck, vid)

	acc, err := b.st.FindAccountByVid(vid)
	if err != nil {
		bind(false, "读取账号失败："+err.Error(), 0)
		return
	}
	if acc != nil {
		if b.eng.IsRunning(acc.ID) {
			bind(false, "该账号正在阅读中，请从面板停止任务后再重新扫码绑定。", acc.ID)
			return
		}
		if err := b.st.SaveCookies(acc.ID, map[string]string(ck)); err != nil {
			bind(false, "保存登录状态失败："+err.Error(), acc.ID)
			return
		}
		_ = b.st.UpdateIdentity(acc.ID, vid, nickname, avatar)
		_ = b.st.SetAccountStatus(acc.ID, "active", "", time.Now().Unix())
		if _, err := b.st.BindUserAccount(sess.userID, acc.ID); err != nil {
			bind(false, "账号登录成功但绑定微信用户失败："+err.Error(), acc.ID)
			return
		}
		name := acc.Name
		if nickname != "" {
			name = nickname
		}
		bind(true, fmt.Sprintf("✅ 登录成功！微信读书账号「%s」已绑定到你的微信。\n回复「我的账号」查看，回复「开始 %d 30」立即开始阅读。", name, acc.Seq), acc.ID)
		return
	}

	name := strings.TrimSpace(nickname)
	if name == "" {
		name = "微信读书账号"
	}
	id, err := b.st.CreateAccount(store.Account{
		Name:           name,
		Vid:            vid,
		Nickname:       nickname,
		Avatar:         avatar,
		Status:         "active",
		DefaultMinutes: 30,
		TickSeconds:    30,
		Mode:           "stable",
		LastCheckAt:    time.Now().Unix(),
	}, map[string]string(ck), nil)
	if err != nil {
		bind(false, "创建账号失败："+err.Error(), 0)
		return
	}
	if _, err := b.st.BindUserAccount(sess.userID, id); err != nil {
		bind(false, "账号已登录，但绑定微信用户失败："+err.Error(), id)
		return
	}
	acc, _ = b.st.GetAccount(id)
	seq := int64(0)
	if acc != nil {
		seq = acc.Seq
	}
	bind(true, fmt.Sprintf("✅ 登录成功！微信读书账号「%s」已绑定到你的微信。\n回复「我的账号」查看，回复「开始 %d 30」立即开始阅读。", name, seq), id)
}

// safeReply sends text via the stored client best-effort（记录到对话面板）。
func (b *Bot) safeReply(cl *Client, to, msg string) {
	if msg == "" || to == "" || cl == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := b.sendText(ctx, cl, to, msg); err != nil {
		b.log.Warn("微信主动回复失败", "to", to, "err", err)
		return
	}
	b.record(to, "out", "text", msg, "")
}
