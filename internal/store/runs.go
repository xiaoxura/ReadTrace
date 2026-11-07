package store

import (
	"database/sql"
	"fmt"
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

// UpdateRunProgress persists a tick's progress.
func (s *Store) UpdateRunProgress(id int64, elapsed, success, failed int) error {
	_, err := s.db.Exec(`UPDATE runs SET elapsed_seconds = ?, success_ticks = ?, failed_ticks = ? WHERE id = ?`,
		elapsed, success, failed, id)
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

// ListRuns returns the newest runs, optionally filtered by account.
func (s *Store) ListRuns(accountID int64, limit int) ([]Run, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT ` + runCols + ` FROM runs r LEFT JOIN accounts a ON a.id = r.account_id`
	args := []any{}
	if accountID > 0 {
		q += ` WHERE r.account_id = ?`
		args = append(args, accountID)
	}
	q += ` ORDER BY r.id DESC LIMIT ?`
	args = append(args, limit)

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

// RunsSince returns runs started after the timestamp, oldest first.
func (s *Store) RunsSince(since int64) ([]Run, error) {
	rows, err := s.db.Query(`SELECT `+runCols+` FROM runs r
		LEFT JOIN accounts a ON a.id = r.account_id
		WHERE r.started_at >= ? ORDER BY r.started_at ASC`, since)
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

// InterruptRunningRuns marks leftover running rows as interrupted (called on
// startup because background goroutines do not survive a restart).
func (s *Store) InterruptRunningRuns() error {
	_, err := s.db.Exec(`UPDATE runs SET status = 'failed', error = ?, finished_at = ?
		WHERE status = 'running'`, "服务重启，本次运行已中断", now())
	return err
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
