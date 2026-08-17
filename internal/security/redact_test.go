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

func TestRedactMeshCtrlAuthQuery(t *testing.T) {
	value := Redact(`Unable to connect to wss://127.0.0.1/control.ashx?auth=temporary-secret`)
	if strings.Contains(value, "temporary-secret") || !strings.Contains(value, "auth=[FILTERED]") {
		t.Fatalf("MeshCtrl 临时认证参数未脱敏: %s", value)
	}
}
