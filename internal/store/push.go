package store

import "database/sql"

const pushCols = `id, name, type, config_enc, enabled, created_at`

func (s *Store) scanPush(sc interface{ Scan(...any) error }) (*PushChannel, error) {
	var v PushChannel
	var enc []byte
	var enabled int
	if err := sc.Scan(&v.ID, &v.Name, &v.Type, &enc, &enabled, &v.CreatedAt); err != nil {
		return nil, err
	}
	v.Enabled = enabled == 1
	v.Config = map[string]string{}
	if len(enc) > 0 {
		if err := s.openJSON(enc, &v.Config); err != nil {
			return nil, err
		}
	}
	return &v, nil
}

// CreatePushChannel inserts a notification channel.
func (s *Store) CreatePushChannel(v *PushChannel) error {
	cfg, err := s.sealJSON(v.Config)
	if err != nil {
		return err
	}
	en := 0
	if v.Enabled {
		en = 1
	}
	res, err := s.db.Exec(`INSERT INTO push_channels (name, type, config_enc, enabled, created_at)
		VALUES (?,?,?,?,?)`, v.Name, v.Type, cfg, en, now())
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	v.ID = id
	return nil
}

// UpdatePushChannel updates name/type/config/enabled.
func (s *Store) UpdatePushChannel(v *PushChannel) error {
	cfg, err := s.sealJSON(v.Config)
	if err != nil {
		return err
	}
	en := 0
	if v.Enabled {
		en = 1
	}
	_, err = s.db.Exec(`UPDATE push_channels SET name = ?, type = ?, config_enc = ?, enabled = ? WHERE id = ?`,
		v.Name, v.Type, cfg, en, v.ID)
	return err
}

// ListPushChannels returns all channels.
func (s *Store) ListPushChannels() ([]PushChannel, error) {
	rows, err := s.db.Query(`SELECT ` + pushCols + ` FROM push_channels ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PushChannel{}
	for rows.Next() {
		v, err := s.scanPush(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, rows.Err()
}

// GetPushChannel loads one channel.
func (s *Store) GetPushChannel(id int64) (*PushChannel, error) {
	v, err := s.scanPush(s.db.QueryRow(`SELECT `+pushCols+` FROM push_channels WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return v, err
}

// DeletePushChannel removes a channel.
func (s *Store) DeletePushChannel(id int64) error {
	_, err := s.db.Exec(`DELETE FROM push_channels WHERE id = ?`, id)
	return err
}
