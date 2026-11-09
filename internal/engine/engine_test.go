package engine

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"readtrace/internal/secure"
	"readtrace/internal/store"
	"readtrace/internal/weread"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testEngineStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	box, err := secure.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "engine.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// addReadablePoolChapter 给账号的书池补「一本书 + 一个可用章节」。缺陷 05
// 之后，custom 模式指定书籍且章节为空的运行要求书池里有该书的可用章节，
// 起 Start 的测试 fixture 统一走这里。
func addReadablePoolChapter(t *testing.T, st *store.Store, accountID int64, book, chapter string) {
	t.Helper()
	if _, err := st.AddPoolItem(store.KindBook, book, book, accountID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddChapterItem(accountID, book, chapter, book+" · 第一章", 300); err != nil {
		t.Fatal(err)
	}
}

func TestLoopPreservesCumulativeProgress(t *testing.T) {
	for _, mode := range []string{"complete", "stop", "retry", "renew"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				st := testEngineStore(t)
				id, err := st.CreateAccount(store.Account{Name: "test", Mode: "custom", BookID: "book"}, nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				acc, err := st.GetAccount(id)
				if err != nil {
					t.Fatal(err)
				}
				acc.MaxRetries, acc.RetryDelay = 1, 1
				run := &store.Run{AccountID: id, PlanSeconds: 90, Status: "running", StartedAt: time.Now().Unix()}
				if err := st.CreateRun(run); err != nil {
					t.Fatal(err)
				}
				client := weread.NewClient()
				reads := 0
				client.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
					body, headers := `{"succ":1,"synckey":1}`, http.Header{}
					if r.URL.Path == "/web/login/renewal" {
						headers.Set("Set-Cookie", "wr_skey=new")
					} else {
						reads++
						if mode == "renew" && reads == 2 {
							body = `{"succ":0,"errCode":-2012}`
						}
						if mode == "retry" && reads > 1 && reads <= 6 {
							body = `{"succ":0,"errCode":57}`
						}
					}
					return &http.Response{StatusCode: 200, Header: headers, Body: io.NopCloser(strings.NewReader(body))}, nil
				})
				e := New(st, client, nil, NewHub(), slog.New(slog.NewTextHandler(io.Discard, nil)))
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				ar := &activeRun{runID: run.ID, accountID: id, account: "test", target: 90, ctx: ctx, cancel: cancel, done: make(chan struct{})}
				e.act[run.ID] = ar
				if mode == "stop" {
					go func() { time.Sleep(35 * time.Second); cancel() }()
				}
				e.loop(ar, acc, weread.Cookies{}, map[string]any{"b": "book", "c": "chapter"})
				got, err := st.GetRun(run.ID)
				if err != nil {
					t.Fatal(err)
				}
				if got.ElapsedSeconds <= 0 || got.SuccessTicks <= 0 {
					t.Fatalf("lost progress: %+v", got)
				}
				if mode == "stop" {
					if got.Status != "stopped" || got.ElapsedSeconds >= 90 {
						t.Fatalf("stopped result: %+v", got)
					}
					// 缺陷 04：停止发生在 35s 墙钟处，记录值只能 ≤ 已覆盖
					// 的真实墙钟（误差 1 秒以内），不再是预记的 300/90。
					if got.ElapsedSeconds > 36 {
						t.Fatalf("stop recorded more than covered wall clock: %+v", got)
					}
				} else if got.Status != "success" || got.ElapsedSeconds != 90 {
					t.Fatalf("cumulative result: %+v", got)
				}
				if mode != "stop" {
					// 缺陷 04 语义变化：首个成功心跳按运行开始时间起算
					// 覆盖≈0，目标 90s 要按墙钟长跑；单步覆盖在 0.9~1.0
					// 个 interval 之间（抖动值封顶 interval），因此成功
					// 心跳次数在 4~5 之间由随机抖动决定（旧语义预记完整
					// 间隔，定死 3 次）。
					if got.SuccessTicks < 4 || got.SuccessTicks > 5 {
						t.Fatalf("success ticks = %d (want 4~5 under wall-clock accounting)", got.SuccessTicks)
					}
				}
				if mode == "retry" && got.FailedTicks != 5 {
					t.Fatalf("failed ticks = %d", got.FailedTicks)
				}
			})
		})
	}
}
