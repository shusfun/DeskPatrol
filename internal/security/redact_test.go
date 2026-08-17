package security

import (
	"strings"
	"testing"
)

func TestRedactConnectionKey(t *testing.T) {
	value := Redact(`{"connectionKey":"dp-link.secret","message":"connectionKey=dp-link.secret"}`)
	if strings.Contains(value, "dp-link.secret") || !strings.Contains(value, "[FILTERED]") {
		t.Fatalf("连接密钥未脱敏: %s", value)
	}
}
