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
	urlPattern       = regexp.MustCompile(`^https?://`)
	headerPair       = regexp.MustCompile(`^([^:]+):\s*(.*)$`)
)

// ParseCurl accepts either a full cURL command or a bare cookie string.
func ParseCurl(raw string) (*CurlImport, error) {
	return parseCurl(raw, true)
}

func parseCurl(raw string, requireCookies bool) (*CurlImport, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("内容为空")
	}
	// 行尾续行先折行成空格再分词（bash 语义：续行处在 token 之间时等价，
	// Chrome 复制的多行命令都落在参数之间，不会切开引号串内部）。
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

	words := shellWords(raw)

	// 按选项流逐个收值：-H 头、-b/--cookie 与请求体参数；URL 取第一个
	// http(s) 开头且不是选项值的 token。
	var cookieArg string
	for i := 0; i < len(words); i++ {
		switch words[i] {
		case "-H", "--header":
			if i+1 < len(words) {
				i++
				if mm := headerPair.FindStringSubmatch(words[i]); mm != nil {
					imp.Headers[strings.ToLower(strings.TrimSpace(mm[1]))] = strings.TrimSpace(mm[2])
				}
			}
		case "-b", "--cookie":
			if i+1 < len(words) {
				i++
				// 支持重复出现的 cookie 参数，取最后一项（与 curl 语义相近）。
				cookieArg = strings.TrimSpace(words[i])
			}
		case "--url":
			if i+1 < len(words) && imp.URL == "" {
				i++
				imp.URL = words[i]
			} else if i+1 < len(words) {
				i++
			}
		case "--data-raw", "--data-binary", "--data", "-d":
			if i+1 < len(words) {
				i++
				// curl 允许重复传体（会拼接），这里取最后一项即可覆盖
				// Chrome 复制场景的单体请求。
				applyDataBody(imp, words[i])
			}
		default:
			if imp.URL == "" && urlPattern.MatchString(words[i]) {
				imp.URL = words[i]
			}
		}
	}

	if imp.Cookies == nil {
		imp.Cookies = Cookies{}
	}
	// 头里的 cookie 字串是 Cookie: 头的正常形态；-b/--cookie 显式参数出现
	// 时优先（与 curl 语义一致）。
	if h := imp.Headers["cookie"]; h != "" {
		imp.Cookies = ParseCookieString(h)
	}
	if cookieArg != "" {
		imp.Cookies = ParseCookieString(cookieArg)
	}

	if requireCookies && len(imp.Cookies) == 0 {
		return nil, fmt.Errorf("未从 curl 中找到 Cookie，请确认复制的是 /web/book/read 请求")
	}
	return imp, nil
}

// applyDataBody 把一次请求体参数解析成 payload 模板：合法 JSON 才采用，
// 并维持导入语义——剥掉签名字段 s/sg。
func applyDataBody(imp *CurlImport, body string) {
	var payload map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(body)), &payload); err == nil && len(payload) > 0 {
		delete(payload, "s")
		delete(payload, "sg")
		imp.Payload = payload
	}
}

// shellWords 按 POSIX shell 的分词规则把整条命令拆成 token 列表，覆盖
// "Copy as cURL" 会遇到的所有形态：
//   - 单引号串：字面量，内部无转义；bash 常用 `'\”` 分段转义一个单引号，
//     多个单引号段与裸段自然拼进同一个 token（如 Don'\”t → Don't）；
//   - 双引号串：处理 \" \\ 等转义，变量/命令替换（$x、`..`）无法在本地
//     求值，跳过其文本只保留字面量；
//   - 无引号 token：空格分隔，反斜杠转义下一字节；
//   - 行尾续行已在进入分词前折行。
func shellWords(s string) []string {
	var words []string
	var cur strings.Builder
	hasWord := false
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			if hasWord {
				words = append(words, cur.String())
				cur.Reset()
				hasWord = false
			}
			i++
		case c == '\'':
			// 单引号段：找到闭合引号；未闭合的残串按字面量收尾。
			hasWord = true
			end := strings.IndexByte(s[i+1:], '\'')
			if end < 0 {
				cur.WriteString(s[i+1:])
				i = len(s)
				break
			}
			cur.WriteString(s[i+1 : i+1+end])
			i += end + 2
		case c == '"':
			// 双引号段：逐字节处理转义，未闭合按字面量收尾。
			hasWord = true
			i++
			for i < len(s) {
				d := s[i]
				if d == '\\' && i+1 < len(s) {
					next := s[i+1]
					if next == '"' || next == '\\' || next == '$' || next == '`' {
						cur.WriteByte(next)
						i += 2
						continue
					}
					cur.WriteByte(d)
					i++
					continue
				}
				if d == '"' {
					i++
					break
				}
				if d == '$' || d == '`' {
					// 无法本地求值，跳过变量/命令替换文本。
					i++
					continue
				}
				cur.WriteByte(d)
				i++
			}
		case c == '\\':
			// 裸段转义：保留下一字节，结尾悬空的反斜杠丢弃。
			hasWord = true
			if i+1 < len(s) {
				cur.WriteByte(s[i+1])
				i += 2
			} else {
				i++
			}
		default:
			cur.WriteByte(c)
			hasWord = true
			i++
		}
	}
	if hasWord {
		words = append(words, cur.String())
	}
	return words
}

// ParseReadTemplate accepts a JSON request body or a captured cURL without
// requiring login credentials. Credential imports still use ParseCurl.
func ParseReadTemplate(raw string) (map[string]any, error) {
	raw = strings.TrimSpace(raw)
	var payload map[string]any
	var err error
	if strings.HasPrefix(raw, "{") {
		payload, err = DecodePayload(raw)
	} else {
		var imported *CurlImport
		imported, err = parseCurl(raw, false)
		if err == nil {
			payload = imported.Payload
		}
	}
	if err != nil {
		return nil, err
	}
	if len(payload) == 0 {
		return nil, fmt.Errorf("未找到请求体模板，请粘贴 JSON 或包含 --data-raw 的 read 请求")
	}
	for _, key := range []string{"b", "c"} {
		id, ok := payload[key].(string)
		if !ok || strings.TrimSpace(id) == "" {
			return nil, fmt.Errorf("模板缺少有效的 %q 字段（书籍/章节 ID）", key)
		}
	}
	return payload, nil
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
