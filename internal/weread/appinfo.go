package weread

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// DeriveAppID 从 User-Agent 确定性推导 read 心跳 payload 里的 appId 字段，
// 与官方网页端的行为一致：前缀 = 前 12 个空格分隔段落的长度对 10 取余的
// 依次拼接；哈希 = 逐字符 (131*h + rune) & 0x7fffffff。
// 算法与样例来自 finlater/weread.koplugin 的协议文档，且与本仓库作者
// 2026-11 捕获的一次真实请求（"wb1557874663h565891605"）互相验证——
// 因此模板不再必须依赖人工抓包里的 appId。
func DeriveAppID(userAgent string) string {
	parts := strings.Fields(userAgent)
	var prefix strings.Builder
	for i, part := range parts {
		if i >= 12 {
			break
		}
		fmt.Fprint(&prefix, len(part)%10)
	}
	hash := int64(0)
	for _, r := range userAgent {
		hash = (131*hash + int64(r)) & 0x7fffffff
	}
	return fmt.Sprintf("wb%sh%d", prefix.String(), hash)
}

// ReaderState is the fingerprint material of one reader session, parsed from
// the SSR page of the web reader. PSVTS/PCLTS are the volatile page state
// fields read.py 心跳 payload 的 ps/pc 字段；PCLTS 为空（或 0/缺失）时可以用
// _e(ct) 兜底（与官方前端一致）。
type ReaderState struct {
	PSVTS  string
	PCLTS  string
	BookID string
	Title  string
}

var (
	readerPSVTSRe  = regexp.MustCompile(`"psvts"\s*:\s*"([^"]*)"`)
	readerPCLTSRe  = regexp.MustCompile(`"pclts"\s*:\s*"([^"]*)"`)
	readerBookIDRe = regexp.MustCompile(`"bookInfo"\s*:\s*\{[^{]*"bookId"\s*:\s*"([^"]*)"`)
	readerTitleRe  = regexp.MustCompile(`"bookInfo"\s*:\s*\{[^{]*"title"\s*:\s*"([^"]*)"`)
)

// FetchReaderState opens the SSR page of the web reader
// (GET /web/reader/{bookHash}) and pulls reader.psvts / reader.pclts from
// window.__INITIAL_STATE__. 这一步不需要浏览器或 DevTools 抓包：登录 Cookie
// + 这个 GET 就能拿到全部指纹素材。失败通常意味着登录态失效（HTML 是登录
// 页）或官方改版，调用方应提示重新登录/改用抓包模板。
func (c *Client) FetchReaderState(ctx context.Context, ck Cookies, bookID string) (*ReaderState, error) {
	hash := NormalizePayloadID(bookID)
	if hash == "" {
		return nil, fmt.Errorf("书籍 ID 不能为空")
	}

	req, err := c.newRequest(ctx, http.MethodGet, "/web/reader/"+hash, ck, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("%w（HTTP 401）", ErrUnauthorized)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("阅读页返回 HTTP %d", resp.StatusCode)
	}
	html := string(data)
	if isHTML([]byte(html)) && len(html) > 0 && !strings.Contains(html, "window.__INITIAL_STATE__") {
		// 登录态失效时会被重定向到普通登录页：页面能打开却拿不到状态。
		return nil, fmt.Errorf("阅读页没有返回运行状态，登录可能已失效（请重新扫码或改用抓包模板）")
	}

	out := &ReaderState{BookID: bookID}
	if m := readerPSVTSRe.FindStringSubmatch(html); m != nil {
		out.PSVTS = decodeJSString(m[1])
	}
	if m := readerPCLTSRe.FindStringSubmatch(html); m != nil {
		out.PCLTS = decodeJSString(m[1])
	}
	if m := readerBookIDRe.FindStringSubmatch(html); m != nil && m[1] != "" {
		out.BookID = decodeJSString(m[1])
	}
	if m := readerTitleRe.FindStringSubmatch(html); m != nil && m[1] != "" {
		out.Title = decodeJSString(m[1])
	}
	if out.PSVTS == "" && out.PCLTS == "" {
		return nil, fmt.Errorf("阅读页未包含 psvts/pclts 状态，可能官方改版；请改用抓包模板")
	}
	return out, nil
}

// decodeJSString unescapes the common JSON/JS escapes found inside
// __INITIAL_STATE__ fragments (\\xHH、\\uXXXX、\\\"、\\\\).
func decodeJSString(s string) string {
	if s == "" {
		return s
	}
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		switch s[i+1] {
		case 'n':
			b.WriteByte('\n')
			i++
		case 't':
			b.WriteByte('\t')
			i++
		case 'r':
			i++
		case '\\', '"', '\'':
			b.WriteByte(s[i+1])
			i++
		case 'u':
			if i+5 < len(s) {
				if v, err := strconv.ParseUint(s[i+2:i+6], 16, 32); err == nil {
					b.WriteRune(rune(v))
					i += 5
				}
			}
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}
