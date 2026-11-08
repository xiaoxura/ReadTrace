package weread

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// BaseURL is the WeRead web origin.
const BaseURL = "https://weread.qq.com"

// DefaultUA keeps requests indistinguishable from a regular desktop browser.
const DefaultUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

// ErrRiskControl is returned when the API answers with an HTML page instead of
// JSON, which usually means the session tripped a bot check.
var ErrRiskControl = errors.New("接口返回了 HTML 页面，可能触发了风控或登录已失效")

// ErrUnauthorized is returned when the session is no longer valid
// (errCode -2012/-2013/401). Callers check errors.Is to distinguish true
// login expiry from transient network/API failures.
var ErrUnauthorized = errors.New("登录状态异常")

func isAuthCode(code int) bool {
	return code == -2012 || code == -2013 || code == 401
}

// Client is a small WeRead web API client.
type Client struct {
	HTTP *http.Client
	Base string
	UA   string
}

// NewClient builds a client with a browser-like HTTP transport.
func NewClient() *Client {
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 70 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	return &Client{
		HTTP: &http.Client{Timeout: 90 * time.Second, Transport: tr},
		Base: BaseURL,
		UA:   DefaultUA,
	}
}

// ReadResult is the decoded /web/book/read response. The API returns either
// {"succ":1,"synckey":...} on success or {"errCode":...,"errMsg":...}.
type ReadResult struct {
	Succ       bool
	HasSyncKey bool
	SyncKey    int64
	ErrCode    int
	ErrMsg     string
	Raw        map[string]any
}

func (c *Client) newRequest(ctx context.Context, method, path string, ck Cookies, body any) (*http.Request, error) {
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("accept", "application/json, text/plain, */*")
	req.Header.Set("accept-language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("user-agent", c.UA)
	req.Header.Set("referer", c.Base+"/")
	req.Header.Set("origin", c.Base)
	if body != nil {
		req.Header.Set("content-type", "application/json")
	}
	for k, v := range ck {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	return req, nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, ck Cookies, body, out any) (*http.Response, error) {
	req, err := c.newRequest(ctx, method, path, ck, body)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return resp, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp, fmt.Errorf("HTTP %d: %s", resp.StatusCode, snippet(data))
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			if isHTML(data) {
				return resp, ErrRiskControl
			}
			return resp, fmt.Errorf("解析响应失败: %w", err)
		}
	}
	return resp, nil
}

func isHTML(b []byte) bool {
	s := strings.TrimSpace(string(b))
	return strings.HasPrefix(s, "<!DOCTYPE") || strings.HasPrefix(s, "<html") || strings.HasPrefix(s, "<")
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// Read sends one reading heartbeat.
func (c *Client) Read(ctx context.Context, ck Cookies, payload map[string]any) (*ReadResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()

	req, err := c.newRequest(ctx, http.MethodPost, "/web/book/read", ck, payload)
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
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		if isHTML(data) {
			return nil, ErrRiskControl
		}
		return nil, fmt.Errorf("read 接口返回非 JSON（HTTP %d）：%s", resp.StatusCode, snippet(data))
	}

	res := &ReadResult{Raw: raw}
	res.Succ = toInt(raw["succ"]) == 1
	if v, ok := raw["synckey"]; ok {
		res.HasSyncKey = true
		res.SyncKey = toInt64(v)
	}
	res.ErrCode = toInt(raw["errCode"])
	if s, ok := raw["errMsg"].(string); ok {
		res.ErrMsg = s
	}
	return res, nil
}

// Renew refreshes the wr_skey cookie via /web/login/renewal. The endpoint
// accepts a few different payload shapes, so all known variants are tried.
// New cookies are captured even from non-2xx responses: the endpoint
// occasionally answers with an unusual status code while still handing out a
// fresh session key.
func (c *Client) Renew(ctx context.Context, ck Cookies) (string, error) {
	variants := []map[string]any{
		{"rq": "/web/book/read", "ql": false},
		{"rq": "/web/book/read", "ql": true},
		{"rq": "/web/book/read"},
	}
	var lastErr error
	for _, body := range variants {
		req, err := c.newRequest(ctx, http.MethodPost, "/web/login/renewal", ck, body)
		if err != nil {
			lastErr = err
			continue
		}
		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		for _, rc := range resp.Cookies() {
			if rc.Name == "wr_skey" && rc.Value != "" {
				_ = resp.Body.Close()
				ck["wr_skey"] = rc.Value
				return rc.Value, nil
			}
		}
		_, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			continue
		}
		lastErr = errors.New("renewal 未返回新的 wr_skey")
	}
	if lastErr == nil {
		lastErr = errors.New("renewal 未返回新的 wr_skey")
	}
	return "", lastErr
}

// FixSyncKey asks the chapterInfos endpoint to re-bind the sync state.
func (c *Client) FixSyncKey(ctx context.Context, ck Cookies, bookID string) error {
	if bookID == "" {
		bookID = "3300060341"
	}
	body := map[string]any{"bookIds": []string{bookID}}
	_, err := c.doJSON(ctx, http.MethodPost, "/web/book/chapterInfos", ck, body, nil)
	return err
}

// Validate checks whether the session is still alive. True login failures wrap
// ErrUnauthorized (check with errors.Is); everything else — network trouble,
// odd API codes — is transient and must not expire the account.
func (c *Client) Validate(ctx context.Context, ck Cookies) error {
	var raw map[string]any
	if _, err := c.doJSON(ctx, http.MethodGet, "/web/user", ck, nil, &raw); err != nil {
		return err
	}
	if code := toInt(raw["errCode"]); code != 0 {
		if isAuthCode(code) {
			return fmt.Errorf("%w（errCode=%d）%s", ErrUnauthorized, code, apiErrText(raw, code))
		}
		return fmt.Errorf("%s（errCode=%d）", apiErrText(raw, code), code)
	}
	return nil
}

// apiErrText renders a server-provided message when available, else a short
// translation for codes the community has already pinned down (-2003
// 参数格式错误, -2014 限频, -2041 缺 synckey, -2010 用户不存在).
func apiErrText(raw map[string]any, code int) string {
	if s, ok := raw["errMsg"].(string); ok && s != "" {
		return s
	}
	if s, ok := raw["errmsg"].(string); ok && s != "" {
		return s
	}
	switch code {
	case -2003:
		return "参数格式错误"
	case -2010:
		return "用户不存在"
	case -2014:
		return "请求频率过高"
	case -2041:
		return "缺少 synckey"
	default:
		return "接口返回异常"
	}
}

// UserInfo fetches the nickname and avatar bound to a user id.
func (c *Client) UserInfo(ctx context.Context, ck Cookies, vid string) (nickname, avatar string) {
	q := "/api/userInfo"
	if vid != "" {
		q += "?userVid=" + vid
	}
	var raw map[string]any
	if _, err := c.doJSON(ctx, http.MethodGet, q, ck, nil, &raw); err != nil {
		return "", ""
	}
	return findString(raw, "name", "nickName", "nickname"), findString(raw, "avatar", "avatarUrl", "avatar_url")
}

// CurrentVID looks up the user id bound to a cookie jar. It is used as a
// fallback when the login response only carries an access token.
func (c *Client) CurrentVID(ctx context.Context, ck Cookies) string {
	var raw any
	if _, err := c.doJSON(ctx, http.MethodGet, "/web/user", ck, nil, &raw); err != nil {
		return ""
	}
	return findScalar(raw, "userVid", "vid", "webLoginVid")
}

// Warmup visits the home page so the server can hand out auxiliary cookies
// (fingerprint, tracking) that the read endpoint may expect.
func (c *Client) Warmup(ctx context.Context, ck Cookies) {
	req, err := c.newRequest(ctx, http.MethodGet, "/", ck, nil)
	if err != nil {
		return
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 256<<10))
	for _, rc := range resp.Cookies() {
		if rc.Value == "" {
			continue
		}
		if cur, ok := ck[rc.Name]; ok && cur != "" {
			continue
		}
		ck[rc.Name] = rc.Value
	}
}

func toInt(v any) int {
	switch x := v.(type) {
	case bool:
		if x {
			return 1
		}
		return 0
	case float64:
		return int(x)
	case int:
		return x
	case int64:
		return int(x)
	case json.Number:
		n, _ := x.Int64()
		return int(n)
	case string:
		n, _ := strconv.Atoi(x)
		return n
	default:
		return 0
	}
}

func toInt64(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int64:
		return x
	case int:
		return int64(x)
	case json.Number:
		n, _ := x.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	default:
		return 0
	}
}

func pickString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

// findString walks a decoded JSON value and returns the first string value
// matching one of the candidate keys.
func findString(v any, keys ...string) string {
	return findScalar(v, keys...)
}

// findScalar is like findString but also accepts numeric values, which some
// login payloads use for user ids.
func findScalar(v any, keys ...string) string {
	want := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		want[k] = struct{}{}
	}
	var walk func(any) string
	walk = func(cur any) string {
		switch x := cur.(type) {
		case map[string]any:
			for k, val := range x {
				if _, ok := want[k]; !ok {
					continue
				}
				switch s := val.(type) {
				case string:
					if s != "" {
						return s
					}
				case float64:
					return strconv.FormatInt(int64(s), 10)
				case json.Number:
					return s.String()
				case int64:
					return strconv.FormatInt(s, 10)
				case int:
					return strconv.Itoa(s)
				}
			}
			for _, val := range x {
				if s := walk(val); s != "" {
					return s
				}
			}
		case []any:
			for _, val := range x {
				if s := walk(val); s != "" {
					return s
				}
			}
		}
		return ""
	}
	return walk(v)
}
