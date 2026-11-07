package store

import (
	"database/sql"
)

const scheduleCols = `s.id, s.account_id, COALESCE(a.name, ''), s.cron, s.minutes, s.enabled,
	s.last_run_at, s.next_run_at, s.created_at, s.updated_at`

func scanSchedule(sc interface{ Scan(...any) error }) (*Schedule, error) {
	var v Schedule
	var enabled int
	if err := sc.Scan(&v.ID, &v.AccountID, &v.AccountName, &v.Cron, &v.Minutes, &enabled,
		&v.LastRunAt, &v.NextRunAt, &v.CreatedAt, &v.UpdatedAt); err != nil {
		return nil, err
	}
	v.Enabled = enabled == 1
	return &v, nil
}

// CreateSchedule inserts a schedule.
func (s *Store) CreateSchedule(v *Schedule) error {
	ts := now()
	en := 0
	if v.Enabled {
		en = 1
	}
	res, err := s.db.Exec(`INSERT INTO schedules
		(account_id, cron, minutes, enabled, last_run_at, next_run_at, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?)`,
		v.AccountID, v.Cron, v.Minutes, en, v.LastRunAt, v.NextRunAt, ts, ts)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	v.ID, v.CreatedAt, v.UpdatedAt = id, ts, ts
	return nil
}

// UpdateSchedule updates the cron/minutes/enabled fields.
func (s *Store) UpdateSchedule(v *Schedule) error {
	en := 0
	if v.Enabled {
		en = 1
	}
	_, err := s.db.Exec(`UPDATE schedules SET cron = ?, minutes = ?, enabled = ?,
		next_run_at = ?, updated_at = ? WHERE id = ?`,
		v.Cron, v.Minutes, en, v.NextRunAt, now(), v.ID)
	return err
}

// SetScheduleEnabled toggles a schedule.
func (s *Store) SetScheduleEnabled(id int64, enabled bool) error {
	en := 0
	if enabled {
		en = 1
	}
	_, err := s.db.Exec(`UPDATE schedules SET enabled = ?, updated_at = ? WHERE id = ?`, en, now(), id)
	return err
}

// SetScheduleTimes stores the last/next fire timestamps.
func (s *Store) SetScheduleTimes(id, last, next int64) error {
	_, err := s.db.Exec(`UPDATE schedules SET last_run_at = ?, next_run_at = ? WHERE id = ?`, last, next, id)
	return err
}

// GetSchedule loads one schedule.
func (s *Store) GetSchedule(id int64) (*Schedule, error) {
	v, err := scanSchedule(s.db.QueryRow(`SELECT `+scheduleCols+` FROM schedules s
		LEFT JOIN accounts a ON a.id = s.account_id WHERE s.id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return v, err
}

// ListSchedules returns all schedules ordered by id.
func (s *Store) ListSchedules() ([]Schedule, error) {
	rows, err := s.db.Query(`SELECT ` + scheduleCols + ` FROM schedules s
		LEFT JOIN accounts a ON a.id = s.account_id ORDER BY s.id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Schedule{}
	for rows.Next() {
		v, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, rows.Err()
}

// EnabledSchedules returns schedules that should be registered with cron.
func (s *Store) EnabledSchedules() ([]Schedule, error) {
	rows, err := s.db.Query(`SELECT ` + scheduleCols + ` FROM schedules s
		LEFT JOIN accounts a ON a.id = s.account_id WHERE s.enabled = 1 ORDER BY s.id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Schedule{}
	for rows.Next() {
		v, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, rows.Err()
}

// DeleteSchedule removes a schedule.
func (s *Store) DeleteSchedule(id int64) error {
	_, err := s.db.Exec(`DELETE FROM schedules WHERE id = ?`, id)
	return err
}
