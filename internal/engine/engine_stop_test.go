package engine

// 停止门闩测试：覆盖 StopAll 后「迟到的启动请求」「普通启动路径」以及
// 「已运行任务被取消收尾」三种情况。对应审查项 08 的探针场景。

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"readtrace/internal/store"
	"readtrace/internal/weread"
)

// stopTestAccount 建一个可成功启动阅读的 custom 模式账号（有模板与 Cookie）。
func stopTestAccount(t *testing.T, st *store.Store) *store.Account {
	t.Helper()
	id, err := st.CreateAccount(store.Account{Name: "test", Mode: "custom", BookID: "BOOK_A"},
		map[string]string{"wr_skey": "key"}, map[string]any{"b": "BOOK_A", "c": "CHAPTER_A"})
	if err != nil {
		t.Fatal(err)
	}
	// 缺陷 05：custom 指定书籍且章节为空需要书池可用章节。
	addReadablePoolChapter(t, st, id, "BOOK_A", "CHAPTER_A")
	acc, err := st.GetAccount(id)
	if err != nil || acc == nil {
		t.Fatalf("GetAccount: %v", err)
	}
	return acc
}

// fakeSuccessClient 回一而成的心跳/续期响应，供运行循环正常前进。
func fakeSuccessClient() *weread.Client {
	client := weread.NewClient()
	client.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		headers := http.Header{}
		if r.URL.Path == "/web/login/renewal" {
			headers.Set("Set-Cookie", "wr_skey=new")
		}
		return &http.Response{StatusCode: 200, Header: headers,
			Body: io.NopCloser(strings.NewReader(`{"succ":1,"synckey":1}`))}, nil
	})
	return client
}

// TestStopAllRejectsStartWaitingOnAccountLock 复现并修复审查探针
// TestReviewProbeStopAllAcceptsLaterStart：锁住账号锁，StopAll 先返回，
// 释放锁后等待中的 Start 必须走门闩拒绝路径，不得创建新运行。
func TestStopAllRejectsStartWaitingOnAccountLock(t *testing.T) {
	st := testEngineStore(t)
	acc := stopTestAccount(t, st)
	e := New(st, fakeSuccessClient(), nil, NewHub(), slog.New(slog.NewTextHandler(io.Discard, nil)))

	lock := e.accountLock(acc.ID)
	lock.Lock()
	started := make(chan error, 1)
	go func() {
		_, err := e.Start(acc, 1, "manual")
		started <- err
	}()
	e.StopAll()
	lock.Unlock()

	select {
	case err := <-started:
		if !errors.Is(err, ErrEngineStopped) {
			t.Fatalf("Start after StopAll err = %v, want ErrEngineStopped", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown Start did not return after StopAll")
	}
	if e.IsRunning(acc.ID) {
		t.Fatal("late start left an active run after StopAll")
	}
	runs, err := st.ListRuns(acc.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("runs created after StopAll: %+v", runs)
	}
}

// TestStartRejectedAfterStopAll 普通拒绝路径：StopAll 之后直接 Start
// 返回 ErrEngineStopped；StopAll 幂等，可安全重复调用。
func TestStartRejectedAfterStopAll(t *testing.T) {
	st := testEngineStore(t)
	acc := stopTestAccount(t, st)
	e := New(st, fakeSuccessClient(), nil, NewHub(), slog.New(slog.NewTextHandler(io.Discard, nil)))

	e.StopAll()
	e.StopAll() // 幂等：重复调用不得 panic 或破坏状态

	if _, err := e.Start(acc, 1, "manual"); !errors.Is(err, ErrEngineStopped) {
		t.Fatalf("Start after StopAll err = %v, want ErrEngineStopped", err)
	}
	if len(e.Snapshot()) != 0 || e.IsRunning(acc.ID) {
		t.Fatal("engine accepted a start after StopAll")
	}
	runs, err := st.ListRuns(acc.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("runs created after StopAll: %+v", runs)
	}
}

// TestStopAllStopsActiveRun 已运行的记录在 StopAll 中被取消并完成收尾，
// active map 清空、运行行落库为 stopped。
func TestStopAllStopsActiveRun(t *testing.T) {
	st := testEngineStore(t)
	acc := stopTestAccount(t, st)
	e := New(st, fakeSuccessClient(), nil, NewHub(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	run, err := e.Start(acc, 1, "manual")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !e.IsRunning(acc.ID) {
		if time.Now().After(deadline) {
			t.Fatal("run never became active")
		}
		time.Sleep(2 * time.Millisecond)
	}
	// 等第一次心跳完成，保证运行真的进入了计时循环。
	time.Sleep(50 * time.Millisecond)

	e.StopAll()
	if e.IsRunning(acc.ID) || len(e.Snapshot()) != 0 {
		t.Fatal("active run survived StopAll")
	}
	got, err := st.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "stopped" {
		t.Fatalf("run final status = %s, want stopped", got.Status)
	}
}
