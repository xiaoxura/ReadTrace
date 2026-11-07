package weread

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SignKey is the salt used by the web reader when signing read payloads.
const SignKey = "3c5c8717f3daf09iop3423zafeqoi"

// Quote mirrors Python's urllib.parse.quote(value, safe="") so that the
// request signature matches the one produced by the official web client.
func Quote(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func toSignedString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		if x {
			return "True"
		}
		return "False"
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case int32:
		return strconv.FormatInt(int64(x), 10)
	case uint64:
		return strconv.FormatUint(x, 10)
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(x), 'f', -1, 32)
	case jsonNumber:
		return string(x)
	default:
		return fmt.Sprint(v)
	}
}

// jsonNumber lets callers pass numbers through without importing encoding/json.
type jsonNumber string

// EncodeData builds the "k=v&k=v..." string with keys sorted, matching the
// official client's signature input.
func EncodeData(data map[string]any) string {
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(Quote(toSignedString(data[k])))
	}
	return b.String()
}

// CalHash is a faithful port of the obfuscated checksum routine used by the
// WeRead web client to produce the "s" field.
func CalHash(input string) string {
	var a, c uint64 = 0x15051505, 0x15051505
	length := len(input)
	i := length - 1
	for i > 0 {
		a = 0x7fffffff & (a ^ (uint64(input[i]) << (uint64(length-i) % 30)))
		c = 0x7fffffff & (c ^ (uint64(input[i-1]) << (uint64(i) % 30)))
		i -= 2
	}
	return strconv.FormatUint(a+c, 16)
}

// Signature returns the sha256(ts + rn + key) used for the "sg" field.
func Signature(ts int64, rn int, key string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d%d%s", ts, rn, key)))
	return hex.EncodeToString(sum[:])
}

// SignPayload clones the template and stamps the volatile fields for one
// reading tick. The returned map is safe to marshal as the request body.
func SignPayload(template map[string]any, now, last time.Time, rnd *rand.Rand) map[string]any {
	out := make(map[string]any, len(template)+8)
	for k, v := range template {
		switch k {
		case "s", "sg":
			continue
		}
		out[k] = v
	}

	rt := int(now.Sub(last).Seconds())
	if rt < 1 {
		rt = 1
	}
	if rt > 120 {
		rt = 120
	}

	ts := now.UnixMilli() + rnd.Int63n(1000)
	rn := rnd.Intn(1000)

	out["ct"] = now.Unix()
	out["rt"] = rt
	out["ts"] = ts
	out["rn"] = rn
	out["sg"] = Signature(ts, rn, SignKey)
	out["s"] = CalHash(EncodeData(out))
	return out
}
