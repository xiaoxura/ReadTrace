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

	"readtrace/internal/store"
)

// wxPusherMaximum is the documented SPT content limit (40000 chars); sending
// near it via a JSON body is fine, so nothing is URL-encoded any more.
const wxPusherMaximum = 39000

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

// do performs one request and turns non-2xx responses into errors.
func (s *Sender) do(req *http.Request) ([]byte, error) {
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return body, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(body)))
	}
	return body, nil
}

func (s *Sender) postJSON(ctx context.Context, endpoint string, payload any) ([]byte, error) {
	buf, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return s.do(req)
}

func (s *Sender) pushPlus(ctx context.Context, token, title, content string) error {
	if token == "" {
		return fmt.Errorf("缺少 token")
	}
	_, err := s.postJSON(ctx, "https://www.pushplus.plus/send", map[string]any{
		"token": token, "title": title, "content": content, "template": "txt",
	})
	return err
}

func (s *Sender) wxPusher(ctx context.Context, spt, content string) error {
	if spt == "" {
		return fmt.Errorf("缺少 SPT")
	}
	// The SPT POST API accepts arbitrary content length; the old GET-in-URL
	// form broke on long messages (URL length limits).
	if len(content) > wxPusherMaximum {
		content = truncate(content)
	}
	_, err := s.postJSON(ctx, "https://wxpusher.zjiecode.com/api/send/message/simple-push", map[string]any{
		"spt": spt, "content": content, "summary": truncate(content), "contentType": 1,
	})
	return err
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
	buf, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(body)))
	}
	return nil
}

func (s *Sender) serverChan(ctx context.Context, sendkey, title, content string) error {
	if sendkey == "" {
		return fmt.Errorf("缺少 SendKey")
	}
	_, err := s.postJSON(ctx, "https://sctapi.ftqq.com/"+url.PathEscape(sendkey)+".send", map[string]any{
		"title": title, "desp": content,
	})
	return err
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
	_, err := s.postJSON(ctx, trimHost(cfg["server"], "https://api.day.app")+"/push", payload)
	return err
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
	_, err = s.do(req)
	return err
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
	_, err := s.postJSON(ctx, endpoint, payload)
	return err
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
	_, err := s.postJSON(ctx, "https://api2.pushdeer.com/message/push", payload)
	return err
}

func (s *Sender) feishu(ctx context.Context, cfg map[string]string, title, content string) error {
	webhook := strings.TrimSpace(cfg["webhook_url"])
	if webhook == "" {
		return fmt.Errorf("缺少 webhook_url")
	}
	_, err := s.postJSON(ctx, webhook, map[string]any{
		"msg_type": "text", "content": map[string]any{"text": "【" + title + "】\n" + content},
	})
	return err
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
	_, err := s.postJSON(ctx, webhook, payload)
	return err
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
	_, err := s.postJSON(ctx, webhook, payload)
	return err
}

func (s *Sender) serverChan3(ctx context.Context, cfg map[string]string, title, content string) error {
	uid, sendkey := strings.TrimSpace(cfg["uid"]), strings.TrimSpace(cfg["sendkey"])
	if uid == "" || sendkey == "" {
		return fmt.Errorf("缺少 uid 或 sendkey")
	}
	payload := map[string]any{"uid": uid, "sendkey": sendkey, "title": title, "desp": content}
	if v := strings.TrimSpace(cfg["short"]); v != "" {
		payload["short"] = v
	}
	if v := strings.TrimSpace(cfg["tags"]); v != "" {
		payload["tags"] = v
	}
	_, err := s.postJSON(ctx, "https://sc3.ft07.com/send/"+url.PathEscape(uid)+".send", payload)
	return err
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
	_, err := s.postJSON(ctx, trimHost(server, "")+"/notify/"+url.PathEscape(token), payload)
	return err
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
