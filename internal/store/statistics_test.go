package store

import (
	"path/filepath"
	"testing"
	"time"

	"readtrace/internal/secure"
)

func TestPurgePreservesStatisticsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	box, err := secure.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "statistics.db")
	st, err := Open(path, box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	accountID, err := st.CreateAccount(Account{Name: "first"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	otherID, err := st.CreateAccount(Account{Name: "second"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	loc := time.FixedZone("CST", 8*3600)
	start := time.Date(2026, 10, 1, 23, 50, 0, 0, loc).Unix()
	old := &Run{AccountID: accountID, Trigger: "manual", PlanSeconds: 1800,
		ElapsedSeconds: 1800, Status: "success", StartedAt: start, FinishedAt: start + 1800}
	if err := st.CreateRun(old); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddLog(old.ID, start+1, "info", "detail to remove"); err != nil {
		t.Fatal(err)
	}
	recent := &Run{AccountID: otherID, Trigger: "manual", PlanSeconds: 60,
		ElapsedSeconds: 60, Status: "success", StartedAt: start + 2*86400, FinishedAt: start + 2*86400 + 60}
	if err := st.CreateRun(recent); err != nil {
		t.Fatal(err)
	}
	live := &Run{AccountID: accountID, Trigger: "manual", PlanSeconds: 60,
		Status: "running", StartedAt: start - 86400}
	if err := st.CreateRun(live); err != nil {
		t.Fatal(err)
	}
	dayStart := time.Date(2026, 10, 2, 0, 0, 0, 0, loc).Unix()
	check := func(wantOldDetail bool) {
		t.Helper()
		totals, err := st.Totals()
		if err != nil || totals != (Totals{Runs: 3, Seconds: 1860}) {
			t.Fatalf("totals = %+v, err = %v", totals, err)
		}
		one, err := st.TotalsForAccount(accountID)
		if err != nil || one != (Totals{Runs: 2, Seconds: 1800}) {
			t.Fatalf("account totals = %+v, err = %v", one, err)
		}
		runs, err := st.RunsSince(dayStart)
		if err != nil || len(runs) != 2 {
			t.Fatalf("overlapping facts = %+v, err = %v", runs, err)
		}
		_, byAccount := WindowStatsFromRuns(runs, dayStart, dayStart+3*86400, loc, 0)
		if len(byAccount) != 2 || byAccount[0].AccountID != accountID || byAccount[0].Seconds != 1200 ||
			byAccount[1].AccountID != otherID || byAccount[1].Seconds != 60 {
			t.Fatalf("account window stats = %+v", byAccount)
		}
		split := SplitRunSeconds(runs[0], loc)
		if split["2026-10-01"] != 600 || split["2026-10-02"] != 1200 {
			t.Fatalf("daily split = %+v", split)
		}
		got, err := st.GetRun(old.ID)
		if err != nil || (got != nil) != wantOldDetail {
			t.Fatalf("old detail = %+v, err = %v", got, err)
		}
	}
	check(true)
	cutoff := start + 1801
	n, err := st.PurgeHistoryBefore(cutoff)
	if err != nil || n != 1 {
		t.Fatalf("purge = %d, err = %v", n, err)
	}
	check(false)
	logs, err := st.ListLogs(old.ID, 0, 10)
	if err != nil || len(logs) != 0 {
		t.Fatalf("old logs = %+v, err = %v", logs, err)
	}
	if got, err := st.GetRun(live.ID); err != nil || got == nil {
		t.Fatalf("unfinished run was deleted: %+v, %v", got, err)
	}
	n, err = st.PurgeHistoryBefore(cutoff)
	if err != nil || n != 0 {
		t.Fatalf("repeated purge = %d, err = %v", n, err)
	}
	var archived int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM run_statistics`).Scan(&archived); err != nil || archived != 1 {
		t.Fatalf("archived rows = %d, err = %v", archived, err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path, box)
	if err != nil {
		t.Fatal(err)
	}
	check(false)
	if err := st.DeleteAccount(accountID); err != nil {
		t.Fatal(err)
	}
	totals, err := st.Totals()
	if err != nil || totals != (Totals{Runs: 1, Seconds: 60}) {
		t.Fatalf("totals after account deletion = %+v, err = %v", totals, err)
	}
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM run_statistics`).Scan(&archived); err != nil || archived != 0 {
		t.Fatalf("statistics survived account deletion: %d, err = %v", archived, err)
	}
}

func TestPurgeRollbackKeepsRunsLogsAndStatisticsTogether(t *testing.T) {
	st := newTestStore(t)
	accountID, err := st.CreateAccount(Account{Name: "first"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	run := &Run{AccountID: accountID, Trigger: "manual", PlanSeconds: 60,
		ElapsedSeconds: 60, Status: "success", StartedAt: 100, FinishedAt: 160}
	if err := st.CreateRun(run); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddLog(run.ID, 110, "info", "keep me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`CREATE TRIGGER fail_log_delete BEFORE DELETE ON run_logs
		BEGIN SELECT RAISE(ABORT, 'deliberate purge failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PurgeHistoryBefore(200); err == nil {
		t.Fatal("purge should fail when log deletion fails")
	}
	var archived int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM run_statistics`).Scan(&archived); err != nil || archived != 0 {
		t.Fatalf("archive was not rolled back: %d, err = %v", archived, err)
	}
	if got, err := st.GetRun(run.ID); err != nil || got == nil {
		t.Fatalf("run was deleted after failed purge: %+v, %v", got, err)
	}
	logs, err := st.ListLogs(run.ID, 0, 10)
	if err != nil || len(logs) != 1 {
		t.Fatalf("logs after failed purge = %+v, err = %v", logs, err)
	}
}

func TestArchivedRunSplitsAcrossDSTAndTimezoneChange(t *testing.T) {
	st := newTestStore(t)
	accountID, err := st.CreateAccount(Account{Name: "first"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 3, 7, 23, 30, 0, 0, ny).Unix()
	run := &Run{AccountID: accountID, Trigger: "manual", PlanSeconds: 10800,
		ElapsedSeconds: 10800, Status: "success", StartedAt: start, FinishedAt: start + 10800}
	if err := st.CreateRun(run); err != nil {
		t.Fatal(err)
	}
	if n, err := st.PurgeHistoryBefore(run.FinishedAt + 1); err != nil || n != 1 {
		t.Fatalf("purge = %d, err = %v", n, err)
	}
	since := time.Date(2026, 3, 8, 0, 0, 0, 0, ny).Unix()
	facts, err := st.RunsSince(since)
	if err != nil || len(facts) != 1 {
		t.Fatalf("overlapping archived facts = %+v, err = %v", facts, err)
	}
	nyDaily := SplitRunSeconds(facts[0], ny)
	if nyDaily["2026-03-07"] != 1800 || nyDaily["2026-03-08"] != 9000 {
		t.Fatalf("New York DST split = %+v", nyDaily)
	}
	_, stats := WindowStatsFromRuns(facts, since, since, ny, 0)
	if len(stats) != 1 || stats[0].Seconds != 9000 {
		t.Fatalf("account window after DST = %+v", stats)
	}
	utcDaily := SplitRunSeconds(facts[0], time.UTC)
	if len(utcDaily) != 1 || utcDaily["2026-03-08"] != 10800 {
		t.Fatalf("UTC split of same archived fact = %+v", utcDaily)
	}
}

func TestStatsByAccountIncludesArchivedOverlap(t *testing.T) {
	st := newTestStore(t)
	accountID := seedAccount(t, st, "first")
	day := time.Now().UTC().Add(-24 * time.Hour).Truncate(24 * time.Hour)
	start := day.Add(-10 * time.Minute).Unix()
	run := &Run{AccountID: accountID, Trigger: "manual", PlanSeconds: 1200,
		ElapsedSeconds: 1200, Status: "success", StartedAt: start, FinishedAt: start + 1200}
	if err := st.CreateRun(run); err != nil {
		t.Fatal(err)
	}
	if n, err := st.PurgeHistoryBefore(run.FinishedAt + 1); err != nil || n != 1 {
		t.Fatalf("archive = %d, err = %v", n, err)
	}
	stats, err := st.StatsByAccount(day.Unix(), time.UTC)
	if err != nil || len(stats) != 1 || stats[0].AccountID != accountID || stats[0].Seconds != 600 {
		t.Fatalf("stored account stats = %+v, err = %v", stats, err)
	}
}

func TestWindowStatsMatchesAccountTotalsWithRunningAndFutureFacts(t *testing.T) {
	st := newTestStore(t)
	first := seedAccount(t, st, "first")
	second := seedAccount(t, st, "second")
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	oldStart := time.Date(2026, 3, 7, 23, 30, 0, 0, ny).Unix()
	old := &Run{AccountID: first, Trigger: "manual", PlanSeconds: 10800,
		ElapsedSeconds: 10800, Status: "success", StartedAt: oldStart, FinishedAt: oldStart + 10800}
	if err := st.CreateRun(old); err != nil {
		t.Fatal(err)
	}
	if n, err := st.PurgeHistoryBefore(old.FinishedAt + 1); err != nil || n != 1 {
		t.Fatalf("archive old run = %d, err = %v", n, err)
	}
	runningStart := time.Date(2026, 3, 8, 23, 50, 0, 0, ny).Unix()
	running := &Run{AccountID: second, Trigger: "manual", PlanSeconds: 1200,
		ElapsedSeconds: 1200, Status: "running", StartedAt: runningStart}
	if err := st.CreateRun(running); err != nil {
		t.Fatal(err)
	}
	futureStart := time.Date(2026, 3, 10, 12, 0, 0, 0, ny).Unix()
	future := &Run{AccountID: first, Trigger: "manual", PlanSeconds: 600,
		ElapsedSeconds: 600, Status: "success", StartedAt: futureStart, FinishedAt: futureStart + 600}
	if err := st.CreateRun(future); err != nil {
		t.Fatal(err)
	}
	since := time.Date(2026, 3, 8, 0, 0, 0, 0, ny).Unix()
	facts, err := st.RunsSince(since)
	if err != nil || len(facts) != 3 {
		t.Fatalf("overlapping facts = %+v, err = %v", facts, err)
	}
	daily, accounts := WindowStatsFromRuns(facts, since, since, ny, 0)
	if len(daily) != 1 || daily["2026-03-08"] != 9600 || len(accounts) != 2 ||
		accounts[0].AccountID != first || accounts[0].Seconds != 9000 ||
		accounts[1].AccountID != second || accounts[1].Seconds != 600 {
		t.Fatalf("window daily=%+v accounts=%+v", daily, accounts)
	}
	if accounts[0].Seconds+accounts[1].Seconds != int64(daily["2026-03-08"]) {
		t.Fatalf("account totals diverged from series: daily=%+v accounts=%+v", daily, accounts)
	}
	onlyFirst, onlyFirstAccounts := WindowStatsFromRuns(facts, since, since, ny, first)
	if len(onlyFirst) != 1 || onlyFirst["2026-03-08"] != 9000 ||
		len(onlyFirstAccounts) != 1 || onlyFirstAccounts[0].AccountID != first || onlyFirstAccounts[0].Seconds != 9000 {
		t.Fatalf("filtered window daily=%+v accounts=%+v", onlyFirst, onlyFirstAccounts)
	}
}
