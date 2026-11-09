package checker

// checker 生命周期测试：关闭 stop 后取消正在检查中的请求与失效推送，
// 扫描不再触达后续账号，Wait 快速返回、不遗留 goroutine。对应审查项 09。

import (
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"readtrace/internal/engine"
	"readtrace/internal/notify"
	"readtrace/internal/secure"
	"readtrace/internal/store"
	"readtrace/internal/weread"
)

// roundTrip 是可直接替换 http.Client.Transport 的 fake 传输层。
type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testCheckerStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	box, err := secure.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "checker.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestWaitCancelsInFlightScan 三账号扫描时关闭 stop：第一个账号卡住的
// 请求被 ctx 取消，后续账号不再被检查（Validate 每账号恰好发起一次），
// Wait 快速返回。
func TestWaitCancelsInFlightScan(t *testing.T) {
	st := testCheckerStore(t)
	for i := 0; i < 3; i++ {
		if _, err := st.CreateAccount(store.Account{Name: "acc"}, map[string]string{"wr_skey": "key"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	eng := engine.New(st, nil, nil, engine.NewHub(), testLogger())
	wc := weread.NewClient()
	var mu sync.Mutex
	validateCalls := 0 // /web/user 请求数 = 被实际检查过的账号数
	reached := make(chan struct{}, 1)
	wc.HTTP.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet && r.URL.Path == "/web/user" {
			mu.Lock()
			first := validateCalls == 0
			validateCalls++
			mu.Unlock()
			if first {
				select {
				case reached <- struct{}{}:
				default:
				}
			}
		}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	c := New(st, eng, wc, notify.New(), testLogger())
	c.Start()
	c.lastRun = time.Time{} // 让下一次 tick 立即开扫
	go c.tick()

	select {
	case <-reached:
	case <-time.After(2 * time.Second):
		t.Fatal("first account check never started")
	}

	start := time.Now()
	c.Wait()
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Wait took %s, want prompt return after cancel", d)
	}
	mu.Lock()
	defer mu.Unlock()
	if validateCalls != 1 {
		t.Fatalf("accounts validated = %d, want 1 (later accounts skipped after stop)", validateCalls)
	}
}

// TestWaitCancelsInFlightExpiredPush 失效推送 goroutine 纳入生命周期：
// 推送进行中调用 Wait，ctx 被取消、推送请求中断，Wait 等到该 goroutine
// 退出后返回，不再遗留 background 推送。
func TestWaitCancelsInFlightExpiredPush(t *testing.T) {
	st := testCheckerStore(t)
	ch := store.PushChannel{Name: "test", Type: "ntfy", Config: map[string]string{"topic": "tester"}, Enabled: true}
	if err := st.CreatePushChannel(&ch); err != nil {
		t.Fatal(err)
	}
	nt := notify.New()
	var mu sync.Mutex
	pushCalls := 0
	pushReached := make(chan struct{}, 1)
	nt.HTTP.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		first := pushCalls == 0
		pushCalls++
		mu.Unlock()
		if first {
			select {
			case pushReached <- struct{}{}:
			default:
			}
		}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	eng := engine.New(st, nil, nil, engine.NewHub(), testLogger())
	c := New(st, eng, weread.NewClient(), nt, testLogger())
	c.Start()

	acc := store.Account{ID: 1, Name: "acc", PushChannelID: ch.ID}
	c.pushExpiredAsync(c.ctx, acc, nil) // 与 RunOnce 相同的推送入口

	select {
	case <-pushReached:
	case <-time.After(2 * time.Second):
		t.Fatal("push never started")
	}

	start := time.Now()
	c.Wait()
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Wait took %s, want prompt return after push cancel", d)
	}
	mu.Lock()
	defer mu.Unlock()
	if pushCalls != 1 {
		t.Fatalf("push attempts = %d, want 1 (retry blocked after cancel)", pushCalls)
	}
}

// TestWaitIdempotent Wait 幂等且并发安全：重复调用不 close 已关闭的
// channel，所有调用都等到 goroutine 退出后返回。
func TestWaitIdempotent(t *testing.T) {
	st := testCheckerStore(t)
	eng := engine.New(st, nil, nil, engine.NewHub(), testLogger())
	c := New(st, eng, weread.NewClient(), notify.New(), testLogger())
	c.Start()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); c.Wait() }()
	}
	if done := waitChannelWithTimeout(&wg, 2*time.Second); !done {
		t.Fatal("concurrent Wait calls did not all return")
	}
}

func waitChannelWithTimeout(wg *sync.WaitGroup, d time.Duration) bool {
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}
