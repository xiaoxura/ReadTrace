package api

import (
	"net/http"
	"sort"
	"strconv"
	"time"

	"readtrace/internal/store"
)

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	loc := s.timezone()
	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)

	days := 14
	if v, err := strconv.Atoi(r.URL.Query().Get("days")); err == nil && v > 0 {
		days = v
	}
	if days < 7 {
		days = 7
	}
	if days > 90 {
		days = 90
	}
	accID, _ := strconv.ParseInt(r.URL.Query().Get("account_id"), 10, 64)

	since := today.AddDate(0, 0, -(days - 1))
	// One full-year query feeds both the 14-day window and the yearly stats;
	// previously the same time range was scanned twice.
	allRuns, err := s.st.RunsSince(today.AddDate(0, -12, 0).Unix())
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, "统计读取失败")
		return
	}

	daily := map[string]int{}
	yearDaily := map[string]int{}
	for _, run := range allRuns {
		if accID > 0 && run.AccountID != accID {
			continue
		}
		for day, sec := range splitRunSeconds(run, loc) {
			yearDaily[day] += sec
			dayStart, err := time.ParseInLocation("2006-01-02", day, loc)
			if err == nil && !dayStart.Before(since) {
				daily[day] += sec
			}
		}
	}

	series := make([]map[string]any, 0, days)
	weekSeconds := 0
	for i := 0; i < days; i++ {
		d := since.AddDate(0, 0, i)
		key := d.Format("2006-01-02")
		sec := daily[key]
		series = append(series, map[string]any{"date": key, "seconds": sec})
		if i >= days-7 {
			weekSeconds += sec
		}
	}

	todaySec := daily[today.Format("2006-01-02")]
	yesterdaySec := daily[today.AddDate(0, 0, -1).Format("2006-01-02")]

	// Streak: if nothing read today yet, count from yesterday so the streak
	// does not reset until the day is actually over.
	cursor := today
	if yearDaily[cursor.Format("2006-01-02")] == 0 {
		cursor = cursor.AddDate(0, 0, -1)
	}
	streak := 0
	for i := 0; i < 366; i++ {
		if yearDaily[cursor.Format("2006-01-02")] <= 0 {
			break
		}
		streak++
		cursor = cursor.AddDate(0, 0, -1)
	}

	totals := storeTotals{}
	if accID > 0 {
		t, _ := s.st.TotalsForAccount(accID)
		totals = storeTotals{Runs: t.Runs, Seconds: t.Seconds}
	} else {
		t, _ := s.st.Totals()
		totals = storeTotals{Runs: t.Runs, Seconds: t.Seconds}
	}

	byAccount, _ := s.st.StatsByAccount(since.Unix())
	if accID > 0 {
		filtered := byAccount[:0]
		for _, a := range byAccount {
			if a.AccountID == accID {
				filtered = append(filtered, a)
			}
		}
		byAccount = filtered
	}

	recent, _ := s.st.ListRuns(accID, 10)

	active := []any{}
	for _, p := range s.eng.Snapshot() {
		if accID > 0 && p.AccountID != accID {
			continue
		}
		active = append(active, p)
	}

	accounts, _ := s.st.ListAccounts()
	accountBrief := make([]map[string]any, 0, len(accounts))
	expiredAccounts := 0
	for _, a := range accounts {
		accountBrief = append(accountBrief, map[string]any{
			"id": a.ID, "name": a.Name, "status": a.Status, "nickname": a.Nickname,
		})
		if a.Status != "active" {
			expiredAccounts++
		}
	}

	type nextRun struct {
		ScheduleID  int64  `json:"schedule_id"`
		AccountID   int64  `json:"account_id"`
		AccountName string `json:"account_name"`
		Cron        string `json:"cron"`
		Minutes     int    `json:"minutes"`
		NextAt      int64  `json:"next_at"`
	}
	nextMap := s.sch.NextTimes()
	schedules, _ := s.st.ListSchedules()
	upcoming := []nextRun{}
	for _, v := range schedules {
		if !v.Enabled || (accID > 0 && v.AccountID != accID) {
			continue
		}
		n := nextMap[v.ID]
		if n == 0 {
			n = v.NextRunAt
		}
		if n <= 0 {
			continue
		}
		upcoming = append(upcoming, nextRun{
			ScheduleID: v.ID, AccountID: v.AccountID, AccountName: v.AccountName,
			Cron: v.Cron, Minutes: v.Minutes, NextAt: n,
		})
	}
	sort.SliceStable(upcoming, func(i, j int) bool { return upcoming[i].NextAt < upcoming[j].NextAt })
	if len(upcoming) > 5 {
		upcoming = upcoming[:5]
	}

	scheduleCount := 0
	for _, v := range schedules {
		if v.Enabled {
			scheduleCount++
		}
	}
	wxStats := map[string]any{}
	if s.wxBot != nil {
		wxStats = s.wxBot.Status()
	}

	s.writeJSON(w, http.StatusOK, map[string]any{
		"today_seconds":     todaySec,
		"yesterday_seconds": yesterdaySec,
		"week_seconds":      weekSeconds,
		"streak_days":       streak,
		"total_seconds":     totals.Seconds,
		"total_runs":        totals.Runs,
		"series":            series,
		"by_account":        byAccount,
		"recent":            recent,
		"active":            active,
		"accounts":          accountBrief,
		"account_count":     len(accounts),
		"expired_accounts":  expiredAccounts,
		"schedule_count":    scheduleCount,
		"wxbot":             wxStats,
		"next_runs":         upcoming,
		"today":             today.Format("2006-01-02"),
		"timezone":          loc.String(),
		"generated_at":      time.Now().Unix(),
	})
}

// splitRunSeconds spreads one run's reading time across the natural days it
// overlaps. Without this a 30-minute run started at 23:50 would be counted
// entirely on the previous day, contradicting the "natural day" contract.
func splitRunSeconds(run store.Run, loc *time.Location) map[string]int {
	out := map[string]int{}
	total := run.ElapsedSeconds
	if total <= 0 || run.StartedAt <= 0 {
		return out
	}
	start := run.StartedAt
	end := run.FinishedAt
	if end <= start {
		end = start + int64(total)
	}
	span := end - start
	if int64(total) > span {
		// 墙钟跨度短于累计阅读时长（重试/时钟偏差）时按阅读时长展开。
		span = int64(total)
		end = start + span
	}
	remaining := total
	cursor := start
	for cursor < end && remaining > 0 {
		d := time.Unix(cursor, 0).In(loc)
		dayEnd := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 1).Unix()
		segEnd := dayEnd
		if segEnd > end {
			segEnd = end
		}
		part := total * int(segEnd-cursor) / int(span)
		if segEnd == end || part > remaining {
			part = remaining
		}
		if part < 0 {
			part = 0
		}
		out[d.Format("2006-01-02")] += part
		remaining -= part
		cursor = segEnd
	}
	if remaining > 0 {
		out[time.Unix(end-1, 0).In(loc).Format("2006-01-02")] += remaining
	}
	return out
}

type storeTotals struct {
	Runs    int64
	Seconds int64
}
