package connectionkey

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildAndParse(t *testing.T) {
	key, err := Build("https://monitor.example.test/", "ABCDEF-GHIJKL-MNOPQR-STUVWX")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key, Prefix) {
		t.Fatalf("连接密钥前缀不正确: %s", key)
	}
	payload, err := Parse(key)
	if err != nil {
		t.Fatal(err)
	}
	if payload.ServerURL != "https://monitor.example.test" || payload.ActivationCode != "ABCDEF-GHIJKL-MNOPQR-STUVWX" {
		t.Fatalf("连接密钥内容不正确: %+v", payload)
	}
}

func TestParseRejectsLegacyAndMalformedKeys(t *testing.T) {
	for _, value := range []string{"", "ABCDEF-GHIJKL-MNOPQR-STUVWX", "dp-link.invalid", "http://monitor.example.test"} {
		if _, err := Parse(value); err == nil {
			t.Fatalf("应拒绝 %q", value)
		}
	}
	raw, err := json.Marshal(Payload{ServerURL: "https://monitor.example.test/path", ActivationCode: "code"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(Prefix + base64.RawURLEncoding.EncodeToString(raw)); err == nil {
		t.Fatal("应拒绝包含路径的服务地址")
	}
}
