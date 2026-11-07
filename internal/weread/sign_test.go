package weread

import "testing"

func TestCalHashMatchesOfficialClient(t *testing.T) {
	data := map[string]any{
		"appId": "wb182564874603h266381671",
		"b":     "ce032b305a9bc1ce0b0dd2a",
		"c":     "7f632b502707f6ffaa6bf2e",
		"ci":    27,
		"co":    389,
		"sm":    "19聚会《三体》网友的聚会地点是一处僻静",
		"pr":    74,
		"rt":    15,
		"ts":    int64(1744264311434),
		"rn":    466,
		"sg":    "2b2ec618394b99deea35104168b86381da9f8946d4bc234e062fa320155409fb",
		"ct":    int64(1744264311),
		"ps":    "4ee326507a65a465g015fae",
		"pc":    "aab32e207a65a466g010615",
	}
	encoded := EncodeData(data)
	const wantEncoded = "appId=wb182564874603h266381671&b=ce032b305a9bc1ce0b0dd2a&c=7f632b502707f6ffaa6bf2e&ci=27&co=389&ct=1744264311&pc=aab32e207a65a466g010615&pr=74&ps=4ee326507a65a465g015fae&rn=466&rt=15&sg=2b2ec618394b99deea35104168b86381da9f8946d4bc234e062fa320155409fb&sm=19%E8%81%9A%E4%BC%9A%E3%80%8A%E4%B8%89%E4%BD%93%E3%80%8B%E7%BD%91%E5%8F%8B%E7%9A%84%E8%81%9A%E4%BC%9A%E5%9C%B0%E7%82%B9%E6%98%AF%E4%B8%80%E5%A4%84%E5%83%BB%E9%9D%99&ts=1744264311434"
	if encoded != wantEncoded {
		t.Fatalf("EncodeData mismatch\n got: %s\nwant: %s", encoded, wantEncoded)
	}
	if got := CalHash(encoded); got != "b352ad8c" {
		t.Fatalf("CalHash = %s, want b352ad8c", got)
	}
}

func TestSignature(t *testing.T) {
	got := Signature(1744264311434, 466, SignKey)
	const want = "2b2ec618394b99deea35104168b86381da9f8946d4bc234e062fa320155409fb"
	if got != want {
		t.Fatalf("Signature = %s, want %s", got, want)
	}
}
