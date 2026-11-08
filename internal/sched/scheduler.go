// Package sched drives scheduled reading with cron expressions.
package sched

import (
	"fmt"
	"log/slog"
	"math/rand"
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
	opMu sync.Mutex // serializes Start/Add/Remove so a reload cannot interleave

	mu     sync.Mutex // guards the fields below
	cron   *cron.Cron
	loc    *time.Location
	ids    map[int64]cron.EntryID
	next   map[int64]int64 // schedule id -> next fire unix, self-managed
	stopCh chan struct{}   // closed by Stop; releases fire() during jitter delay
	stopMu sync.Mutex      // guards stopCh lifecycle (each generation closes once)
	stopOn bool
}

var parser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// New creates a scheduler (call Start to activate it).
func New(st *store.Store, eng *engine.Engine, log *slog.Logger) *Scheduler {
	return &Scheduler{
		st: st, eng: eng, log: log,
		ids: map[int64]cron.EntryID{}, next: map[int64]int64{}, loc: time.Local,
	}
}

// rotateStopCh closes the previous stop channel (if open) and opens a fresh
// one for the new cron generation.
func (s *Scheduler) rotateStopCh() {
	s.stopMu.Lock()
	defer s.stopMu.Unlock()
	if s.stopOn {
		close(s.stopCh)
	}
	s.stopCh = make(chan struct{})
	s.stopOn = true
}

// closeStopCh closes the active stop channel so waiting fire() goroutines
// return; calling it again is a no-op until rotateStopCh.
func (s *Scheduler) closeStopCh() {
	s.stopMu.Lock()
	defer s.stopMu.Unlock()
	if s.stopOn {
		close(s.stopCh)
		s.stopOn = false
	}
}

// Start builds a fresh cron instance for the timezone and registers all
// enabled schedules. robfig/cron v3 does no internal locking, so Next times
// are never read from cron.Entry(); they are computed with the parser here and
// kept in s.next.
func (s *Scheduler) Start(loc *time.Location) error {
	if loc == nil {
		loc = time.Local
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()

	s.mu.Lock()
	if s.cron != nil {
		s.cron.Stop()
	}
	// 新一代 stopCh：旧的（若已关闭）让历史 fire goroutine 直接退出。
	s.rotateStopCh()
	s.loc = loc
	c := cron.New(cron.WithLocation(loc), cron.WithParser(parser),
		cron.WithChain(cron.Recover(cron.DefaultLogger)))
	s.cron = c
	s.ids = map[int64]cron.EntryID{}
	s.next = map[int64]int64{}
	s.mu.Unlock()

	rows, err := s.st.EnabledSchedules()
	if err != nil {
		// Start the (empty) cron anyway so later Add() calls work.
		s.mu.Lock()
		c.Start()
		s.mu.Unlock()
		return err
	}
	s.mu.Lock()
	c.Start()
	s.mu.Unlock()
	for i := range rows {
		if _, next, err := s.tryAdd(c, loc, &rows[i]); err != nil {
			s.log.Warn("注册定时任务失败", "schedule", rows[i].ID, "cron", rows[i].Cron, "err", err)
		} else {
			_ = s.st.SetScheduleTimes(rows[i].ID, rows[i].LastRunAt, next.Unix())
		}
	}
	return nil
}

// Reload rebuilds the cron instance (timezone or schedule changes).
func (s *Scheduler) Reload(loc *time.Location) error {
	return s.Start(loc)
}

// Stop shuts the cron instance down and releases any fire() waiting out a
// jitter delay.
func (s *Scheduler) Stop() {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	if s.cron != nil {
		s.cron.Stop()
	}
	s.mu.Unlock()
	s.closeStopCh()
}

// Add registers one schedule. It is safe to call on a started scheduler.
func (s *Scheduler) Add(v *store.Schedule) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()

	s.mu.Lock()
	c := s.cron
	loc := s.loc
	s.mu.Unlock()
	if c == nil {
		return fmt.Errorf("调度器尚未启动")
	}

	s.mu.Lock()
	old, ok := s.ids[v.ID]
	s.mu.Unlock()
	if ok {
		c.Remove(old)
	}
	_, next, err := s.tryAdd(c, loc, v)
	if err != nil {
		return err
	}
	_ = s.st.SetScheduleTimes(v.ID, v.LastRunAt, next.Unix())
	return nil
}

// tryAdd registers one schedule on c and updates the next-time map. It must
// only be called while opMu is held (or from Start, which holds it).
func (s *Scheduler) tryAdd(c *cron.Cron, loc *time.Location, v *store.Schedule) (cron.EntryID, time.Time, error) {
	schedParsed, err := parser.Parse(v.Cron)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("Cron 表达式无效：%w", err)
	}
	entryID, err := c.AddFunc(v.Cron, func() { s.fire(v.ID) })
	if err != nil {
		return 0, time.Time{}, err
	}
	next := schedParsed.Next(time.Now().In(loc))
	s.mu.Lock()
	s.ids[v.ID] = entryID
	s.next[v.ID] = next.Unix()
	s.mu.Unlock()
	return entryID, next, nil
}

// Remove unregisters a schedule.
func (s *Scheduler) Remove(id int64) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if entryID, ok := s.ids[id]; ok {
		s.cron.Remove(entryID)
		delete(s.ids, id)
	}
	delete(s.next, id)
}

// Next returns the next fire time for a schedule (zero when unknown).
func (s *Scheduler) Next(id int64) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.next[id]
}

// NextTimes returns the next fire timestamps for every registered schedule.
func (s *Scheduler) NextTimes() map[int64]int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[int64]int64, len(s.next))
	for id, n := range s.next {
		if n > 0 {
			out[id] = n
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
	schedParsed, err := parser.Parse(expr)
	if err != nil {
		return nil, err
	}
	if n <= 0 || n > 20 {
		n = 5
	}
	loc := s.Timezone()

	out := make([]time.Time, 0, n)
	cur := time.Now().In(loc)
	for i := 0; i < n; i++ {
		cur = schedParsed.Next(cur)
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
		// The schedule row is gone (e.g. its account was deleted); drop the
		// entry so it cannot keep firing on removed data.
		s.Remove(id)
		return
	}
	now := time.Now().Unix()
	next := int64(0)
	if schedParsed, perr := parser.Parse(v.Cron); perr == nil {
		if nx := schedParsed.Next(time.Now().In(s.Timezone())); !nx.IsZero() {
			next = nx.Unix()
		}
	}
	s.mu.Lock()
	s.next[id] = next
	s.mu.Unlock()
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

	// 触发抖动：cron 整点启动是固定秒级指纹，加 0~90s 随机延迟弱化它。
	// Stop（退出或时区重载）会立即放行并取消本次触发。
	if d := time.Duration(rand.Int63n(int64(90 * time.Second))); d > 0 {
		timer := time.NewTimer(d)
		select {
		case <-timer.C:
		case <-s.stopCh:
			timer.Stop()
			return
		}
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
