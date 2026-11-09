package store

import (
	"database/sql"
	"time"
)

// GetSetting reads one key; missing keys return an empty string.
func (s *Store) GetSetting(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

// SetSetting upserts one key.
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// SetSettings commits a group of settings together, so a failed write cannot
// leave a partially applied configuration.
func (s *Store) SetSettings(values map[string]string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for key, value := range values {
		if _, err := tx.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SetSettingIfEmpty sets a value only when it is absent or empty. The single
// SQLite statement makes first-time setup safe across concurrent requests.
func (s *Store) SetSettingIfEmpty(key, value string) (bool, error) {
	res, err := s.db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value WHERE settings.value = ''`, key, value)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// AllSettings returns every setting.
func (s *Store) AllSettings() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// CreateSession stores a panel session.
func (s *Store) CreateSession(id, ip, ua string, ttl time.Duration) error {
	now := time.Now().Unix()
	_, err := s.db.Exec(`INSERT INTO sessions (id, created_at, expires_at, ip, ua) VALUES (?, ?, ?, ?, ?)`,
		id, now, now+int64(ttl.Seconds()), ip, ua)
	return err
}

// GetSession returns a live session, or nil when absent/expired.
func (s *Store) GetSession(id string) (*Session, error) {
	var ses Session
	err := s.db.QueryRow(`SELECT id, created_at, expires_at, ip, ua FROM sessions WHERE id = ?`, id).
		Scan(&ses.ID, &ses.CreatedAt, &ses.ExpiresAt, &ses.IP, &ses.UA)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if ses.ExpiresAt < time.Now().Unix() {
		_ = s.DeleteSession(id)
		return nil, nil
	}
	return &ses, nil
}

// ExtendSession pushes the expiry forward for a live session. Used for
// rolling TTLs so active users are not logged out after 30 days.
func (s *Store) ExtendSession(id string, ttl time.Duration) error {
	until := time.Now().Add(ttl).Unix()
	_, err := s.db.Exec(`UPDATE sessions SET expires_at = ? WHERE id = ? AND expires_at > ?`,
		until, id, time.Now().Unix())
	return err
}

// DeleteSession removes one session.
func (s *Store) DeleteSession(id string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE id = ?`, id)
	return err
}

// DeleteOtherSessions keeps only the given session (password change).
func (s *Store) DeleteOtherSessions(keep string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE id <> ?`, keep)
	return err
}

// ChangePassword replaces the expected hash and revokes other sessions in one
// transaction. A stale password change cannot overwrite a newer one.
func (s *Store) ChangePassword(expected, next, keepSession string) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE settings SET value = ? WHERE key = ? AND value = ?`, next, "password_hash", expected)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil || n != 1 {
		return false, err
	}
	if _, err := tx.Exec(`DELETE FROM sessions WHERE id <> ?`, keepSession); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// PurgeSessions removes expired sessions.
func (s *Store) PurgeSessions() error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at < ?`, time.Now().Unix())
	return err
}
