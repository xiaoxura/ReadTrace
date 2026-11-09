package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// postAuth 通过真实 Handler 链路（recoverMW → securityMW → logMW → mux）发起
// 公开认证写接口请求，host 模拟浏览器访问的面板 Host。
func postAuth(s *Server, host, target, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	req.Host = host
	req.RemoteAddr = "127.0.0.1:51000"
	if headers != nil {
		for k, v := range headers {
			req.Header.Set(k, v)
		}
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestAuthWriteGuardRejectsFormAndCrossOriginRequests(t *testing.T) {
	s := newAuthSecurityServer(t)
	jsonBody := `{"password":"123456","site_name":"阅迹"}`
	appMark := map[string]string{"X-Requested-With": appValue}
	cases := []struct {
		name     string
		body     string
		headers  map[string]string
		wantCode int
	}{
		{"form-urlencoded 提交", "password=123456", appMark, http.StatusUnsupportedMediaType},
		{"form text/plain 提交", `{"password":"123456"}=` + "\r\n", appMark, http.StatusUnsupportedMediaType},
		{"Content-Type 缺失", jsonBody, appMark, http.StatusUnsupportedMediaType},
		{"缺少应用标记", jsonBody, nil, http.StatusForbidden},
		{"恶意 Origin 且无应用标记", jsonBody,
			map[string]string{"Origin": "http://evil.example.com"}, http.StatusForbidden},
		{"应用标记 + 恶意 Origin", jsonBody,
			map[string]string{"X-Requested-With": appValue, "Origin": "http://evil.example.com"},
			http.StatusForbidden},
		{"伪造 null Origin", jsonBody,
			map[string]string{"X-Requested-With": appValue, "Origin": "null"}, http.StatusForbidden},
		{"ftp 伪协议 Origin", jsonBody,
			map[string]string{"X-Requested-With": appValue, "Origin": "ftp://127.0.0.1:8720"},
			http.StatusForbidden},
	}
	for i, c := range cases {
		rec := postAuth(s, "127.0.0.1:8720", "/api/setup", c.body, c.headers)
		if rec.Code != c.wantCode {
			t.Errorf("case %d %s：状态码 = %d，期望 %d（响应 %s）",
				i, c.name, rec.Code, c.wantCode, rec.Body.String())
		}
		if rec.Code >= 400 && len(rec.Result().Cookies()) != 0 {
			t.Errorf("case %d %s：被拒请求不应下发会话 Cookie", i, c.name)
		}
	}
}

// TestAuthWriteGuardAllowsSameOriginFlow 验证合法初始化流程在防护下仍然可用：
// 同源 fetch（含 Origin）、回环白名单主机、无 Origin 的脚本调用均能完成初始化。
func TestAuthWriteGuardAllowsSameOriginFlow(t *testing.T) {
	cases := []struct {
		name         string
		allowedHosts []string
		host         string
		headers      map[string]string
	}{
		{"同源 fetch Origin 匹配 Host", nil, "127.0.0.1:8720", map[string]string{
			"Content-Type": "application/json", "Origin": "http://127.0.0.1:8720", "X-Requested-With": appValue,
		}},
		{"无 Origin 的脚本调用", nil, "127.0.0.1:8720", map[string]string{
			"Content-Type": "application/json", "X-Requested-With": appValue,
		}},
		{"Origin 主机落在 allowedHosts", []string{"panel.example.com"}, "panel.example.com:8720", map[string]string{
			"Content-Type": "application/json", "Origin": "https://panel.example.com", "X-Requested-With": appValue,
		}},
	}
	for i, c := range cases {
		s := newAuthSecurityServer(t)
		s.allowedHosts = c.allowedHosts
		rec := postAuth(s, c.host, "/api/setup",
			`{"password":"123456","site_name":"阅迹"}`, c.headers)
		if rec.Code != http.StatusOK {
			t.Errorf("case %d %s：状态码 = %d（响应 %s）", i, c.name, rec.Code, rec.Body.String())
			continue
		}
		if body := rec.Body.String(); !strings.Contains(body, `"ok":true`) {
			t.Errorf("case %d %s：响应缺少 ok 字段：%s", i, c.name, body)
		}
		cookies := rec.Result().Cookies()
		if len(cookies) != 1 {
			t.Errorf("case %d %s：初始化成功应下发 1 个会话 Cookie，实际 %d", i, c.name, len(cookies))
			continue
		}
		if ses, err := s.st.GetSession(cookies[0].Value); err != nil || ses == nil {
			t.Errorf("case %d %s：会话未入库：err=%v ses=%v", i, c.name, err, ses)
		}
	}
}

// TestAuthWriteGuardCoversLoginAndLogout 验证 login/logout 同样受防护约束。
func TestAuthWriteGuardCoversLoginAndLogout(t *testing.T) {
	s := newAuthSecurityServer(t)
	jsonHeaders := map[string]string{"Content-Type": "application/json"}

	if rec := postAuth(s, "127.0.0.1:8720", "/api/login", `{"password":"x"}`, jsonHeaders); rec.Code != http.StatusForbidden {
		t.Errorf("login 缺少应用标记：状态码 = %d，期望 403", rec.Code)
	}
	if rec := postAuth(s, "127.0.0.1:8720", "/api/logout", `{}`, jsonHeaders); rec.Code != http.StatusForbidden {
		t.Errorf("logout 缺少应用标记：状态码 = %d，期望 403", rec.Code)
	}
	if rec := postAuth(s, "127.0.0.1:8720", "/api/logout", `{}`,
		map[string]string{"X-Requested-With": appValue, "Content-Type": "application/json",
			"Origin": "http://evil.example.com"}); rec.Code != http.StatusForbidden {
		t.Errorf("logout 携带恶意 Origin：状态码 = %d，期望 403", rec.Code)
	}
	rec := postAuth(s, "127.0.0.1:8720", "/api/logout", `{}`,
		map[string]string{"X-Requested-With": appValue, "Content-Type": "application/json"})
	if rec.Code != http.StatusOK {
		t.Errorf("logout 同源标记请求：状态码 = %d，期望 200", rec.Code)
	}
}

// TestDecodeRejectsTrailingContent 验证 decode 只消费一个完整 JSON 值，
// 尾部的多余内容（如表单 text/plain 附加的 `=\r\n` 或后续值）会被拒绝。
func TestDecodeRejectsTrailingContent(t *testing.T) {
	s := newAuthSecurityServer(t)
	headers := map[string]string{
		"X-Requested-With": appValue,
		"Content-Type":     "application/json",
	}
	cases := []struct {
		name string
		body string
	}{
		{"JSON 后跟表单尾巴", `{"password":"x"}=` + "\r\n"},
		{"JSON 后跟多个值", `{"password":"x"}{"password":"y"}`},
		{"JSON 后跟垃圾字节", `{"password":"x"}xxx`},
	}
	for i, c := range cases {
		rec := postAuth(s, "127.0.0.1:8720", "/api/setup", c.body, headers)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("case %d %s：状态码 = %d，期望 400（响应 %s）", i, c.name, rec.Code, rec.Body.String())
			continue
		}
		if !strings.Contains(rec.Body.String(), "多余内容") {
			t.Errorf("case %d %s：错误说明不明确：%s", i, c.name, rec.Body.String())
		}
	}
}

// TestDecodeAcceptsStandardJSON 验证 decode 对标准 JSON 调用（含 charset 参数）
// 保持开放，未初始化的登录拒绝不被 Content-Type 之外的原因改写。
func TestDecodeAcceptsStandardJSON(t *testing.T) {
	s := newAuthSecurityServer(t)
	rec := postAuth(s, "127.0.0.1:8720", "/api/setup",
		`{"password":"123456","site_name":"阅迹"}`,
		map[string]string{
			"X-Requested-With": appValue,
			"Content-Type":     "application/json; charset=utf-8",
		})
	if rec.Code != http.StatusOK {
		t.Fatalf("标准 JSON 初始化请求：状态码 = %d（响应 %s）", rec.Code, rec.Body.String())
	}
	// 初始化后：GET 请求不受 X-Requested-With / Content-Type 限制影响。
	if rec := postAuth(s, "127.0.0.1:8720", "/api/login", `{"password":"123456"}`,
		map[string]string{"X-Requested-With": appValue, "Content-Type": "application/json"}); rec.Code != http.StatusOK {
		t.Errorf("JSON 登录应通过：状态码 = %d（响应 %s）", rec.Code, rec.Body.String())
	}
	statusRec := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	statusRec.Host = "127.0.0.1:8720"
	getW := httptest.NewRecorder()
	s.Handler().ServeHTTP(getW, statusRec)
	if getW.Code != http.StatusOK {
		t.Errorf("GET /api/status 不应被拦截：状态码 = %d", getW.Code)
	}
}

// TestOriginAllowed 校验 originAllowed 的同源判定与 allowedHosts 白名单口径。
func TestOriginAllowed(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/login", nil)
	r.Host = "127.0.0.1:8720"
	cases := []struct {
		origin       string
		allowedHosts []string
		want         bool
	}{
		{"http://127.0.0.1:8720", nil, true},
		{"https://panel.example.com", []string{"panel.example.com"}, true},
		{"http://evil.example.com", []string{"panel.example.com"}, false},
		{"http://127.0.0.1:9999", nil, false},             // 端口不同，非同源
		{"null", nil, false},                              // 被 sanitize 的 Origin
		{"ftp://127.0.0.1:8720", nil, false},              // 非 http(s) scheme
		{"http://127.0.0.1.8720.example.com", nil, false}, // 后缀伪装
	}
	for i, c := range cases {
		if got := originAllowed(c.origin, r, c.allowedHosts); got != c.want {
			t.Errorf("case %d originAllowed(%q) = %v, want %v", i, c.origin, got, c.want)
		}
	}
	// IPv6 回环的精确匹配（请求 Host 同为 IPv6）。
	ipv6Req := httptest.NewRequest(http.MethodPost, "/api/login", nil)
	ipv6Req.Host = "[::1]:8720"
	if !originAllowed("http://[::1]:8720", ipv6Req, nil) {
		t.Error("IPv6 同源 Origin 应放行")
	}
}
