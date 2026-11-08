// Package notify delivers run results to PushPlus, WxPusher, Telegram and
// ServerChan.
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
	default:
		return fmt.Errorf("不支持的推送渠道: %s", ch.Type)
	}
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
