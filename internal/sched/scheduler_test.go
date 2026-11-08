package sched

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"readtrace/internal/engine"
	"readtrace/internal/store"
)

// slogDefault returns a logger that discards output (scheduler warn paths).
func slogDefault() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
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
