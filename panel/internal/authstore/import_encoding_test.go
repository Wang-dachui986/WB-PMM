package authstore

import "testing"

func TestCredentialUTF8BOM(t *testing.T) {
	for _, text := range []string{
		`{"uid":"fake001","accessToken":"synthetic","domain":"codebuddy.cn"}`,
		`{"account":{"uid":"fake001"},"auth":{"accessToken":"synthetic","domain":"www.codebuddy.cn"}}`,
	} {
		raw := append([]byte{0xef, 0xbb, 0xbf}, []byte(text)...)
		a, err := Parse(raw)
		if err != nil || a.UID != "fake001" || a.AccessToken != "synthetic" {
			t.Fatalf("BOM credential not parsed: %v", err)
		}
	}
	for _, raw := range [][]byte{{0xef, 0xbb, 0xbf}, {0xff, 0xfe, '{', 0, '}', 0}, append([]byte{0xef, 0xbb, 0xbf}, []byte(`{"uid":"fake001"}`)...)} {
		if _, err := Parse(raw); err == nil {
			t.Fatal("invalid credential accepted")
		}
	}
}
