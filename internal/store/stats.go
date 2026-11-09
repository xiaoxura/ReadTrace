package store

import (
	"database/sql"
	"sort"
	"time"
)

// AccountBridge finds an account by WeRead vid.
func (s *Store) FindAccountByVid(vid string) (*Account, error) {
	if vid == "" {
		return nil, nil
	}
	a, err := scanAccount(s.db.QueryRow(`SELECT `+accountCols+` FROM accounts WHERE vid = ? ORDER BY id ASC LIMIT 1`, vid))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return a, err
}

// Totals aggregates lifetime reading volume.
type Totals struct {
	Runs    int64 `json:"runs"`
	Seconds int64 `json:"seconds"`
}

// Totals returns run count and total elapsed seconds.
func (s *Store) Totals() (Totals, error) {
	var t Totals
	err := s.db.QueryRow(`SELECT
		(SELECT COUNT(*) FROM runs) + (SELECT COUNT(*) FROM run_statistics),
		(SELECT COALESCE(SUM(elapsed_seconds), 0) FROM runs) +
		(SELECT COALESCE(SUM(elapsed_seconds), 0) FROM run_statistics)`).Scan(&t.Runs, &t.Seconds)
	return t, err
}

// TotalsForAccount aggregates one account's lifetime reading volume.
func (s *Store) TotalsForAccount(accountID int64) (Totals, error) {
	var t Totals
	err := s.db.QueryRow(`SELECT
		(SELECT COUNT(*) FROM runs WHERE account_id = ?) +
		(SELECT COUNT(*) FROM run_statistics WHERE account_id = ?),
		(SELECT COALESCE(SUM(elapsed_seconds), 0) FROM runs WHERE account_id = ?) +
		(SELECT COALESCE(SUM(elapsed_seconds), 0) FROM run_statistics WHERE account_id = ?)`,
		accountID, accountID, accountID, accountID).Scan(&t.Runs, &t.Seconds)
	return t, err
}

// AccountStat is per-account reading volume since a timestamp.
type AccountStat struct {
	AccountID int64  `json:"account_id"`
	Name      string `json:"name"`
	Seconds   int64  `json:"seconds"`
}

// WindowStatsFromRuns computes daily and account totals from one set of facts
// over the same inclusive local-day window. accountID=0 includes all accounts.
func WindowStatsFromRuns(runs []Run, since, through int64, loc *time.Location, accountID int64) (map[string]int, []AccountStat) {
	startDay := time.Unix(since, 0).In(loc).Format("2006-01-02")
	endDay := time.Unix(through, 0).In(loc).Format("2006-01-02")
	daily := make(map[string]int)
	byID := make(map[int64]*AccountStat)
	for _, run := range runs {
		if accountID > 0 && run.AccountID != accountID {
			continue
		}
		for day, seconds := range SplitRunSeconds(run, loc) {
			if day < startDay || day > endDay || seconds <= 0 {
				continue
			}
			daily[day] += seconds
			stat := byID[run.AccountID]
			if stat == nil {
				stat = &AccountStat{AccountID: run.AccountID, Name: run.AccountName}
				byID[run.AccountID] = stat
			}
			stat.Seconds += int64(seconds)
		}
	}
	out := make([]AccountStat, 0, len(byID))
	for _, stat := range byID {
		out = append(out, *stat)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Seconds != out[j].Seconds {
			return out[i].Seconds > out[j].Seconds
		}
		return out[i].AccountID < out[j].AccountID
	})
	return daily, out
}

// SplitRunSeconds spreads one run's reading time across the natural days it
// overlaps in loc. Archived statistical facts have the same required fields.
func SplitRunSeconds(run Run, loc *time.Location) map[string]int {
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
		// Wall-clock span can be shorter than accumulated reading time.
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

// DailySecondsByAccount returns today's retained and live reading seconds for
// every account using the same local-day split as the overview statistics.
func (s *Store) DailySecondsByAccount(at time.Time, loc *time.Location) (map[int64]int, error) {
	if loc == nil {
		loc = time.Local
	}
	day := at.In(loc)
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
	runs, err := s.RunsSince(start.Unix())
	if err != nil {
		return nil, err
	}
	key := start.Format("2006-01-02")
	out := make(map[int64]int)
	for _, run := range runs {
		out[run.AccountID] += SplitRunSeconds(run, loc)[key]
	}
	return out, nil
}

// DailySeconds returns one account's reading seconds for the local day of at.
func (s *Store) DailySeconds(accountID int64, at time.Time, loc *time.Location) (int, error) {
	byAccount, err := s.DailySecondsByAccount(at, loc)
	if err != nil {
		return 0, err
	}
	return byAccount[accountID], nil
}

// StatsByAccount groups the retained and live facts by account from the local
// day containing since through today. The optional location preserves callers
// that used the former one-argument method.
func (s *Store) StatsByAccount(since int64, locations ...*time.Location) ([]AccountStat, error) {
	loc := time.Local
	if len(locations) > 0 && locations[0] != nil {
		loc = locations[0]
	}
	runs, err := s.RunsSince(since)
	if err != nil {
		return nil, err
	}
	_, byAccount := WindowStatsFromRuns(runs, since, time.Now().In(loc).Unix(), loc, 0)
	return byAccount, nil
}
