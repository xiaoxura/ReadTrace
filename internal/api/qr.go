package api

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/skip2/go-qrcode"

	"readtrace/internal/store"
	"readtrace/internal/weread"
)

// qrFlow tracks one QR login attempt.
type qrFlow struct {
	UID        string
	CreatedAt  int64
	ExpiresAt  int64
	Status     string // pending | need_otp | binding | confirmed | expired | failed
	AccountID  int64
	Error      string
	Logic      string
	LastPollAt int64
	PNG        []byte
	cancel     context.CancelFunc
	mu         sync.Mutex
}

func (f *qrFlow) snapshot() (status, errMsg, logic string, accountID, expiresAt, lastPollAt int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Status, f.Error, f.Logic, f.AccountID, f.ExpiresAt, f.LastPollAt
}

type qrManager struct {
	mu    sync.Mutex
	flows map[string]*qrFlow
	st    *store.Store
	wc    *weread.Client
	log   *slog.Logger
	// isRunning reports whether an account already has an active reading run;
	// rebinding such an account would race with the engine's cookie renewal.
	isRunning func(accountID int64) bool
}

func newQRManager(st *store.Store, wc *weread.Client, log *slog.Logger, isRunning func(int64) bool) *qrManager {
	if isRunning == nil {
		isRunning = func(int64) bool { return false }
	}
	return &qrManager{flows: map[string]*qrFlow{}, st: st, wc: wc, log: log, isRunning: isRunning}
}

// Start creates a fresh QR login flow.
func (m *qrManager) Start(ctx context.Context) (*qrFlow, error) {
	uid, err := m.wc.LoginUID(ctx)
	if err != nil {
		return nil, err
	}
	png, err := qrcode.Encode(weread.ConfirmURL(uid), qrcode.Medium, 280)
	if err != nil {
		return nil, err
	}

	// getLoginInfo long-polls for ~55s and then returns LOGIN_TIMEOUT. That is
	// only one poll window, not a dead QR: keep an active poll going so a scan
	// at any time is picked up immediately.
	pollCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	flow := &qrFlow{
		UID: uid, CreatedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(10 * time.Minute).Unix(),
		Status: "pending", PNG: png, cancel: cancel,
	}
	m.mu.Lock()
	m.flows[uid] = flow
	m.mu.Unlock()

	go func() {
		defer func() {
			// The poll loop must not panic the whole process; a broken flow
			// simply fails with a refresh suggestion.
			if r := recover(); r != nil {
				m.log.Error("扫码轮询 panic", "uid", uid, "panic", r)
				m.set(uid, func(fl *qrFlow) {
					if fl.Status != "confirmed" {
						fl.Status = "failed"
						fl.Error = "服务内部错误，请刷新二维码重试"
					}
				})
			}
		}()
		m.poll(pollCtx, flow)
	}()
	go m.expire(uid)

	return flow, nil
}

func (m *qrManager) get(uid string) *qrFlow {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.flows[uid]
}

func (m *qrManager) set(uid string, fn func(f *qrFlow)) {
	m.mu.Lock()
	f := m.flows[uid]
	m.mu.Unlock()
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (m *qrManager) poll(ctx context.Context, f *qrFlow) {
	defer f.cancel()
	for {
		if ctx.Err() != nil {
			m.set(f.UID, func(fl *qrFlow) {
				if fl.Status == "pending" {
					fl.Status = "expired"
				}
			})
			return
		}
		res, err := m.wc.LoginPoll(ctx, f.UID, "")
		if err != nil {
			if ctx.Err() != nil {
				m.set(f.UID, func(fl *qrFlow) {
					if fl.Status == "pending" {
						fl.Status = "expired"
					}
				})
				return
			}
			if errors.Is(err, weread.ErrLoginSessionExpired) {
				m.log.Warn("扫码会话已失效", "uid", f.UID)
				m.set(f.UID, func(fl *qrFlow) {
					if fl.Status == "pending" {
						fl.Status = "expired"
					}
				})
				return
			}
			m.log.Warn("扫码登录轮询失败", "err", err)
			if !sleep(ctx, 2*time.Second) {
				return
			}
			continue
		}
		m.set(f.UID, func(fl *qrFlow) {
			fl.Logic = res.LogicCode
			fl.LastPollAt = time.Now().Unix()
		})
		cookieNames := make([]string, 0, len(res.Cookies))
		for k := range res.Cookies {
			cookieNames = append(cookieNames, k)
		}
		sort.Strings(cookieNames)
		m.log.Info("扫码轮询返回",
			"succeed", res.Success, "logic", res.LogicCode,
			"has_token", res.AccessToken != "", "has_vid", res.Vid != "",
			"keys", strings.Join(res.Keys, ","), "cookies", strings.Join(cookieNames, ","))
		if res.Success {
			m.bind(f, res)
			return
		}
		switch strings.ToUpper(res.LogicCode) {
		case "NEED_OTP":
			m.log.Info("扫码成功，等待输入双重验证码", "uid", f.UID)
			m.set(f.UID, func(fl *qrFlow) { fl.Status = "need_otp" })
			return
		case "LOGIN_TIMEOUT":
			// Not an expired QR — just the end of this long-poll window.
			// Immediately start the next poll so a scan is never missed.
			m.log.Info("扫码轮询窗口结束，继续等待扫码", "uid", f.UID)
			continue
		}
		if !sleep(ctx, 900*time.Millisecond) {
			return
		}
	}
}

func (m *qrManager) bind(f *qrFlow, res *weread.LoginPoll) {
	m.set(f.UID, func(fl *qrFlow) { fl.Status = "binding" })

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	ck := weread.Cookies{}
	for k, v := range res.Cookies {
		ck[k] = v
	}
	if res.AccessToken != "" {
		ck["wr_skey"] = res.AccessToken
	}
	vid := res.Vid
	if vid == "" {
		vid = ck["wr_vid"]
	}

	m.wc.Warmup(ctx, ck)
	if vid == "" {
		vid = ck["wr_vid"]
	}
	if vid == "" {
		vid = m.wc.CurrentVID(ctx, ck)
	}
	if vid == "" {
		m.fail(f, "登录成功但未获取到用户 ID，请刷新二维码重试")
		return
	}
	ck["wr_vid"] = vid
	nickname, avatar := m.wc.UserInfo(ctx, ck, vid)

	acc, err := m.st.FindAccountByVid(vid)
	if err != nil {
		m.fail(f, "读取账号失败："+err.Error())
		return
	}
	if acc != nil {
		if m.isRunning(acc.ID) {
			m.fail(f, "该账号正在阅读中，请先停止任务，再重新扫码绑定")
			return
		}
		if err := m.st.SaveCookies(acc.ID, map[string]string(ck)); err != nil {
			m.fail(f, "保存登录状态失败："+err.Error())
			return
		}
		_ = m.st.UpdateIdentity(acc.ID, vid, nickname, avatar)
		_ = m.st.SetAccountStatus(acc.ID, "active", "", time.Now().Unix())
		m.set(f.UID, func(fl *qrFlow) {
			fl.Status = "confirmed"
			fl.AccountID = acc.ID
		})
		return
	}

	name := strings.TrimSpace(nickname)
	if name == "" {
		name = "微信读书账号"
	}
	name = truncateRunes(name, 60)
	id, err := m.st.CreateAccount(store.Account{
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
		m.fail(f, "创建账号失败："+err.Error())
		return
	}
	m.set(f.UID, func(fl *qrFlow) {
		fl.Status = "confirmed"
		fl.AccountID = id
	})
	m.log.Info("扫码登录成功", "account", id, "nickname", nickname)
}

func (m *qrManager) fail(f *qrFlow, msg string) {
	m.set(f.UID, func(fl *qrFlow) {
		fl.Status = "failed"
		fl.Error = msg
	})
	m.log.Warn("扫码登录失败", "err", msg)
}

func (m *qrManager) expire(uid string) {
	time.Sleep(15 * time.Minute)
	m.mu.Lock()
	delete(m.flows, uid)
	m.mu.Unlock()
}

// SubmitOTP completes a flow that asked for a numeric code.
func (m *qrManager) SubmitOTP(uid, code string) error {
	f := m.get(uid)
	if f == nil {
		return fmt.Errorf("二维码已失效，请刷新")
	}
	status, _, _, _, _, _ := f.snapshot()
	switch status {
	case "confirmed", "binding":
		return fmt.Errorf("已经登录成功")
	case "expired", "failed":
		return fmt.Errorf("二维码已失效，请刷新后重新扫码")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
	defer cancel()
	res, err := m.wc.LoginPoll(ctx, uid, code)
	if err != nil {
		return err
	}
	m.log.Info("验证码校验返回", "succeed", res.Success, "logic", res.LogicCode,
		"has_token", res.AccessToken != "", "has_vid", res.Vid != "")
	if res.Success {
		f.cancel()
		m.bind(f, res)
		return nil
	}
	switch strings.ToUpper(res.LogicCode) {
	case "OTP_NOT_MATCH":
		return fmt.Errorf("验证码不正确")
	case "OTP_EXPIRED":
		m.set(uid, func(fl *qrFlow) { fl.Status = "expired" })
		return fmt.Errorf("验证码已过期，请重新扫码")
	case "LOGIN_TIMEOUT":
		m.set(uid, func(fl *qrFlow) { fl.Status = "expired" })
		return fmt.Errorf("二维码已过期，请重新扫码")
	}
	return fmt.Errorf("验证失败，请重试")
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// ---------- HTTP handlers ----------

func (s *Server) handleQRStart(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	f, err := s.qr.Start(ctx)
	if err != nil {
		s.writeErr(w, http.StatusBadGateway, "获取二维码失败："+err.Error())
		return
	}
	status0, _, _, _, expiresAt0, _ := f.snapshot()
	s.writeJSON(w, http.StatusOK, map[string]any{
		"id":          f.UID,
		"qr":          "data:image/png;base64," + base64.StdEncoding.EncodeToString(f.PNG),
		"expires_at":  expiresAt0,
		"status":      status0,
		"ttl_seconds": 600,
	})
}

func (s *Server) handleQRPoll(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	f := s.qr.get(id)
	if f == nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"status": "expired"})
		return
	}
	status, errMsg, logic, accountID, expiresAt, lastPollAt := f.snapshot()
	resp := map[string]any{
		"status":       status,
		"error":        errMsg,
		"account_id":   accountID,
		"expires_at":   expiresAt,
		"logic":        logic,
		"last_poll_at": lastPollAt,
	}
	if status == "confirmed" && accountID > 0 {
		if acc, err := s.st.GetAccount(accountID); err == nil && acc != nil {
			resp["account"] = acc
		}
	}
	s.writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleQROTP(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Code string `json:"code"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	code := strings.TrimSpace(body.Code)
	if code == "" {
		s.writeErr(w, http.StatusBadRequest, "请输入验证码")
		return
	}
	if err := s.qr.SubmitOTP(id, code); err != nil {
		s.writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
