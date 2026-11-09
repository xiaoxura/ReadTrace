package weread

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// encodeWeReadID 移植微信读书网页端的混淆 ID 算法（社区逆向常称 weread_e /
// getValue），bookId 与 chapterUid 在心跳 payload 的 b/c 字段、阅读器 URL、
// 以及部分响应字段里都以该编码形态出现：
//
//	result := md5(value)[:3] + typeFlag（数字=3，其他=4）+ "2" + md5[-2:]
//	         + 每段长度两位 hex + 数据段（多段数字间用 g 分隔）
//	         + 不足 20 位用 md5 补齐
//	         + md5(result)[:3]
//
// 数字 inputs按每 9 位切成多段转 hex；字符串 inputs全部 rune 的 ord 拼成
// 一段 hex。
func encodeWeReadID(value string) string {
	sum := md5.Sum([]byte(value))
	h := hex.EncodeToString(sum[:])

	typeFlag := "4"
	var chunks []string
	if isAllDigits(value) {
		typeFlag = "3"
		for i := 0; i < len(value); i += 9 {
			end := i + 9
			if end > len(value) {
				end = len(value)
			}
			part := value[i:end]
			n, err := strconv.ParseUint(part, 10, 64)
			if err != nil {
				// 理论不可达（isAllDigits + ≤9 位）；退化按字符串处理。
				chunks = []string{hexOfRunes(value)}
				typeFlag = "4"
				break
			}
			chunks = append(chunks, strconv.FormatUint(n, 16))
		}
	} else {
		chunks = append(chunks, hexOfRunes(value))
	}

	var b strings.Builder
	b.WriteString(h[:3])
	b.WriteString(typeFlag)
	b.WriteString("2")
	b.WriteString(h[len(h)-2:])
	for idx, chunk := range chunks {
		fmt.Fprintf(&b, "%02x", len(chunk))
		b.WriteString(chunk)
		if idx < len(chunks)-1 {
			b.WriteString("g")
		}
	}
	out := b.String()
	if len(out) < 20 {
		out += h[:20-len(out)]
	}
	sum2 := md5.Sum([]byte(out))
	out += hex.EncodeToString(sum2[:])[:3]
	return out
}

func hexOfRunes(s string) string {
	var b strings.Builder
	for _, r := range s {
		b.WriteString(strconv.FormatInt(int64(r), 16))
	}
	return b.String()
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ChapterIDFromUID 由数字 chapterUid 计算出阅读请求里 c 字段使用的编码 ID。
func ChapterIDFromUID(uid int64) string {
	return encodeWeReadID(strconv.FormatInt(uid, 10))
}

// NormalizePayloadID 把一个入库标识转换为心跳 payload/阅读页 URL 使用的形态：
//   - 纯数字 ID（chapterUid、原始 bookId）需要 _e() 编码；
//   - 已编码的 20~25 位 hex 串（书城书 bookId、ChapterIDFromUID 的输出）原样使用；
//   - 其余字符串形态（上传书 CB_/Magic/MP_WXS_ 等）需要 _e() 编码——官方阅读页
//     的 URL 与 payload 的 b 对这类 ID 都用编码后的长 hex（与真实抓包互相验证）。
func NormalizePayloadID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if isAllDigits(value) {
		return encodeWeReadID(value)
	}
	if looksEncodedID(value) {
		return value
	}
	return encodeWeReadID(value)
}

// looksEncodedID 判断一段 ID 是否已经是 _e() 编码产物（或可等价直传的裸 hex
// 书 ID），判据保守分两层，避免把真实书 ID 误判成编码串：
//
//  1. 20~25 位纯 hex：保留老规则——书城书 bookId（如 24 位 hex）与章节 ID
//     （23 位 hex）都落在这一范围。这类裸 ID 不是 encodeWeReadID 的产物，
//     没有尾部校验和，不能要求 checksum，只能按范围宽松放行。
//
//  2. 其余长度的串：用「可由 encodeWeReadID 产出」的判据识别真实编码形态，
//     老规则认不出它们（66 字符抓包形态会被二次编码改写成 144 字符；数字
//     分段产物带 'g' 也会被误再编码）：
//     - 字符集仅 hex 与数字分段分隔符 'g'；
//     - 结构头符合算法：3 位 md5 前缀 + typeFlag（'3'|'4'）+ '2' + md5 尾
//     2 位，共 7 位固定形态；
//     - 尾部 3 位校验和 == md5(串去掉尾部 3 位)[:3]（算法固定最后拼上这段）。
//     三条同时成立对非编码串的概率近乎为 0，长 ID 不会误放。
func looksEncodedID(value string) bool {
	if len(value) < 20 {
		return false
	}
	// 第一层：老规则原样保留，20~25 位纯 hex 直传（裸书城书/章节 ID 没有
	// 尾部校验和，只能按范围放行）。数字分段产物带 'g' 会落到第二层。
	if len(value) <= 25 && isHexRunes(value) {
		return true
	}
	return looksEncodedOutput(value)
}

// looksEncodedOutput 按「可由 encodeWeReadID 产出」的形态校验串：结构头 +
// 字符集（hex 与分段符 'g'）+ 尾部 md5 校验和。
func looksEncodedOutput(value string) bool {
	if len(value) < 23 || len([]rune(value)) != len(value) {
		// 算法保证输出至少 20 位本体 + 3 位校验和，且产物都是 ASCII。
		return false
	}
	// 结构头：md5 前 3 位（hex）+ typeFlag（数字='3'，其他='4'）+ '2' +
	// md5 尾 2 位（hex），共 7 位。
	if value[3] != '3' && value[3] != '4' {
		return false
	}
	if value[4] != '2' || !isHexRunes(value[:3]+"2"+value[5:7]) {
		return false
	}
	for i := 7; i < len(value); i++ {
		c := value[i]
		if !isHexByte(c) && c != 'g' {
			return false
		}
	}
	// 尾部校验和：md5(本体)[:3]，按位比对放行大小写差异。
	sum := md5.Sum([]byte(value[:len(value)-3]))
	tail := hex.EncodeToString(sum[:])[:3]
	return strings.EqualFold(tail, value[len(value)-3:])
}

func isHexByte(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func isHexRunes(s string) bool {
	for i := 0; i < len(s); i++ {
		if !isHexByte(s[i]) {
			return false
		}
	}
	return true
}
