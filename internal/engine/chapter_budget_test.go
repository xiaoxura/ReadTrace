package engine

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"readtrace/internal/store"
)

// TestChapterTickBudgetConversion 预算显式换算：ceil(字数 ÷（速度 × 间隔 ÷ 60))。
func TestChapterTickBudgetConversion(t *testing.T) {
	cases := []struct {
		name                   string
		wordCount, speed, tick int
		want                   int
	}{
		{"600字_默认300_30s心跳", 600, 300, 30, 4},
		{"600字_速度600_30s心跳快一倍", 600, 600, 30, 2},
		{"600字_速度600_60s心跳_墙钟仍约1分钟", 600, 600, 60, 1},
		{"600字_速度300_60s心跳", 600, 300, 60, 2},
		{"2300字_默认速度", 2300, 300, 30, 16},
		{"150字_默认速度1心跳", 150, 300, 30, 1},
		{"不足一心跳向上取整", 1, 3000, 300, 1},
		{"字数未知按4次.interval兜底", 0, 300, 30, 4},
		{"字数未知速度快不加速", 0, 3000, 10, 4},
		{"未clamp的0速度按默认口径", 600, 0, 30, 4},
	}
	for _, c := range cases {
		if got := chapterTickBudget(c.wordCount, c.speed, c.tick); got != c.want {
			t.Fatalf("%s: chapterTickBudget(%d,%d,%d) = %d, want %d",
				c.name, c.wordCount, c.speed, c.tick, got, c.want)
		}
	}
}

// newSeqAdvanceAr 造一个顺序书单的 activeRun：BOOK_A 两章
// （CHAP_A1 600 字，CHAP_A2 1200 字），预算直接读引擎；日志写进运行记录。
func newSeqAdvanceAr(t *testing.T, st *store.Store, accountID int64, runID int64, speed int) (*Engine, *activeRun) {
	t.Helper()
	e := New(st, nil, nil, NewHub(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	ar := &activeRun{
		runID: runID, accountID: accountID, account: "seq",
		readSpeed:         speed,
		tick:              30,
		orderedBookList:   []string{"BOOK_A"},
		seqMark:           seqMark{bi: 0, ci: 0, chapters: []string{"CHAP_A1", "CHAP_A2"}, rawChapter: "CHAP_A1"},
		bookTitles:        map[string]string{"BOOK_A": "甲书"},
		bookByNorm:        map[string]string{},
		chapterTitlesRaw:  map[string]string{},
		chapterWordCounts: map[string]int{},
		bookChapterTitles: map[string]map[string]string{"BOOK_A": {"CHAP_A1": "第一章", "CHAP_A2": "第二章"}},
		bookChapterWords:  map[string]map[string]int{"BOOK_A": {"CHAP_A1": 600, "CHAP_A2": 1200}},
		ctx:               context.Background(),
		cancel:            func() {},
		done:              make(chan struct{}),
	}
	return e, ar
}

// 顺序书单切章预算随速度与间隔变化：默认 300 字/分钟 + 30 秒心跳 = 150 字/
// 心跳 → 600 字章节 4 次心跳后切章；速度 600 → 2 次心跳即切章，日志同时
// 带上本章字数与预计分钟数。
func TestSeqAdvanceBudgetFollowsSpeedAndInterval(t *testing.T) {
	for _, tc := range []struct {
		name          string
		speed         int
		switchAfter   int // 第几次成功心跳切章
		wantLogSubstr string
	}{
		{"_速度300_600字_4心跳切章", 300, 4, "本章 600 字，按当前 300 字/分钟约 2.0 分钟"},
		{"_速度600_2心跳切章", 600, 2, "本章 600 字，按当前 600 字/分钟约 1.0 分钟"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := testEngineStore(t)
			id, err := st.CreateAccount(store.Account{Name: "seq", Mode: "sequence", ReadSpeed: tc.speed}, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			addReadablePoolChapter(t, st, id, "BOOK_A", "CHAP_A1")
			if _, err := st.AddChapterItem(id, "BOOK_A", "CHAP_A2", "BOOK_A · 第二章", 1200); err != nil {
				t.Fatal(err)
			}
			run := &store.Run{AccountID: id, Trigger: "test", PlanSeconds: 600, Status: "running", StartedAt: time.Now().Unix()}
			if err := st.CreateRun(run); err != nil {
				t.Fatal(err)
			}
			e, ar := newSeqAdvanceAr(t, st, id, run.ID, tc.speed)
			prevSt := -1
			for i := 1; i <= tc.switchAfter; i++ {
				e.seqAdvance(ar)
				switched := ar.seqPos.Ci == 1
				if !switched && ar.seqPos.Ci != 0 {
					t.Fatalf("第 %d 次推进后 Ci=%d", i, ar.seqPos.Ci)
				}
				if i < tc.switchAfter {
					if switched {
						t.Fatalf("%s：第 %d 次心跳不应切章，Ci=%d", tc.name, i, ar.seqPos.Ci)
					}
					if ar.seqPos.St <= prevSt {
						t.Fatalf("第 %d 次推进 St 未累积: st=%d prev=%d", i, ar.seqPos.St, prevSt)
					}
					prevSt = ar.seqPos.St
				} else {
					// 预算读满：切到下一章，章节内计数归零。
					if !switched || ar.seqPos.St != 0 {
						t.Fatalf("%s：第 %d 次心跳应切章 (Ci=%d St=%d)", tc.name, i, ar.seqPos.Ci, ar.seqPos.St)
					}
				}
			}
			logs, err := st.ListLogs(run.ID, 0, 10)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, l := range logs {
				if strings.Contains(l.Message, tc.wantLogSubstr) && strings.Contains(l.Message, "顺序书单") {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("切章日志缺少节奏信息 %q，日志=%v", tc.wantLogSubstr, logs)
			}
		})
	}
}

// 字数未知（无 book_id 归属、无 rawChapter）时保持 4 次 interval 心跳兜底。
func TestSeqAdvanceUnknownWordCountFallsBackToFourTicks(t *testing.T) {
	st := testEngineStore(t)
	id, err := st.CreateAccount(store.Account{Name: "seq", Mode: "sequence"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	addReadablePoolChapter(t, st, id, "BOOK_A", "CHAP_A1")
	run := &store.Run{AccountID: id, Trigger: "test", PlanSeconds: 600, Status: "running", StartedAt: time.Now().Unix()}
	if err := st.CreateRun(run); err != nil {
		t.Fatal(err)
	}
	e, ar := newSeqAdvanceAr(t, st, id, run.ID, 3000)
	// rawChapter 为空 = buildPayload 没记录章节，走 4 次兜底。
	ar.seqMark.rawChapter = ""
	for i := 1; i <= 3; i++ {
		e.seqAdvance(ar)
		if ar.seqPos.Ci != 0 {
			t.Fatalf("第 %d 次推进不应切章", i)
		}
	}
	e.seqAdvance(ar)
	if ar.seqPos.Ci != 1 || len(ar.orderedBookList) == 0 && false {
		t.Fatalf("4 次心跳后应切章: pos=%+v", ar.seqPos)
	}
}

// 引擎内部的速度折算也按 0/未填 = 默认 300、上限 3000 折算（与 store 同口径）。
func TestEngineClampReadSpeed(t *testing.T) {
	if got := clampReadSpeed(0); got != 300 {
		t.Fatalf("clampReadSpeed(0) = %d, want 300", got)
	}
	if got := clampReadSpeed(-1); got != 300 {
		t.Fatalf("clampReadSpeed(-1) = %d, want 300", got)
	}
	if got := clampReadSpeed(99999); got != 3000 {
		t.Fatalf("clampReadSpeed(99999) = %d, want 3000", got)
	}
	if got := clampReadSpeed(600); got != 600 {
		t.Fatalf("clampReadSpeed(600) = %d, want 600", got)
	}
}
