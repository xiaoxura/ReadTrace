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

	mu      sync.Mutex
	lastRun time.Time
	running bool
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

	defer func() {
		c.mu.Lock()
		c.running = false
		c.mu.Unlock()
	}()
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
		actx, cancel := context.WithTimeout(ctx, perAccountTimeout)
		var notifyAcc *store.Account
		outcome, outcomeErr, opErr := CheckLoginAndUpdate(actx, c.st, c.wc, c.eng, acc.ID,
			func(_ weread.Cookies, outcome string, checkErr error) error {
				current, err := c.st.GetAccount(acc.ID)
				if err != nil {
					return err
				}
				if current == nil {
					return errors.New("账号不存在")
				}
				switch outcome {
				case "active":
					return c.st.SetAccountStatus(acc.ID, "active", "", time.Now().Unix())
				case "expired":
					if current.Status != "expired" && current.PushChannelID > 0 {
						notifyAcc = current
					}
					return c.st.SetAccountStatus(acc.ID, "expired", validateErrString(checkErr), time.Now().Unix())
				default:
					// Keep the current status and last error on transient failures.
					if current.Status != "expired" {
						return c.st.SetAccountStatus(acc.ID, current.Status, "", time.Now().Unix())
					}
					return nil
				}
			})
		cancel()
		if errors.Is(opErr, engine.ErrAccountRunning) || errors.Is(opErr, ErrNoCookies) {
			continue
		}
		if opErr != nil {
			c.log.Warn("账号自动检查提交失败", "account", acc.Name, "err", opErr)
			continue
		}
		checked++
		switch outcome {
		case "active":
		case "expired":
			expired++
			if notifyAcc != nil {
				go c.notifyExpired(notifyAcc, outcomeErr)
			}
		default: // "error": transient trouble — log only, never touch the row.
			// -2003 这类参数错误/网络抖动是临时的，手动检查就能恢复；
			// 写进 last_error 只会让账号列表反复出现「自动检查遇到临时问题」
			// 的噪音。保持 last_error 原样（失效账号的真报错不受影响），刷新
			// last_check_at 即可。
			c.log.Info("账号自动检查遇到临时错误，本次跳过",
				"account", acc.Name, "status", acc.Status, "err", validateErrString(outcomeErr))
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
	return CheckLoginWithEngine(ctx, c.st, c.wc, c.eng, accountID, ck)
}

// ErrNoCookies means an account has no credentials to validate.
var ErrNoCookies = errors.New("账号没有可用的 Cookie")

// CheckLoginWithEngine serializes renewal with credential replacement and run
// startup. The latest stored cookies are loaded inside that account lock.
func CheckLoginWithEngine(ctx context.Context, st *store.Store, wc *weread.Client,
	eng *engine.Engine, accountID int64, ck weread.Cookies,
) (string, error) {
	outcome, checkErr, opErr := CheckLoginAndUpdate(ctx, st, wc, eng, accountID, nil)
	if errors.Is(opErr, engine.ErrAccountRunning) {
		// A running loop owns the skey. Validate only; never renew or persist the
		// caller's possibly stale cookie copy underneath it.
		if validateErr := wc.Validate(ctx, ck); validateErr != nil {
			return "error", validateErr
		}
		return "active", nil
	}
	if opErr != nil {
		return "error", opErr
	}
	return outcome, checkErr
}

// CheckLoginAndUpdate holds the account lock from the latest cookie read
// through validation, optional renewal and the caller's resulting DB update.
// A running account is left untouched: its loop owns the credentials/status.
func CheckLoginAndUpdate(ctx context.Context, st *store.Store, wc *weread.Client,
	eng *engine.Engine, accountID int64,
	update func(ck weread.Cookies, outcome string, checkErr error) error,
) (outcome string, checkErr, opErr error) {
	opErr = eng.WithIdleAccount(accountID, func() error {
		latest, _, readErr := st.AccountSecrets(accountID)
		if readErr != nil {
			return readErr
		}
		if len(latest) == 0 {
			return ErrNoCookies
		}
		ck := weread.Cookies(latest)
		outcome, checkErr = CheckLogin(ctx, st, wc, eng.IsRunning, accountID, ck)
		if update != nil {
			return update(ck, outcome, checkErr)
		}
		return nil
	})
	return outcome, checkErr, opErr
}

// CheckLogin is the shared classified check, callable without a Checker.
func CheckLogin(ctx context.Context, st *store.Store, wc *weread.Client,
	isRunning func(int64) bool, accountID int64, ck weread.Cookies,
) (string, error) {
	err := wc.Validate(ctx, ck)
	if err == nil {
		return "active", nil
	}
	if isRunning(accountID) {
		return "error", err
	}
	if !errors.Is(err, weread.ErrUnauthorized) && !errors.Is(err, weread.ErrRiskControl) {
		key, renewErr := wc.Renew(ctx, ck)
		if renewErr != nil || key == "" {
			if errors.Is(renewErr, weread.ErrUnauthorized) {
				return "expired", renewErr
			}
			// Can't prove liveness one way or the other; keep the account
			// untouched and surface the original failure.
			return "error", err
		}
		if saveErr := st.SaveCookies(accountID, map[string]string(ck)); saveErr != nil {
			return "error", fmt.Errorf("保存续期 Cookie 失败: %w", saveErr)
		}
		// Renewal succeeded ⇒ the session is alive even though /web/user misbehaved.
		return "active", nil
	}
	key, renewErr := wc.Renew(ctx, ck)
	if renewErr != nil || key == "" {
		if errors.Is(renewErr, weread.ErrUnauthorized) {
			return "expired", renewErr
		}
		if renewErr == nil {
			renewErr = errors.New("renewal 未返回新的 wr_skey")
		}
		return "error", renewErr
	}
	if saveErr := st.SaveCookies(accountID, map[string]string(ck)); saveErr != nil {
		return "error", fmt.Errorf("保存续期 Cookie 失败: %w", saveErr)
	}
	e := wc.Validate(ctx, ck)
	if e == nil {
		return "active", nil
	}
	if errors.Is(e, weread.ErrUnauthorized) {
		return "expired", e
	}
	return "error", e
}

func validateErrString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
