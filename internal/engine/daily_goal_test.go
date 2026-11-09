package engine

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"readtrace/internal/notify"
	"readtrace/internal/store"
	"readtrace/internal/weread"
)

func goalEngine(t *testing.T, goal int) (*store.Store, *Engine, *store.Account) {
	t.Helper()
	st := testEngineStore(t)
	id, err := st.CreateAccount(store.Account{Name: "goal", Mode: "custom", BookID: "book",
		DailyGoalMinutes: goal, TickSeconds: 5}, map[string]string{"wr_skey": "key"}, map[string]any{"b": "book", "c": "chapter"})
	if err != nil {
		t.Fatal(err)
	}
	acc, err := st.GetAccount(id)
	if err != nil {
		t.Fatal(err)
	}
	client := weread.NewClient()
	client.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/web/login/renewal" {
			return &http.Response{StatusCode: 200, Header: http.Header{"Set-Cookie": {"wr_skey=new"}},
				Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	e := New(st, client, nil, NewHub(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(e.StopAll)
	return st, e, acc
}

func addGoalHistory(t *testing.T, st *store.Store, accountID int64, seconds int, status string) {
	t.Helper()
	today := time.Now().In(time.Local)
	start := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 1, 0, time.Local).Unix()
	r := &store.Run{AccountID: accountID, PlanSeconds: seconds, ElapsedSeconds: seconds,
		Status: status, StartedAt: start, FinishedAt: start + int64(seconds)}
	if err := st.CreateRun(r); err != nil {
		t.Fatal(err)
	}
}

func TestStartCapsGoalAndSkipsWithoutUpstream(t *testing.T) {
	st, e, acc := goalEngine(t, 30)
	addGoalHistory(t, st, acc.ID, 20*60, "stopped")
	otherID, err := st.CreateAccount(store.Account{Name: "other"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	addGoalHistory(t, st, otherID, 30*60, "failed")
	run, err := e.Start(acc, 30, "manual")
	if err != nil || run.PlanSeconds != 600 {
		t.Fatalf("capped run=%+v err=%v", run, err)
	}
	if _, err := e.Start(acc, 30, "manual"); err == nil || errors.Is(err, ErrDailyGoalReached) {
		t.Fatal("concurrent start accepted")
	}
	if err := e.Stop(run.ID); err != nil {
		t.Fatal(err)
	}
	// Only seven seconds remain; no minute rounding may enlarge the run.
	acc.DailyGoalMinutes = 21
	if err := st.UpdateAccount(*acc); err != nil {
		t.Fatal(err)
	}
	addGoalHistory(t, st, acc.ID, 53, "failed")
	run, err = e.Start(acc, 30, "manual")
	if err != nil || run.PlanSeconds != 7 {
		t.Fatalf("seven-second run=%+v err=%v", run, err)
	}
	if err := e.Stop(run.ID); err != nil {
		t.Fatal(err)
	}
	addGoalHistory(t, st, acc.ID, 7, "failed")
	before, err := st.ListRuns(acc.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Start(acc, 30, "manual"); !errors.Is(err, ErrDailyGoalReached) {
		t.Fatalf("reached goal err=%v", err)
	}
	after, err := st.ListRuns(acc.ID, 100)
	if err != nil || len(after) != len(before) {
		t.Fatalf("goal skip created run: before=%d after=%d err=%v", len(before), len(after), err)
	}
}

func TestFinalNotificationOnlyForTerminalFailureAndNewExpiry(t *testing.T) {
	st := testEngineStore(t)
	ch := &store.PushChannel{Name: "local", Type: "ntfy", Enabled: true,
		Config:    map[string]string{"server": "http://localhost", "topic": "test"},
		Templates: map[string]string{"failed_title": "FAILED", "expired_title": "EXPIRED"}}
	if err := st.CreatePushChannel(ch); err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateAccount(store.Account{Name: "notify", PushChannelID: ch.ID}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	acc, err := st.GetAccount(id)
	if err != nil {
		t.Fatal(err)
	}
	sent := make(chan string, 8)
	nt := notify.New()
	nt.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		sent <- r.Header.Get("Title")
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	e := New(st, nil, nt, NewHub(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	finalize := func(status, msg string, newExpiry bool) {
		t.Helper()
		r := &store.Run{AccountID: id, Status: "running", PlanSeconds: 60, StartedAt: time.Now().Unix()}
		if err := st.CreateRun(r); err != nil {
			t.Fatal(err)
		}
		ar := &activeRun{runID: r.ID, accountID: id, account: acc.Name, target: 60, newExpiry: newExpiry}
		e.finalize(ar, acc, status, msg, &tickState{failed: 2}, 60)
	}
	for _, status := range []string{"success", "stopped"} {
		finalize(status, "", false)
	}
	select {
	case title := <-sent:
		t.Fatalf("quiet result sent %q", title)
	case <-time.After(30 * time.Millisecond):
	}
	finalize("failed", "terminal", false)
	select {
	case title := <-sent:
		if title != "FAILED" {
			t.Fatalf("failure title=%q", title)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal failure not sent")
	}
	ar := &activeRun{}
	e.markExpired(ar, acc)
	if !ar.newExpiry {
		t.Fatal("first explicit expiry was not marked")
	}
	finalize("failed", "登录已失效，请重新扫码登录", ar.newExpiry)
	select {
	case title := <-sent:
		if title != "EXPIRED" {
			t.Fatalf("expiry title=%q", title)
		}
	case <-time.After(time.Second):
		t.Fatal("expiry not sent")
	}
	ar = &activeRun{}
	e.markExpired(ar, acc)
	if ar.newExpiry {
		t.Fatal("repeat expiry was marked as new")
	}
	finalize("failed", "登录已失效，请重新扫码登录", ar.newExpiry)
	select {
	case title := <-sent:
		t.Fatalf("repeat expiry sent %q", title)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestConcurrentStartsCannotClaimSameSevenSeconds(t *testing.T) {
	st, e, acc := goalEngine(t, 1)
	addGoalHistory(t, st, acc.ID, 53, "stopped")
	type result struct {
		run *store.Run
		err error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for range 2 {
		go func() {
			<-start
			run, err := e.Start(acc, 1, "manual")
			results <- result{run, err}
		}()
	}
	close(start)
	first, second := <-results, <-results
	winners := 0
	var winner *store.Run
	for _, got := range []result{first, second} {
		if got.err == nil {
			winners++
			winner = got.run
		} else if got.run != nil {
			t.Fatalf("failed start returned a run: %+v", got)
		}
	}
	if winners != 1 || winner == nil || winner.PlanSeconds != 7 {
		t.Fatalf("concurrent results = %+v %+v", first, second)
	}
	runs, err := st.ListRuns(acc.ID, 10)
	if err != nil || len(runs) != 2 || runs[0].ID != winner.ID {
		t.Fatalf("duplicate or missing run: %+v, err=%v", runs, err)
	}
	if err := e.Stop(winner.ID); err != nil {
		t.Fatal(err)
	}
}

func TestRetryKeepsSevenSecondTargetAndRecoveredRunQuiet(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		st, e, acc := goalEngine(t, 1)
		addGoalHistory(t, st, acc.ID, 53, "failed")
		ch := &store.PushChannel{Name: "local", Type: "ntfy", Enabled: true,
			Config: map[string]string{"server": "http://localhost", "topic": "test"}}
		if err := st.CreatePushChannel(ch); err != nil {
			t.Fatal(err)
		}
		acc.PushChannelID = ch.ID
		acc.TickSeconds = 30
		acc.MaxRetries = 1
		acc.RetryDelay = 1
		if err := st.UpdateAccount(*acc); err != nil {
			t.Fatal(err)
		}
		pushes := make(chan struct{}, 1)
		nt := notify.New()
		nt.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			pushes <- struct{}{}
			return &http.Response{StatusCode: 200, Header: http.Header{},
				Body: io.NopCloser(strings.NewReader(""))}, nil
		})
		e.nt = nt
		reads := 0
		wc := weread.NewClient()
		wc.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			headers := http.Header{}
			body := `{"succ":1,"synckey":1}`
			if r.URL.Path == "/web/login/renewal" {
				headers.Set("Set-Cookie", "wr_skey=new")
			} else if r.URL.Path == "/web/book/read" {
				reads++
				if reads <= 5 {
					body = `{"succ":0,"errCode":57}`
				}
			}
			return &http.Response{StatusCode: 200, Header: headers,
				Body: io.NopCloser(strings.NewReader(body))}, nil
		})
		e.wc = wc
		run, err := e.Start(acc, 1, "manual")
		if err != nil || run.PlanSeconds != 7 {
			t.Fatalf("start = %+v, err=%v", run, err)
		}
		e.mu.Lock()
		active := e.act[run.ID]
		e.mu.Unlock()
		if active == nil {
			t.Fatal("run finalized before observation")
		}
		<-active.done
		synctest.Wait()
		finished, err := st.GetRun(run.ID)
		if err != nil || finished == nil || finished.Status != "success" ||
			finished.PlanSeconds != 7 || finished.ElapsedSeconds != 7 || finished.FailedTicks != 5 || reads != 6 {
			t.Fatalf("retried run=%+v, reads=%d, err=%v", finished, reads, err)
		}
		if len(pushes) != 0 {
			t.Fatalf("recovered success sent %d notifications", len(pushes))
		}
	})
}
