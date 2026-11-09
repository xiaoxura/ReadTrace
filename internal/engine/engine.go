// Package engine runs the reading heartbeats and broadcasts live progress.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"strconv"
	"strings"
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

// activeRun struct (excerpt) gains sequential-reading state.
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
	newExpiry bool // explicit auth failure changed this account to expired

	// 书池快照按账号划分：账号专属优先，其次共享，最后回落内置常量。
	// poolChaptersBy 保存「书 → 章节」映射；无归属章节进入全局备用池。
	poolBooksOwned       []string
	poolChaptersByOwned  map[string][]string
	poolChaptersByShared map[string][]string

	// 顺序书单模式：orderedBookList 为按池内顺序拼接的书单；seqPos 为当前
	// 读到的书/章下标（持久化在 settings，跨次运行衔接）；seqMark 记录本次
	// 心跳实际使用的位置，成功后才推进。
	orderedBookList []string
	seqPos          seqPointer
	seqMark         seqMark
	pageCurrent     map[string]any // co/pr from the last successful heartbeat
	pagePending     map[string]any // candidate reused until a heartbeat succeeds

	// 书/章节的可读标题（value → title），写日志展示用；章节词数用于按
	// 字数折算每个章节需要多少次心跳。
	bookTitles        map[string]string
	chapterTitles     map[string]string
	chapterTitlesRaw  map[string]string
	chapterWordCounts map[string]int

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

// seqPointer 是顺序书单在「书单 → 章节」里的持久化位置（St = 当前章节里
// 已经花掉的心跳数，跨次运行衔接）。
type seqPointer struct {
	Bi int `json:"bi"`
	Ci int `json:"ci"`
	St int `json:"st,omitempty"`
}

// seqMark 记录当前心跳所选的（书下标，章节下标，书内章节序列，原始章节值）。
type seqMark struct {
	bi         int
	ci         int
	chapters   []string
	rawChapter string
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
	st             *store.Store
	wc             *weread.Client
	nt             *notify.Sender
	hub            *Hub
	log            *slog.Logger
	mu             sync.Mutex
	act            map[int64]*activeRun
	accountLocksMu sync.Mutex
	accountLocks   map[int64]*sync.Mutex
}

// New creates an Engine.
func New(st *store.Store, wc *weread.Client, nt *notify.Sender, hub *Hub, log *slog.Logger) *Engine {
	return &Engine{st: st, wc: wc, nt: nt, hub: hub, log: log, act: map[int64]*activeRun{}, accountLocks: map[int64]*sync.Mutex{}}
}

// Hub exposes the event hub.
func (e *Engine) Hub() *Hub { return e.hub }

// ErrAccountRunning means credentials cannot be replaced while a run owns a
// copy that may later be written back by renewal.
var ErrAccountRunning = errors.New("该账号正在阅读中，请先停止任务再更新登录凭据")

// ErrDailyGoalReached means the local-day target has already been consumed.
var ErrDailyGoalReached = errors.New("今日目标已完成")

func (e *Engine) accountLock(id int64) *sync.Mutex {
	e.accountLocksMu.Lock()
	defer e.accountLocksMu.Unlock()
	lock := e.accountLocks[id]
	if lock == nil {
		lock = &sync.Mutex{}
		e.accountLocks[id] = lock
	}
	return lock
}

// WithIdleAccount coordinates credential replacement with Start's secret
// read and active-run registration for the same account.
func (e *Engine) WithIdleAccount(id int64, fn func() error) error {
	lock := e.accountLock(id)
	lock.Lock()
	defer lock.Unlock()
	if e.IsRunning(id) {
		return ErrAccountRunning
	}
	return fn()
}

// poolValueSet 返回一个 kind 的抽取池（该账号自己的池，无共享层）；
// 池为空时引擎会在启动时报错提示用户导入，不用内置常量兜底。
func (e *Engine) poolValueSet(kind string, accountID int64) (first, second []string) {
	owned, shared, err := e.st.PoolPoolForAccount(kind, accountID)
	if err != nil {
		e.log.Warn("读取书池失败", "kind", kind, "err", err)
	}
	if len(owned) == 0 {
		return shared, nil
	}
	return append(owned, shared...), nil
}

// defaultTemplate resolves the admin-configured default read template from the
// book pool settings. 返回 nil 表示没有配置——稳定模板模式此时无法发起阅读。
func (e *Engine) defaultTemplate() (map[string]any, bool) {
	raw, err := e.st.GetDefaultTemplate()
	if err != nil {
		e.log.Warn("读取账号指纹模板设置失败", "err", err)
	}
	if raw = strings.TrimSpace(raw); raw != "" {
		var m map[string]any
		if err := json.Unmarshal([]byte(raw), &m); err == nil && m["b"] != nil && m["c"] != nil {
			return m, true
		}
		e.log.Warn("账号指纹模板无效（需为含 b/c 的 JSON 对象）")
	}
	return nil, false
}

// Start begins a run for the account. It returns the created run row.
func (e *Engine) Start(acc *store.Account, minutes int, trigger string) (*store.Run, error) {
	if acc == nil {
		return nil, errors.New("账号不存在")
	}
	lock := e.accountLock(acc.ID)
	lock.Lock()
	defer lock.Unlock()
	if e.IsRunning(acc.ID) {
		return nil, fmt.Errorf("「%s」正在阅读中，请先停止", acc.Name)
	}
	// The caller may have loaded this row before another request edited it.
	acc, err := e.st.GetAccount(acc.ID)
	if err != nil {
		return nil, fmt.Errorf("读取账号配置失败: %w", err)
	}
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
	if acc.DailyGoalMinutes > 0 {
		loc := time.Local
		zone, err := e.st.GetSetting("timezone")
		if err != nil {
			return nil, fmt.Errorf("读取站点时区失败: %w", err)
		}
		if strings.TrimSpace(zone) != "" {
			loc, err = time.LoadLocation(strings.TrimSpace(zone))
			if err != nil {
				return nil, fmt.Errorf("加载站点时区失败: %w", err)
			}
		}
		today, err := e.st.DailySeconds(acc.ID, time.Now(), loc)
		if err != nil {
			return nil, fmt.Errorf("读取今日阅读统计失败: %w", err)
		}
		remaining := acc.DailyGoalMinutes*60 - today
		if remaining <= 0 {
			return nil, ErrDailyGoalReached
		}
		if remaining < target {
			target = remaining
		}
	}

	cookies, payload, err := e.st.AccountSecrets(acc.ID)
	if err != nil {
		return nil, err
	}
	if len(cookies) == 0 {
		return nil, errors.New("该账号没有可用的 Cookie，请重新登录")
	}
	template := payload
	if len(template) == 0 {
		template, _ = e.defaultTemplate()
	}
	byOwned, byShared, _, _, err := e.st.PoolChapterMapForAccount(acc.ID)
	if err != nil {
		return nil, fmt.Errorf("读取章节池失败: %w", err)
	}
	bookFirst, _ := e.poolValueSet(store.KindBook, acc.ID)
	// Legacy chapters without book_id cannot be paired with an arbitrary book.
	// Only enabled books with their own enabled chapters can enter a run.
	readableBooks := make([]string, 0, len(bookFirst))
	chapterCount := 0
	for _, book := range bookFirst {
		n := len(byOwned[book]) + len(byShared[book])
		if n > 0 {
			readableBooks = append(readableBooks, book)
			chapterCount += n
		}
	}
	bookTitles, _ := e.st.PoolTitleMapForAccount(store.KindBook, acc.ID)
	chapterTitlesRaw, _ := e.st.PoolTitleMapForAccount(store.KindChapter, acc.ID)
	chapterWordCounts, _ := e.st.PoolWordCountMapForAccount(store.KindChapter, acc.ID)
	// 心跳 payload 里的 b/c 是 NormalizePayloadID 之后的形态（纯数字书 ID 会被
	// 编码），标题映射的键也要换成同一形态，日志才能查到名字。
	bookTitlesN := make(map[string]string, len(bookTitles))
	for k, v := range bookTitles {
		bookTitlesN[weread.NormalizePayloadID(k)] = v
	}
	chapterTitlesN := make(map[string]string, len(chapterTitlesRaw))
	for k, v := range chapterTitlesRaw {
		chapterTitlesN[weread.NormalizePayloadID(k)] = v
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
	// 顺序书单：按书池列表顺序（sort_order）读完整书单再循环。
	var seqBooks []string
	if acc.Mode != "custom" {
		seqBooks = append(seqBooks, readableBooks...)
	}

	// 池与模板均为纯用户配置：缺内容就明确报错，引导用户去书池导入。
	// 所有模式都需要一份模板作为请求基底（设备/会话字段），书/章节由模式决定。
	if len(template) == 0 {
		e.mu.Unlock()
		return nil, errors.New("没有可用的阅读模板：请在账号设置保存抓包模板，或在「设置 · 账号指纹模板」配置一份全局模板")
	}
	switch acc.Mode {
	case "custom":
		if acc.BookID == "" {
			e.mu.Unlock()
			return nil, errors.New("请在账号设置中先指定书籍（阅读模式：指定书籍）")
		}
	case "rotate":
		if len(bookFirst) == 0 {
			e.mu.Unlock()
			return nil, errors.New("书池为空：请先在「书池 · 书籍导入」导入书籍")
		}
		if len(readableBooks) == 0 {
			e.mu.Unlock()
			return nil, errors.New("书池里没有带章节的可读书籍：请在导入时选择「同时导入章节」，或在章节抽屉里手动添加")
		}
	default:
		// 默认/未知/缺失模式一律按顺序书单处理。
		if len(seqBooks) == 0 {
			e.mu.Unlock()
			if len(bookFirst) > 0 {
				return nil, errors.New("书单里的书都没有可读章节：请在导入时选择「同时导入章节」，或在章节抽屉里手动添加")
			}
			return nil, errors.New("顺序书单为空：请先在「书池 · 书籍导入」导入书籍")
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
		poolBooksOwned:       readableBooks,
		poolChaptersByOwned:  byOwned,
		poolChaptersByShared: byShared,
		orderedBookList:      seqBooks,
		seqPos:               e.loadSeqPos(acc.ID),
		bookTitles:           bookTitlesN,
		chapterTitles:        chapterTitlesN,
		chapterTitlesRaw:     chapterTitlesRaw,
		chapterWordCounts:    chapterWordCounts,
		ctx:                  ctx, cancel: cancel, done: make(chan struct{}),
	}
	e.act[run.ID] = ar
	e.mu.Unlock()

	e.hub.Publish(Event{Type: "run.start", RunID: run.ID, AccountID: acc.ID, TS: time.Now().Unix(), Data: e.progress(ar)})
	if acc.Mode == "rotate" {
		e.logLine(ar, "info", fmt.Sprintf("轮换书池 %d 本书、%d 个章节", len(ar.poolBooksOwned), chapterCount))
	}
	if acc.Mode == "sequence" {
		e.logLine(ar, "info", fmt.Sprintf("顺序书单 %d 本书，从「%s」继续（进度跨次运行衔接）",
			len(seqBooks), seqBookName(ar, ar.seqPos.Bi)))
	}
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

// describeReading 生成当前心跳正在读的「书名 · 章节名」（日志展示用）。
// 书池里没有对应条目的（模板自带的书），回退显示原始 ID。
func describeReading(ar *activeRun, acc *store.Account, payload map[string]any) string {
	b, _ := payload["b"].(string)
	c, _ := payload["c"].(string)
	bookName := ar.bookTitles[b]
	if bookName == "" && acc.BookID != "" && b == weread.NormalizePayloadID(acc.BookID) {
		bookName = acc.BookTitle
	}
	if bookName == "" {
		bookName = b
	}
	chapName := ar.chapterTitles[c]
	if chapName == "" && acc.ChapterID != "" && c == weread.NormalizePayloadID(acc.ChapterID) {
		chapName = acc.ChapterTitle
	}
	// 章节条目的标题存成「书名 · 章节名」：剥掉书名前缀。
	if i := strings.Index(chapName, " · "); i >= 0 {
		chapName = chapName[i+3:]
	}
	if chapName == "" {
		chapName = c
	}
	return fmt.Sprintf("《%s》· %s", bookName, chapName)
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
	// consecutiveFailures counts failures since the last success within this
	// session; tick.failed stays cumulative for the run record.
	consecutive := 0
	authRetried, repairs := 0, 0
	renewAfterAuth := func() (expired bool, renewErr error) {
		if authRetried >= 2 {
			// Two successful renewals followed by another explicit auth
			// rejection are enough evidence that this login cannot read.
			return true, nil
		}
		key, err := e.wc.Renew(ar.ctx, ck)
		if errors.Is(err, weread.ErrUnauthorized) {
			return true, err
		}
		if err != nil {
			return false, err
		}
		if key == "" {
			return false, errors.New("renewal 未返回新的 wr_skey")
		}
		authRetried++
		_ = e.st.SaveCookies(acc.ID, map[string]string(ck))
		e.logLine(ar, "info", "已自动续期登录状态，继续阅读")
		return false, nil
	}
	// Refresh the session up front; failures are non-fatal.
	if key, err := e.wc.Renew(ar.ctx, ck); err == nil && key != "" {
		_ = e.st.SaveCookies(acc.ID, map[string]string(ck))
		e.logLine(ar, "info", "登录状态已刷新")
	} else if err != nil {
		e.logLine(ar, "warn", "登录状态刷新失败，继续使用现有 Cookie："+err.Error())
	}

	for tick.elapsed < ar.target {
		if ar.ctx.Err() != nil {
			finishS("stopped", "", false)
			return
		}
		if time.Now().After(deadline) {
			finishS("failed", "运行超时，已自动终止", false)
			return
		}

		tick.tickIndex++
		built := e.buildPayload(ar, template, acc, rnd)
		reading := describeReading(ar, acc, built)
		payload := weread.SignPayload(built, time.Now(), tick.last, rnd)
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
			e.update(ar, tick.elapsed, tick.success, tick.failed, "running")
			if errors.Is(err, weread.ErrUnauthorized) {
				expired, renewErr := renewAfterAuth()
				if expired {
					e.markExpired(ar, acc)
					finishS("failed", "登录已失效，请重新扫码登录", false)
					return
				}
				if renewErr == nil {
					continue
				}
				e.logLine(ar, "warn", "登录续期暂时失败，稍后重试："+renewErr.Error())
			}
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
			if acc.Status != "active" {
				if err := e.st.SetAccountStatus(acc.ID, "active", "", time.Now().Unix()); err != nil {
					e.log.Warn("恢复账号状态失败", "account", acc.ID, "err", err)
				} else {
					acc.Status = "active"
				}
			}
			ar.pageCurrent, ar.pagePending = ar.pagePending, nil
			tick.success++
			tick.elapsed += int(interval.Seconds())
			if tick.elapsed > ar.target {
				tick.elapsed = ar.target
			}
			tick.last = now
			// 顺序书单：成功一次才推进书/章指针（失败重试不跳章）。
			if acc.Mode == "sequence" {
				e.seqAdvance(ar)
			}
			e.update(ar, tick.elapsed, tick.success, tick.failed, "running")
			e.logLine(ar, "info", fmt.Sprintf("第 %d 次成功 · %s · 已读 %s / %s · synckey=%d",
				tick.tickIndex, reading, humanMinutes(tick.elapsed), humanMinutes(ar.target), res.SyncKey))
			if !sleepCtx(ar.ctx, tickJitter(interval, rnd)) {
				finishS("stopped", "", false)
				return
			}

		case res.Succ:
			repairs++
			consecutive++
			tick.failed++
			e.logLine(ar, "warn", fmt.Sprintf("第 %d 次未返回 synckey，尝试修复（%d/3）· %s", tick.tickIndex, repairs, reading))
			e.update(ar, tick.elapsed, tick.success, tick.failed, "running")
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
				e.update(ar, tick.elapsed, tick.success, tick.failed, "running")
				expired, renewErr := renewAfterAuth()
				if expired {
					e.markExpired(ar, acc)
					finishS("failed", "登录已失效，请重新扫码登录", false)
					return
				}
				if renewErr == nil {
					continue
				}
				e.logLine(ar, "warn", "登录续期暂时失败，稍后重试："+renewErr.Error())
			}

			msg := res.ErrMsg
			if msg == "" {
				msg = fmt.Sprintf("errCode=%d", res.ErrCode)
			}
			e.logLine(ar, "warn", fmt.Sprintf("第 %d 次返回异常：%s · %s", tick.tickIndex, msg, reading))
			e.update(ar, tick.elapsed, tick.success, tick.failed, "running")
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

// markExpired records a confirmed authentication failure once per valid to
// expired transition. The active run owns the account credentials until it
// finalizes, so the checker and rebind path cannot race this state change.
func (e *Engine) markExpired(ar *activeRun, acc *store.Account) {
	current, err := e.st.GetAccount(acc.ID)
	if err != nil || current == nil {
		e.log.Warn("读取账号失效状态失败", "account", acc.ID, "err", err)
		return
	}
	if err := e.st.SetAccountStatus(acc.ID, "expired", "登录已失效，请重新扫码登录", time.Now().Unix()); err != nil {
		e.log.Warn("保存账号失效状态失败", "account", acc.ID, "err", err)
		return
	}
	ar.newExpiry = current.Status != "expired"
	acc.Status = "expired"
}

// finalize persists the run result, broadcasts run.end and pushes the
// notification.
func (e *Engine) finalize(ar *activeRun, acc *store.Account, inStatus, errMsg string, tick *tickState, target int) {
	status, errMsg := inStatus, errMsg
	elapsed, success, failed := tick.elapsed, tick.success, tick.failed

	// 先把结束原因写进运行日志，再持久化结果并广播 run.end——否则前端收到
	// end 事件立即拉取日志时，失败原因那一行可能还没落库，看起来就像
	// 「日志里没有失败原因」。
	switch status {
	case "success":
		e.logLine(ar, "info", fmt.Sprintf("阅读完成，本次计入约 %s", humanMinutes(elapsed)))
	case "stopped":
		e.logLine(ar, "warn", fmt.Sprintf("已手动停止，本次计入约 %s", humanMinutes(elapsed)))
	default:
		e.logLine(ar, "error", "阅读失败："+errMsg)
	}

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

	if status == "failed" && acc.PushChannelID > 0 && (errMsg != "登录已失效，请重新扫码登录" || ar.newExpiry) {
		go e.notifyResult(acc, ar, status, errMsg, elapsed)
	}
}

// ---------- 顺序书单支持 ----------

// seqKey 每个账号的顺序书单持久化位置（跨次运行衔接）。
func seqKey(accountID int64) string {
	return fmt.Sprintf("sequence_pos_%d", accountID)
}

func (e *Engine) loadSeqPos(accountID int64) seqPointer {
	raw, _ := e.st.GetSetting(seqKey(accountID))
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return seqPointer{}
	}
	var pos seqPointer
	if err := json.Unmarshal([]byte(raw), &pos); err != nil || pos.Bi < 0 || pos.Ci < 0 {
		return seqPointer{}
	}
	return pos
}

func (e *Engine) saveSeqPos(accountID int64, pos seqPointer) {
	b, err := json.Marshal(pos)
	if err != nil {
		return
	}
	_ = e.st.SetSetting(seqKey(accountID), string(b))
}

// chaptersForBook 返回某本书在池中的章节序列（专属优先，导入顺序即目录顺序）。
func chaptersForBook(ar *activeRun, bookID string) []string {
	owned := ar.poolChaptersByOwned[bookID]
	shared := ar.poolChaptersByShared[bookID]
	if len(owned) == 0 && len(shared) == 0 {
		return nil
	}
	out := make([]string, 0, len(owned)+len(shared))
	out = append(out, owned...)
	out = append(out, shared...)
	return out
}

// seqBookName 所在书单下标的书名（打日志用，优先取池内标题，回退原始 ID）。
func seqBookName(ar *activeRun, idx int) string {
	books := ar.orderedBookList
	if len(books) == 0 {
		return "（书单为空）"
	}
	book := books[idx%len(books)]
	if title := ar.bookTitles[weread.NormalizePayloadID(book)]; title != "" {
		return title
	}
	return book
}

// seqNextBookIdx 从 idx 起向后找第一本「池中还有章节」的书，wrap 到开头；
// 若整张书单都没有章节则原样返回。
func seqNextBookIdx(ar *activeRun, idx int) int {
	n := len(ar.orderedBookList)
	if n == 0 {
		return 0
	}
	for k := 0; k < n; k++ {
		bi := (idx + k) % n
		if len(chaptersForBook(ar, ar.orderedBookList[bi])) > 0 {
			return bi
		}
	}
	return idx % n
}

// chapterTickBudget 一个章节需要多少次 30 秒心跳才算“读完”：按字数折算，
// 150 字 / 心跳 ≈ 真人 300 字/分钟。字数未知按 4 次（约 2 分钟）兜底。
func chapterTickBudget(wordCount int) int {
	if wordCount <= 0 {
		return 4
	}
	ticks := (wordCount + 149) / 150
	if ticks < 1 {
		return 1
	}
	return ticks
}

// chapterDisplayName 章节条目的显示名（剥掉「书名 · 」前缀）。
func chapterDisplayName(ar *activeRun, rawValue string) string {
	title := ar.chapterTitlesRaw[rawValue]
	if title == "" {
		return rawValue
	}
	if i := strings.Index(title, " · "); i >= 0 {
		title = title[i+3:]
	}
	return title
}

// seqAdvance 在一次成功心跳后推进书单指针，并持久化（跨次运行衔接）。
// 章节切换按字数预算：读满一章的字数才切下一章，符合真人阅读节奏；
// 读完整本书切下一本，书单尾部 wrap 回第一本。
func (e *Engine) seqAdvance(ar *activeRun) {
	pos := ar.seqPos
	pos.St++
	budget := chapterTickBudget(ar.chapterWordCounts[ar.seqMark.rawChapter])
	if ar.seqMark.rawChapter == "" {
		budget = 4 // buildPayload 未记录原始章节（兜底路径），保守处理
	}
	if pos.St < budget {
		ar.seqPos = pos
		e.saveSeqPos(ar.accountID, pos)
		return
	}
	// 本章读满 → 下一章；本书读完 → 下一本。
	pos.Ci = ar.seqMark.ci + 1
	pos.St = 0
	chapters := ar.seqMark.chapters
	finishedBook := false
	if len(chapters) == 0 || pos.Ci >= len(chapters) {
		finishedBook = true
		pos.Ci = 0
		pos.Bi = seqNextBookIdx(ar, ar.seqMark.bi+1)
	}
	ar.seqPos = pos
	e.saveSeqPos(ar.accountID, pos)

	bookName := seqBookName(ar, ar.seqMark.bi)
	if finishedBook {
		e.logLine(ar, "info", fmt.Sprintf("顺序书单：《%s》已读完，切换到《%s》（第 %d 本）", bookName, seqBookName(ar, pos.Bi), pos.Bi+1))
	} else {
		next := ""
		if pos.Ci < len(chapters) {
			next = chapters[pos.Ci]
		}
		nextName := chapterDisplayName(ar, next)
		if next == "" {
			nextName = "下一章"
		}
		e.logLine(ar, "info", fmt.Sprintf("顺序书单：《%s》读完「%s」，开始「%s」", bookName, chapterDisplayName(ar, ar.seqMark.rawChapter), nextName))
	}
}

func (e *Engine) applyRotatePayload(ar *activeRun, p map[string]any, rnd *rand.Rand) {
	books := ar.poolBooksOwned
	if len(books) == 0 {
		return // Start 已校验书池非空，这里只是防御
	}
	book := books[rnd.Intn(len(books))]
	p["b"] = weread.NormalizePayloadID(book)
	// Start only admitted books with linked, enabled chapters.
	chapters := chaptersForBook(ar, book)
	if len(chapters) == 0 {
		delete(p, "c") // defensive: never pair this book with the template chapter
		return
	}
	p["c"] = weread.NormalizePayloadID(chapters[rnd.Intn(len(chapters))])
}

func (e *Engine) buildPayload(ar *activeRun, template map[string]any, acc *store.Account, rnd *rand.Rand) map[string]any {
	p := make(map[string]any, len(template)+4)
	for k, v := range template {
		p[k] = v
	}

	switch acc.Mode {
	default:
		// 顺序书单（默认模式）：unknown / "" / "sequence" 都按顺序推进。
		books := ar.orderedBookList
		if len(books) == 0 {
			// 书单为空：退化为随机轮换行为，至少不空转。
			e.applyRotatePayload(ar, p, rnd)
			break
		}
		pos := ar.seqPos
		if pos.Bi < 0 || pos.Bi >= len(books) {
			pos.Bi = ((pos.Bi % len(books)) + len(books)) % len(books)
			pos.Ci = 0
		}
		book := books[pos.Bi]
		chapters := chaptersForBook(ar, book)
		for k := 0; k < len(books) && len(chapters) == 0; k++ {
			pos.Bi = (pos.Bi + 1) % len(books)
			pos.Ci = 0
			book = books[pos.Bi]
			chapters = chaptersForBook(ar, book)
		}
		ci := pos.Ci
		if ci < 0 || (len(chapters) > 0 && ci >= len(chapters)) {
			ci = 0
		}
		if len(chapters) == 0 {
			delete(p, "c") // defensive: never reuse the template chapter
			break
		}
		chapter := chapters[ci]
		p["b"] = weread.NormalizePayloadID(book)
		p["c"] = weread.NormalizePayloadID(chapter)
		e.applyPagePayload(ar, p, rnd)
		ar.seqPos = pos
		ar.seqMark = seqMark{bi: pos.Bi, ci: ci, chapters: chapters, rawChapter: chapter}
	case "rotate":
		e.applyRotatePayload(ar, p, rnd)
	case "custom":
		if acc.BookID != "" {
			p["b"] = weread.NormalizePayloadID(acc.BookID)
		}
		if acc.ChapterID != "" {
			p["c"] = weread.NormalizePayloadID(acc.ChapterID)
		}
		e.applyPagePayload(ar, p, rnd)
	}
	return p
}

// applyPagePayload prepares one page turn. Failed requests reuse the same
// candidate; only the successful heartbeat commits it as the next baseline.
func (e *Engine) applyPagePayload(ar *activeRun, p map[string]any, rnd *rand.Rand) {
	if ar.pagePending != nil {
		for k, v := range ar.pagePending {
			p[k] = v
		}
		return
	}
	for k, v := range ar.pageCurrent {
		p[k] = v
	}
	pending := make(map[string]any, 2)
	if co, ok := numeric(p["co"]); ok {
		p["co"] = co + 1 + rnd.Intn(3)
		pending["co"] = p["co"]
	}
	if pr, ok := numeric(p["pr"]); ok {
		if pr < 100 {
			p["pr"] = pr + 1
		}
		pending["pr"] = p["pr"]
	}
	if len(pending) > 0 {
		ar.pagePending = pending
	}
}

func (e *Engine) notifyResult(acc *store.Account, ar *activeRun, status, errMsg string, elapsed int) {
	ch, err := e.st.GetPushChannel(acc.PushChannelID)
	if err != nil || ch == nil {
		return
	}
	kind, title := notify.KindFailed, "微信读书 · 阅读失败"
	if ar.newExpiry {
		kind, title = notify.KindExpired, "微信读书 · 账号失效"
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
		return "顺序书单"
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

// tickJitter perturbs the heartbeat sleep by ±10% so the request rhythm never
// shows an exact fixed-interval fingerprint over long sessions. Reading
// accounting keeps counting the nominal interval, so a longer sleep simply
// ends the run a few seconds late (bounded by the deadline above).
func tickJitter(d time.Duration, rnd *rand.Rand) time.Duration {
	if d <= 0 {
		return d
	}
	return time.Duration(float64(d) * (0.9 + 0.2*rnd.Float64()))
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
