package engine

// 疑似缺陷回归测试（第二轮修复）：04 心跳按实际覆盖时间计账、05 custom 换书
// 章节解析、06 终结/进度落库失败可见、03 续期 CAS。探针场景来自
// .tmp/review-2026-11-09-JpsDQT（TestReviewProbeStopCountsUnelapsedInterval /
// TestReviewProbeCustomBookKeepsOtherChapter / TestReviewProbeFinishWriteFailure
// Hidden / TestReviewProbeStaleRenewalOverwritesRebind 的引擎侧对应项）。

import (
	"context" // used by hub subscription only
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math/rand"
	//
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"readtrace/internal/secure"
	"readtrace/internal/store"
	"readtrace/internal/weread"
)

// startTimeRequestClient 返回一个“立即成功”的心跳客户端，并记录上报的 rt。
func startTimeRequestClient(t *testing.T, reportedRT *atomic.Int64) *weread.Client {
	t.Helper()
	client := weread.NewClient()
	client.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		headers := http.Header{}
		body := `{"succ":1,"synckey":1}`
		if r.URL.Path == "/web/login/renewal" {
			headers.Set("Set-Cookie", "wr_skey=new")
		} else {
			var p map[string]any
			_ = json.NewDecoder(r.Body).Decode(&p)
			if rt, ok := p["rt"].(float64); ok && reportedRT != nil {
				reportedRT.Store(int64(rt))
			}
		}
		return &http.Response{StatusCode: 200, Header: headers,
			Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	return client
}

// TestStopCountsOnlyCoveredWallTime 复现并修复探针
// TestReviewProbeStopCountsUnelapsedInterval：TickSeconds=300，首个成功心跳
// 后立即 Stop——计时只允许覆盖真实经过的墙钟（不再是预记的 300），与
// weread 上报的 rt 同为真实墙钟口径。
func TestStopCountsOnlyCoveredWallTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		st := testEngineStore(t)
		id, err := st.CreateAccount(store.Account{Name: "probe", Mode: "custom", BookID: "book"},
			map[string]string{"wr_skey": "key"}, map[string]any{"b": "book", "c": "chapter"})
		if err != nil {
			t.Fatal(err)
		}
		addReadablePoolChapter(t, st, id, "book", "chapter")
		acc, err := st.GetAccount(id)
		if err != nil || acc == nil {
			t.Fatalf("GetAccount: %v", err)
		}
		acc.TickSeconds = 300
		if err := st.UpdateAccount(*acc); err != nil {
			t.Fatal(err)
		}
		var reportedRT atomic.Int64
		e := New(st, startTimeRequestClient(t, &reportedRT), nil, NewHub(),
			slog.New(slog.NewTextHandler(io.Discard, nil)))
		t.Cleanup(e.StopAll)
		sub, ch := e.hub.Subscribe()
		defer e.hub.Unsubscribe(sub)

		start := time.Now()
		run, err := e.Start(acc, 5, "manual")
		if err != nil {
			t.Fatal(err)
		}
	waitLoop:
		for {
			select {
			case ev := <-ch:
				if ev.Type == "run.tick" {
					break waitLoop
				}
			case <-time.After(2 * time.Second):
				t.Fatal("no heartbeat tick observed")
			}
		}
		// synctest 虚拟时钟下事件之间的墙钟几乎不动，Stop 正好落在首个
		// 成功心跳之后的未经历间隔里。
		if err := e.Stop(run.ID); err != nil {
			t.Fatal(err)
		}
		wall := int(time.Since(start).Seconds())
		got, err := st.GetRun(run.ID)
		if err != nil || got == nil {
			t.Fatalf("GetRun: %v %v", got, err)
		}
		if got.Status != "stopped" {
			t.Fatalf("status = %s, want stopped", got.Status)
		}
		if got.ElapsedSeconds < 0 || got.ElapsedSeconds > wall+1 {
			t.Fatalf("stash recorded %d s while wall clock covered %d s", got.ElapsedSeconds, wall)
		}
		if got.ElapsedSeconds >= 300 {
			t.Fatalf("pre-recorded interval survived stop: %+v", got)
		}
		// rt 口径核对：首个成功心跳 rt 只允许个位数（这里墙钟未推进），
		// 与计账（≈0）一致，绝不出现 300。
		if rt := reportedRT.Load(); rt < 1 || rt > 5 {
			t.Fatalf("reported rt = %d, want a few covered seconds at most", rt)
		}
		// 每日目标（引擎侧停止即时结算，缺陷 04c 的引擎半边）：今日统计
		// 与运行记录一致，且不包含未经历的墙钟。
		today, err := st.DailySeconds(acc.ID, time.Now(), time.Local)
		if err != nil {
			t.Fatal(err)
		}
		if today != got.ElapsedSeconds {
			t.Fatalf("daily seconds = %d, run recorded %d", today, got.ElapsedSeconds)
		}
	})
}

// TestFullRunElapsedMatchesWallClock 完整运行收敛：假客户端秒级响应、
// TickSeconds 小，总计账 ≈ 实际墙钟（允许的误差：单步覆盖封顶 interval 加
// ±10% 抖动、取整 1 秒、首步按运行开始时间起算——合计 ≤ 墙钟 20% + 2 秒）。
func TestFullRunElapsedMatchesWallClock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		st := testEngineStore(t)
		id, err := st.CreateAccount(store.Account{Name: "probe", Mode: "custom", BookID: "book"},
			map[string]string{"wr_skey": "key"}, map[string]any{"b": "book", "c": "chapter"})
		if err != nil {
			t.Fatal(err)
		}
		addReadablePoolChapter(t, st, id, "book", "chapter")
		acc, err := st.GetAccount(id)
		if err != nil || acc == nil {
			t.Fatalf("GetAccount: %v", err)
		}
		acc.TickSeconds = 5
		if err := st.UpdateAccount(*acc); err != nil {
			t.Fatal(err)
		}
		e := New(st, startTimeRequestClient(t, nil), nil, NewHub(),
			slog.New(slog.NewTextHandler(io.Discard, nil)))
		sub, ch := e.hub.Subscribe()
		defer e.hub.Unsubscribe(sub)

		start := time.Now()
		run, err := e.Start(acc, 1, "manual") // 目标 60s
		if err != nil {
			t.Fatal(err)
		}
		for {
			select {
			case ev := <-ch:
				if ev.Type == "run.end" {
					goto done
				}
			case <-time.After(2 * time.Minute):
				t.Fatal("run never finished")
			}
		}
	done:
		wall := int(time.Since(start).Seconds())
		got, err := st.GetRun(run.ID)
		if err != nil || got == nil {
			t.Fatalf("GetRun: %v %v", got, err)
		}
		if got.Status != "success" {
			t.Fatalf("status = %s, want success", got.Status)
		}
		if got.ElapsedSeconds != 60 {
			t.Fatalf("elapsed = %d, want capped at plan 60", got.ElapsedSeconds)
		}
		// 计账 ≈ 墙钟：60 s 的目标不应跑超过 ~1.2 倍墙钟（抖动 + 封顶 +
		// 取整余量），也不能凭空少记（elapsed ≤ 墙钟 + 1）。
		if wall < 58 || wall > 74 {
			t.Fatalf("run took %d s of wall clock for a 60 s plan, want ≈60 s", wall)
		}
		if got.ElapsedSeconds > wall+1 {
			t.Fatalf("elapsed %d exceeds covered wall %d", got.ElapsedSeconds, wall)
		}
	})
}

// TestCustomBookResolvesPoolChapter 复现探针
// TestReviewProbeCustomBookKeepsOtherChapter：模板仍是书 A/章 A1、custom
// 指定书 B、章节为空——生成的请求必须解析为 B 的可用章节，ci 归到 B 的
// 书系；书里没有任何可用章节时 Start 直接拒绝而不是混淆旧书章节。
func TestCustomBookResolvesPoolChapter(t *testing.T) {
	st := testEngineStore(t)
	id, err := st.CreateAccount(store.Account{Name: "cb", Mode: "custom", BookID: "BOOK_B"},
		map[string]string{"wr_skey": "key"}, map[string]any{"b": "BOOK_A", "c": "CHAPTER_A", "ci": 7})
	if err != nil {
		t.Fatal(err)
	}
	addReadablePoolChapter(t, st, id, "BOOK_B", "CHAP_B1")
	acc, err := st.GetAccount(id)
	if err != nil || acc == nil {
		t.Fatalf("GetAccount: %v", err)
	}
	e := New(st, fakeSuccessClient(), nil, NewHub(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	run, err := e.Start(acc, 1, "manual")
	if err != nil {
		t.Fatalf("Start rejected a resolvable custom book: %v", err)
	}
	e.mu.Lock()
	ar := e.act[run.ID]
	e.mu.Unlock()
	e.StopAll() // 停掉循环后再做单线程断言
	if ar == nil {
		t.Fatal("run disappeared before inspection")
	}
	_, plc, err := st.AccountSecrets(id)
	if err != nil {
		t.Fatal(err)
	}
	payload := e.buildPayload(ar, plc, acc, rand.New(rand.NewSource(1)))
	if payload["b"] != weread.NormalizePayloadID("BOOK_B") {
		t.Fatalf("book = %v, want normalized BOOK_B", payload["b"])
	}
	if payload["c"] != weread.NormalizePayloadID("CHAP_B1") {
		t.Fatalf("chapter stayed with the template book: %v", payload["c"])
	}
	// ci 归属：CHAP_B1 在 B 的目录里没有官方下标（fixture 未导入），
	// 按书内位置兜底为 1，绝不能沿用模板 A 的 ci=7。
	if payload["ci"] != 1 {
		t.Fatalf("ci = %v, want B-book positional index 1", payload["ci"])
	}
}

// TestCustomBookWithoutChaptersRejectedAtStart：指定书籍已收录进书池但一个
// 可用章节都没有 → Start 在创建运行前直接失败，错误与 ErrDailyGoalReached
// 风格一致；书池完全没有这本书时保留模板 b/c 的回落行为（老配置可用）。
func TestCustomBookWithoutChaptersRejectedAtStart(t *testing.T) {
	st := testEngineStore(t)
	id, err := st.CreateAccount(store.Account{Name: "cb", Mode: "custom", BookID: "BOOK_C"},
		map[string]string{"wr_skey": "key"}, map[string]any{"b": "BOOK_A", "c": "CHAPTER_A", "ci": 7})
	if err != nil {
		t.Fatal(err)
	}
	// 书池收录了 BOOK_C，但没有收录任何章节 → 拒绝。
	if _, err := st.AddPoolItem(store.KindBook, "BOOK_C", "书C", id); err != nil {
		t.Fatal(err)
	}
	acc, err := st.GetAccount(id)
	if err != nil || acc == nil {
		t.Fatalf("GetAccount: %v", err)
	}
	e := New(st, nil, nil, NewHub(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	_, err = e.Start(acc, 1, "manual")
	if !errors.Is(err, ErrBookNoChapters) {
		t.Fatalf("Start err = %v, want ErrBookNoChapters", err)
	}
	runs, lerr := st.ListRuns(id, 10)
	if lerr != nil {
		t.Fatal(lerr)
	}
	if len(runs) != 0 || e.IsRunning(id) {
		t.Fatalf("rejected start left a run: %+v/%v", runs, e.IsRunning(id))
	}

	// 对照：书池没有这本书 → 模板回落可用，Start 照常放行。
	id2, err := st.CreateAccount(store.Account{Name: "cb2", Mode: "custom", BookID: "BOOK_D"},
		map[string]string{"wr_skey": "key"}, map[string]any{"b": "BOOK_A", "c": "CHAPTER_A"})
	if err != nil {
		t.Fatal(err)
	}
	acc2, err := st.GetAccount(id2)
	if err != nil || acc2 == nil {
		t.Fatalf("GetAccount: %v", err)
	}
	e2 := New(st, fakeSuccessClient(), nil, NewHub(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := e2.Start(acc2, 1, "manual"); err != nil {
		t.Fatalf("unpooled book with template fallback rejected: %v", err)
	}
	e2.StopAll()
}

// TestFinalizeWriteFailureVisible 复现探针 TestReviewProbeFinishWriteFailure
// Hidden：SQL 触发器拒绝终态 UPDATE。finalize 短暂重试一次仍失败时：
// 调用方日志、运行日志与 run.end 事件都要出现落库失败信息，active 仍旧清理。
func TestFinalizeWriteFailureVisible(t *testing.T) {
	dir := t.TempDir()
	box, err := secure.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "finalize.db")
	st, err := store.Open(dbPath, box)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	id, err := st.CreateAccount(store.Account{Name: "probe"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	run := &store.Run{AccountID: id, PlanSeconds: 60, Status: "running", StartedAt: time.Now().Unix()}
	if err := st.CreateRun(run); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE probe_calls(n INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO probe_calls VALUES (0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER fail_finish BEFORE UPDATE OF status ON runs
		WHEN NEW.status != 'running' BEGIN
			UPDATE probe_calls SET n = n + 1;
			SELECT RAISE(FAIL,'probe failure');
		END`); err != nil {
		t.Fatal(err)
	}
	var logBuf strings.Builder
	e := New(st, nil, nil, NewHub(), slog.New(slog.NewTextHandler(&logBuf, nil)))
	ar := &activeRun{runID: run.ID, accountID: id, account: "probe", tick: 5, target: 60}
	e.act[run.ID] = ar
	sub, ch := e.hub.Subscribe()
	defer e.hub.Unsubscribe(sub)

	e.finalize(ar, &store.Account{ID: id, Name: "probe"}, "success", "", &tickState{elapsed: 60, success: 2}, 60)

	got, gerr := st.GetRun(run.ID)
	if gerr != nil || got == nil {
		t.Fatalf("GetRun: %v %v", got, gerr)
	}
	if got.Status != "running" {
		// 触发器拒绝终态写入，数据库只能保留旧状态。
		t.Fatalf("unexpected DB status %s (write should have failed)", got.Status)
	}
	var persisted int
	if err := db.QueryRow(`SELECT n FROM probe_calls`).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if persisted != 2 {
		t.Fatalf("FinishRun attempted %d times, want 2 (one short retry)", persisted)
	}
	if e.IsRunning(id) {
		t.Fatal("active map leaked on terminated persist failure")
	}
	var endSeen, warnSeen bool
	for len(ch) > 0 {
		ev := <-ch
		if ev.Type != "run.end" {
			continue
		}
		endSeen = true
		data := ev.Data.(map[string]any)
		if s, ok := data["persist_error"].(string); !ok || s == "" {
			t.Fatalf("run.end without persist_error: %+v", data)
		}
	}
	if !endSeen {
		t.Fatal("run.end event missing")
	}
	logs, gerr := st.ListLogs(run.ID, 0, 50)
	if gerr != nil {
		t.Fatal(gerr)
	}
	for _, l := range logs {
		if l.Level == "error" && strings.Contains(l.Message, "落库失败") {
			warnSeen = true
		}
	}
	if !warnSeen {
		t.Fatal("error-level run log about the persist failure missing")
	}
	if !strings.Contains(logBuf.String(), "运行结束落库失败") {
		t.Fatalf("caller log missing persist failure: %s", logBuf.String())
	}
}

// TestProgressWriteFailureWarnsButKeepsRunning：UPDATE 触发器拒绝进行中的
// 进度落库（running → running 的行更新失败），阅读循环必须照常前进；落库
// 失败以 Warn 记录且按 30 秒频控收敛（这里 60s 假时钟的运行最多两条）。
func TestProgressWriteFailureWarnsButKeepsRunning(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		box, err := secure.New(dir)
		if err != nil {
			t.Fatal(err)
		}
		dbPath := filepath.Join(dir, "progress.db")
		st, err := store.Open(dbPath, box)
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		id, err := st.CreateAccount(store.Account{Name: "probe", Mode: "custom", BookID: "book",
			TickSeconds: 5}, map[string]string{"wr_skey": "key"}, map[string]any{"b": "book", "c": "chapter"})
		if err != nil {
			t.Fatal(err)
		}
		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if _, err := db.Exec(`CREATE TABLE probe_calls(n INTEGER)`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO probe_calls VALUES (0)`); err != nil {
			t.Fatal(err)
		}
		// 注意：进度落库不回写 status 列，触发器要用无 OF 子句的
		// BEFORE UPDATE 才能挂到该语句上（SQLite 的 UPDATE OF 仅对
		// SET 列表里出现的列生效）。
		if _, err := db.Exec(`CREATE TRIGGER fail_progress BEFORE UPDATE ON runs
			WHEN NEW.status = OLD.status AND NEW.status = 'running' BEGIN
				UPDATE probe_calls SET n = n + 1;
				SELECT RAISE(FAIL,'probe failure');
			END`); err != nil {
			t.Fatal(err)
		}
		run := &store.Run{AccountID: id, PlanSeconds: 30, Status: "running", StartedAt: time.Now().Unix()}
		if err := st.CreateRun(run); err != nil {
			t.Fatal(err)
		}
		var logBuf strings.Builder
		e := New(st, startTimeRequestClient(t, nil), nil, NewHub(),
			slog.New(slog.NewTextHandler(&logBuf, nil)))
		acc, err := st.GetAccount(id)
		if err != nil || acc == nil {
			t.Fatalf("GetAccount: %v %v", acc, err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ar := &activeRun{runID: run.ID, accountID: id, account: "probe", target: 30, tick: 5,
			ctx: ctx, cancel: cancel, done: make(chan struct{})}
		e.loop(ar, acc, weread.Cookies{}, map[string]any{"b": "book", "c": "chapter"})

		got, gerr := st.GetRun(run.ID)
		if gerr != nil || got == nil {
			t.Fatalf("GetRun: %v %v", got, gerr)
		}
		if got.Status != "success" || got.ElapsedSeconds <= 0 {
			t.Fatalf("loop derailed on failed progress writes: %+v", got)
		}
		var attempts int
		if err := db.QueryRow(`SELECT n FROM probe_calls`).Scan(&attempts); err != nil {
			t.Fatal(err)
		}
		warns := strings.Count(logBuf.String(), "运行进度落库失败")
		if attempts < 4 {
			t.Fatalf("progress writes attempted %d times, want several", attempts)
		}
		// 30 秒静默期：目标 30s 的运行跨越 ≤1 个静默窗口，第一次告警 +
		// 一次跨静默重发，正好 2 条（60~65s 的长运行会跨到第三个窗口）。
		if warns != 2 {
			t.Fatalf("progress warn count = %d, want rate-limited 2; log:\n%s", warns, logBuf.String())
		}
	})
}

// TestRenewalCASRespectsThirdPartyChange 引擎侧 CAS（缺陷 03）：旧续期请求
// 返回时库中凭据已被新登录替换 → 写回被跳过；expected 与库中一致时正常写回。
func TestRenewalCASRespectsThirdPartyChange(t *testing.T) {
	st := testEngineStore(t)
	id, err := st.CreateAccount(store.Account{Name: "probe"},
		map[string]string{"wr_vid": "1", "wr_skey": "BASE"}, map[string]any{"b": "book", "c": "chapter"})
	if err != nil {
		t.Fatal(err)
	}
	e := New(st, nil, nil, NewHub(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	// 第三方（新扫码登录）已把库中凭据换成 NEW_LOGIN。
	if err := st.SaveCookies(id, map[string]string{"wr_vid": "2", "wr_skey": "NEW_LOGIN"}); err != nil {
		t.Fatal(err)
	}
	e.saveRenewedCookies(&activeRun{accountID: id}, id, "BASE", weread.Cookies{"wr_skey": "BASE_RENEWED"})
	saved, _, err := st.AccountSecrets(id)
	if err != nil {
		t.Fatal(err)
	}
	if saved["wr_skey"] != "NEW_LOGIN" {
		t.Fatalf("stale renewal overwrote new login: %q", saved["wr_skey"])
	}
	// 正常路径：库未被动过（expected=NEW_LOGIN）→ 写回成功。
	e.saveRenewedCookies(&activeRun{accountID: id}, id, "NEW_LOGIN", weread.Cookies{"wr_vid": "2", "wr_skey": "RENEWED_OK"})
	saved, _, err = st.AccountSecrets(id)
	if err != nil {
		t.Fatal(err)
	}
	if saved["wr_skey"] != "RENEWED_OK" {
		t.Fatalf("normal renewal write-back failed: %q", saved["wr_skey"])
	}
}
