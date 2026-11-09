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
						if mode == "renew" && reads == 2 { body = `{"succ":0,"errCode":-2012}` }
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
				} else if got.Status != "success" || got.ElapsedSeconds != 90 || got.SuccessTicks != 3 {
					t.Fatalf("cumulative result: %+v", got)
				}
				if mode == "retry" && got.FailedTicks != 5 {
					t.Fatalf("failed ticks = %d", got.FailedTicks)
				}
			})
		})
	}
}
