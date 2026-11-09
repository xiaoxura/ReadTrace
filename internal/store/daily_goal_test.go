package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"readtrace/internal/secure"
)

func TestDailyGoalMigrationBackfillsOnlyOnce(t *testing.T) {
	dir := t.TempDir()
	box, err := secure.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	legacySchema := strings.Replace(schema, "\tdaily_goal_minutes INTEGER NOT NULL DEFAULT 0,\n", "", 1)
	if _, err := db.Exec(legacySchema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO accounts (name, default_minutes, created_at, updated_at) VALUES
		('valid', 20, 1, 1), ('zero', 0, 1, 1), ('too_large', 1500, 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := Open(path, box)
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range []int{20, 30, 30} {
		acc, err := st.GetAccount(int64(id + 1))
		if err != nil || acc == nil || acc.DailyGoalMinutes != want {
			t.Fatalf("account %d = %+v, err=%v", id+1, acc, err)
		}
	}
	acc, _ := st.GetAccount(1)
	acc.DailyGoalMinutes = 0
	if err := st.UpdateAccount(*acc); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path, box)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	acc, err = st.GetAccount(1)
	if err != nil || acc.DailyGoalMinutes != 0 {
		t.Fatalf("disabled goal restored on restart: %+v, %v", acc, err)
	}
	id, err := st.CreateAccount(Account{Name: "new"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	acc, err = st.GetAccount(id)
	if err != nil || acc.DailyGoalMinutes != 0 {
		t.Fatalf("store default goal = %+v, %v", acc, err)
	}
	acc.DailyGoalMinutes = 45
	if err := st.UpdateAccount(*acc); err != nil {
		t.Fatal(err)
	}
	acc, err = st.GetAccount(id)
	if err != nil || acc.DailyGoalMinutes != 45 {
		t.Fatalf("goal update = %+v, %v", acc, err)
	}
}

func TestDailySecondsIncludesStatusAndArchiveAcrossLocalDays(t *testing.T) {
	st := newTestStore(t)
	a, err := st.CreateAccount(Account{Name: "a"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateAccount(Account{Name: "b"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 3, 8, 23, 59, 0, 0, loc).Unix()
	for _, tc := range []struct {
		account    int64
		status     string
		start, end int64
		elapsed    int
	}{
		{a, "failed", start, start + 120, 120},
		{a, "stopped", start + 61, start + 68, 7},
		{b, "success", start + 61, start + 661, 600},
	} {
		r := &Run{AccountID: tc.account, Status: tc.status, PlanSeconds: 600,
			StartedAt: tc.start, FinishedAt: tc.end, ElapsedSeconds: tc.elapsed}
		if err := st.CreateRun(r); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Date(2026, 3, 9, 12, 0, 0, 0, loc)
	check := func() {
		t.Helper()
		got, err := st.DailySecondsByAccount(at, loc)
		if err != nil || got[a] != 67 || got[b] != 600 {
			t.Fatalf("daily = %v, err=%v", got, err)
		}
		previous, err := st.DailySeconds(a, at.AddDate(0, 0, -1), loc)
		if err != nil || previous != 60 {
			t.Fatalf("previous day = %d, err=%v", previous, err)
		}
	}
	check()
	if _, err := st.PurgeHistoryBefore(start + 1000); err != nil {
		t.Fatal(err)
	}
	check()
}
