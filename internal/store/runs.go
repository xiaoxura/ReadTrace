package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const runCols = `r.id, r.account_id, COALESCE(a.name, ''), r.trigger, r.plan_seconds,
	r.elapsed_seconds, r.success_ticks, r.failed_ticks, r.status, r.error,
	r.started_at, r.finished_at`

func scanRun(sc interface{ Scan(...any) error }) (*Run, error) {
	var r Run
	err := sc.Scan(&r.ID, &r.AccountID, &r.AccountName, &r.Trigger, &r.PlanSeconds,
		&r.ElapsedSeconds, &r.SuccessTicks, &r.FailedTicks, &r.Status, &r.Error,
		&r.StartedAt, &r.FinishedAt)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// CreateRun inserts a new run row.
func (s *Store) CreateRun(r *Run) error {
	res, err := s.db.Exec(`INSERT INTO runs
		(account_id, trigger, plan_seconds, elapsed_seconds, success_ticks, failed_ticks,
		 status, error, started_at, finished_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`,
		r.AccountID, r.Trigger, r.PlanSeconds, r.ElapsedSeconds, r.SuccessTicks,
		r.FailedTicks, r.Status, r.Error, r.StartedAt, r.FinishedAt)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	r.ID = id
	return nil
}

// UpdateRunProgress persists a tick's progress. 同步写 last_progress_at（缺
// 陷 07）：记录最后心跳落库时刻，进程重启打断遗留运行时用它近似真正的活动
// 结束时刻，避免把整段停机时间算进阅读时段。
func (s *Store) UpdateRunProgress(id int64, elapsed, success, failed int) error {
	_, err := s.db.Exec(`UPDATE runs SET elapsed_seconds = ?, success_ticks = ?, failed_ticks = ?,
		last_progress_at = ? WHERE id = ?`, elapsed, success, failed, now(), id)
	return err
}

// FinishRun finalizes a run.
func (s *Store) FinishRun(id int64, status string, elapsed, success, failed int, errMsg string) error {
	_, err := s.db.Exec(`UPDATE runs SET status = ?, elapsed_seconds = ?, success_ticks = ?,
		failed_ticks = ?, error = ?, finished_at = ? WHERE id = ?`,
		status, elapsed, success, failed, errMsg, now(), id)
	return err
}

// GetRun loads one run with its account name.
func (s *Store) GetRun(id int64) (*Run, error) {
	r, err := scanRun(s.db.QueryRow(`SELECT `+runCols+` FROM runs r
		LEFT JOIN accounts a ON a.id = r.account_id WHERE r.id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return r, err
}

// ListRuns returns the newest runs, optionally filtered by account. The
// optional offset 参数（缺陷 18：此前 storage 只有 LIMIT，前端请求第 301 条
// 以前的记录不可达）指定跳过最新的前 offset 条做服务端分页；不传则为 0，
// 既有的调用点行为不变。
func (s *Store) ListRuns(accountID int64, limit int, offset ...int) ([]Run, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	off := 0
	if len(offset) > 0 && offset[0] > 0 {
		off = offset[0]
	}
	q := `SELECT ` + runCols + ` FROM runs r LEFT JOIN accounts a ON a.id = r.account_id`
	args := []any{}
	if accountID > 0 {
		q += ` WHERE r.account_id = ?`
		args = append(args, accountID)
	}
	q += ` ORDER BY r.id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, off)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// RunsSince returns statistical run facts overlapping the time from since,
// oldest first. Purged rows carry only the fields retained in run_statistics;
// use GetRun or ListRuns when full run details are needed.
func (s *Store) RunsSince(since int64) ([]Run, error) {
	rows, err := s.db.Query(`SELECT `+runCols+` FROM runs r
		LEFT JOIN accounts a ON a.id = r.account_id
		WHERE MAX(r.finished_at, r.started_at + r.elapsed_seconds) > ?
		UNION ALL
		SELECT h.run_id, h.account_id, COALESCE(a.name, ''), '', 0,
			h.elapsed_seconds, 0, 0, '', '', h.started_at, h.finished_at
		FROM run_statistics h LEFT JOIN accounts a ON a.id = h.account_id
		WHERE MAX(h.finished_at, h.started_at + h.elapsed_seconds) > ?
		ORDER BY 11 ASC, 1 ASC`, since, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// PurgeHistory deletes finished runs older than retainDays, together with
// their run_logs rows. Runs that are still running (or failed to finish) are
// never touched. Returns the number of runs deleted.
func (s *Store) PurgeHistory(retainDays int) (int64, error) {
	if retainDays <= 0 {
		return 0, nil
	}
	cutoff := time.Now().AddDate(0, 0, -retainDays).Unix()
	return s.PurgeHistoryBefore(cutoff)
}

// PurgeHistoryBefore deletes finished runs with finished_at < cutoff.
// Exposed as-is so tests can pin the cutoff.
func (s *Store) PurgeHistoryBefore(cutoff int64) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO run_statistics
		(run_id, account_id, started_at, finished_at, elapsed_seconds)
		SELECT id, account_id, started_at, finished_at, elapsed_seconds
		FROM runs WHERE finished_at > 0 AND finished_at < ?`, cutoff); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`DELETE FROM run_logs WHERE run_id IN
		(SELECT id FROM runs WHERE finished_at > 0 AND finished_at < ?)`, cutoff); err != nil {
		return 0, err
	}
	res, err := tx.Exec(`DELETE FROM runs WHERE finished_at > 0 AND finished_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

// errRestartInterrupted 一条遗留 running 行被打断时的 error 文案：措辞必须
// 让用户一眼看出是「外部中断（升级/重启）且已自动续跑」，而不是程序错误
// ——status 也因此用 'stopped' 与真正失败的 'failed' 区分开。
const errRestartInterrupted = "程序重启中断（如升级/手动重启），剩余时间已自动续跑"

// InterruptedRun 描述一条在启动时被「程序重启」打断的运行：引擎用它找到
// 原记录并自动续跑（读剩余目标，账号按最新一条去重）。
type InterruptedRun struct {
	RunID          int64
	AccountID      int64
	AccountName    string
	ElapsedSeconds int
}

// humanElapsed 把秒数折算成人读的「X 分钟」文案（约数，与引擎 humanMinutes
// 口径一致：不足 10 分钟保留一位小数，更大的值取整）。
func humanElapsed(seconds int) string {
	if seconds < 0 {
		seconds = 0
	}
	m := float64(seconds) / 60
	if m >= 10 {
		return fmt.Sprintf("%.0f 分钟", m)
	}
	return fmt.Sprintf("%.1f 分钟", m)
}

// finished_at 取值语义（缺陷 07）：优先用 last_progress_at——最后一次心跳
// 落库时刻，是重启前最后已知的活动时刻，也是最接近真实活动时段边界的值；
// 该列为 0（老数据或从未写进度）时退回 started_at+elapsed_seconds 近似
// （成功心跳覆盖的秒数与时钟跨度大体一致）。两者都比把「重启时刻」写进
// finished_at 好：后者把整段跨日停机时间塞进阅读跨度，SplitRunSeconds 会
// 把停机前一天的已读秒数均摊到次日的停机时段上。SplitRunSeconds 的线性
// 均摊逻辑不需要改，finished_at 修正后自然按活动时段归属。
//
// 打断语义（缺陷 07 的延伸）：后台 goroutine 不跨进程存活，遗留的 running
// 行是被「外部中断」（升级/手动重启）打断，不是程序错误——status 置
// 'stopped'（与真正失败的 'failed' 区分）、error 写明已自动续跑，并逐条
// AddLog 一条 warn 提醒阅读被切断了多久、剩余目标如何跟进。单事务里先
// SELECT 后按 id 精确 UPDATE，保证「读到的就是要改的」，不会误伤并发
// 新建的 running 行。
func (s *Store) InterruptRunningRuns() ([]InterruptedRun, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rows, err := tx.Query(`SELECT r.id, r.account_id, COALESCE(a.name, ''), r.elapsed_seconds
		FROM runs r LEFT JOIN accounts a ON a.id = r.account_id
		WHERE r.status = 'running' ORDER BY r.id DESC`)
	if err != nil {
		return nil, err
	}
	var list []InterruptedRun
	for rows.Next() {
		var ir InterruptedRun
		if err := rows.Scan(&ir.RunID, &ir.AccountID, &ir.AccountName, &ir.ElapsedSeconds); err != nil {
			rows.Close()
			return nil, err
		}
		list = append(list, ir)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	if len(list) == 0 {
		return nil, nil
	}

	q := `UPDATE runs SET status = 'stopped', error = ?, finished_at = MAX(started_at, CASE
		WHEN last_progress_at > 0 THEN last_progress_at
		ELSE started_at + elapsed_seconds
		END)
		WHERE id IN (` + strings.Repeat("?,", len(list)-1) + "?)"
	args := make([]any, 0, len(list)+1)
	args = append(args, errRestartInterrupted)
	for _, ir := range list {
		args = append(args, ir.RunID)
	}
	if _, err := tx.Exec(q, args...); err != nil {
		return nil, err
	}

	// 打断原因写进每条被打断运行的 logs（所在事务一致落库）。单条日志
	// 写失败不回滚中断标记本身——状态与 error 已经说明因果，剩余时间由
	// 引擎侧 ResumeInterrupted 继续跟进，日志缺失不改变处置。
	for _, ir := range list {
		if _, err := tx.Exec(`INSERT INTO run_logs (run_id, ts, level, message) VALUES (?,?,?,?)`,
			ir.RunID, now(), "warn",
			fmt.Sprintf("程序重启中断：本次已读约 %s，剩余时间已安排自动续跑", humanElapsed(ir.ElapsedSeconds))); err != nil {
			continue
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return list, nil
}

// AddLog appends one log line and returns its id.
func (s *Store) AddLog(runID int64, ts int64, level, message string) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO run_logs (run_id, ts, level, message) VALUES (?,?,?,?)`,
		runID, ts, level, message)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListLogs returns logs for a run; afterID enables incremental fetches.
func (s *Store) ListLogs(runID, afterID int64, limit int) ([]RunLog, error) {
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	q := `SELECT id, run_id, ts, level, message FROM run_logs WHERE run_id = ?`
	args := []any{runID}
	if afterID > 0 {
		q += ` AND id > ?`
		args = append(args, afterID)
	}
	q += ` ORDER BY id ASC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("查询日志失败: %w", err)
	}
	defer rows.Close()
	out := []RunLog{}
	for rows.Next() {
		var l RunLog
		if err := rows.Scan(&l.ID, &l.RunID, &l.TS, &l.Level, &l.Message); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// RecentLogs returns the newest logs for a run in chronological order.
func (s *Store) RecentLogs(runID int64, limit int) ([]RunLog, error) {
	if limit <= 0 || limit > 2000 {
		limit = 20
	}
	rows, err := s.db.Query(`SELECT id, run_id, ts, level, message FROM run_logs
		WHERE run_id = ? ORDER BY id DESC LIMIT ?`, runID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]RunLog, 0, limit)
	for rows.Next() {
		var l RunLog
		if err := rows.Scan(&l.ID, &l.RunID, &l.TS, &l.Level, &l.Message); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}
