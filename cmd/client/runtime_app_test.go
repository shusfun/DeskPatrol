package main

import "testing"

func TestNormalizeServerURLRequiresOriginHTTPS(t *testing.T) {
	for _, value := range []string{"http://example.com", "https://example.com/path", "https://example.com?token=secret", "example.com"} {
		if _, err := normalizeServerURL(value); err == nil {
			t.Fatalf("应拒绝 %q", value)
		}
	}
	value, err := normalizeServerURL(" https://monitor.example.com ")
	if err != nil || value != "https://monitor.example.com" {
		t.Fatalf("合法地址归一化失败: %q %v", value, err)
	}
}
