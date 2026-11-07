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

// PurgeSessions removes expired sessions.
func (s *Store) PurgeSessions() error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at < ?`, time.Now().Unix())
	return err
}
