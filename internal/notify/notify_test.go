package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"

	"readtrace/internal/store"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// secretLiteral 是 19 的传输失败抹敏用例统一假想凭据（不得出现在错误串里）。
const secretLiteral = "LOCAL_FAKE_SECRET"

func testSender(body string, status int, inspect func(*http.Request)) *Sender {
	return &Sender{HTTP: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if inspect != nil {
			inspect(req)
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: req}, nil
	})}}
}

func channel(kind string) store.PushChannel {
	configs := map[string]map[string]string{
		"pushplus":    {"token": "secret"},
		"wxpusher":    {"spt": "secret"},
		"telegram":    {"bot_token": "secret", "chat_id": "123"},
		"serverchan":  {"sendkey": "secret"},
		"bark":        {"device_key": "secret"},
		"ntfy":        {"topic": "topic"},
		"gotify":      {"server": "https://gotify.example", "token": "secret"},
		"pushdeer":    {"pushkey": "secret"},
		"feishu":      {"webhook_url": "https://feishu.example/hook"},
		"wework":      {"webhook_url": "https://wecom.example/hook"},
		"dingtalk":    {"webhook_url": "https://dingtalk.example/hook"},
		"serverchan3": {"uid": "user-1", "sendkey": "secret"},
		"apprise":     {"server": "https://apprise.example", "token": "secret"},
	}
	return store.PushChannel{Type: kind, Enabled: true, Config: configs[kind]}
}

func TestJSONChannelBusinessResponses(t *testing.T) {
	tests := []struct{ kind, success, failure, missing string }{
		{"pushplus", `{"code":200}`, `{"code":500,"msg":"rejected"}`, `{"msg":"missing"}`},
		{"wxpusher", `{"code":1000}`, `{"code":1001,"msg":"rejected"}`, `{"msg":"missing"}`},
		{"telegram", `{"ok":true}`, `{"ok":false,"error_code":400,"description":"rejected"}`, `{"description":"missing"}`},
		{"serverchan", `{"code":0}`, `{"code":1,"message":"rejected"}`, `{"message":"missing"}`},
		{"bark", `{"code":200}`, `{"code":400,"message":"rejected"}`, `{"message":"missing"}`},
		{"pushdeer", `{"code":0,"error":null}`, `{"code":1,"error":"rejected"}`, `{"error":null}`},
		{"feishu", `{"code":0}`, `{"code":19024,"msg":"rejected"}`, `{"msg":"missing"}`},
		{"wework", `{"errcode":0}`, `{"errcode":400,"errmsg":"rejected"}`, `{"errmsg":"missing"}`},
		{"dingtalk", `{"errcode":0}`, `{"errcode":400,"errmsg":"rejected"}`, `{"errmsg":"missing"}`},
		{"serverchan3", `{"code":0}`, `{"code":1,"message":"rejected"}`, `{"message":"missing"}`},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			for _, tc := range []struct{ name, body, want string }{
				{"success", tt.success, ""},
				{"business_error", tt.failure, "rejected"},
				{"missing_field", tt.missing, "响应协议错误"},
				{"invalid_json", `{`, "响应协议错误"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					err := testSender(tc.body, 200, nil).Send(context.Background(), channel(tt.kind), "title", "body")
					if tc.want == "" && err != nil {
						t.Fatalf("unexpected error: %v", err)
					}
					if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
						t.Fatalf("error = %v, want %q", err, tc.want)
					}
				})
			}
		})
	}
}

func TestFeishuLegacyStatusCode(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`{"StatusCode":0}`, ""},
		{`{"StatusCode":1,"StatusMessage":"blocked"}`, "blocked"},
		{`{"code":null,"StatusCode":0}`, "响应协议错误"},
	} {
		err := testSender(tc.body, 200, nil).Send(context.Background(), channel("feishu"), "title", "body")
		if tc.want == "" && err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
			t.Fatalf("error = %v, want %q", err, tc.want)
		}
	}
}

func TestStatusAndReadFailures(t *testing.T) {
	err := testSender(`{"code":200}`, 503, nil).Send(context.Background(), channel("pushplus"), "title", "body")
	if err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("non-2xx error = %v", err)
	}
	for _, kind := range []string{"pushplus", "telegram"} {
		t.Run(kind, func(t *testing.T) {
			s := &Sender{HTTP: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: failingBody{}, Request: req}, nil
			})}}
			err := s.Send(context.Background(), channel(kind), "title", "body")
			if err == nil || !strings.Contains(err.Error(), "读取推送响应失败") {
				t.Fatalf("read error = %v", err)
			}
		})
	}
}

type failingBody struct{}

func (failingBody) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (failingBody) Close() error             { return nil }

func TestHTTPStatusChannels(t *testing.T) {
	for _, tc := range []struct{ kind, body, want string }{
		{"ntfy", `{"id":"123"}`, ""},
		{"ntfy", `{"error":"rejected"}`, "rejected"},
		{"gotify", `{"id":123}`, ""},
		{"gotify", `{"error":"rejected"}`, "rejected"},
		{"apprise", "notification sent", ""},
	} {
		err := testSender(tc.body, 200, nil).Send(context.Background(), channel(tc.kind), "title", "body")
		if tc.want == "" && err != nil {
			t.Fatalf("%s unexpected error: %v", tc.kind, err)
		}
		if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
			t.Fatalf("%s error = %v", tc.kind, err)
		}
	}
	for _, tc := range []struct {
		status int
		want   string
	}{{204, "有效的通知目标"}, {424, "HTTP 424"}} {
		err := testSender("", tc.status, nil).Send(context.Background(), channel("apprise"), "title", "body")
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("Apprise HTTP %d error = %v", tc.status, err)
		}
	}
}

func TestWxPusherLimits(t *testing.T) {
	for _, tc := range []struct {
		name, content                     string
		wantChars, wantBytes, wantSummary int
	}{
		{"summary_unicode", strings.Repeat("界", 101), 101, 303, 100},
		{"byte_limit", strings.Repeat("界", 30000), 21845, 65535, 100},
		{"character_limit", strings.Repeat("a", 41000), 40000, 40000, 100},
		{"malformed_utf8", "a\xffb", 3, 5, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got map[string]any
			s := testSender(`{"code":1000}`, 200, func(req *http.Request) {
				if err := json.NewDecoder(req.Body).Decode(&got); err != nil {
					t.Errorf("decode request: %v", err)
				}
			})
			if err := s.Send(context.Background(), channel("wxpusher"), "title", tc.content); err != nil {
				t.Fatal(err)
			}
			content, summary := got["content"].(string), got["summary"].(string)
			if !utf8.ValidString(content) || utf8.RuneCountInString(content) != tc.wantChars || len(content) != tc.wantBytes {
				t.Fatalf("content chars=%d bytes=%d valid=%v", utf8.RuneCountInString(content), len(content), utf8.ValidString(content))
			}
			if !utf8.ValidString(summary) || utf8.RuneCountInString(summary) != tc.wantSummary {
				t.Fatalf("summary chars=%d valid=%v", utf8.RuneCountInString(summary), utf8.ValidString(summary))
			}
		})
	}
}

func TestServerChan3EndpointAndUID(t *testing.T) {
	var gotURL string
	var payload map[string]any
	s := testSender(`{"code":0}`, 200, func(req *http.Request) {
		gotURL = req.URL.String()
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
	})
	ch := channel("serverchan3")
	ch.Config["short"], ch.Config["tags"] = "short", "tag"
	if err := s.Send(context.Background(), ch, "title", "body"); err != nil {
		t.Fatal(err)
	}
	if gotURL != "https://user-1.push.ft07.com/send/secret.send" {
		t.Fatalf("URL = %q", gotURL)
	}
	if payload["title"] != "title" || payload["desp"] != "body" || payload["short"] != "short" || payload["tags"] != "tag" {
		t.Fatalf("payload = %#v", payload)
	}
	for _, uid := range []string{"bad.name", "-bad", "bad-", strings.Repeat("a", 64)} {
		ch.Config["uid"] = uid
		if err := s.Send(context.Background(), ch, "title", "body"); err == nil {
			t.Fatalf("uid %q accepted", uid)
		}
	}
	ch.Config["uid"] = "valid"
	ch.Config["sendkey"] = "secret/token"
	s.HTTP.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) { return nil, errors.New(req.URL.String()) })
	if err := s.Send(context.Background(), ch, "title", "body"); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("sendkey leaked: %v", err)
	}
}

// TestTransportFailureRedactsWebhookSecret 转正归档探针
// TestReviewProbeWebhookSecretInError（缺陷 19）：本地假 RoundTripper 按
// client.Do 的真实失败形态（*url.Error 携带完整请求 URL）注入传输失败，
// 对 13 个渠道断言错误串不含任何凭据值；密钥在路径/查询串里的渠道还要保留
// 「POST "https://host/路径": 原因」的可读结构。
func TestTransportFailureRedactsWebhookSecret(t *testing.T) {
	type tc struct {
		kind     string
		config   map[string]string
		readable bool // assert "Post \"https://...\"": 前缀可读
	}
	tests := []tc{
		// 凭据在 JSON body 或配置字段里的渠道，URL 本身无密钥。
		{"pushplus", map[string]string{"token": secretLiteral}, false},
		{"wxpusher", map[string]string{"spt": secretLiteral}, false},
		{"bark", map[string]string{"device_key": secretLiteral, "server": "https://day.example"}, false},
		{"pushdeer", map[string]string{"pushkey": secretLiteral}, false},
		{"ntfy", map[string]string{"topic": "topic", "token": secretLiteral, "server": "https://ntfy.example"}, false},
		// 凭据在 URL 路径里的渠道。
		{"telegram", map[string]string{"bot_token": secretLiteral, "chat_id": "123"}, false},
		{"serverchan", map[string]string{"sendkey": secretLiteral}, false},
		{"feishu", map[string]string{"webhook_url": "https://feishu.example/open-apis/bot/v2/hook/" + secretLiteral}, true},
		{"wework", map[string]string{"webhook_url": "https://qyapi.weixin.qq.com/cgi-bin/webhook/" + secretLiteral}, true},
		// 凭据在 URL 查询串里的渠道。
		{"gotify", map[string]string{"server": "https://gotify.example", "token": secretLiteral}, true},
		{"dingtalk", map[string]string{"webhook_url": "https://oapi.dingtalk.com/robot/send?access_token=" + secretLiteral}, true},
		{"serverchan3", map[string]string{"uid": "user-1", "sendkey": secretLiteral}, false},
		{"apprise", map[string]string{"server": "https://apprise.example", "token": secretLiteral}, false},
	}
	for _, test := range tests {
		s := New()
		s.HTTP.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return nil, &url.Error{Op: "Post", URL: req.URL.String(), Err: errors.New("local transport failure")}
		})
		err := s.Send(context.Background(), store.PushChannel{Type: test.kind, Enabled: true, Config: test.config}, "title", "body")
		if err == nil {
			t.Fatalf("%s: 应返回传输失败错误", test.kind)
		}
		if msg := err.Error(); strings.Contains(msg, secretLiteral) {
			t.Fatalf("%s: 错误泄漏密钥: %s", test.kind, msg)
		} else if test.readable && !strings.Contains(msg, `Post "https://`) {
			t.Fatalf("%s: 错误应保留可读结构: %s", test.kind, msg)
		}
	}
}

// TestTransportFailureKeepsReadableShape 固化三类 webhook 渠道脱敏后的输出
// 形态：主机与路径保留、密钥变占位符、原因文本保留（用户能看出是传输失败）。
func TestTransportFailureKeepsReadableShape(t *testing.T) {
	tests := []struct {
		kind, webhook, want, notWant string
	}{
		{"wework", "https://qyapi.weixin.qq.com/cgi-bin/webhook/" + secretLiteral,
			`Post "https://qyapi.weixin.qq.com/cgi-bin/webhook/[REDACTED]": local transport failure`,
			"LOCAL_FAKE_SECRET"},
		{"feishu", "https://feishu.example/open-apis/bot/v2/hook/" + secretLiteral,
			`Post "https://feishu.example/open-apis/bot/v2/hook/[REDACTED]": local transport failure`,
			"LOCAL_FAKE_SECRET"},
		{"dingtalk", "https://oapi.dingtalk.com/robot/send?access_token=" + secretLiteral,
			`Post "https://oapi.dingtalk.com/robot/send?access_token=[REDACTED]": local transport failure`,
			"LOCAL_FAKE_SECRET"},
	}
	for _, test := range tests {
		s := New()
		s.HTTP.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return nil, &url.Error{Op: "Post", URL: req.URL.String(), Err: errors.New("local transport failure")}
		})
		err := s.Send(context.Background(), store.PushChannel{
			Type: test.kind, Enabled: true, Config: map[string]string{"webhook_url": test.webhook},
		}, "title", "body")
		if err == nil {
			t.Fatalf("%s: 应返回错误", test.kind)
		}
		if msg := err.Error(); msg != test.want {
			t.Fatalf("%s: got %q want %q", test.kind, msg, test.want)
		}
	}
}

// TestWebhookSecretCoversFeishuWeWorkPathKey 校验 webhookSecret 的取值与
// 边界：钉钉路径无凭据不误报，空串不参与替换。
func TestWebhookSecretCoversFeishuWeWorkPathKey(t *testing.T) {
	if got := webhookSecret("https://qyapi.weixin.qq.com/cgi-bin/webhook/KEY123"); got != "KEY123" {
		t.Fatalf("webhookSecret = %q", got)
	}
	if got := webhookSecret("https://oapi.dingtalk.com/robot/send?access_token=KEY123"); got != "send" {
		t.Fatalf("dingtalk path key = %q", got)
	}
	if got := webhookSecret("https://feishu.example"); got != "" {
		t.Fatalf("bare host = %q", got)
	}
}
