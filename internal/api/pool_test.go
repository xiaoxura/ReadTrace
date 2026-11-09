package api

import (
	"strings"
	"testing"
)

func TestValidateDefaultTemplate(t *testing.T) {
	ok := `{"b": "ce032b305a9bc1ce0b0dd2a", "c": "7f632b502707f6ffaa6bf2e", "ci": 27}`
	got, err := validateDefaultTemplate(ok)
	if err != nil {
		t.Fatalf("valid template rejected: %v", err)
	}
	// 规范化后仍是可解析的 JSON，且保留 b/c。
	if got == "" {
		t.Fatal("normalized template empty")
	}

	cases := []string{
		"",                 // 空串
		"not json",         // 非对象
		`[]`,               // 数组
		`{}`,               // 缺 b/c
		`{"b":"x"}`,        // 缺 c
		`{"b":"","c":"y"}`, // b 为空
		`{"b":1,"c":"y"}`,  // b 非字符串
	}
	for _, c := range cases {
		if _, err := validateDefaultTemplate(c); err == nil {
			t.Errorf("template %q accepted, want rejection", c)
		}
	}
}

func TestValidateDefaultTemplateAcceptsCurl(t *testing.T) {
	raw := `curl --url 'https://weread.qq.com/web/book/read' \
		-H 'content-type: application/json;charset=UTF-8' \
		--data-raw '{"b":"book-1","c":"chapter-1","ci":7,"co":381}'`
	got, err := validateDefaultTemplate(raw)
	if err != nil {
		t.Fatalf("curl template rejected: %v", err)
	}
	if !strings.Contains(got, `"b":"book-1"`) || !strings.Contains(got, `"c":"chapter-1"`) {
		t.Fatalf("unexpected normalized template: %s", got)
	}
}
