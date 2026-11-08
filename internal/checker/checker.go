// Package checker periodically validates every account's login status so the
// panel notices a dead cookie without a manual check, and pushes an "账号失效"
// notification the first time an account is found expired.
package checker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"readtrace/internal/engine"
	"readtrace/internal/notify"
	"readtrace/internal/store"
	"readtrace/internal/weread"
)

// SettingInterval stores the auto-check cadence, in minutes; 0 disables it.
const SettingInterval = "check_interval_minutes"

// DefaultInterval is the cadence used when the setting is absent/invalid.
const DefaultInterval = defaultInterval

const (
	defaultInterval = 60 // minutes, when the setting is absent/invalid

	// pollEvery is how often the loop re-reads the interval setting. Actual
	// checks fire at intervalMinutes() boundaries on top of this.
	pollEvery = time.Minute

	// perAccountTimeout bounds one account's validate/renewal round trip.
	perAccountTimeout = 30 * time.Second

	// pushTimeout bounds one notification delivery.
	pushTimeout = 25 * time.Second
)

// Checker owns the auto-check loop.
type Checker struct {
	st  *store.Store
	eng *engine.Engine
	wc  *weread.Client
	nt  *notify.Sender
	log *slog.Logger

	stop chan struct{}
	done sync.WaitGroup

	mu       sync.Mutex
	lastRun  time.Time
	running  bool
}

// New creates a Checker (call Start to activate the background loop).
func New(st *store.Store, eng *engine.Engine, wc *weread.Client, nt *notify.Sender, log *slog.Logger) *Checker {
	return &Checker{st: st, eng: eng, wc: wc, nt: nt, log: log, stop: make(chan struct{})}
}

// Start launches the background loop. It never returns an error; per-account
// issues are logged, not fatal.
func (c *Checker) Start() {
	c.done.Add(1)
	go func() {
		defer c.done.Done()
		t := time.NewTicker(pollEvery)
		defer t.Stop()
		for {
			select {
			case <-c.stop:
				return
			case <-t.C:
				c.tick()
			}
		}
	}()
}

// Wait stops the loop at shutdown.
func (c *Checker) Wait() {
	select {
	case <-c.stop:
	default:
		close(c.stop)
	}
	c.done.Wait()
}

// intervalMinutes reads the cadence setting; invalid values fall back to the
// default, 0 disables the loop.
func (c *Checker) intervalMinutes() int {
	v, err := c.st.GetSetting(SettingInterval)
	if err != nil || v == "" {
		return defaultInterval
	}
	n := 0
	for _, r := range v {
		if r < '0' || r > '9' {
			n = -1
			break
		}
		n = n*10 + int(r-'0')
	}
	if n < 0 || n > 24*60 {
		return defaultInterval
	}
	return n
}

// tick runs when the configured interval has elapsed since the last pass.
func (c *Checker) tick() {
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return
	}
	interval := c.intervalMinutes()
	if interval <= 0 || time.Since(c.lastRun) < time.Duration(interval)*time.Minute {
		c.mu.Unlock()
		return
	}
	c.running = true
	c.lastRun = time.Now()
	c.mu.Unlock()

	c.RunOnce(context.Background())
}

// RunOnce validates every account once. Running accounts are skipped (their
// loop refreshes the sign-in state itself) as are accounts without cookies.
func (c *Checker) RunOnce(ctx context.Context) {
	accounts, err := c.st.ListAccounts()
	if err != nil {
		c.log.Warn("自动检查账号状态失败", "err", err)
		return
	}
	checked, expired := 0, 0
	for i := range accounts {
		select {
		case <-ctx.Done():
			return
		default:
		}
		acc := &accounts[i]
		// Never rotate the skey underneath a running reading loop.
		if c.eng.IsRunning(acc.ID) {
			continue
		}
		cookies, _, err := c.st.AccountSecrets(acc.ID)
		if err != nil || len(cookies) == 0 {
			continue
		}
		actx, cancel := context.WithTimeout(ctx, perAccountTimeout)
		outcome, outcomeErr := c.CheckLogin(actx, acc.ID, weread.Cookies(cookies))
		cancel()
		checked++
		switch outcome {
		case "active":
			_ = c.st.SetAccountStatus(acc.ID, "active", "", time.Now().Unix())
		case "expired":
			expired++
			_ = c.st.SetAccountStatus(acc.ID, "expired", validateErrString(outcomeErr), time.Now().Unix())
			if acc.Status != "expired" && acc.PushChannelID > 0 {
				go c.notifyExpired(acc, outcomeErr)
			}
		default: // "error": transient trouble — record it but never expire.
			_ = c.st.SetAccountStatus(acc.ID, acc.Status, "自动检查遇到临时问题："+validateErrString(outcomeErr), time.Now().Unix())
		}
	}
	c.log.Info("账号状态自动检查完成", "checked", checked, "active", checked-expired, "expired", expired)
}

// notifyExpired pushes the "账号失效" template to the account's channel.
func (c *Checker) notifyExpired(acc *store.Account, cause error) {
	ch, err := c.st.GetPushChannel(acc.PushChannelID)
	if err != nil || ch == nil || !ch.Enabled {
		return
	}
	vars := map[string]string{
		"account": acc.Name,
		"error":   validateErrString(cause),
		"time":    time.Now().Format("2006-01-02 15:04:05"),
	}
	title, content := notify.BuildMessage(ch.Templates, notify.KindExpired,
		"微信读书 · 账号失效",
		fmt.Sprintf("账号：%s\n状态：登录已失效，请重新扫码登录\n检查时间：%s", acc.Name, vars["time"]),
		vars)
	ctx, cancel := context.WithTimeout(context.Background(), pushTimeout)
	defer cancel()
	if err := c.nt.Send(ctx, *ch, title, content); err != nil {
		c.log.Warn("账号失效推送失败", "channel", ch.Name, "account", acc.Name, "err", err)
		return
	}
	c.log.Info("已推送账号失效通知", "account", acc.Name, "channel", ch.Name)
}

// CheckLoginClassified validates the cookie, renewing exactly once on a true
// auth failure. It returns a three-way outcome instead of collapsing every
// failure into "expired":
//   - "active":  session valid (possibly after an in-place renewal)
//   - "expired": WeRead explicitly rejected the session after a renewal retry
//   - "error":   transient failure (network, proxy, unexpected API code or
//     risk control HTML); the caller must NOT expire the account
//
// /web/user can reject non-dead sessions with odd codes (e.g. -2003
// 参数格式错误 after recent API changes), so an unknown failure on a not-running
// account gets one renewal probe: a live session can always renew, a dead one
// is refused — that decides liveness more reliably than /web/user itself.
func (c *Checker) CheckLogin(ctx context.Context, accountID int64, ck weread.Cookies) (string, error) {
	isRunning := func(id int64) bool { return c.eng.IsRunning(id) }
	return CheckLogin(ctx, c.st, c.wc, isRunning, accountID, ck)
}

// CheckLogin is the shared classified check, callable without a Checker.
func CheckLogin(ctx context.Context, st *store.Store, wc *weread.Client,
	isRunning func(int64) bool, accountID int64, ck weread.Cookies,
) (string, error) {
	err := wc.Validate(ctx, ck)
	if err == nil {
		return "active", nil
	}
	if !errors.Is(err, weread.ErrUnauthorized) && !errors.Is(err, weread.ErrRiskControl) {
		if isRunning(accountID) {
			// Never rotate the skey underneath a running loop; report only.
			return "error", err
		}
		key, renewErr := wc.Renew(ctx, ck)
		if renewErr != nil || key == "" {
			// Can't prove liveness one way or the other; keep the account
			// untouched and surface the original failure.
			return "error", err
		}
		_ = st.SaveCookies(accountID, map[string]string(ck))
		// Renewal succeeded ⇒ the session is alive even though /web/user misbehaved.
		return "active", nil
	}
	key, renewErr := wc.Renew(ctx, ck)
	if renewErr != nil || key == "" {
		if renewErr == nil {
			renewErr = errors.New("renewal 未返回新的 wr_skey")
		}
		return "expired", renewErr
	}
	_ = st.SaveCookies(accountID, map[string]string(ck))
	e := wc.Validate(ctx, ck)
	if e == nil {
		return "active", nil
	}
	if !errors.Is(e, weread.ErrUnauthorized) && !errors.Is(e, weread.ErrRiskControl) {
		return "error", e
	}
	return "expired", e
}

func validateErrString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
