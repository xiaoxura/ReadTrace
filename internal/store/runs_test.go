package store

import (
	"testing"
	"time"
)

// 缺陷 07 转正测试：前日深夜遗留的 running 行在重启打断后，finished_at 必须
// 落在重启前的最后活动时刻（last_progress_at，缺列值时退回 started_at+
// elapsed 近似），把已读秒数分配到实际活动的自然日，而不是把整段停机时间
// 记进重启动当天的阅读时段。
func TestInterruptRunningRunsStopsAtProgressTime(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	nowW := time.Now().In(loc)
	midnight := time.Date(nowW.Year(), nowW.Month(), nowW.Day(), 0, 0, 0, 0, loc)
	start := midnight.Add(-time.Minute) // 前日 23:59
	progressAt := midnight.Add(-30 * time.Second)
	todayKey := midnight.Format("2006-01-02")
	yesterdayKey := midnight.AddDate(0, 0, -1).Format("2006-01-02")

	for _, tc := range []struct {
		name                     string
		progressAt               int64
		wantFinished             int64
		wantYesterday, wantToday int
	}{
		// 心跳进度时刻在停机前最后已知：结算到该时刻，昨日 60 秒、今日 0。
		{"with_progress_time", progressAt.Unix(), progressAt.Unix(), 60, 0},
		// 老数据没有进度时刻：退回 started_at+elapsed 近似，同样不让停机
		// 时段参与日分配。
		{"without_progress_time", 0, start.Add(60 * time.Second).Unix(), 60, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newTestStore(t)
			acc := seedAccount(t, st, "A")
			run := &Run{AccountID: acc, PlanSeconds: 600, ElapsedSeconds: 60,
				Status: "running", StartedAt: start.Unix()}
			if err := st.CreateRun(run); err != nil {
				t.Fatal(err)
			}
			if tc.progressAt != 0 {
				if _, err := st.db.Exec(`UPDATE runs SET last_progress_at = ? WHERE id = ?`,
					tc.progressAt, run.ID); err != nil {
					t.Fatal(err)
				}
			}
			// 模拟次日下午重启。
			if err := st.InterruptRunningRuns(); err != nil {
				t.Fatal(err)
			}
			got, err := st.GetRun(run.ID)
			if err != nil || got == nil {
				t.Fatalf("get run: %v %v", got, err)
			}
			if got.Status != "failed" {
				t.Fatalf("status = %q, want failed", got.Status)
			}
			if got.FinishedAt != tc.wantFinished {
				t.Fatalf("finished_at = %d (%s), want %d (%s)", got.FinishedAt,
					time.Unix(got.FinishedAt, 0).In(loc), tc.wantFinished,
					time.Unix(tc.wantFinished, 0).In(loc))
			}
			split := SplitRunSeconds(*got, loc)
			if split[yesterdayKey] != tc.wantYesterday || split[todayKey] != tc.wantToday {
				t.Fatalf("day split = %v, want %s=%d %s=%d", split, yesterdayKey, tc.wantYesterday, todayKey, tc.wantToday)
			}
		})
	}
}

// 缺陷 18：ListRuns 支持 offset 服务端分页，行为与 filter/总数语义不变。
func TestListRunsOffsetPagination(t *testing.T) {
	st := newTestStore(t)
	acc := seedAccount(t, st, "A")
	other := seedAccount(t, st, "B")
	ids := []int64{}
	for i := 0; i < 5; i++ {
		run := &Run{AccountID: acc, PlanSeconds: 30, Status: "success",
			StartedAt: now() - int64(100-i), FinishedAt: now()}
		if err := st.CreateRun(run); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, run.ID)
	}
	if err := st.CreateRun(&Run{AccountID: other, PlanSeconds: 30, Status: "success",
		StartedAt: now(), FinishedAt: now()}); err != nil {
		t.Fatal(err)
	}
	pick := func(runs []Run) []int64 {
		out := []int64{}
		for _, r := range runs {
			out = append(out, r.ID)
		}
		return out
	}
	// 不带 offset：仍旧返回最新 limit 条（既有调用点行为不变）。
	first, err := st.ListRuns(acc, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := pick(first); len(got) != 2 || got[0] != ids[4] || got[1] != ids[3] {
		t.Fatalf("first page = %v, want %v + %v", got, ids[4], ids[3])
	}
	// offset=2：跳过最新的 2 条，返回第 3、4 新的记录。
	second, err := st.ListRuns(acc, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := pick(second); len(got) != 2 || got[0] != ids[2] || got[1] != ids[1] {
		t.Fatalf("offset page = %v, want %v + %v", got, ids[2], ids[1])
	}
	// 尾页：只剩最旧的一条。
	tail, err := st.ListRuns(acc, 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	if got := pick(tail); len(got) != 1 || got[0] != ids[0] {
		t.Fatalf("tail page = %v, want %v", got, ids[0])
	}
	// offset 超出总数：空集，不报错。
	past, err := st.ListRuns(acc, 2, 99)
	if err != nil || len(past) != 0 {
		t.Fatalf("past-end page = %v, err = %v", past, err)
	}
	// 账号过滤与 offset 组合仍然成立。
	others, err := st.ListRuns(other, 10, 0)
	if err != nil || len(others) != 1 {
		t.Fatalf("other account page = %v, err = %v", others, err)
	}
	// 默认 limit 行为不变：limit<=0 → 100。
	all, err := st.ListRuns(acc, 0)
	if err != nil || len(all) != 5 {
		t.Fatalf("default limit = %v, err = %v, want all 5", all, err)
	}
}
