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
