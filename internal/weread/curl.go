package weread

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// CurlImport is the result of parsing a "Copy as cURL" command captured from
// the browser while reading a book.
type CurlImport struct {
	Cookies Cookies
	Payload map[string]any
	URL     string
	Headers map[string]string
}

var (
	lineContinuation = regexp.MustCompile(`\\\r?\n`)
	singleHeader     = regexp.MustCompile(`(?:^|\s)-H\s+'([^']*)'`)
	doubleHeader     = regexp.MustCompile(`(?:^|\s)-H\s+"([^"]*)"`)
	singleCookie     = regexp.MustCompile(`(?:^|\s)(?:-b|--cookie)\s+'([^']*)'`)
	doubleCookie     = regexp.MustCompile(`(?:^|\s)(?:-b|--cookie)\s+"([^"]*)"`)
	singleData       = regexp.MustCompile(`(?:^|\s)(?:--data-raw|--data-binary|--data|-d)\s+'([^']*)'`)
	doubleData       = regexp.MustCompile(`(?:^|\s)(?:--data-raw|--data-binary|--data|-d)\s+"([^"]*)"`)
	urlPattern       = regexp.MustCompile(`https?://[^\s'"]+`)
	headerPair       = regexp.MustCompile(`^([^:]+):\s*(.*)$`)
)

// ParseCurl accepts either a full cURL command or a bare cookie string.
func ParseCurl(raw string) (*CurlImport, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("内容为空")
	}
	raw = lineContinuation.ReplaceAllString(raw, " ")

	imp := &CurlImport{Cookies: Cookies{}, Headers: map[string]string{}}

	if !strings.Contains(raw, "curl") {
		// Treat the input as a raw cookie header / document.cookie string.
		ck := ParseCookieString(raw)
		if len(ck) == 0 {
			return nil, fmt.Errorf("无法识别内容，请粘贴完整的 curl 命令或 Cookie 字符串")
		}
		imp.Cookies = ck
		return imp, nil
	}

	if m := urlPattern.FindString(raw); m != "" {
		imp.URL = m
	}

	for _, re := range []*regexp.Regexp{singleHeader, doubleHeader} {
		for _, m := range re.FindAllStringSubmatch(raw, -1) {
			h := strings.TrimSpace(m[1])
			if mm := headerPair.FindStringSubmatch(h); mm != nil {
				imp.Headers[strings.ToLower(strings.TrimSpace(mm[1]))] = strings.TrimSpace(mm[2])
			}
		}
	}

	cookieHeader := imp.Headers["cookie"]
	for _, re := range []*regexp.Regexp{singleCookie, doubleCookie} {
		if m := re.FindStringSubmatch(raw); m != nil {
			cookieHeader = m[1]
		}
	}
	if cookieHeader != "" {
		imp.Cookies = ParseCookieString(cookieHeader)
	}

	for _, re := range []*regexp.Regexp{singleData, doubleData} {
		if m := re.FindStringSubmatch(raw); m != nil {
			body := strings.TrimSpace(m[1])
			var payload map[string]any
			if err := json.Unmarshal([]byte(body), &payload); err == nil && len(payload) > 0 {
				delete(payload, "s")
				delete(payload, "sg")
				imp.Payload = payload
			}
		}
	}

	if len(imp.Cookies) == 0 {
		return nil, fmt.Errorf("未从 curl 中找到 Cookie，请确认复制的是 /web/book/read 请求")
	}
	return imp, nil
}

// ParseCookieString parses "a=1; b=2" into a map.
func ParseCookieString(s string) Cookies {
	out := Cookies{}
	for _, part := range strings.Split(s, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		k := strings.TrimSpace(kv[0])
		v := strings.TrimSpace(kv[1])
		if k == "" {
			continue
		}
		out[k] = v
	}
	return out
}

// Model returns "1" when the account looks authenticated.
func (c Cookies) HasAuth() bool {
	return c["wr_skey"] != "" || c["wr_vid"] != ""
}

// Header renders cookies for a Cookie request header.
func (c Cookies) Header() string {
	parts := make([]string, 0, len(c))
	for k, v := range c {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, "; ")
}

// Clone copies the cookie map.
func (c Cookies) Clone() Cookies {
	out := make(Cookies, len(c))
	for k, v := range c {
		out[k] = v
	}
	return out
}
