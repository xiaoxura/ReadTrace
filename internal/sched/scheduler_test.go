package sched

import (
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"readtrace/internal/engine"
	"readtrace/internal/secure"
	"readtrace/internal/store"
	"readtrace/internal/weread"
)

type scheduleRoundTripFunc func(*http.Request) (*http.Response, error)

func (f scheduleRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// slogDefault returns a logger that discards output (scheduler warn paths).
func slogDefault() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestPendingFireCancelledByScheduleChange(t *testing.T) {
	for _, change := range []string{"remove", "disable", "edit", "reload", "stop"} {
		t.Run(change, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				dir := t.TempDir()
				box, err := secure.New(dir)
				if err != nil {
					t.Fatal(err)
				}
				st, err := store.Open(filepath.Join(dir, "sched.db"), box)
				if err != nil {
					t.Fatal(err)
				}
				defer st.Close()
				accountID, err := st.CreateAccount(store.Account{Name: "test", Mode: "custom", BookID: "book"},
					map[string]string{"wr_skey": "key"}, map[string]any{"b": "book", "c": "chapter"})
				if err != nil {
					t.Fatal(err)
				}
				v := &store.Schedule{AccountID: accountID, Cron: "* * * * *", Minutes: 1, Enabled: true}
				if err := st.CreateSchedule(v); err != nil {
					t.Fatal(err)
				}
				log := slogDefault()
				eng := engine.New(st, nil, nil, engine.NewHub(), log)
				s := New(st, eng, log)
				s.jitter = func() time.Duration { return 60 * time.Second }
				if err := s.Start(time.UTC); err != nil {
					t.Fatal(err)
				}
				defer s.Stop()
				s.mu.Lock()
				job := s.jobs[v.ID]
				s.mu.Unlock()
				finished := make(chan struct{})
				go func() { s.fire(job); close(finished) }()
				synctest.Wait() // fire is parked in its jitter delay
				switch change {
				case "remove":
					s.Remove(v.ID)
					if err := st.DeleteSchedule(v.ID); err != nil {
						t.Fatal(err)
					}
				case "disable":
					v.Enabled = false
					if err := st.UpdateSchedule(v); err != nil {
						t.Fatal(err)
					}
					s.Remove(v.ID)
				case "edit":
					v.Minutes = 2
					if err := st.UpdateSchedule(v); err != nil {
						t.Fatal(err)
					}
					if err := s.Add(v); err != nil {
						t.Fatal(err)
					}
				case "reload":
					if err := s.Reload(time.FixedZone("new", 8*3600)); err != nil {
						t.Fatal(err)
					}
				case "stop":
					s.Stop()
				}
				synctest.Wait()
				select {
				case <-finished:
				default:
					t.Fatal("old fire did not stop during jitter")
				}
				runs, err := st.ListRuns(accountID, 10)
				if err != nil {
					t.Fatal(err)
				}
				if len(runs) != 0 {
					t.Fatalf("cancelled fire started a run: %+v", runs)
				}
			})
		})
	}
}

func TestReloadReadFailurePreservesExistingJob(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "sched.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	log := slogDefault()
	s := New(st, engine.New(st, nil, nil, engine.NewHub(), log), log)
	v := &store.Schedule{AccountID: 1, Cron: "30 2 * * *", Minutes: 30, Enabled: true}
	if err := st.CreateSchedule(v); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(time.UTC); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	s.mu.Lock()
	job := s.jobs[v.ID]
	s.mu.Unlock()
	next := s.Next(v.ID)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Reload(time.FixedZone("new", 8*3600)); err == nil {
		t.Fatal("reload unexpectedly succeeded with closed store")
	}
	if s.Next(v.ID) != next {
		t.Fatal("failed reload changed next time")
	}
	s.mu.Lock()
	kept := s.jobs[v.ID] == job
	s.mu.Unlock()
	if !kept {
		t.Fatal("failed reload replaced old job")
	}
	select {
	case <-job.stop:
		t.Fatal("failed reload cancelled old job")
	default:
	}
}

func TestAlreadyRunningAccountSkipsBeforeJitter(t *testing.T) {
	dir := t.TempDir()
	box, err := secure.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "sched.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	id, err := st.CreateAccount(store.Account{Name: "test", Mode: "custom", BookID: "book"},
		map[string]string{"wr_skey": "old"}, map[string]any{"b": "book", "c": "chapter"})
	if err != nil {
		t.Fatal(err)
	}
	acc, err := st.GetAccount(id)
	if err != nil {
		t.Fatal(err)
	}
	wc := weread.NewClient()
	wc.HTTP.Transport = scheduleRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		headers := http.Header{}
		if r.URL.Path == "/web/login/renewal" {
			headers.Set("Set-Cookie", "wr_skey=new")
		}
		return &http.Response{StatusCode: 200, Header: headers,
			Body: io.NopCloser(strings.NewReader(`{"succ":1,"synckey":1}`))}, nil
	})
	log := slogDefault()
	eng := engine.New(st, wc, nil, engine.NewHub(), log)
	run, err := eng.Start(acc, 1, "manual")
	if err != nil {
		t.Fatal(err)
	}
	defer eng.StopAll()
	v := &store.Schedule{AccountID: id, Cron: "30 2 * * *", Minutes: 1, Enabled: true}
	if err := st.CreateSchedule(v); err != nil {
		t.Fatal(err)
	}
	s := New(st, eng, log)
	jitterCalls := 0
	s.jitter = func() time.Duration { jitterCalls++; return time.Hour }
	if err := s.Start(time.UTC); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	s.mu.Lock()
	job := s.jobs[v.ID]
	s.mu.Unlock()
	s.fire(job)
	if jitterCalls != 0 {
		t.Fatalf("jitter called %d times for already-running account", jitterCalls)
	}
	runs, err := st.ListRuns(id, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].ID != run.ID {
		t.Fatalf("extra scheduled run: %+v", runs)
	}
}

// The scheduler must never read robfig's Entry().Next concurrently; these
// tests pin the self-managed next-time map behaviour instead.
func TestSchedulerSelfManagedNext(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "sched.db"), nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	log := slogDefault()
	s := New(st, engine.New(st, nil, nil, engine.NewHub(), log), log)
	if err := s.Start(time.Local); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(s.Stop)

	v := &store.Schedule{ID: 1, AccountID: 1, Cron: "30 2 * * *", Minutes: 30, Enabled: true}
	if err := s.Add(v); err != nil {
		t.Fatalf("add: %v", err)
	}

	next := s.Next(1)
	if next <= 0 {
		t.Fatalf("next must be a real timestamp, got %d (year-1 sentinel is a bug)", next)
	}
	tm := time.Unix(next, 0)
	if !tm.After(time.Now()) {
		t.Fatalf("next time is not in the future: %v", tm)
	}
	nt := s.NextTimes()
	if nt[1] != next {
		t.Fatalf("NextTimes mismatch: %d vs %d", nt[1], next)
	}

	if got := s.Next(999); got != 0 {
		t.Fatalf("unknown schedule should be 0, got %d", got)
	}
	s.Remove(1)
	if got := s.Next(1); got != 0 {
		t.Fatalf("removed schedule should be 0, got %d", got)
	}

	if err := s.Add(&store.Schedule{ID: 2, AccountID: 1, Cron: "not a cron", Enabled: true}); err == nil {
		t.Fatal("invalid cron should be rejected")
	}

	times, err := s.Preview("*/5 * * * *", 3)
	if err != nil || len(times) != 3 {
		t.Fatalf("preview: %v %v", times, err)
	}
	for i := 1; i < len(times); i++ {
		if !times[i].After(times[i-1]) {
			t.Fatalf("preview times not increasing: %v", times)
		}
	}
}
