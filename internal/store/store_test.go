package store

import (
	"path/filepath"
	"testing"
	"time"

	"readtrace/internal/secure"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	box, err := secure.New(dir)
	if err != nil {
		t.Fatalf("new secure box: %v", err)
	}
	st, err := Open(filepath.Join(dir, "test.db"), box)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestPurgeHistoryKeepsRunningAndRecent(t *testing.T) {
	st := newTestStore(t)

	id, err := st.CreateAccount(Account{Name: "a", Vid: "v1"}, nil, nil)
	if err != nil {
		t.Fatalf("create account: %v", err)
	}

	// A simulated two-week-old finished run with logs.
	oldRun := &Run{AccountID: id, Trigger: "manual", PlanSeconds: 60, Status: "running", StartedAt: now()}
	if err := st.CreateRun(oldRun); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if _, err := st.AddLog(oldRun.ID, now(), "info", "old log"); err != nil {
		t.Fatalf("add log: %v", err)
	}
	if err := st.FinishRun(oldRun.ID, "success", 60, 2, 0, ""); err != nil {
		t.Fatalf("finish run: %v", err)
	}
	// FinishRun stamps finished_at with now(); pretend it was 14 days ago.
	if _, err := st.db.Exec(`UPDATE runs SET finished_at = ? WHERE id = ?`, now()-14*86400, oldRun.ID); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	// A正常 recent finished run and an in-flight run must survive.
	recentRun := &Run{AccountID: id, Trigger: "manual", PlanSeconds: 60, Status: "running", StartedAt: now()}
	if err := st.CreateRun(recentRun); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if err := st.FinishRun(recentRun.ID, "success", 60, 2, 0, ""); err != nil {
		t.Fatalf("finish run: %v", err)
	}
	liveRun := &Run{AccountID: id, Trigger: "manual", PlanSeconds: 60, Status: "running", StartedAt: now()}
	if err := st.CreateRun(liveRun); err != nil {
		t.Fatalf("create run: %v", err)
	}

	// Cutoff is 7 days ago: only the finished 14-day-old run qualifies.
	cutoff := now() - 7*86400
	n, err := st.PurgeHistoryBefore(cutoff)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 purged run, got %d", n)
	}

	if r, err := st.GetRun(oldRun.ID); err != nil || r != nil {
		t.Fatalf("old run should be gone: %v %v", r, err)
	}
	for _, id := range []int64{recentRun.ID, liveRun.ID} {
		r, err := st.GetRun(id)
		if err != nil || r == nil {
			t.Fatalf("run %d should survive: %v %v", id, r, err)
		}
	}
	logs, err := st.ListLogs(liveRun.ID, 0, 10)
	if err != nil || len(logs) != 0 {
		t.Fatalf("live run logs check: %v %v", logs, err)
	}
	if _, err := st.ListLogs(oldRun.ID, 0, 10); err != nil {
		t.Fatalf("list logs of purged run should not error: %v", err)
	}
}

func TestExtendSessionRollsForward(t *testing.T) {
	st := newTestStore(t)
	if err := st.CreateSession("s1", "ip", "ua", 30*time.Minute); err != nil {
		t.Fatalf("create session: %v", err)
	}
	// Manipulate to expire soon.
	if _, err := st.db.Exec(`UPDATE sessions SET expires_at = ? WHERE id = ?`, now()+3600, "s1"); err != nil {
		t.Fatalf("shrink session: %v", err)
	}
	if err := st.ExtendSession("s1", 30*24*time.Hour); err != nil {
		t.Fatalf("extend: %v", err)
	}
	ses, err := st.GetSession("s1")
	if err != nil || ses == nil {
		t.Fatalf("session should exist: %v %v", ses, err)
	}
	if ses.ExpiresAt-now() < 29*24*3600 {
		t.Fatalf("session should have ~30 days left, got %d", ses.ExpiresAt-now())
	}
}

func TestAccountSeqStartsAtZeroAndIncrements(t *testing.T) {
	st := newTestStore(t)
	seen := map[int64]bool{}
	for want := int64(0); want < 3; want++ {
		id, err := st.CreateAccount(Account{Name: "a"}, nil, nil)
		if err != nil {
			t.Fatalf("create account %d: %v", want, err)
		}
		acc, err := st.GetAccount(id)
		if err != nil || acc == nil {
			t.Fatalf("get account: %v %v", acc, err)
		}
		if acc.Seq != want {
			t.Fatalf("account seq = %d, want %d", acc.Seq, want)
		}
		if seen[acc.Seq] {
			t.Fatalf("duplicate seq %d", acc.Seq)
		}
		seen[acc.Seq] = true
	}
	// 删除中段账号后，不回收序号，只往下递增。
	if err := st.DeleteAccount(2); err != nil {
		t.Fatalf("delete account: %v", err)
	}
	id, err := st.CreateAccount(Account{Name: "b"}, nil, nil)
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	acc, _ := st.GetAccount(id)
	if acc.Seq != 3 {
		t.Fatalf("seq after delete = %d, want 3", acc.Seq)
	}
	// 重试配置默认值：新账号默认自动重试 5 次、间隔 30 秒。
	if acc.RetryDelay != 30 || acc.MaxRetries != 5 {
		t.Fatalf("retry defaults = (%d,%d), want (5,30)", acc.MaxRetries, acc.RetryDelay)
	}
}

func TestPushChannelTemplatesRoundTrip(t *testing.T) {
	st := newTestStore(t)
	v := &PushChannel{Name: "c", Type: "pushplus", Config: map[string]string{"token": "t"},
		Templates: map[string]string{"success_title": "读完啦", "failed_body": "坏了：{{error}}"}}
	if err := st.CreatePushChannel(v); err != nil {
		t.Fatalf("create channel: %v", err)
	}
	got, err := st.GetPushChannel(v.ID)
	if err != nil || got == nil {
		t.Fatalf("get channel: %v %v", got, err)
	}
	if got.Templates["success_title"] != "读完啦" || got.Templates["failed_body"] != "坏了：{{error}}" {
		t.Fatalf("templates lost: %v", got.Templates)
	}
	// 更新为空模板后应回落到空 map。
	got.Templates = map[string]string{}
	if err := st.UpdatePushChannel(got); err != nil {
		t.Fatalf("update channel: %v", err)
	}
	got, _ = st.GetPushChannel(v.ID)
	if len(got.Templates) != 0 {
		t.Fatalf("templates should be empty, got %v", got.Templates)
	}
}
