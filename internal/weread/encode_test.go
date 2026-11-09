package weread

import (
	"strings"
	"testing"
)

// TestNormalizePayloadIDIdempotentEncoded 转正归档探针
// TestReviewProbeLongIDDoubleEncoding（缺陷 21）：上传书的真实编码产物
// （66 字符抓包形态）再次正规化必须原样返回，不得二次编码。
func TestNormalizePayloadIDIdempotentEncoded(t *testing.T) {
	in := encodeWeReadID("CB_45SBDGBEC9jf74V75BAdW7jp")
	if len(in) <= 25 {
		t.Fatalf("样本应长于老规则认得的范围，got %d", len(in))
	}
	if out := NormalizePayloadID(in); out != in {
		t.Fatalf("66 字符编码形态被二次编码: %d -> %d", len(in), len(out))
	}
}

// TestLooksEncodedIDAcceptsAlgorithmOutputs 校验第二层判据对算法全部产物
// 成立：字符串编码（无 'g'）、数字分位编码（带 'g'，老规则会漏）。
func TestLooksEncodedIDAcceptsAlgorithmOutputs(t *testing.T) {
	samples := []string{
		"CB_45SBDGBEC9jf74V75BAdW7jp",  // 上传书长 ID：66 字符纯 hex
		"MP_WXS_COVER123456789",        // 公众号文章形态
		"Magic_translated_abc_def_ghi", // Magic 形态
		"3300060341",                   // 10 位数字：分两段，产物含 'g'
		"1000000000000000000000000000", // 28 位数字：分三段
		"0", "42",                      // 短数字：产物 23 位
		"smallcb", "x", // 短字符串：本体走 md5 填充
	}
	for _, s := range samples {
		enc := encodeWeReadID(s)
		if !looksEncodedID(enc) {
			t.Fatalf("looksEncodedID 拒绝了算法产物 %q（输入 %q）", enc, s)
		}
		if out := NormalizePayloadID(enc); out != enc {
			t.Fatalf("NormalizePayloadID 不幂等: %q -> %q", enc, out)
		}
	}
}

// TestNormalizePayloadIDNonEncodedStillEncoded 防误判：长得不像产物的串
// 仍要编码。
func TestNormalizePayloadIDNonEncodedStillEncoded(t *testing.T) {
	raw := []string{"CB_45SBDGBEC9jf74V75BAdW7jp", "mpbook", "wr_tem_px_j1SPkEpD", "Magic_a", "3300060341"}
	for _, s := range raw {
		enc := NormalizePayloadID(s)
		if enc == s && !isAllDigits(s) {
			t.Fatalf("原始 ID %q 未被编码", s)
		}
		if !looksEncodedID(enc) {
			t.Fatalf("编码产物 %q 未被判为已编码（输入 %q）", enc, s)
		}
	}
}

// TestLooksEncodedIDRejectsForgedHead 结构头对但校验和不对的长串会被拒绝
// （走编码路径），固定不出现误放行。
func TestLooksEncodedIDRejectsForgedHead(t *testing.T) {
	// 66 字符、结构头形似产物，但尾部校验和随机。
	forged := "17c424b3643425f343553424447424543396a66373456373542416457376a70879"
	forged = forged[:len(forged)-3] + "000"
	if looksEncodedID(forged) {
		t.Fatal("校验和不匹配的长串不应被认成已编码")
	}
	// 26~39 位、头尾都像的串无法通过校验和门（库里没有这种抓包形态）。
	if looksEncodedID(strings.Repeat("abc", 10)) { // 30 位，含非法 'b' 之外的字母…abc 全是 hex
		t.Fatal("随机 hex 不应被认成已编码")
	}
}
