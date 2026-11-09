package engine

// 程序重启后的自动续跑测试：ResumeInterrupted 复用 Start 的「每日只补不
// 足」目标计算，把升级/手动重启打断的阅读在原记录上留下清晰因果（旧运行
// 引用运行 #新，新运行写明接续来源）。

import (
	"io"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"readtrace/internal/store"
)

// resumeAccount 建一个可成功启动阅读的 custom 账号（模板 + Cookie + 书池），
// 默认时长 1 分钟避免测试结束后留下长跑。
func resumeAccount(t *testing.T, st *store.Store) *store.Account {
	t.Helper()
	id, err := st.CreateAccount(store.Account{Name: "resume", Mode: "custom",
		BookID: "BOOK_A", DefaultMinutes: 1, TickSeconds: 5},
		map[string]string{"wr_skey": "key"}, map[string]any{"b": "BOOK_A", "c": "CHAPTER_A"})
	if err != nil {
		t.Fatal(err)
	}
	addReadablePoolChapter(t, st, id, "BOOK_A", "CHAPTER_A")
	acc, err := st.GetAccount(id)
	if err != nil || acc == nil {
		t.Fatalf("GetAccount: %v", err)
	}
	return acc
}

// logTexts 把一次运行的日志行摁成字符串列表，供断言关键词。
func logTexts(t *testing.T, st *store.Store, runID int64) []string {
	t.Helper()
	logs, err := st.ListLogs(runID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(logs))
	for _, l := range logs {
		out = append(out, l.Message)
	}
	return out
}

func TestResumeInterruptedStartsAndLinksRuns(t *testing.T) {
	st := testEngineStore(t)
	acc := resumeAccount(t, st)
	e := New(st, fakeSuccessClient(), nil, NewHub(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(e.StopAll)

	// 模拟重启前被打断的旧运行（elapsed 12 分钟，store 侧已改写为 stopped）。
	old := &store.Run{AccountID: acc.ID, Trigger: "manual", PlanSeconds: 1800,
		ElapsedSeconds: 720, Status: "stopped", StartedAt: time.Now().Unix() - 1800}
	if err := st.CreateRun(old); err != nil {
		t.Fatal(err)
	}

	e.ResumeInterrupted([]store.InterruptedRun{{RunID: old.ID, AccountID: acc.ID,
		AccountName: acc.Name, ElapsedSeconds: 720}})

	runs, err := st.ListRuns(acc.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("runs = %+v, want old + resumed", runs)
	}
	if runs[0].Status != "running" || runs[0].Trigger != "resume" {
		t.Fatalf("resumed run = %+v, want running/resume", runs[0])
	}
	newRun := runs[0]

	// 旧记录留「中断原因 + 运行 #new」，新运行写「接续运行 #old」。
	oldLogs := strings.Join(logTexts(t, st, old.ID), "\n")
	newLogs := strings.Join(logTexts(t, st, newRun.ID), "\n")
	if !strings.Contains(oldLogs, "中断原因：程序重启") ||
		!strings.Contains(oldLogs, "运行 #"+strconv.FormatInt(newRun.ID, 10)) {
		t.Fatalf("old-run logs missing resume link:\n%s", oldLogs)
	}
	if !strings.Contains(newLogs, "接续运行 #"+strconv.FormatInt(old.ID, 10)) ||
		!strings.Contains(newLogs, "已读") {
		t.Fatalf("new-run logs missing continuity note:\n%s", newLogs)
	}
	if err := e.Stop(newRun.ID); err != nil {
		t.Fatal(err)
	}
}

// 同一账号多条被中断记录按最新一条去重：只触发一次 Start，创建一条新运行。
func TestResumeInterruptedDedupesSameAccount(t *testing.T) {
	st := testEngineStore(t)
	acc := resumeAccount(t, st)
	e := New(st, fakeSuccessClient(), nil, NewHub(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(e.StopAll)

	older := &store.Run{AccountID: acc.ID, Trigger: "schedule", PlanSeconds: 600,
		ElapsedSeconds: 120, Status: "stopped", StartedAt: time.Now().Unix() - 6000}
	newest := &store.Run{AccountID: acc.ID, Trigger: "manual", PlanSeconds: 900,
		ElapsedSeconds: 300, Status: "stopped", StartedAt: time.Now().Unix() - 3000}
	for _, r := range []*store.Run{older, newest} {
		if err := st.CreateRun(r); err != nil {
			t.Fatal(err)
		}
	}

	e.ResumeInterrupted([]store.InterruptedRun{
		{RunID: newest.ID, AccountID: acc.ID, AccountName: acc.Name, ElapsedSeconds: 300},
		{RunID: older.ID, AccountID: acc.ID, AccountName: acc.Name, ElapsedSeconds: 120},
	})

	runs, err := st.ListRuns(acc.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 3 || runs[0].Status != "running" {
		t.Fatalf("runs = %+v, want exactly one additional running run", runs)
	}
	if err := e.Stop(runs[0].ID); err != nil {
		t.Fatal(err)
	}
}

// 今日目标已达成：不创建运行，只记 info 跳过。
func TestResumeInterruptedSkipsWhenGoalReached(t *testing.T) {
	st := testEngineStore(t)
	acc := resumeAccount(t, st)
	if err := st.UpdateAccount(store.Account{ID: acc.ID, Name: "resume", Mode: "custom",
		BookID: "BOOK_A", DefaultMinutes: 1, DailyGoalMinutes: 1, TickSeconds: 5}); err != nil {
		t.Fatal(err)
	}
	addGoalHistory(t, st, acc.ID, 60, "success")
	acc, err := st.GetAccount(acc.ID)
	if err != nil {
		t.Fatal(err)
	}
	var slogBuf strings.Builder
	e := New(st, fakeSuccessClient(), nil, NewHub(),
		slog.New(slog.NewTextHandler(&slogBuf, nil)))
	t.Cleanup(e.StopAll)

	old := &store.Run{AccountID: acc.ID, Trigger: "manual", PlanSeconds: 600,
		ElapsedSeconds: 600, Status: "stopped", StartedAt: time.Now().Unix() - 600}
	if err := st.CreateRun(old); err != nil {
		t.Fatal(err)
	}
	before, err := st.ListRuns(acc.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	e.ResumeInterrupted([]store.InterruptedRun{{RunID: old.ID, AccountID: acc.ID,
		AccountName: acc.Name, ElapsedSeconds: 600}})
	after, err := st.ListRuns(acc.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !sameRuns(before, after) {
		t.Fatalf("goal-reached resume created runs: before=%d after=%d %+v",
			len(before), len(after), after)
	}
	if !strings.Contains(slogBuf.String(), "今日目标已达成，跳过续跑") {
		t.Fatalf("skip was not logged at info level:\n%s", slogBuf.String())
	}
}

// sameRuns 比较两份「最新→最旧」运行快照是否一致（id 相同即视为没有新增）。
func sameRuns(a, b []store.Run) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID {
			return false
		}
	}
	return true
}

// 登录已失效的账号不续跑（续读立刻会被上游拒），只跳过。
func TestResumeInterruptedSkipsExpiredAccount(t *testing.T) {
	st := testEngineStore(t)
	acc := resumeAccount(t, st)
	if err := st.SetAccountStatus(acc.ID, "expired", "检查发现登录失效", time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	acc, err := st.GetAccount(acc.ID)
	if err != nil {
		t.Fatal(err)
	}
	e := New(st, fakeSuccessClient(), nil, NewHub(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(e.StopAll)

	old := &store.Run{AccountID: acc.ID, Trigger: "manual", PlanSeconds: 600,
		ElapsedSeconds: 300, Status: "stopped", StartedAt: time.Now().Unix() - 600}
	if err := st.CreateRun(old); err != nil {
		t.Fatal(err)
	}
	e.ResumeInterrupted([]store.InterruptedRun{{RunID: old.ID, AccountID: acc.ID,
		AccountName: acc.Name, ElapsedSeconds: 300}})
	runs, err := st.ListRuns(acc.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].ID != old.ID {
		t.Fatalf("expired-account resume created runs: %+v, want only old run", runs)
	}
}

// StopAll 已关门闩：续跑循环立即结束，不创建任何运行。
func TestResumeInterruptedReturnsRightAfterStopAll(t *testing.T) {
	st := testEngineStore(t)
	acc := resumeAccount(t, st)
	e := New(st, fakeSuccessClient(), nil, NewHub(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	e.StopAll()

	old := &store.Run{AccountID: acc.ID, Trigger: "manual", PlanSeconds: 600,
		ElapsedSeconds: 300, Status: "stopped", StartedAt: time.Now().Unix() - 600}
	if err := st.CreateRun(old); err != nil {
		t.Fatal(err)
	}
	e.ResumeInterrupted([]store.InterruptedRun{{RunID: old.ID, AccountID: acc.ID,
		AccountName: acc.Name, ElapsedSeconds: 300}})
	runs, err := st.ListRuns(acc.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].ID != old.ID {
		t.Fatalf("resume after StopAll created runs: %+v, want only old run", runs)
	}
	if e.IsRunning(acc.ID) || len(e.Snapshot()) != 0 {
		t.Fatal("start accepted after StopAll via resume")
	}
}
