package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"readtrace/internal/store"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

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
