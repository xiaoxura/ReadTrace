package store

import "database/sql"

// AccountBridge finds an account by WeRead vid.
func (s *Store) FindAccountByVid(vid string) (*Account, error) {
	if vid == "" {
		return nil, nil
	}
	a, err := scanAccount(s.db.QueryRow(`SELECT `+accountCols+` FROM accounts WHERE vid = ? ORDER BY id ASC LIMIT 1`, vid))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return a, err
}

// Totals aggregates lifetime reading volume.
type Totals struct {
	Runs    int64 `json:"runs"`
	Seconds int64 `json:"seconds"`
}

// Totals returns run count and total elapsed seconds.
func (s *Store) Totals() (Totals, error) {
	var t Totals
	err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(elapsed_seconds), 0) FROM runs`).Scan(&t.Runs, &t.Seconds)
	return t, err
}

// TotalsForAccount aggregates one account's lifetime reading volume.
func (s *Store) TotalsForAccount(accountID int64) (Totals, error) {
	var t Totals
	err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(elapsed_seconds), 0) FROM runs WHERE account_id = ?`,
		accountID).Scan(&t.Runs, &t.Seconds)
	return t, err
}

// AccountStat is per-account reading volume since a timestamp.
type AccountStat struct {
	AccountID int64  `json:"account_id"`
	Name      string `json:"name"`
	Seconds   int64  `json:"seconds"`
}

// StatsByAccount groups run duration by account since a timestamp.
func (s *Store) StatsByAccount(since int64) ([]AccountStat, error) {
	rows, err := s.db.Query(`SELECT r.account_id, COALESCE(a.name, ''), COALESCE(SUM(r.elapsed_seconds), 0)
		FROM runs r LEFT JOIN accounts a ON a.id = r.account_id
		WHERE r.started_at >= ? GROUP BY r.account_id ORDER BY 3 DESC`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AccountStat{}
	for rows.Next() {
		var v AccountStat
		if err := rows.Scan(&v.AccountID, &v.Name, &v.Seconds); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
