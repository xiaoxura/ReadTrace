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

// NormalizePayloadID 把一个入库标识转换为心跳 payload 字段使用的形态：
// - 纯数字 ID（书架导入的新书、chapterInfos 的 chapterUid）需要 e() 编码；
// - hex 型编码串（内置书池、阅读页 URL 里的 encodeId、ChapterIDFromUID）原样使用。
func NormalizePayloadID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if isAllDigits(value) {
		return encodeWeReadID(value)
	}
	return value
}
