package notify

import (
	"regexp"
	"strings"
)

// 推送事件类型。每种类型对应两个模板键：
// {kind}_title（标题）与 {kind}_body（内容），存放在渠道的 Templates 里。
const (
	KindSuccess = "success" // 阅读成功
	KindFailed  = "failed"  // 阅读失败（含手动停止）
	KindExpired = "expired" // 账号失效
)

// 模板占位符：{{account}} {{plan}} {{elapsed}} {{status}} {{error}}
// {{success}} {{failed}} {{time}}。未出现的变量渲染为空字符串。
var tplVarRe = regexp.MustCompile(`\{\{\s*([a-z_]+)\s*\}\}`)

// ApplyTemplate renders one template, replacing {{变量}} placeholders. Any key
// outside the sanctioned set is dropped for a predictable output.
func ApplyTemplate(tpl string, v map[string]string) string {
	out := tplVarRe.ReplaceAllStringFunc(tpl, func(m string) string {
		key := tplVarRe.FindStringSubmatch(m)[1]
		return v[key]
	})
	return strings.TrimSpace(out)
}

// BuildMessage renders the title/content for a push event. The fallback pair
// is this project's built-in wording; user templates override it when set for
// the kind's {kind}_title / {kind}_body keys.
func BuildMessage(templates map[string]string, kind, fallbackTitle, fallbackBody string, v map[string]string) (string, string) {
	templates = normalize(templates)
	if t := templates[kind+"_title"]; t != "" {
		fallbackTitle = ApplyTemplate(t, v)
	}
	if b := templates[kind+"_body"]; b != "" {
		fallbackBody = ApplyTemplate(b, v)
	}
	return fallbackTitle, fallbackBody
}

// normalize strips whitespace-only entries so blanks fall back to defaults.
func normalize(in map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		if v = strings.TrimSpace(v); v != "" {
			out[k] = v
		}
	}
	return out
}
