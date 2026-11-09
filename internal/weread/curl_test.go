package weread

import "testing"

func TestParseCurlChromeStyle(t *testing.T) {
	raw := `curl 'https://weread.qq.com/web/book/read' \
  -H 'accept: application/json, text/plain, */*' \
  -H 'cookie: wr_vid=abc123; wr_skey=def456; wr_fp=xyz' \
  --data-raw '{"appId":"wb1","b":"book123456789","c":"chap123456789","sm":"中文内容","ci":1,"co":2,"pr":3,"rt":4,"ts":5,"rn":6,"sg":"sig","ct":7,"ps":"p","pc":"p","s":"sum"}'`

	imp, err := ParseCurl(raw)
	if err != nil {
		t.Fatalf("ParseCurl: %v", err)
	}
	if imp.Cookies["wr_vid"] != "abc123" || imp.Cookies["wr_skey"] != "def456" {
		t.Fatalf("cookies = %#v", imp.Cookies)
	}
	if imp.Payload["sm"] != "中文内容" {
		t.Fatalf("sm = %v", imp.Payload["sm"])
	}
	if _, ok := imp.Payload["s"]; ok {
		t.Fatal("signature field s should be stripped during import")
	}
	if imp.URL != "https://weread.qq.com/web/book/read" {
		t.Fatalf("url = %s", imp.URL)
	}
}

func TestParseBareCookie(t *testing.T) {
	imp, err := ParseCurl("wr_vid=1; wr_skey=2")
	if err != nil {
		t.Fatalf("ParseCurl: %v", err)
	}
	if len(imp.Cookies) != 2 || imp.Cookies["wr_skey"] != "2" {
		t.Fatalf("cookies = %#v", imp.Cookies)
	}
}

func TestDecodePayloadRemovesSignature(t *testing.T) {
	payload, err := DecodePayload(`{"b":"x","s":"1","sg":"2"}`)
	if err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if _, ok := payload["s"]; ok {
		t.Fatal("s should be removed")
	}
	if _, ok := payload["sg"]; ok {
		t.Fatal("sg should be removed")
	}
}

// TestParseReadTemplateApostrophe 转正归档探针 TestReviewProbeCurlApostrophe
// （缺陷 22）：payload 里的英文单引号以 bash 分段转义 `'`'` 写法出现时，
// 不能把 --data-raw 截断。
func TestParseReadTemplateApostrophe(t *testing.T) {
	payload, err := ParseReadTemplate(`curl 'https://weread.qq.com/web/book/read' --data-raw '{"b":"book","c":"chapter","sm":"Don'"'"'t stop"}'`)
	if err != nil {
		t.Fatalf("ParseReadTemplate: %v", err)
	}
	if payload["sm"] != "Don't stop" {
		t.Fatalf("sm = %v", payload["sm"])
	}
}

// TestShellWords 覆盖分词规则各形态：单引号、双引号转义、裸 token、
// 反斜杠转义与拼接后的多段引号。
func TestShellWords(t *testing.T) {
	tests := []struct {
		raw  string
		want []string
	}{
		{`a 'b c' "d e" f`, []string{"a", "b c", "d e", "f"}},
		{`'It'\''s "ok"' tail`, []string{`It's "ok"`, "tail"}},
		// shell 语义：无引号 token 内嵌的引号会被 shell 吃掉（解析器只按
		// 分词规则重构 token，正确行为就是这样）。
		{`{"sm":"Don't"}`, []string{`{sm:Don't}`}},
		{`'{"sm":"It'"'"'s"}'`, []string{`{"sm":"It's"}`}},
		{`"escaped \"quote\" done"`, []string{`escaped "quote" done`}}, // 双引号转义
		{`bare\ space next`, []string{"bare space", "next"}},           // 反斜杠转义空格 → 同一 token
		{`a'""'b`, []string{`a""b`}},                                   // 单引号内字面量，引号段拼接
	}
	for _, tc := range tests {
		words := shellWords(tc.raw)
		if len(words) != len(tc.want) {
			t.Fatalf("raw=%q words=%#v want=%#v", tc.raw, words, tc.want)
		}
		for i := range words {
			if words[i] != tc.want[i] {
				t.Fatalf("raw=%q words=%#v want=%#v", tc.raw, words, tc.want)
			}
		}
	}
	if words := shellWords(`"unterminated`); len(words) != 1 || words[0] != "unterminated" {
		t.Fatalf("unterminated quote words=%#v", words)
	}
}

// TestParseCurlMultilineWithContinuation 多行续行（Chrome 复制的换行反斜杠
// + 分段转义单引号）照常解析。
func TestParseCurlMultilineWithContinuation(t *testing.T) {
	raw := "curl 'https://weread.qq.com/web/book/read' \\\n" +
		"  -H 'cookie: wr_vid=mvid; wr_skey=mskey' \\\n" +
		"  --data-raw '{\"b\":\"book\",\"c\":\"chapter\",\"sm\":\"It'\"'\"'s fine\"}'"
	imp, err := ParseCurl(raw)
	if err != nil {
		t.Fatalf("ParseCurl: %v", err)
	}
	if imp.Cookies["wr_vid"] != "mvid" || imp.Cookies["wr_skey"] != "mskey" {
		t.Fatalf("cookies = %#v", imp.Cookies)
	}
	if imp.Payload["sm"] != "It's fine" {
		t.Fatalf("sm = %v", imp.Payload["sm"])
	}
	if imp.URL != "https://weread.qq.com/web/book/read" {
		t.Fatalf("url = %q", imp.URL)
	}
}

// TestParseCurlUnquotedAndCookieArg 回归双引号与无引号形态，并覆盖 -b/--cookie
// 显式参数与 Cookie: 头并存时的优先级（-b 获胜）。
func TestParseCurlUnquotedAndCookieArg(t *testing.T) {
	raw := `curl "https://weread.qq.com/web/book/read" -H "content-type: application/json" ` +
		`-H "cookie: wr_vid=header_vid; wr_skey=header_key" ` +
		`-b "wr_vid=arg_vid; wr_skey=arg_key" --data '{"b":"book","c":"chapter","s":"s","sg":"sg"}'`
	imp, err := ParseCurl(raw)
	if err != nil {
		t.Fatalf("ParseCurl: %v", err)
	}
	if imp.Cookies["wr_vid"] != "arg_vid" || imp.Cookies["wr_skey"] != "arg_key" {
		t.Fatalf("-b 参数应优先于 Cookie 头: %#v", imp.Cookies)
	}
	if imp.Headers["content-type"] != "application/json" {
		t.Fatalf("headers = %#v", imp.Headers)
	}
	if _, ok := imp.Payload["s"]; ok {
		t.Fatal("s 字段应被剥离")
	}
	if imp.URL != "https://weread.qq.com/web/book/read" {
		t.Fatalf("url = %q", imp.URL)
	}
}

// TestParseReadTemplateChromeStyleRegression Chrome Copy as cURL 的典型完整
// 样例（多行、双引号头、JSON 串内含单引号）走模板导入也能解析。
func TestParseReadTemplateChromeStyleRegression(t *testing.T) {
	raw := "curl 'https://weread.qq.com/web/book/read' \\\n" +
		"  -H 'connection: keep-alive' \\\n" +
		"  -H 'content-type: application/json;charset=UTF-8' \\\n" +
		"  --data-raw '{\"appId\":\"wcOg8PS1\",\"b\":\"book\",\"c\":\"chapter\",\"ci\":7,\"ct\":0,\"co\":100,\"mp\":0,\"rt\":88,\"r\":0,\"sm\":1.2,\"s\":\"sig\"}'"
	payload, err := ParseReadTemplate(raw)
	if err != nil {
		t.Fatalf("ParseReadTemplate: %v", err)
	}
	if payload["b"] != "book" || payload["c"] != "chapter" {
		t.Fatalf("payload = %#v", payload)
	}
	if _, ok := payload["s"]; ok {
		t.Fatal("s 应剥离")
	}
}
