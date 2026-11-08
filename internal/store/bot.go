// Package bot 数据层：微信机器人登录凭据、用户与账号绑定关系。
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// BotUser is a WeChat user that has talked to the bot at least once. Admin
// rights are granted from the dashboard, never self-claimed.
type BotUser struct {
	UserID    string `json:"user_id"`
	IsAdmin   bool   `json:"is_admin"`
	FirstSeen int64  `json:"first_seen"`
	LastSeen  int64  `json:"last_seen"`
}

// BotBinding links one WeChat user to one WeRead account it may operate.
// One account has exactly ONE owner WeChat user at a time (scan = ownership);
// one WeChat user may own many accounts. The dashboard admin can rebind.
type BotBinding struct {
	ID          int64  `json:"id"`
	UserID      string `json:"user_id"`
	AccountID   int64  `json:"account_id"`
	AccountName string `json:"account_name,omitempty"`
	CreatedAt   int64  `json:"created_at"`
}

// BotLogin is the bot's own WeChat-side credential (iLink bot token).
type BotLogin struct {
	BotID     string `json:"bot_id"`
	UserID    string `json:"user_id"`
	BaseURL   string `json:"base_url"`
	HasToken  bool   `json:"has_token"`
	UpdatedAt int64  `json:"updated_at"`
}

// BotUpdatesBuf is the durable long-poll cursor for the bot monitor.
func (s *Store) BotUpdatesBuf() (string, error) {
	var buf []byte
	err := s.db.QueryRow(`SELECT get_updates_buf FROM bot_logins ORDER BY id DESC LIMIT 1`).Scan(&buf)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(buf), nil
}

// SaveBotUpdatesBuf persists the long-poll cursor after each poll window.
func (s *Store) SaveBotUpdatesBuf(buf string) error {
	_, err := s.db.Exec(`UPDATE bot_logins SET get_updates_buf = ?`, []byte(buf))
	return err
}

// GetBotLogin returns the latest stored bot login, or nil when unbound.
func (s *Store) GetBotLogin() (*BotLogin, error) {
	row := s.db.QueryRow(`SELECT bot_id, user_id, base_url, token_enc, updated_at
		FROM bot_logins ORDER BY id DESC LIMIT 1`)
	var v BotLogin
	var tokenEnc []byte
	err := row.Scan(&v.BotID, &v.UserID, &v.BaseURL, &tokenEnc, &v.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if s.box != nil {
		plain, err := s.box.Open(tokenEnc)
		if err != nil {
			return nil, fmt.Errorf("解密机器人凭据失败: %w", err)
		}
		var tok string
		if err := json.Unmarshal(plain, &tok); err == nil {
			plain = []byte(tok)
		}
		v.HasToken = strings.TrimSpace(string(plain)) != ""
	}
	if v.BaseURL == "" {
		v.BaseURL = "https://ilinkai.weixin.qq.com"
	}
	return &v, nil
}

// BotToken decrypts the stored iLink bot token.
func (s *Store) BotToken() (string, error) {
	var tokenEnc []byte
	var updated int64
	err := s.db.QueryRow(`SELECT token_enc, updated_at FROM bot_logins ORDER BY id DESC LIMIT 1`).Scan(&tokenEnc, &updated)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if len(tokenEnc) == 0 {
		return "", nil
	}
	plain, err := s.box.Open(tokenEnc)
	if err != nil {
		return "", fmt.Errorf("解密机器人凭据失败: %w", err)
	}
	var tok string
	if err := json.Unmarshal(plain, &tok); err == nil {
		plain = []byte(tok)
	}
	return string(plain), nil
}

// SaveBotLogin stores or replaces the bot credential (token encrypted).
// Empty token keeps the existing encrypted token so a re-poll can keep working.
func (s *Store) SaveBotLogin(botID, userID, token, baseURL string) error {
	existing, err := s.BotToken()
	if err != nil {
		existing = ""
	}
	if token == "" {
		token = existing
	}
	enc, err := s.sealJSON(token)
	if err != nil {
		return err
	}
	if enc == nil {
		enc = []byte{}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM bot_logins`); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO bot_logins (bot_id, user_id, token_enc, base_url, updated_at)
		VALUES (?,?,?,?,?)`, botID, userID, enc, baseURL, time.Now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

// ClearBotLogin removes the stored credential (不代表解绑用户关系).
func (s *Store) ClearBotLogin() error {
	_, err := s.db.Exec(`DELETE FROM bot_logins`)
	return err
}

// ---- users ----

// EnsureBotUser records that this WeChat user exists and just messaged.
func (s *Store) EnsureBotUser(userID string) error {
	if userID == "" {
		return fmt.Errorf("空用户")
	}
	ts := time.Now().Unix()
	_, err := s.db.Exec(`INSERT INTO bot_users (user_id, is_admin, first_seen, last_seen)
		VALUES (?, 0, ?, ?) ON CONFLICT(user_id) DO UPDATE SET last_seen = excluded.last_seen`,
		userID, ts, ts)
	return err
}

// GetBotUser returns one bot user or nil.
func (s *Store) GetBotUser(userID string) (*BotUser, error) {
	var v BotUser
	var admin int
	err := s.db.QueryRow(`SELECT user_id, is_admin, first_seen, last_seen FROM bot_users WHERE user_id = ?`, userID).
		Scan(&v.UserID, &admin, &v.FirstSeen, &v.LastSeen)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	v.IsAdmin = admin == 1
	return &v, nil
}

// ListBotUsers lists known WeChat users, newest activity first.
func (s *Store) ListBotUsers() ([]BotUser, error) {
	rows, err := s.db.Query(`SELECT user_id, is_admin, first_seen, last_seen FROM bot_users ORDER BY last_seen DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BotUser{}
	for rows.Next() {
		var v BotUser
		var admin int
		if err := rows.Scan(&v.UserID, &admin, &v.FirstSeen, &v.LastSeen); err != nil {
			return nil, err
		}
		v.IsAdmin = admin == 1
		out = append(out, v)
	}
	return out, rows.Err()
}

// SetBotUserAdmin grants or revokes dashboard admin rights in the bot.
func (s *Store) SetBotUserAdmin(userID string, admin bool) error {
	v := 0
	if admin {
		v = 1
	}
	_, err := s.db.Exec(`UPDATE bot_users SET is_admin = ? WHERE user_id = ?`, v, userID)
	return err
}

// DeleteBotUser removes the user and its bindings.
func (s *Store) DeleteBotUser(userID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM bot_bindings WHERE user_id = ?`, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM bot_users WHERE user_id = ?`, userID); err != nil {
		return err
	}
	return tx.Commit()
}

// ---- bindings ----

// BindUserAccount makes userID the single owner WeChat user of the account.
// Any previous owner binding for the account is removed first.
func (s *Store) BindUserAccount(userID string, accountID int64) (int64, error) {
	if userID == "" || accountID <= 0 {
		return 0, fmt.Errorf("无效绑定参数")
	}
	if _, err := s.GetAccount(accountID); err != nil {
		return 0, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM bot_bindings WHERE account_id = ?`, accountID); err != nil {
		return 0, err
	}
	res, err := tx.Exec(`INSERT OR IGNORE INTO bot_bindings (user_id, account_id, created_at) VALUES (?,?,?)`,
		userID, accountID, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	return id, nil
}

// UnbindUserAccount removes one binding; returns whether it was deleted.
func (s *Store) UnbindUserAccount(userID string, accountID int64) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM bot_bindings WHERE user_id = ? AND account_id = ?`, userID, accountID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// UnbindAccount removes every binding of an account (admin action / rebind).
func (s *Store) UnbindAccount(accountID int64) error {
	_, err := s.db.Exec(`DELETE FROM bot_bindings WHERE account_id = ?`, accountID)
	return err
}

// ListBotBindings lists bindings joined with account names.
func (s *Store) ListBotBindings() ([]BotBinding, error) {
	rows, err := s.db.Query(`SELECT b.id, b.user_id, b.account_id, COALESCE(a.name, ''), b.created_at
		FROM bot_bindings b LEFT JOIN accounts a ON a.id = b.account_id
		ORDER BY b.created_at ASC, b.id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BotBinding{}
	for rows.Next() {
		var v BotBinding
		if err := rows.Scan(&v.ID, &v.UserID, &v.AccountID, &v.AccountName, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// UserBoundAccounts returns accounts owned by the given WeChat user.
func (s *Store) UserBoundAccounts(userID string) ([]Account, error) {
	users, err := s.UserBoundAccountIDs(userID)
	if err != nil {
		return nil, err
	}
	out := []Account{}
	for _, id := range users {
		acc, err := s.GetAccount(id)
		if err != nil {
			return nil, err
		}
		if acc != nil {
			out = append(out, *acc)
		}
	}
	return out, nil
}

// UserBoundAccountIDs returns the account IDs owned by the WeChat user.
func (s *Store) UserBoundAccountIDs(userID string) ([]int64, error) {
	rows, err := s.db.Query(`SELECT account_id FROM bot_bindings WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ---- 消息记录（面板「微信」页对话视图） ----

// BotMessage 是一条用户↔机器人的往来消息。
type BotMessage struct {
	ID        int64  `json:"id"`
	UserID    string `json:"user_id"`
	Direction string `json:"direction"` // in = 用户发出, out = 机器人回复
	Kind      string `json:"kind"`      // text | image | media
	Content   string `json:"content"`
	MsgRef    string `json:"msg_ref,omitempty"`
	TS        int64  `json:"ts"`
}

// RecordBotMessage 落一条对话记录；记录失败只影响面板展示，不阻断收发。
// 返回新行 ID（0 表示空内容被跳过），供 SSE 增量推送去重。
func (s *Store) RecordBotMessage(userID, direction, kind, content, msgRef string) (int64, error) {
	if userID == "" || content == "" {
		return 0, nil
	}
	res, err := s.db.Exec(`INSERT INTO bot_messages (user_id, direction, kind, content, msg_ref, ts)
		VALUES (?,?,?,?,?,?)`, userID, direction, kind, content, msgRef, now())
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	return id, nil
}

// ListBotMessages 分页拉取某用户的最近对话，按 id 倒序返回（调用方负责反转展示）。
// beforeID=0 表示从最新开始。
func (s *Store) ListBotMessages(userID string, beforeID int64, limit int) ([]BotMessage, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := `SELECT id, user_id, direction, kind, content, msg_ref, ts FROM bot_messages WHERE user_id = ?`
	args := []any{userID}
	if beforeID > 0 {
		q += ` AND id < ?`
		args = append(args, beforeID)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BotMessage{}
	for rows.Next() {
		var v BotMessage
		if err := rows.Scan(&v.ID, &v.UserID, &v.Direction, &v.Kind, &v.Content, &v.MsgRef, &v.TS); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// AccountOwner returns the WeChat user that owns the account, or "".
func (s *Store) AccountOwner(accountID int64) (string, error) {
	var userID string
	err := s.db.QueryRow(`SELECT user_id FROM bot_bindings WHERE account_id = ? LIMIT 1`, accountID).Scan(&userID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return userID, err
}
