// Package engine runs the reading heartbeats and broadcasts live progress.
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"strconv"
	"sync"
	"time"

	"readtrace/internal/notify"
	"readtrace/internal/store"
	"readtrace/internal/weread"
)

// Progress is the live state of one run.
type Progress struct {
	RunID       int64  `json:"run_id"`
	AccountID   int64  `json:"account_id"`
	AccountName string `json:"account_name"`
	Target      int    `json:"target"`
	Elapsed     int    `json:"elapsed"`
	Success     int    `json:"success"`
	Failed      int    `json:"failed"`
	Status      string `json:"status"`
	StartedAt   int64  `json:"started_at"`
	TickSeconds int    `json:"tick_seconds"`
}

type activeRun struct {
	runID     int64
	accountID int64
	account   string
	target    int
	started   int64
	elapsed   int
	success   int
	failed    int
	status    string
	tick      int

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

// tickState carries cumulative counters across automatic retries so a retried
// session keeps adding up on top of what the earlier attempts achieved.
type tickState struct {
	elapsed   int
	success   int
	failed    int // cumulative, persisted to the run row and shown in the UI
	tickIndex int
	last      time.Time // previous tick timestamp used by the signature
}

// Engine manages concurrent reading runs.
type Engine struct {
	st  *store.Store
	wc  *weread.Client
	nt  *notify.Sender
	hub *Hub
	log *slog.Logger
	mu  sync.Mutex
	act map[int64]*activeRun
}

// New creates an Engine.
func New(st *store.Store, wc *weread.Client, nt *notify.Sender, hub *Hub, log *slog.Logger) *Engine {
	return &Engine{st: st, wc: wc, nt: nt, hub: hub, log: log, act: map[int64]*activeRun{}}
}

// Hub exposes the event hub.
func (e *Engine) Hub() *Hub { return e.hub }

// Start begins a run for the account. It returns the created run row.
func (e *Engine) Start(acc *store.Account, minutes int, trigger string) (*store.Run, error) {
	if acc == nil {
		return nil, errors.New("账号不存在")
	}
	if minutes <= 0 {
		minutes = acc.DefaultMinutes
	}
	if minutes <= 0 {
		minutes = 30
	}
	if minutes > 24*60 {
		minutes = 24 * 60
	}
	target := minutes * 60

	cookies, payload, err := e.st.AccountSecrets(acc.ID)
	if err != nil {
		return nil, err
	}
	if len(cookies) == 0 {
		return nil, errors.New("该账号没有可用的 Cookie，请重新登录")
	}
	template := payload
	if len(template) == 0 {
		template = weread.DefaultTemplate()
	}

	run := &store.Run{
		AccountID:   acc.ID,
		Trigger:     trigger,
		PlanSeconds: target,
		Status:      "running",
		StartedAt:   time.Now().Unix(),
	}

	e.mu.Lock()
	for _, ar := range e.act {
		if ar.accountID == acc.ID {
			e.mu.Unlock()
			return nil, fmt.Errorf("「%s」正在阅读中，请先停止", acc.Name)
		}
	}
	if err := e.st.CreateRun(run); err != nil {
		e.mu.Unlock()
		return nil, fmt.Errorf("创建运行记录失败: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ar := &activeRun{
		runID: run.ID, accountID: acc.ID, account: acc.Name, target: target,
		started: run.StartedAt, status: "running", tick: clampTick(acc.TickSeconds),
		ctx: ctx, cancel: cancel, done: make(chan struct{}),
	}
	e.act[run.ID] = ar
	e.mu.Unlock()

	e.hub.Publish(Event{Type: "run.start", RunID: run.ID, AccountID: acc.ID, TS: time.Now().Unix(), Data: e.progress(ar)})
	startMsg := fmt.Sprintf("开始阅读：目标 %s，间隔 %d 秒，模式 %s", humanMinutes(target), clampTick(acc.TickSeconds), modeName(acc.Mode))
	if clampRetries(acc.MaxRetries) > 0 {
		startMsg += fmt.Sprintf("，失败自动重试 %d 次（间隔 %d 秒）", clampRetries(acc.MaxRetries), clampDelay(acc.RetryDelay))
	}
	e.logLine(ar, "info", startMsg)
	go e.loop(ar, acc, weread.Cookies(cookies), template)
	return run, nil
}

// Stop cancels a run and waits briefly for it to finalize.
func (e *Engine) Stop(runID int64) error {
	e.mu.Lock()
	ar := e.act[runID]
	e.mu.Unlock()
	if ar == nil {
		return errors.New("运行不存在或已结束")
	}
	ar.cancel()
	select {
	case <-ar.done:
	case <-time.After(8 * time.Second):
	}
	return nil
}

// StopByAccount cancels the account's active run, if any. Used when the
// account itself is being removed so a racing run cannot keep writing rows.
func (e *Engine) StopByAccount(accountID int64) {
	e.mu.Lock()
	var runID int64
	for _, ar := range e.act {
		if ar.accountID == accountID {
			runID = ar.runID
			break
		}
	}
	e.mu.Unlock()
	if runID > 0 {
		_ = e.Stop(runID)
	}
}

// StopAll cancels every active run (used on shutdown).
func (e *Engine) StopAll() {
	e.mu.Lock()
	runs := make([]*activeRun, 0, len(e.act))
	for _, ar := range e.act {
		runs = append(runs, ar)
	}
	e.mu.Unlock()
	for _, ar := range runs {
		ar.cancel()
	}
	for _, ar := range runs {
		select {
		case <-ar.done:
		case <-time.After(5 * time.Second):
		}
	}
}

// Snapshot returns all active runs.
func (e *Engine) Snapshot() []Progress {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Progress, 0, len(e.act))
	for _, ar := range e.act {
		out = append(out, e.progressLocked(ar))
	}
	return out
}

// IsRunning tells whether the account currently has an active run.
func (e *Engine) IsRunning(accountID int64) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, ar := range e.act {
		if ar.accountID == accountID {
			return true
		}
	}
	return false
}

func (e *Engine) progress(ar *activeRun) Progress {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.progressLocked(ar)
}

func (e *Engine) progressLocked(ar *activeRun) Progress {
	return Progress{
		RunID: ar.runID, AccountID: ar.accountID, AccountName: ar.account,
		Target: ar.target, Elapsed: ar.elapsed, Success: ar.success,
		Failed: ar.failed, Status: ar.status, StartedAt: ar.started, TickSeconds: ar.tick,
	}
}

func (e *Engine) update(ar *activeRun, elapsed, success, failed int, status string) {
	e.mu.Lock()
	ar.elapsed, ar.success, ar.failed, ar.status = elapsed, success, failed, status
	p := e.progressLocked(ar)
	e.mu.Unlock()
	_ = e.st.UpdateRunProgress(ar.runID, elapsed, success, failed)
	e.hub.Publish(Event{Type: "run.tick", RunID: ar.runID, AccountID: ar.accountID, TS: time.Now().Unix(), Data: p})
}

func (e *Engine) logLine(ar *activeRun, level, msg string) {
	ts := time.Now().Unix()
	id, err := e.st.AddLog(ar.runID, ts, level, msg)
	if err != nil {
		e.log.Warn("写入运行日志失败", "err", err)
	}
	e.hub.Publish(Event{
		Type: "run.log", RunID: ar.runID, AccountID: ar.accountID, TS: ts,
		Data: map[string]any{"id": id, "run_id": ar.runID, "ts": ts, "level": level, "message": msg},
	})
}

// loop performs the reading ticks until the target or an error is reached.
// A failed session is automatically retried while the account allows it
// (max_retries > 0); success, manual stop and non-retryable failures return
// immediately.
func (e *Engine) loop(ar *activeRun, acc *store.Account, ck weread.Cookies, template map[string]any) {
	defer close(ar.done)

	rnd := rand.New(rand.NewSource(time.Now().UnixNano()))
	tick := &tickState{last: time.Now()}
	maxRetries := clampRetries(acc.MaxRetries)
	retryDelay := time.Duration(clampDelay(acc.RetryDelay)) * time.Second
	deadline := time.Now().Add(time.Duration(ar.target)*2*time.Second + 10*time.Minute)
	status, errMsg := "success", ""
	finish := func(st, msg string) {
		status, errMsg = st, msg
	}

	defer func() {
		// A panic anywhere in the loop is caught here so one broken account
		// cannot take down the whole process. This defer runs before
		// close(ar.done), so Stop()/StopAll() waiters still return normally.
		if r := recover(); r != nil {
			status, errMsg = "failed", fmt.Sprintf("内部错误：%v", r)
			e.log.Error("阅读循环 panic", "run", ar.runID, "account", acc.ID, "panic", r)
		}
		e.finalize(ar, acc, status, errMsg, tick, ar.target)
	}()

	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			e.logLine(ar, "info", fmt.Sprintf("第 %d/%d 次自动重试，%s 后重新开始",
				attempt, maxRetries, humanDelay(retryDelay)))
			if !sleepCtx(ar.ctx, retryDelay) {
				finish("stopped", "")
				return
			}
		}
		st, msg, retryable := e.session(ar, acc, ck, template, rnd, tick, deadline)
		finish(st, msg)
		if st != "failed" || !retryable || attempt >= maxRetries {
			return
		}
	}
}

// session runs one full reading session (renewal + heartbeat loop) and never
// panics past its own recover: the third return states whether the failure is
// worth retrying (network hiccups yes, a dead login no).
func (e *Engine) session(ar *activeRun, acc *store.Account, ck weread.Cookies,
	template map[string]any, rnd *rand.Rand, tick *tickState, deadline time.Time,
) (status, errMsg string, retryable bool) {
	finishS := func(st, msg string, r bool) {
		status, errMsg, retryable = st, msg, r
	}
	defer func() {
		if r := recover(); r != nil {
			status, errMsg, retryable = "failed", fmt.Sprintf("内部错误：%v", r), false
			e.log.Error("阅读会话 panic", "run", ar.runID, "account", acc.ID, "panic", r)
		}
	}()

	interval := time.Duration(clampTick(acc.TickSeconds)) * time.Second
	elapsed, success := tick.elapsed, tick.success
	// consecutiveFailures counts failures since the last success within this
	// session; tick.failed stays cumulative for the run record.
	consecutive := 0
	authRetried, repairs := 0, 0
	// Refresh the session up front; failures are non-fatal.
	if key, err := e.wc.Renew(ar.ctx, ck); err == nil && key != "" {
		_ = e.st.SaveCookies(acc.ID, map[string]string(ck))
		e.logLine(ar, "info", "登录状态已刷新")
	} else if err != nil {
		e.logLine(ar, "warn", "登录状态刷新失败，继续使用现有 Cookie："+err.Error())
	}

	for elapsed < ar.target {
		if ar.ctx.Err() != nil {
			finishS("stopped", "", false)
			return
		}
		if time.Now().After(deadline) {
			finishS("failed", "运行超时，已自动终止", false)
			return
		}

		tick.tickIndex++
		payload := weread.SignPayload(e.buildPayload(template, acc, tick.tickIndex, rnd), time.Now(), tick.last, rnd)
		now := time.Now()

		res, err := e.wc.Read(ar.ctx, ck, payload)
		if ar.ctx.Err() != nil {
			finishS("stopped", "", false)
			return
		}
		if err != nil {
			consecutive++
			tick.failed++
			e.logLine(ar, "warn", fmt.Sprintf("第 %d 次请求失败：%v", tick.tickIndex, err))
			e.update(ar, elapsed, success, tick.failed, "running")
			if consecutive >= 5 {
				finishS("failed", fmt.Sprintf("连续请求失败：%v", err), true)
				return
			}
			if !sleepCtx(ar.ctx, backoff(consecutive)) {
				finishS("stopped", "", false)
				return
			}
			continue
		}

		switch {
		case res.Succ && res.HasSyncKey:
			repairs, authRetried, consecutive = 0, 0, 0
			success++
			elapsed += int(interval.Seconds())
			if elapsed > ar.target {
				elapsed = ar.target
			}
			tick.last = now
			e.update(ar, elapsed, success, tick.failed, "running")
			e.logLine(ar, "info", fmt.Sprintf("第 %d 次成功 · 已读 %s / %s · synckey=%d",
				tick.tickIndex, humanMinutes(elapsed), humanMinutes(ar.target), res.SyncKey))
			if !sleepCtx(ar.ctx, interval) {
				finishS("stopped", "", false)
				return
			}

		case res.Succ:
			repairs++
			consecutive++
			tick.failed++
			e.logLine(ar, "warn", fmt.Sprintf("第 %d 次未返回 synckey，尝试修复（%d/3）", tick.tickIndex, repairs))
			e.update(ar, elapsed, success, tick.failed, "running")
			if repairs > 3 {
				finishS("failed", "连续多次未获得 synckey，已停止", true)
				return
			}
			bookID, _ := payload["b"].(string)
			if err := e.wc.FixSyncKey(ar.ctx, ck, bookID); err != nil {
				e.logLine(ar, "warn", "修复 synckey 请求失败："+err.Error())
			}
			if !sleepCtx(ar.ctx, 5*time.Second) {
				finishS("stopped", "", false)
				return
			}

		default:
			consecutive++
			tick.failed++
			if isAuthError(res.ErrCode) {
				e.logLine(ar, "warn", fmt.Sprintf("登录状态异常（errCode=%d），尝试自动续期", res.ErrCode))
				e.update(ar, elapsed, success, tick.failed, "running")
				if authRetried >= 2 {
					_ = e.st.SetAccountStatus(acc.ID, "expired", "登录已失效，请重新扫码登录", time.Now().Unix())
					finishS("failed", "登录已失效，请重新扫码登录", false)
					return
				}
				authRetried++
				key, renewErr := e.wc.Renew(ar.ctx, ck)
				if renewErr != nil || key == "" {
					_ = e.st.SetAccountStatus(acc.ID, "expired", "登录已失效，请重新扫码登录", time.Now().Unix())
					finishS("failed", "登录已失效，请重新扫码登录", false)
					return
				}
				_ = e.st.SaveCookies(acc.ID, map[string]string(ck))
				e.logLine(ar, "info", "已自动续期登录状态，继续阅读")
				success = 0
				continue
			}

			msg := res.ErrMsg
			if msg == "" {
				msg = fmt.Sprintf("errCode=%d", res.ErrCode)
			}
			e.logLine(ar, "warn", fmt.Sprintf("第 %d 次返回异常：%s", tick.tickIndex, msg))
			e.update(ar, elapsed, success, tick.failed, "running")
			if consecutive >= 5 {
				finishS("failed", "接口持续返回异常："+msg, true)
				return
			}
			if !sleepCtx(ar.ctx, backoff(consecutive)) {
				finishS("stopped", "", false)
				return
			}
		}
	}
	finishS("success", "", false)
	return
}

// finalize persists the run result, broadcasts run.end and pushes the
// notification.
func (e *Engine) finalize(ar *activeRun, acc *store.Account, inStatus, errMsg string, tick *tickState, target int) {
	status, errMsg := inStatus, errMsg
	elapsed, success, failed := tick.elapsed, tick.success, tick.failed
	_ = e.st.FinishRun(ar.runID, status, elapsed, success, failed, errMsg)
	e.mu.Lock()
	ar.elapsed, ar.success, ar.failed, ar.status = elapsed, success, failed, status
	delete(e.act, ar.runID)
	e.mu.Unlock()

	e.hub.Publish(Event{Type: "run.end", RunID: ar.runID, AccountID: acc.ID, TS: time.Now().Unix(),
		Data: map[string]any{
			"run_id": ar.runID, "account_id": acc.ID, "account_name": acc.Name,
			"status": status, "error": errMsg, "elapsed": elapsed,
			"success": success, "failed": failed, "target": target,
		}})

	if status == "success" {
		e.logLine(ar, "info", fmt.Sprintf("阅读完成，本次计入约 %s", humanMinutes(elapsed)))
	} else if status == "stopped" {
		e.logLine(ar, "warn", fmt.Sprintf("已手动停止，本次计入约 %s", humanMinutes(elapsed)))
	} else {
		e.logLine(ar, "error", "阅读失败："+errMsg)
	}

	if acc.PushChannelID > 0 {
		go e.notifyResult(acc, ar, status, errMsg, elapsed)
	}
}

func (e *Engine) buildPayload(template map[string]any, acc *store.Account, tick int, rnd *rand.Rand) map[string]any {
	p := make(map[string]any, len(template)+4)
	for k, v := range template {
		p[k] = v
	}

	switch acc.Mode {
	case "rotate":
		p["b"] = weread.BookPool[rnd.Intn(len(weread.BookPool))]
		p["c"] = weread.ChapterPool[rnd.Intn(len(weread.ChapterPool))]
	case "custom":
		if acc.BookID != "" {
			p["b"] = acc.BookID
		}
		if acc.ChapterID != "" {
			p["c"] = acc.ChapterID
		}
		// Simulate page turns so the session looks like real reading.
		if co, ok := numeric(p["co"]); ok {
			p["co"] = co + 1 + rnd.Intn(3)
		}
		if pr, ok := numeric(p["pr"]); ok && pr < 100 {
			p["pr"] = pr + 1
		}
	default: // stable
		if acc.BookID != "" {
			p["b"] = acc.BookID
		}
		if acc.ChapterID != "" {
			p["c"] = acc.ChapterID
		}
	}
	return p
}

func (e *Engine) notifyResult(acc *store.Account, ar *activeRun, status, errMsg string, elapsed int) {
	ch, err := e.st.GetPushChannel(acc.PushChannelID)
	if err != nil || ch == nil {
		return
	}
	kind := notify.KindSuccess
	title := "微信读书 · 阅读完成"
	if status == "failed" {
		kind, title = notify.KindFailed, "微信读书 · 阅读失败"
	} else if status == "stopped" {
		kind, title = notify.KindFailed, "微信读书 · 已停止"
	}
	vars := map[string]string{
		"account": acc.Name,
		"status":  statusLabel(status),
		"plan":    humanMinutes(ar.target),
		"elapsed": humanMinutes(elapsed),
		"success": strconv.Itoa(ar.success),
		"failed":  strconv.Itoa(ar.failed),
		"error":   errMsg,
		"time":    time.Now().Format("2006-01-02 15:04:05"),
	}
	fallback := fmt.Sprintf("账号：%s\n计划：%s\n实际：%s\n状态：%s",
		acc.Name, humanMinutes(ar.target), humanMinutes(elapsed), statusLabel(status))
	if errMsg != "" {
		fallback += "\n详情：" + errMsg
	}
	title, content := notify.BuildMessage(ch.Templates, kind, title, fallback, vars)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if err := e.nt.Send(ctx, *ch, title, content); err != nil {
		e.log.Warn("推送通知失败", "channel", ch.Name, "err", err)
		e.logLine(ar, "warn", "推送通知失败："+err.Error())
		return
	}
	e.logLine(ar, "info", "已推送结果到 "+ch.Name)
}

func modeName(mode string) string {
	switch mode {
	case "rotate":
		return "随机轮换"
	case "custom":
		return "指定书籍"
	default:
		return "稳定模板"
	}
}

func statusLabel(s string) string {
	switch s {
	case "success":
		return "成功"
	case "stopped":
		return "已停止"
	default:
		return "失败"
	}
}

func humanMinutes(seconds int) string {
	m := float64(seconds) / 60
	if m >= 10 {
		return fmt.Sprintf("%.0f 分钟", m)
	}
	return fmt.Sprintf("%.1f 分钟", m)
}

func clampTick(v int) int {
	if v < 5 {
		return 30
	}
	if v > 300 {
		return 300
	}
	return v
}

// clampRetries keeps the retry count in 0..10.
func clampRetries(v int) int {
	if v < 0 {
		return 0
	}
	if v > 10 {
		return 10
	}
	return v
}

// clampDelay keeps the retry delay in 5..600 seconds (0 means default 15).
func clampDelay(v int) int {
	if v <= 0 {
		return 15
	}
	if v > 600 {
		return 600
	}
	return v
}

func humanDelay(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%d 秒", int(d.Seconds()))
	}
	return fmt.Sprintf("%d 分钟", int(d.Minutes()))
}

func isAuthError(code int) bool {
	return code == -2012 || code == -2013 || code == 401
}

func numeric(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		return int(x), true
	default:
		return 0, false
	}
}

func backoff(failed int) time.Duration {
	d := time.Duration(failed*10) * time.Second
	if d > 60*time.Second {
		d = 60 * time.Second
	}
	if d < 10*time.Second {
		d = 10 * time.Second
	}
	return d
}

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
