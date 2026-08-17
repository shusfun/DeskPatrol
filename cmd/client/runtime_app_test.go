package main

import (
	"testing"

	"deskpatrol/internal/connectionkey"
)

func TestActivationInputRequiresConnectionKey(t *testing.T) {
	key, err := connectionkey.Build("https://monitor.example.com", "ABCDEF-GHIJKL-MNOPQR-STUVWX")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := connectionkey.Parse(key)
	if err != nil || payload.ServerURL != "https://monitor.example.com" {
		t.Fatalf("连接密钥解析失败: %+v %v", payload, err)
	}
}
