package weread

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// LoginPoll is a single result of the QR login long-poll.
type LoginPoll struct {
	Success     bool   `json:"succeed"`
	LogicCode   string `json:"logicCode"`
	AccessToken string `json:"accessToken"`
	Vid         string `json:"webLoginVid"`
	ErrMsg      string `json:"errMsg"`
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
// server side for up to ~60 seconds before returning LOGIN_TIMEOUT.
func (c *Client) LoginPoll(ctx context.Context, uid, otp string) (*LoginPoll, error) {
	ctx, cancel := context.WithTimeout(ctx, 70*time.Second)
	defer cancel()

	q := url.Values{}
	q.Set("uid", uid)
	q.Set("otp", otp)

	var raw map[string]any
	if _, err := c.doJSON(ctx, http.MethodGet, "/api/auth/getLoginInfo?"+q.Encode(), nil, nil, &raw); err != nil {
		return nil, err
	}

	// The endpoint occasionally wraps the payload in a "data" object.
	if d, ok := raw["data"].(map[string]any); ok {
		for k, v := range d {
			if _, exists := raw[k]; !exists {
				raw[k] = v
			}
		}
	}

	out := &LoginPoll{}
	rawBytes, _ := json.Marshal(raw)
	_ = json.Unmarshal(rawBytes, out)
	out.Success = toInt(raw["succeed"]) == 1 || out.AccessToken != "" && out.Vid != ""
	if s, ok := raw["logicCode"].(string); ok {
		out.LogicCode = s
	}
	if out.LogicCode == "" {
		if s, ok := raw["logic_code"].(string); ok {
			out.LogicCode = s
		}
	}
	if out.Vid == "" {
		out.Vid = findString(raw, "webLoginVid", "vid", "web_login_vid")
	}
	if out.AccessToken == "" {
		out.AccessToken = findString(raw, "accessToken", "access_token", "skey")
	}
	if out.ErrMsg == "" {
		out.ErrMsg = pickString(raw, "errMsg", "msg")
	}
	if out.Success && (out.Vid == "" || out.AccessToken == "") {
		return out, fmt.Errorf("登录成功但未返回完整凭据")
	}
	return out, nil
}
