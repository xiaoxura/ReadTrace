package sched

import (
	"path/filepath"
	"testing"
	"time"

	"readtrace/internal/engine"
	"readtrace/internal/secure"
	"readtrace/internal/store"
)

func TestReachedDailyGoalPublishesScheduleSkip(t *testing.T) {
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
	if err := st.SetSetting("timezone", "UTC"); err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateAccount(store.Account{Name: "scheduled", DailyGoalMinutes: 30}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	today := time.Now().UTC()
	start := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 1, 0, time.UTC).Unix()
	run := &store.Run{AccountID: id, Status: "failed", PlanSeconds: 1800,
		ElapsedSeconds: 1800, StartedAt: start, FinishedAt: start + 1800}
	if err := st.CreateRun(run); err != nil {
		t.Fatal(err)
	}
	v := &store.Schedule{AccountID: id, Cron: "30 2 * * *", Minutes: 30, Enabled: true}
	if err := st.CreateSchedule(v); err != nil {
		t.Fatal(err)
	}
	hub := engine.NewHub()
	s := New(st, engine.New(st, nil, nil, hub, slogDefault()), slogDefault())
	s.jitter = func() time.Duration { return 0 }
	if err := s.Start(time.UTC); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	s.mu.Lock()
	job := s.jobs[v.ID]
	s.mu.Unlock()
	sub, events := hub.Subscribe()
	defer hub.Unsubscribe(sub)
	s.fire(job)
	seenSkip, seenError := false, false
	for len(events) > 0 {
		ev := <-events
		seenSkip = seenSkip || ev.Type == "schedule.skip"
		seenError = seenError || ev.Type == "schedule.error"
	}
	if !seenSkip || seenError {
		t.Fatalf("schedule events: skip=%v error=%v", seenSkip, seenError)
	}
	runs, err := st.ListRuns(id, 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("goal skip created run: %+v err=%v", runs, err)
	}
}
