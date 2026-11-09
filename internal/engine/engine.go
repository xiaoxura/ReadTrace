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
	// 字数折算每个章节需要多少次心跳。上传书（CB_）共用每书小号的章节
	// 编码，同一 chapter 值可属于多本书——标题/字数/下标必须**按书分桶**
	// 查询，全局 value → 字典会跨书串名。
	bookTitles        map[string]string
	bookByNorm        map[string]string
	chapterTitlesRaw  map[string]string            // 全局兜底（无归属章节/历史数据）
	chapterWordCounts map[string]int               // 全局兜底
	bookChapterTitles map[string]map[string]string // book → value → 标题
	bookChapterWords  map[string]map[string]int    // book → value → 字数
	bookChapterIdxs   map[string]map[string]int    // book → value → 官方下标
	lastBook          string                       // 当前心跳选定的书（原始 value）

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

	// 结算状态（缺陷 04）：elapsed 不再预记未经历的完整间隔，而是由真实
	// 墙钟差累计而来。settleAt 是上次结算点（loop 初始化时设为运行开始
	// 时间，首次成功心跳从它起算）；covered 是已被成功心跳覆盖并计入的
	// 墙钟总时长；cap 是上一步成功后授予的等待时长（≈ interval − 本次请求
	// 耗时的抖动值），用来给「上游心跳等待造成的超长间隔」封顶。
	covered  time.Duration
	settleAt time.Time
	cap      time.Duration
}

// settleTick 把「上次结算点 → now」的真实墙钟差计入 tick 的累计阅读时长，
// 并重新给出 int 精度的 elapsed。结算边界：
//   - 首个结算步：settleAt 是运行开始时间，首个成功心跳把从运行开始到该
//     心跳之间的墙钟一并计入（c 与 sign.go 的 rt 同为真实墙钟口径）；
//   - 单步封顶 max(tick.cap, interval)：tick.cap 为上一步授予的等待时长。
//     正常节奏下 cover = 等待 + 本次请求耗时 ≈ interval，照实计入；上游
//     心跳等待策略或读请求被长时间阻塞造成的超常间隔最多按一个结算周期
//     计入，不无限累计（weread 上报的 rt 也在 sign.go 封顶 120 秒）。
//   - 停止/失败收尾：finalize 会再结算一次，把最后一次成功心跳之后到
//     停止时刻为止已覆盖的墙钟补进统计（封顶同上），未经历的时间一律
//     不计算。
//
// elapsed 由累计时长一次性取整，避免逐步取整的误差累积。
func settleTick(tick *tickState, now time.Time, interval time.Duration) {
	if tick.settleAt.IsZero() {
		tick.settleAt = now
	}
	cover := now.Sub(tick.settleAt)
	limit := tick.cap
	if limit < interval {
		limit = interval
	}
	if cover > limit {
		cover = limit
	}
	tick.covered += cover
	tick.settleAt = now
	tick.elapsed = int(tick.covered / time.Second)
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

	// 停止门闩：stopCh 在 StopAll 时关闭一次，此后 Start 一律拒绝。
	// 关闭是单纯的状态读取（select 一眼判断），不依赖 e.mu，也没有
	// 与 e.mu 的锁序关系：StopAll 先关门闩（不持 e.mu）再持 e.mu 快照，
	// Start 在持有 e.mu 时也能安全读取门闩，不会死锁。
	stopOnce sync.Once
	stopCh   chan struct{}

	// 进度落库失败的 Warn 频控（缺陷 06）：runID → 上次告警时刻。
	// UpdateRunProgress 每个心跳都会触发，首次失败立即告警后进入静默期
	// （progressWarnSilence），避免每秒刷屏。锁独立于 e.mu，避免锁序。
	progressWarnMu sync.Mutex
	progressWarnAt map[int64]time.Time
}

// progressWarnSilence 同一运行的进度落库失败告警静默期：首条 Warn 之后
// 30 秒内的重复失败不再逐条记录。
const progressWarnSilence = 30 * time.Second

// New creates an Engine.
func New(st *store.Store, wc *weread.Client, nt *notify.Sender, hub *Hub, log *slog.Logger) *Engine {
	return &Engine{
		st: st, wc: wc, nt: nt, hub: hub, log: log,
		act:            map[int64]*activeRun{},
		accountLocks:   map[int64]*sync.Mutex{},
		progressWarnAt: map[int64]time.Time{},
		stopCh:         make(chan struct{}),
	}
}

// stopped 报告停止门闩是否已被 StopAll 关闭。stopCh 为 nil（测试直造
// 零值 Engine）时视为未停止：nil channel 在 select 里永远不会就绪。
func (e *Engine) stopped() bool {
	select {
	case <-e.stopCh:
		return true
	default:
		return false
	}
}

// Hub exposes the event hub.
func (e *Engine) Hub() *Hub { return e.hub }

// ErrAccountRunning means credentials cannot be replaced while a run owns a
// copy that may later be written back by renewal.
var ErrAccountRunning = errors.New("该账号正在阅读中，请先停止任务再更新登录凭据")

// ErrDailyGoalReached means the local-day target has already been consumed.
var ErrDailyGoalReached = errors.New("今日目标已完成")

// ErrBookNoChapters means a book selected in custom mode is in the book pool
// but has no readable chapter (缺陷 05)。
var ErrBookNoChapters = errors.New("该书尚无可用章节：请先在「书池」导入这本书的章节")

// isPooledBook 判断 bookID 是否在书池的书籍列表里（收录了这本书）。
func isPooledBook(books []string, bookID string) bool {
	for _, b := range books {
		if b == bookID {
			return true
		}
	}
	return false
}

// ErrEngineStopped 表示引擎已执行 StopAll（进程正在退出或任务全部停止），
// 不再接受新的阅读启动请求。
var ErrEngineStopped = errors.New("程序正在停止任务，引擎已关闭新运行的启动入口")

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

// Start begins a run for the account. It returns the created run row.
func (e *Engine) Start(acc *store.Account, minutes int, trigger string) (*store.Run, error) {
	if acc == nil {
		return nil, errors.New("账号不存在")
	}
	lock := e.accountLock(acc.ID)
	lock.Lock()
	defer lock.Unlock()
	// 停止门闩（快路径）：锁等待醒来后先核对一次，引擎已停止就走人，
	// 不再做任何读取账号/书池的工作。
	if e.stopped() {
		return nil, ErrEngineStopped
	}
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
	// 全局模板契约（缺陷 13）：账号模板是唯一的请求模板来源，不再回落
	// read_default_payload 全局设置；为空时走下方明确的「需要模板」报错。
	template := payload
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
	// 章节信息按书分桶：跨书同章节 ID（上传书常态）时标题/字数/下标必须
	// 从书自己那本目录取，不能按 value 全局查（会串到先入库的书）。
	chapterItems, _ := e.st.PoolItemsForAccount(store.KindChapter, acc.ID)
	bookChapterTitles := map[string]map[string]string{}
	bookChapterWords := map[string]map[string]int{}
	bookChapterIdxs := map[string]map[string]int{}
	for _, it := range chapterItems {
		if it.Value == "" {
			continue
		}
		bucket := it.BookID
		ensure := func() (map[string]string, map[string]int, map[string]int) {
			if bookChapterTitles[bucket] == nil {
				bookChapterTitles[bucket] = map[string]string{}
				bookChapterWords[bucket] = map[string]int{}
				bookChapterIdxs[bucket] = map[string]int{}
			}
			return bookChapterTitles[bucket], bookChapterWords[bucket], bookChapterIdxs[bucket]
		}
		titles, words, idxs := ensure()
		titles[it.Value] = it.Title
		if it.WordCount > 0 {
			words[it.Value] = it.WordCount
		}
		if it.ChapterIndex > 0 {
			idxs[it.Value] = it.ChapterIndex
		}
	}
	// norm(b) → 原始书 value 的反向索引：describeReading 从 payload b 找书桶。
	bookByNorm := make(map[string]string, len(readableBooks)+1)
	for _, raw := range readableBooks {
		if raw != "" {
			bookByNorm[weread.NormalizePayloadID(raw)] = raw
		}
	}
	if acc.BookID != "" {
		bookByNorm[weread.NormalizePayloadID(acc.BookID)] = acc.BookID
	}
	// 心跳 payload 里的 b/c 是 NormalizePayloadID 之后的形态（纯数字书 ID 会被
	// 编码），书籍标题映射的键也要换成同一形态，日志才能查到名字。
	bookTitlesN := make(map[string]string, len(bookTitles))
	for k, v := range bookTitles {
		bookTitlesN[weread.NormalizePayloadID(k)] = v
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
		return nil, errors.New("没有可用的阅读模板：到「书池」导入书籍会自动生成模板；也可在账号设置点「生成模板」，或粘贴一份抓包模板")
	}
	switch acc.Mode {
	case "custom":
		if acc.BookID == "" {
			e.mu.Unlock()
			return nil, errors.New("请在账号设置中先指定书籍（阅读模式：指定书籍）")
		}
		// 缺陷 05：指定书籍模式下章节为空时，书籍与章节必须共同解析。
		// 两种情况区别对待：
		//   - 书里（书池已收录该书）连一个可用章节都没有 → 这在创建运行
		//     前直接拒绝，绝不发出混用旧书章节的请求；
		//   - 书池完全没收录这本书 → 回落到模板自带的 b/c（抓包/生成模板
		//     的老配置继续可用，运行循环按模板章节上场）。
		if acc.ChapterID == "" && isPooledBook(bookFirst, acc.BookID) &&
			len(poolChaptersForBook(byOwned, byShared, acc.BookID, bookByNorm)) == 0 {
			e.mu.Unlock()
			return nil, ErrBookNoChapters
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
	// 停止门闩（慢路径）：这里已经持有 e.mu。StopAll 先关门闩再持
	// e.mu 快照，因此本检查与 CreateRun 同在一个临界区时排他成立：
	// 门闩未关 → 本运行一定会被 StopAll 的快照扫到并取消；门闩已关
	// → 立即拒绝。StopAll 返回后不可能再有新运行被创建。
	if e.stopped() {
		e.mu.Unlock()
		return nil, ErrEngineStopped
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
		bookByNorm:           bookByNorm,
		chapterTitlesRaw:     chapterTitlesRaw,
		chapterWordCounts:    chapterWordCounts,
		bookChapterTitles:    bookChapterTitles,
		bookChapterWords:     bookChapterWords,
		bookChapterIdxs:      bookChapterIdxs,
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

// StopAll 关闭启动门闩并取消全部进行中的运行（关机/一键停止用）。
//
// 顺序（保证 StopAll 返回后不会有新运行被创建）：
//  1. 先关门闩（一次性，幂等：sync.Once 保证重复调用只 close 一次）；
//     此步不持 e.mu，与 Start 持 e.mu 读门闩不构成锁序，无死锁；
//  2. 再持 e.mu 快照 act 并逐个 cancel，最后按运行等待 finalize 收尾
//     （每个运行 5 秒上限）。等待上限的理由：finalize 只做落库与广播，
//     正常毫秒级完成；上限只防护 finalize 被卡死的极端情况，关机不该
//     因单个停摆的运行无限拖延。
func (e *Engine) StopAll() {
	e.stopOnce.Do(func() {
		if e.stopCh != nil {
			close(e.stopCh)
		}
	})
	// 门闩已关：此刻起 Start 全拒，快照之后不会再有并发新建。
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
	// 缺陷 06：进度落库失败不再静默丢弃。广播照发（前端仍能看到内存态
	// 进度），落库失败改 Warn 并按时间频控，避免每个心跳都刷一行日志。
	if err := e.st.UpdateRunProgress(ar.runID, elapsed, success, failed); err != nil {
		e.warnProgressPersist(ar.runID, err)
	}
	e.hub.Publish(Event{Type: "run.tick", RunID: ar.runID, AccountID: ar.accountID, TS: time.Now().Unix(), Data: p})
}

// warnProgressPersist 记录一次进度落库失败：同一运行首次失败立即 Warn，
// 之后 progressWarnSilence 内的重复失败不再逐条刷屏（30 秒静默期，
// 缺陷 06）。
func (e *Engine) warnProgressPersist(runID int64, err error) {
	e.progressWarnMu.Lock()
	defer e.progressWarnMu.Unlock()
	now := time.Now()
	if last, ok := e.progressWarnAt[runID]; ok && now.Sub(last) < progressWarnSilence {
		return
	}
	e.progressWarnAt[runID] = now
	e.log.Warn("运行进度落库失败", "run", runID, "err", err)
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
	// 缺陷 04：结算基点 = 运行开始时间。首次成功心跳之前不产生阅读计账，
	// 但首个成功心跳会把这段时间（封顶一个 interval）计入。
	tick := &tickState{last: time.Now(), settleAt: time.Now()}
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
	chapName := ""
	if bookRaw := ar.bookByNorm[b]; bookRaw != "" {
		if bucket := ar.bookChapterTitles[bookRaw]; bucket != nil {
			chapName = bucket[c]
		}
	}
	if chapName == "" {
		chapName = ar.chapterTitlesRaw[c] // 无归属/模板兜底
	}
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
		// 缺陷 03：expectedSkey 必须在 Renew 之前记录——Renew 成功时会原地
		// 更新 ck 里的 wr_skey，等待响应期间用户可能已扫码/导入新凭据，
		// 写回前要与库里的旧值比对。
		expectedSkey := ck["wr_skey"]
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
		e.saveRenewedCookies(ar, acc.ID, expectedSkey, ck)
		e.logLine(ar, "info", "已自动续期登录状态，继续阅读")
		return false, nil
	}
	// Refresh the session up front; failures are non-fatal. 写回用 CAS
	// （缺陷 03）：不比对就写回时，等待期间发生的新登录/换绑会被旧请求的
	// 结果覆盖。expectedSkey 取刷新前的值。
	expectedSkey := ck["wr_skey"]
	if key, err := e.wc.Renew(ar.ctx, ck); err == nil && key != "" {
		e.saveRenewedCookies(ar, acc.ID, expectedSkey, ck)
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
			// 缺陷 04：按「上次结算点 → 本次成功心跳」的真实墙钟差计账，
			// 不再预记尚未经历的完整间隔。请求从早先记录的 now 发出，
			// requestDur 是本次成功请求自身消耗的时间。
			requestDur := time.Since(now)
			settleTick(tick, time.Now(), interval)
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
			// 结算节奏（缺陷 04）：本步只补「interval − 本次成功请求耗时」，
			// 让相邻两次结算之间实际经过的墙钟约等于一个 interval（而不是
			// interval + 请求耗时），运行仍按墙钟长跑正常收敛。抖动交给
			// tickJitter（±10%），授予值记入 tick.cap 给下一步封顶用。
			rest := interval - requestDur
			if rest < 0 {
				rest = 0
			}
			wait := tickJitter(rest, rnd)
			tick.cap = wait
			if !sleepCtx(ar.ctx, wait) {
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
				if errors.Is(err, weread.ErrUnauthorized) {
					// 缺陷 03 顺带对齐：FixSyncKey 触及登录失效时也走一次
					// 续期重试，与 Read 的 isAuthError → renewAfterAuth 分支
					// 相同。不重试的话，「连续多次未获得 synckey」的表象会
					// 掩盖真正的登录失效原因。失败已计入上面的 repairs 计数。
					expired, renewErr := renewAfterAuth()
					if expired {
						e.markExpired(ar, acc)
						finishS("failed", "登录已失效，请重新扫码登录", false)
						return
					}
					if renewErr == nil {
						e.logLine(ar, "info", "synckey 修复触及登录失效，已自动续期后重试")
					} else {
						e.logLine(ar, "warn", "登录续期暂时失败，稍后重试："+renewErr.Error())
					}
				}
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

// saveRenewedCookies 把续期结果经 CAS 写回账号凭据（缺陷 03）。
// expectedSkey 是发起 Renew 之前记录的 wr_skey：写回时如果库里仍是它，说明
// 等待期间没有新登录/换绑，正常写入；不一致则跳过写回并给出日志——绝不用
// 旧请求的响应覆盖新凭据。本次会话内的 ck 已被 Renew 原地更新，阅读循环
// 继续用会话值；账号行与会话的短暂差异由该运行后续的续期（同样走 CAS）
// 自然收敛。
func (e *Engine) saveRenewedCookies(ar *activeRun, accountID int64, expectedSkey string, ck weread.Cookies) {
	written, err := e.st.SaveCookiesIfCurrentSkey(accountID, expectedSkey, map[string]string(ck))
	switch {
	case err != nil:
		e.log.Warn("续期结果写回失败", "account", accountID, "err", err)
	case !written:
		e.logLine(ar, "info", "续期等待期间登录凭据已变更（新登录/换绑），跳过本次写回，不覆盖新凭据")
	}
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
	interval := time.Duration(ar.tick) * time.Second
	// 缺陷 04 的停止即时结算：最后一次成功心跳之后到此刻为止已覆盖的墙钟
	// 也要计入（封顶同 read 一侧的规则），不再把停止前后的未经历时间长算。
	// 仅当本运行产生过成功心跳（tick.settleAt 已由 settleTick 初始化）才结算；
	// 手工构造 tickState 的调用（测试/探针）settleAt 为零值，跳过以免污染。
	if tick.success > 0 && !tick.settleAt.IsZero() {
		settleTick(tick, time.Now(), interval)
		if tick.elapsed > target {
			tick.elapsed = target
		}
	}
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

	// 缺陷 06：FinishRun 失败先短暂重试一次，仍失败则不再静默：调用方
	// （UI/检查器）的口径误差要如实暴露。数据库可能仍保留 running/旧时长，
	// 界面状态会回落为运行中；daemon 统计与每日目标会短暂按旧状态结算，
	// 这个偏差记录在案并被接受（不引入「落库失败标记」状态机：跨次重启
	// 已由 InterruptRunningRuns + last_progress_at 恢复，复杂度不值得加）。
	persistErr := e.st.FinishRun(ar.runID, status, elapsed, success, failed, errMsg)
	if persistErr != nil {
		persistErr = e.st.FinishRun(ar.runID, status, elapsed, success, failed, errMsg)
	}
	if persistErr != nil {
		e.log.Error("运行结束落库失败", "run", ar.runID, "status", status, "err", persistErr)
	}

	e.mu.Lock()
	ar.elapsed, ar.success, ar.failed, ar.status = elapsed, success, failed, status
	delete(e.act, ar.runID)
	e.mu.Unlock()
	// 频控表同步清理，防止运行 ID 复用/泄漏。
	e.progressWarnMu.Lock()
	delete(e.progressWarnAt, ar.runID)
	e.progressWarnMu.Unlock()

	endData := map[string]any{
		"run_id": ar.runID, "account_id": acc.ID, "account_name": acc.Name,
		"status": status, "error": errMsg, "elapsed": elapsed,
		"success": success, "failed": failed, "target": target,
	}
	if persistErr != nil {
		// 落库失败把信息带进事件与运行日志：界面上看到的是引擎内存态的
		// 结束，下一次刷新可能回落到数据库里的旧状态。
		endData["persist_error"] = persistErr.Error()
		e.logLine(ar, "error", fmt.Sprintf("运行结束落库失败，界面状态可能回落为运行中的旧数据：%v", persistErr))
	}
	e.hub.Publish(Event{Type: "run.end", RunID: ar.runID, AccountID: acc.ID, TS: time.Now().Unix(), Data: endData})

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

// chapterDisplayName 章节条目的显示名（剥掉「书名 · 」前缀）。跨书同章节
// ID 时按书取桶，避免挂到先入库那本书的标题。
func chapterDisplayName(ar *activeRun, book string, rawValue string) string {
	title := ""
	if book != "" {
		if bucket := ar.bookChapterTitles[book]; bucket != nil {
			title = bucket[rawValue]
		}
	}
	if title == "" {
		title = ar.chapterTitlesRaw[rawValue]
	}
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
	// 预算按书取字数：跨书同章节 ID 时才不至于借到别的书的字数。
	wordCount := 0
	seqBook := ""
	if ar.seqMark.bi >= 0 && ar.seqMark.bi < len(ar.orderedBookList) {
		seqBook = ar.orderedBookList[ar.seqMark.bi]
	}
	if seqBook != "" {
		if bucket := ar.bookChapterWords[seqBook]; bucket != nil {
			wordCount = bucket[ar.seqMark.rawChapter]
		}
	}
	if wordCount <= 0 {
		wordCount = ar.chapterWordCounts[ar.seqMark.rawChapter] // 无归属/历史兜底
	}
	budget := chapterTickBudget(wordCount)
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
		nextName := chapterDisplayName(ar, seqBook, next)
		if next == "" {
			nextName = "下一章"
		}
		e.logLine(ar, "info", fmt.Sprintf("顺序书单：《%s》读完「%s」，开始「%s」", bookName, chapterDisplayName(ar, seqBook, ar.seqMark.rawChapter), nextName))
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
	chapter := chapters[rnd.Intn(len(chapters))]
	p["c"] = weread.NormalizePayloadID(chapter)
	ar.lastBook = book
	e.applyChapterIndex(ar, p, book, chapter)
}

// applyChapterIndex 把当前章节的官方下标填进 payload 的 ci（缺失/历史数据为
// 0 时保持模板原值）。桶表按书键：跨书同章节 ID 时各自取各自的下标。
func (e *Engine) applyChapterIndex(ar *activeRun, p map[string]any, book string, chapter string) {
	if book == "" {
		return
	}
	if idx := ar.bookChapterIdxs[book][chapter]; idx > 0 {
		p["ci"] = idx
	}
}

// poolChaptersForBook 返回指定书籍在章节池（含共享层）里的可用章节，顺序即
// 目录顺序。bookID 先按原值查，再按规范化后的 ID 反查一次（书池存的原始
// value 与账号配置里粘贴的 ID 可能有编码形态差异）。
func poolChaptersForBook(owned, shared map[string][]string, bookID string, bookByNorm map[string]string) []string {
	raws := []string{bookID}
	if raw := bookByNorm[weread.NormalizePayloadID(bookID)]; raw != "" && raw != bookID {
		raws = append(raws, raw)
	}
	for _, raw := range raws {
		out := []string{}
		out = append(out, owned[raw]...)
		out = append(out, shared[raw]...)
		if len(out) > 0 {
			return out
		}
	}
	return []string{}
}

// applyCustomChapterIndex 把指定书籍模式的 ci 解析到新书系（缺陷 05）：
// 官方下标优先；没有官方下标时按章节在书内的位置兜底（顺序书单模式也是
// 这个口径）；章节完全不在新书目录里时，绝不沿用模板里旧书系的 ci，
// 直接移除该字段。
func (e *Engine) applyCustomChapterIndex(ar *activeRun, p map[string]any, book, chapter string, chapters []string) {
	if book == "" {
		return
	}
	if idx := ar.bookChapterIdxs[book][chapter]; idx > 0 {
		p["ci"] = idx
		return
	}
	for i, ch := range chapters {
		if ch == chapter {
			p["ci"] = i + 1
			return
		}
	}
	delete(p, "ci")
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
		p["ci"] = ci + 1 // payload 的 ci 是官方章节下标（1 起），序列位置 0 起
		if raw := ar.bookChapterIdxs[book][chapter]; raw > 0 {
			p["ci"] = raw // 池里存过官方下标就用官方值
		}
		e.applyPagePayload(ar, p, rnd)
		ar.seqPos = pos
		ar.seqMark = seqMark{bi: pos.Bi, ci: ci, chapters: chapters, rawChapter: chapter}
	case "rotate":
		e.applyRotatePayload(ar, p, rnd)
	case "custom":
		if acc.BookID != "" {
			p["b"] = weread.NormalizePayloadID(acc.BookID)
			ar.lastBook = acc.BookID
			chapters := poolChaptersForBook(ar.poolChaptersByOwned, ar.poolChaptersByShared, acc.BookID, ar.bookByNorm)
			switch {
			case acc.ChapterID != "":
				// 用户指定的章节照用（保持原有行为，不做池校验）；但 ci
				// 归属必须按新书系重新解析，模板里的旧 ci 不再沿用。
				chap := weread.NormalizePayloadID(acc.ChapterID)
				p["c"] = chap
				e.applyCustomChapterIndex(ar, p, acc.BookID, acc.ChapterID, chapters)
			case len(chapters) > 0:
				// 缺陷 05：换书后章节为空。引擎没有「账号+书籍」粒度的
				// 最后使用章节持久化（顺序书单的 seqPos/seqMark 只服务
				// 顺序模式），Start 已校验本书至少有一个可用章节；这里
				// 取书池章节列表的第一个可用章节作为默认章节。
				chapter := chapters[0]
				p["c"] = weread.NormalizePayloadID(chapter)
				e.applyCustomChapterIndex(ar, p, acc.BookID, chapter, chapters)
			default:
				// Start 未经过该配置（历史运行/防御路径）：保留模板旧
				// c 兜底，避免凭空构造一个无效章节。
			}
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
// shows an exact fixed-interval fingerprint over long sessions. 计账按真实
// 墙钟（缺陷 04）：抖动让相邻结算的间隔在 0.9~1.1 个 interval 之间浮动，
// 结算单步封顶 interval，超出部分不进入阅读统计，整个运行仍按墙钟收敛。
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
