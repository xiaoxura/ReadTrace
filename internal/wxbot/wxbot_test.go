// wxbot 单元测试：AES-ECB 加解密、协议解析、命令解析。
package wxbot

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestECBEncryptDecryptRoundTrip(t *testing.T) {
	key := bytes.Repeat([]byte{0x2b}, 16)
	plain := []byte("PNG 二维码内容——————————随便的payload123")
	cipher, err := ecbEncrypt(key, plain)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if len(cipher) != ecbPKCS7PaddedSize(len(plain)) {
		t.Fatalf("cipher size = %d, want %d", len(cipher), ecbPKCS7PaddedSize(len(plain)))
	}
	// PKCS7 填充校验+去填充还原 (brute-force ECB semantics)。
	cipher2 := append([]byte{}, cipher...)
	plain2, err := ecbDecrypt(key, cipher2)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(plain2, plain) {
		t.Fatalf("roundtrip mismatch")
	}
	// NewCipher 对 128/192/256 位 key 都合法，这里应成功不 panic。
	if _, err := ecbEncrypt(bytes.Repeat([]byte{1}, 32), plain); err != nil {
		t.Fatalf("aes-256 encrypt should not fail: %v", err)
	}
}

func TestECBPaddedSize(t *testing.T) {
	cases := []struct{ in, want int }{
		{0, 16}, {15, 16}, {16, 32}, {17, 32}, {161, 176},
	}
	for _, c := range cases {
		if got := ecbPKCS7PaddedSize(c.in); got != c.want {
			t.Fatalf("size(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestU64StrAcceptsNumberOfString(t *testing.T) {
	var a struct {
		ID u64str `json:"message_id"`
		B  u64str `json:"svr_id"`
	}
	if err := json.Unmarshal([]byte(`{"message_id": 12345678901234567890, "svr_id": "77"}`), &a); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if a.ID != "12345678901234567890" || a.B != "77" {
		t.Fatalf("ids = %q %q", a.ID, a.B)
	}
}

func TestMessageText(t *testing.T) {
	m := &Message{ItemList: []MessageItem{
		{Type: itemTypeText, TextItem: &MessageTextItem{Text: "今天"}},
		{Type: itemTypeImage},
		{Type: itemTypeText, TextItem: &MessageTextItem{Text: "读多久"}},
	}}
	if got := m.Text(); got != "今天读多久" {
		t.Fatalf("Text() = %q", got)
	}
}

func TestOTPOnly(t *testing.T) {
	cases := map[string]string{
		"1234":      "1234",
		"  456789 ": "456789",
		"12":        "",
		"hello":     "",
		"1a34":      "",
		"开始":        "",
	}
	for in, want := range cases {
		if got := otpOnly(in); got != want {
			t.Fatalf("otpOnly(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseHHMM(t *testing.T) {
	if h, m, err := parseHHMM("22:30"); err != nil || h != 22 || m != 30 {
		t.Fatalf("parse 22:30 = %d %d %v", h, m, err)
	}
	if _, _, err := parseHHMM("25:00"); err == nil {
		t.Fatalf("25:00 should fail")
	}
	if _, _, err := parseHHMM("7:5"); err != nil {
		t.Fatalf("7:5 should pass, got %v", err)
	}
	if _, _, err := parseHHMM("abc"); err == nil {
		t.Fatalf("abc should fail")
	}
}

func TestCronHuman(t *testing.T) {
	if got := cronHuman("30 22 * * *"); got != "每天 22:30" {
		t.Fatalf("cronHuman = %q", got)
	}
	if got := cronHuman("*/5 * * * *"); got == "每天 22:30" {
		t.Fatalf("non-daily cron should stay raw: %q", got)
	}
}

func TestSplitFirstAndNormalize(t *testing.T) {
	a, b := splitFirst("定时 新增 22:30 30")
	if a != "定时" || b != "新增 22:30 30" {
		t.Fatalf("splitFirst = %q,%q", a, b)
	}
	if normalizeCommand("Help") != "menu" {
		t.Fatalf("normalize Help")
	}
	if normalizeCommand("开始") != "start" {
		t.Fatalf("normalize 开始")
	}
}

func TestNum64(t *testing.T) {
	cases := []struct {
		in   any
		want int64
		ok   bool
	}{
		{float64(5), 5, true},
		{int64(7), 7, true},
		{"42", 42, true},
		{"x", 0, false},
		{nil, 0, false},
	}
	for _, c := range cases {
		got, ok := num64(c.in)
		if got != c.want || ok != c.ok {
			t.Fatalf("num64(%v) = %d %v", c.in, got, ok)
		}
	}
}
