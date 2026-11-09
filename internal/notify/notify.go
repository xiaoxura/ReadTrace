// Package notify delivers run results to PushPlus, WxPusher, Telegram,
// ServerChan, Bark, Ntfy, Gotify, PushDeer, Feishu, WeCom, DingTalk,
// ServerChan³ and Apprise.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"readtrace/internal/store"
)

const (
	wxPusherMaxChars        = 40000
	wxPusherMaxBytes        = 65535
	wxPusherMaxSummaryChars = 100
)

// Sender sends notifications.
type Sender struct {
	HTTP *http.Client

	proxyMu      sync.Mutex
	proxyClients map[string]*http.Client // telegram proxy -> client, reused
}

// New creates a Sender.
func New() *Sender {
	return &Sender{HTTP: &http.Client{Timeout: 20 * time.Second}, proxyClients: map[string]*http.Client{}}
}

// Send dispatches one message through the channel's type.
func (s *Sender) Send(ctx context.Context, ch store.PushChannel, title, content string) error {
	if !ch.Enabled {
		return fmt.Errorf("渠道已禁用")
	}
	switch strings.ToLower(ch.Type) {
	case "pushplus":
		return s.pushPlus(ctx, ch.Config["token"], title, content)
	case "wxpusher":
		return s.wxPusher(ctx, ch.Config["spt"], content)
	case "telegram":
		return s.telegram(ctx, ch.Config, title+"\n"+content)
	case "serverchan":
		return s.serverChan(ctx, ch.Config["sendkey"], title, content)
	case "bark":
		return s.bark(ctx, ch.Config, title, content)
	case "ntfy":
		return s.ntfy(ctx, ch.Config, title, content)
	case "gotify":
		return s.gotify(ctx, ch.Config, title, content)
	case "pushdeer":
		return s.pushDeer(ctx, ch.Config, title, content)
	case "feishu":
		return s.feishu(ctx, ch.Config, title, content)
	case "wework":
		return s.weCom(ctx, ch.Config, title, content)
	case "dingtalk":
		return s.dingTalk(ctx, ch.Config, title, content)
	case "serverchan3":
		return s.serverChan3(ctx, ch.Config, title, content)
	case "apprise":
		return s.apprise(ctx, ch.Config, title, content)
	default:
		return fmt.Errorf("不支持的推送渠道: %s", ch.Type)
	}
}

// doRequest performs one request and turns non-2xx responses and body read
// failures into errors. The status is also needed for Apprise's empty 204.
func doRequest(client *http.Client, req *http.Request) ([]byte, int, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("读取推送响应失败 (HTTP %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return body, resp.StatusCode, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(body)))
	}
	return body, resp.StatusCode, nil
}

func (s *Sender) do(req *http.Request) ([]byte, error) {
	body, _, err := doRequest(s.HTTP, req)
	return body, err
}

func (s *Sender) postJSON(ctx context.Context, endpoint string, payload any) ([]byte, error) {
	body, _, err := s.postJSONResponse(ctx, endpoint, payload)
	return body, err
}

func (s *Sender) postJSONResponse(ctx context.Context, endpoint string, payload any) ([]byte, int, error) {
	buf, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(buf))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	return doRequest(s.HTTP, req)
}

func (s *Sender) pushPlus(ctx context.Context, token, title, content string) error {
	if token == "" {
		return fmt.Errorf("缺少 token")
	}
	body, err := s.postJSON(ctx, "https://www.pushplus.plus/send", map[string]any{
		"token": token, "title": title, "content": content, "template": "txt",
	})
	if err != nil {
		return err
	}
	return checkCode(body, "PushPlus", "code", 200, "msg")
}

func (s *Sender) wxPusher(ctx context.Context, spt, content string) error {
	if spt == "" {
		return fmt.Errorf("缺少 SPT")
	}
	content = limitUTF8(content, wxPusherMaxChars, wxPusherMaxBytes)
	body, err := s.postJSON(ctx, "https://wxpusher.zjiecode.com/api/send/message/simple-push", map[string]any{
		"spt": spt, "content": content, "summary": limitUTF8(strings.TrimSpace(content), wxPusherMaxSummaryChars, wxPusherMaxBytes), "contentType": 1,
	})
	if err != nil {
		return err
	}
	return checkCode(body, "WxPusher", "code", 1000, "msg")
}

func (s *Sender) telegram(ctx context.Context, cfg map[string]string, text string) error {
	token, chatID := cfg["bot_token"], cfg["chat_id"]
	if token == "" || chatID == "" {
		return fmt.Errorf("缺少 bot_token 或 chat_id")
	}
	endpoint := "https://api.telegram.org/bot" + token + "/sendMessage"
	payload := map[string]any{"chat_id": chatID, "text": text}

	client := s.HTTP
	if proxy := strings.TrimSpace(cfg["proxy"]); proxy != "" {
		u, err := url.Parse(proxy)
		if err != nil {
			return fmt.Errorf("代理地址无效: %w", err)
		}
		client = s.clientForProxy(u)
	}
	// Reuse postJSON with a custom client by issuing the request manually.
	buf, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(buf))
	if err != nil {
		return redactSecret(err, token)
	}
	req.Header.Set("Content-Type", "application/json")
	body, _, err := doRequest(client, req)
	if err != nil {
		return redactSecret(err, token)
	}
	return redactSecret(checkTelegram(body), token)
}

func (s *Sender) serverChan(ctx context.Context, sendkey, title, content string) error {
	if sendkey == "" {
		return fmt.Errorf("缺少 SendKey")
	}
	body, err := s.postJSON(ctx, "https://sctapi.ftqq.com/"+url.PathEscape(sendkey)+".send", map[string]any{
		"title": title, "desp": content,
	})
	if err != nil {
		return redactSecret(err, sendkey)
	}
	return redactSecret(checkCode(body, "ServerChan", "code", 0, "message", "msg"), sendkey)
}

// orDefault keeps optional host fields out of the config form.
func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return strings.TrimSpace(v)
}

// trimHost strips a trailing slash so endpoint joins stay clean.
func trimHost(v, def string) string {
	return strings.TrimRight(orDefault(v, def), "/")
}

func (s *Sender) bark(ctx context.Context, cfg map[string]string, title, content string) error {
	key := strings.TrimSpace(cfg["device_key"])
	if key == "" {
		return fmt.Errorf("缺少 device_key")
	}
	payload := map[string]any{"device_key": key, "title": title, "body": content}
	if v := strings.TrimSpace(cfg["sound"]); v != "" {
		payload["sound"] = v
	}
	body, err := s.postJSON(ctx, trimHost(cfg["server"], "https://api.day.app")+"/push", payload)
	if err != nil {
		return err
	}
	return checkCode(body, "Bark", "code", 200, "message")
}

func (s *Sender) ntfy(ctx context.Context, cfg map[string]string, title, content string) error {
	topic := strings.TrimSpace(cfg["topic"])
	if topic == "" {
		return fmt.Errorf("缺少 topic")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		trimHost(cfg["server"], "https://ntfy.sh")+"/"+strings.Trim(topic, "/"), strings.NewReader(content))
	if err != nil {
		return err
	}
	req.Header.Set("Title", title)
	if tok := strings.TrimSpace(cfg["token"]); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	body, err := s.do(req)
	if err != nil {
		return err
	}
	return checkOptionalError(body, "ntfy")
}

func (s *Sender) gotify(ctx context.Context, cfg map[string]string, title, content string) error {
	server, token := strings.TrimSpace(cfg["server"]), strings.TrimSpace(cfg["token"])
	if server == "" || token == "" {
		return fmt.Errorf("缺少 server 或 token")
	}
	payload := map[string]any{"title": title, "message": content}
	if p := strings.TrimSpace(cfg["priority"]); p != "" {
		var n int
		if _, err := fmt.Sscanf(p, "%d", &n); err == nil {
			payload["priority"] = n
		}
	}
	endpoint := trimHost(server, "") + "/message?token=" + url.QueryEscape(token)
	body, err := s.postJSON(ctx, endpoint, payload)
	if err != nil {
		return redactSecret(err, token)
	}
	return redactSecret(checkOptionalError(body, "Gotify"), token)
}

func (s *Sender) pushDeer(ctx context.Context, cfg map[string]string, title, content string) error {
	key := strings.TrimSpace(cfg["pushkey"])
	if key == "" {
		return fmt.Errorf("缺少 pushkey")
	}
	payload := map[string]any{"pushkey": key, "text": title + "\n" + content, "type": "text"}
	if v := strings.TrimSpace(cfg["type"]); v == "markdown" || v == "markdown_v2" {
		payload["type"] = v
	}
	body, err := s.postJSON(ctx, "https://api2.pushdeer.com/message/push", payload)
	if err != nil {
		return err
	}
	if err := checkOptionalError(body, "PushDeer"); err != nil {
		return err
	}
	return checkCode(body, "PushDeer", "code", 0, "error")
}

func (s *Sender) feishu(ctx context.Context, cfg map[string]string, title, content string) error {
	webhook := strings.TrimSpace(cfg["webhook_url"])
	if webhook == "" {
		return fmt.Errorf("缺少 webhook_url")
	}
	body, err := s.postJSON(ctx, webhook, map[string]any{
		"msg_type": "text", "content": map[string]any{"text": "【" + title + "】\n" + content},
	})
	if err != nil {
		return err
	}
	return checkFeishu(body)
}

func (s *Sender) weCom(ctx context.Context, cfg map[string]string, title, content string) error {
	webhook := strings.TrimSpace(cfg["webhook_url"])
	if webhook == "" {
		return fmt.Errorf("缺少 webhook_url")
	}
	var payload map[string]any
	if strings.TrimSpace(cfg["msg_type"]) == "markdown" {
		payload = map[string]any{"msgtype": "markdown",
			"markdown": map[string]any{"content": "**" + title + "**\n" + content}}
	} else {
		payload = map[string]any{"msgtype": "text",
			"text": map[string]any{"content": title + "\n" + content}}
	}
	body, err := s.postJSON(ctx, webhook, payload)
	if err != nil {
		return err
	}
	return checkCode(body, "WeCom", "errcode", 0, "errmsg")
}

func (s *Sender) dingTalk(ctx context.Context, cfg map[string]string, title, content string) error {
	webhook := strings.TrimSpace(cfg["webhook_url"])
	if webhook == "" {
		return fmt.Errorf("缺少 webhook_url")
	}
	var payload map[string]any
	if strings.TrimSpace(cfg["msg_type"]) == "markdown" {
		payload = map[string]any{"msgtype": "markdown",
			"markdown": map[string]any{"title": title, "text": "# " + title + "\n\n" + content}}
	} else {
		payload = map[string]any{"msgtype": "text",
			"text": map[string]any{"content": title + "\n" + content}}
	}
	body, err := s.postJSON(ctx, webhook, payload)
	if err != nil {
		return err
	}
	return checkCode(body, "DingTalk", "errcode", 0, "errmsg")
}

func (s *Sender) serverChan3(ctx context.Context, cfg map[string]string, title, content string) error {
	uid, sendkey := strings.TrimSpace(cfg["uid"]), strings.TrimSpace(cfg["sendkey"])
	if uid == "" || sendkey == "" {
		return fmt.Errorf("缺少 uid 或 sendkey")
	}
	if !validHostLabel(uid) {
		return fmt.Errorf("ServerChan³ uid 不是有效的主机标签")
	}
	payload := map[string]any{"title": title, "desp": content}
	if v := strings.TrimSpace(cfg["short"]); v != "" {
		payload["short"] = v
	}
	if v := strings.TrimSpace(cfg["tags"]); v != "" {
		payload["tags"] = v
	}
	body, err := s.postJSON(ctx, "https://"+uid+".push.ft07.com/send/"+url.PathEscape(sendkey)+".send", payload)
	if err != nil {
		return redactSecret(err, sendkey)
	}
	return redactSecret(checkCode(body, "ServerChan³", "code", 0, "message", "msg"), sendkey)
}

// apprise delegates delivery to a self-hosted Apprise API server so one
// channel config can fan out to dozens of services it supports.
func (s *Sender) apprise(ctx context.Context, cfg map[string]string, title, content string) error {
	server, token := strings.TrimSpace(cfg["server"]), strings.TrimSpace(cfg["token"])
	if server == "" || token == "" {
		return fmt.Errorf("缺少 server 或 token")
	}
	payload := map[string]any{"title": title, "body": content}
	if v := strings.TrimSpace(cfg["urls"]); v != "" {
		payload["urls"] = v
	}
	_, status, err := s.postJSONResponse(ctx, trimHost(server, "")+"/notify/"+url.PathEscape(token), payload)
	if err != nil {
		return redactSecret(err, token)
	}
	if status == http.StatusNoContent {
		return fmt.Errorf("Apprise 未找到有效的通知目标 (HTTP 204)")
	}
	return nil
}

// clientForProxy returns a cached HTTP client per proxy URL so successive
// pushes reuse Transport/connections instead of leaking them until the idle
// timeout.
func (s *Sender) clientForProxy(u *url.URL) *http.Client {
	key := u.String()
	s.proxyMu.Lock()
	defer s.proxyMu.Unlock()
	if c, ok := s.proxyClients[key]; ok {
		return c
	}
	c := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(u)}}
	s.proxyClients[key] = c
	return c
}

func truncate(s string) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > 200 {
		return string(r[:200])
	}
	return s
}

// limitUTF8 enforces both WxPusher limits and normalizes malformed UTF-8.
func limitUTF8(s string, maxChars, maxBytes int) string {
	var out strings.Builder
	out.Grow(min(len(s), maxBytes))
	chars := 0
	for _, r := range s {
		if chars >= maxChars || out.Len()+utf8.RuneLen(r) > maxBytes {
			break
		}
		out.WriteRune(r)
		chars++
	}
	return out.String()
}

func validHostLabel(s string) bool {
	if len(s) == 0 || len(s) > 63 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			continue
		}
		if c != '-' || i == 0 || i == len(s)-1 {
			return false
		}
	}
	return true
}

func redactSecret(err error, secret string) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	for _, value := range []string{url.PathEscape(secret), url.QueryEscape(secret), secret} {
		if value != "" {
			message = strings.ReplaceAll(message, value, "[REDACTED]")
		}
	}
	return fmt.Errorf("%s", message)
}

func responseObject(body []byte, channel string) (map[string]json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil || obj == nil {
		return nil, fmt.Errorf("%s 响应协议错误：无效 JSON 对象", channel)
	}
	return obj, nil
}

func responseMessage(obj map[string]json.RawMessage, fields ...string) string {
	for _, field := range fields {
		var msg string
		if raw, ok := obj[field]; ok && json.Unmarshal(raw, &msg) == nil && msg != "" {
			return truncate(msg)
		}
	}
	return ""
}

func checkCode(body []byte, channel, field string, success int, messages ...string) error {
	obj, err := responseObject(body, channel)
	if err != nil {
		return err
	}
	raw, ok := obj[field]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("%s 响应协议错误：缺少 %s", channel, field)
	}
	var code int
	if err := json.Unmarshal(raw, &code); err != nil {
		return fmt.Errorf("%s 响应协议错误：无效 %s", channel, field)
	}
	if code == success {
		return nil
	}
	if msg := responseMessage(obj, messages...); msg != "" {
		return fmt.Errorf("%s 推送失败 (%s=%d): %s", channel, field, code, msg)
	}
	return fmt.Errorf("%s 推送失败 (%s=%d)", channel, field, code)
}

func checkTelegram(body []byte) error {
	obj, err := responseObject(body, "Telegram")
	if err != nil {
		return err
	}
	raw, ok := obj["ok"]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("Telegram 响应协议错误：缺少 ok")
	}
	var success bool
	if err := json.Unmarshal(raw, &success); err != nil {
		return fmt.Errorf("Telegram 响应协议错误：无效 ok")
	}
	if success {
		return nil
	}
	msg := responseMessage(obj, "description")
	var code int
	if raw, ok := obj["error_code"]; ok && json.Unmarshal(raw, &code) == nil {
		return fmt.Errorf("Telegram 推送失败 (error_code=%d): %s", code, msg)
	}
	if msg != "" {
		return fmt.Errorf("Telegram 推送失败: %s", msg)
	}
	return fmt.Errorf("Telegram 推送失败 (ok=false)")
}

func checkFeishu(body []byte) error {
	obj, err := responseObject(body, "Feishu")
	if err != nil {
		return err
	}
	if _, ok := obj["code"]; ok {
		return checkCode(body, "Feishu", "code", 0, "msg")
	}
	return checkCode(body, "Feishu", "StatusCode", 0, "StatusMessage")
}

// ntfy and Gotify use HTTP status as their primary success signal, but some
// installations return a structured error with HTTP 2xx.
func checkOptionalError(body []byte, channel string) error {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[') {
		return nil
	}
	obj, err := responseObject(trimmed, channel)
	if err != nil {
		return err
	}
	raw, ok := obj["error"]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || bytes.Equal(bytes.TrimSpace(raw), []byte(`""`)) {
		return nil
	}
	if msg := responseMessage(obj, "error", "errorDescription"); msg != "" {
		return fmt.Errorf("%s 推送失败: %s", channel, msg)
	}
	return fmt.Errorf("%s 推送失败: %s", channel, truncate(string(raw)))
}
