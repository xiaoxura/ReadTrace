// Package sched drives scheduled reading with cron expressions.
package sched

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"readtrace/internal/engine"
	"readtrace/internal/store"
)

// Scheduler owns the cron instance and its schedule entries.
type Scheduler struct {
	st   *store.Store
	eng  *engine.Engine
	log  *slog.Logger
	mu   sync.Mutex
	cron *cron.Cron
	loc  *time.Location
	ids  map[int64]cron.EntryID
}

var parser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// New creates a scheduler (call Start to activate it).
func New(st *store.Store, eng *engine.Engine, log *slog.Logger) *Scheduler {
	return &Scheduler{st: st, eng: eng, log: log, ids: map[int64]cron.EntryID{}, loc: time.Local}
}

// Start builds a fresh cron instance for the timezone and registers all
// enabled schedules.
func (s *Scheduler) Start(loc *time.Location) error {
	if loc == nil {
		loc = time.Local
	}
	s.mu.Lock()
	if s.cron != nil {
		s.cron.Stop()
	}
	s.loc = loc
	s.cron = cron.New(cron.WithLocation(loc), cron.WithParser(parser),
		cron.WithChain(cron.Recover(cron.DefaultLogger)))
	s.ids = map[int64]cron.EntryID{}
	s.mu.Unlock()

	rows, err := s.st.EnabledSchedules()
	if err != nil {
		return err
	}
	for i := range rows {
		if err := s.Add(&rows[i]); err != nil {
			s.log.Warn("注册定时任务失败", "schedule", rows[i].ID, "cron", rows[i].Cron, "err", err)
		}
	}

	s.mu.Lock()
	s.cron.Start()
	s.mu.Unlock()

	// Persist next-run times now that entries are live.
	for i := range rows {
		next := s.Next(rows[i].ID)
		_ = s.st.SetScheduleTimes(rows[i].ID, rows[i].LastRunAt, next)
	}
	return nil
}

// Reload rebuilds the cron instance (timezone or schedule changes).
func (s *Scheduler) Reload(loc *time.Location) error {
	return s.Start(loc)
}

// Stop shuts the cron instance down.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cron != nil {
		s.cron.Stop()
	}
}

// Add registers one schedule. It is safe to call on a started scheduler.
func (s *Scheduler) Add(v *store.Schedule) error {
	spec := v.Cron
	if _, err := parser.Parse(spec); err != nil {
		return fmt.Errorf("Cron 表达式无效：%w", err)
	}
	id := v.ID

	s.mu.Lock()
	if old, ok := s.ids[id]; ok {
		s.cron.Remove(old)
	}
	entryID, err := s.cron.AddFunc(spec, func() { s.fire(id) })
	if err != nil {
		s.mu.Unlock()
		return err
	}
	s.ids[id] = entryID
	next := s.cron.Entry(entryID).Next
	s.mu.Unlock()

	_ = s.st.SetScheduleTimes(id, v.LastRunAt, next.Unix())
	return nil
}

// Remove unregisters a schedule.
func (s *Scheduler) Remove(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entryID, ok := s.ids[id]; ok {
		s.cron.Remove(entryID)
		delete(s.ids, id)
	}
}

// Next returns the next fire time for a schedule (zero when unknown).
func (s *Scheduler) Next(id int64) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	entryID, ok := s.ids[id]
	if !ok || s.cron == nil {
		return 0
	}
	next := s.cron.Entry(entryID).Next
	if next.IsZero() {
		return 0
	}
	return next.Unix()
}

// NextTimes returns the next fire timestamps for every registered schedule.
func (s *Scheduler) NextTimes() map[int64]int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[int64]int64, len(s.ids))
	if s.cron == nil {
		return out
	}
	for id, entryID := range s.ids {
		next := s.cron.Entry(entryID).Next
		if !next.IsZero() {
			out[id] = next.Unix()
		}
	}
	return out
}

// Validate checks a cron expression.
func (s *Scheduler) Validate(expr string) error {
	_, err := parser.Parse(expr)
	return err
}

// Preview returns the next n fire times for an expression.
func (s *Scheduler) Preview(expr string, n int) ([]time.Time, error) {
	sched, err := parser.Parse(expr)
	if err != nil {
		return nil, err
	}
	if n <= 0 || n > 20 {
		n = 5
	}
	s.mu.Lock()
	loc := s.loc
	s.mu.Unlock()

	out := make([]time.Time, 0, n)
	cur := time.Now().In(loc)
	for i := 0; i < n; i++ {
		cur = sched.Next(cur)
		if cur.IsZero() {
			break
		}
		out = append(out, cur)
	}
	return out, nil
}

// Timezone returns the scheduler's location.
func (s *Scheduler) Timezone() *time.Location {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loc
}

func (s *Scheduler) fire(id int64) {
	v, err := s.st.GetSchedule(id)
	if err != nil || v == nil {
		s.log.Warn("定时任务不存在", "schedule", id, "err", err)
		return
	}
	now := time.Now().Unix()
	next := s.Next(id)
	_ = s.st.SetScheduleTimes(id, now, next)

	acc, err := s.st.GetAccount(v.AccountID)
	if err != nil || acc == nil {
		s.log.Warn("定时任务账号不存在", "schedule", id, "account", v.AccountID)
		s.publish(id, v.AccountID, "schedule.error", map[string]any{
			"schedule_id": id, "error": "账号不存在",
		})
		return
	}

	s.publish(id, acc.ID, "schedule.fire", map[string]any{
		"schedule_id": id, "account_id": acc.ID, "account_name": acc.Name, "minutes": v.Minutes,
	})

	if s.eng.IsRunning(acc.ID) {
		s.log.Info("账号已有运行中的任务，跳过定时触发", "account", acc.ID, "schedule", id)
		s.publish(id, acc.ID, "schedule.skip", map[string]any{
			"schedule_id": id, "account_name": acc.Name, "reason": "该账号已有阅读任务在运行",
		})
		return
	}

	run, err := s.eng.Start(acc, v.Minutes, "schedule")
	if err != nil {
		s.log.Warn("定时任务启动失败", "schedule", id, "err", err)
		s.publish(id, acc.ID, "schedule.error", map[string]any{
			"schedule_id": id, "account_name": acc.Name, "error": err.Error(),
		})
		return
	}
	s.log.Info("定时任务已启动", "schedule", id, "run", run.ID, "account", acc.Name)
}

func (s *Scheduler) publish(id, accountID int64, typ string, data any) {
	s.eng.Hub().Publish(engine.Event{Type: typ, AccountID: accountID, TS: time.Now().Unix(), Data: data})
}
