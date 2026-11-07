package weread

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// ErrLoginSessionExpired means the QR uid is no longer valid for polling.
var ErrLoginSessionExpired = errors.New("扫码会话已失效")

// LoginPoll is a single result of the QR login long-poll.
type LoginPoll struct {
	Success     bool    `json:"succeed"`
	LogicCode   string  `json:"logicCode"`
	AccessToken string  `json:"accessToken"`
	Vid         string  `json:"webLoginVid"`
	ErrMsg      string  `json:"errMsg"`
	Cookies     Cookies `json:"-"`
	Keys        []string
}

// ConfirmURL is the URL encoded into the QR code.
func ConfirmURL(uid string) string {
	return BaseURL + "/web/confirm?uid=" + url.QueryEscape(uid)
}

// LoginUID asks WeRead for a fresh QR login uid.
func (c *Client) LoginUID(ctx context.Context) (string, error) {
	var out struct {
		UID string `json:"uid"`
	}
	if _, err := c.doJSON(ctx, http.MethodGet, "/api/auth/getLoginUid", nil, nil, &out); err != nil {
		return "", err
	}
	if out.UID == "" {
		return "", errors.New("未能获取登录二维码，请稍后重试")
	}
	return out.UID, nil
}

// LoginPoll performs the long-poll for a scanned QR code. It blocks on the
// server side for up to ~55 seconds before returning LOGIN_TIMEOUT.
func (c *Client) LoginPoll(ctx context.Context, uid, otp string) (*LoginPoll, error) {
	ctx, cancel := context.WithTimeout(ctx, 70*time.Second)
	defer cancel()

	q := url.Values{}
	q.Set("uid", uid)
	q.Set("otp", otp)

	req, err := c.newRequest(ctx, http.MethodGet, "/api/auth/getLoginInfo?"+q.Encode(), nil, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}

	// The login endpoints hand out wr_vid / wr_skey via Set-Cookie. Capture
	// them before status checks so they are available even on odd responses.
	cookies := Cookies{}
	for _, rc := range resp.Cookies() {
		if rc.Name != "" && rc.Value != "" {
			cookies[rc.Name] = rc.Value
		}
	}

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, ErrLoginSessionExpired
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, snippet(data))
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		if isHTML(data) {
			return nil, ErrRiskControl
		}
		return nil, fmt.Errorf("解析登录响应失败: %w", err)
	}

	// The endpoint occasionally wraps the payload in a "data" object.
	if d, ok := raw["data"].(map[string]any); ok {
		for k, v := range d {
			if _, exists := raw[k]; !exists {
				raw[k] = v
			}
		}
	}

	out := &LoginPoll{Cookies: cookies}
	rawBytes, _ := json.Marshal(raw)
	_ = json.Unmarshal(rawBytes, out)
	out.LogicCode = pickString(raw, "logicCode", "logic_code")
	if out.LogicCode == "" {
		out.LogicCode = strings.TrimSpace(fmt.Sprint(raw["logicCode"]))
	}
	out.Success = toInt(raw["succeed"]) == 1 || strings.EqualFold(out.LogicCode, "LOGIN_SUCCESS")
	if out.Vid == "" {
		out.Vid = findScalar(raw, "webLoginVid", "vid", "web_login_vid", "userVid")
	}
	if out.Vid == "" {
		out.Vid = cookies["wr_vid"]
	}
	if out.AccessToken == "" {
		out.AccessToken = findScalar(raw, "accessToken", "access_token", "skey")
	}
	if out.ErrMsg == "" {
		out.ErrMsg = pickString(raw, "errMsg", "msg")
	}

	out.Keys = make([]string, 0, len(raw))
	for k := range raw {
		out.Keys = append(out.Keys, k)
	}
	sort.Strings(out.Keys)

	if out.Success && out.AccessToken == "" && out.Vid == "" && len(cookies) == 0 {
		return out, fmt.Errorf("登录成功但未返回完整凭据")
	}
	return out, nil
}
