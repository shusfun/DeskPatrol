package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestSetupRoutesWorkWithoutDatabase(t *testing.T) {
	app, err := New(filepath.Join(t.TempDir(), "config.json"), t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	for _, path := range []string{"/healthz", "/api/setup/status"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		app.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s 返回 %d: %s", path, response.Code, response.Body.String())
		}
		var value map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil || value["data"] == nil {
			t.Fatalf("%s 响应格式不正确: %v", path, err)
		}
	}
}

func TestDiagnosticInspectionUsesFixedScripts(t *testing.T) {
	for _, kind := range []string{"status", "processes", "services", "displays", "network", "events", "meshagent", "package"} {
		script, operation, timeout, err := diagnosticInspection(kind, "monitor.example.com", 8443)
		if err != nil || script == "" || operation != "inspect_"+kind || timeout < 30*time.Second || timeout > 60*time.Second {
			t.Fatalf("诊断预置 %s 不正确: script=%q operation=%q timeout=%s err=%v", kind, script, operation, timeout, err)
		}
	}
	if _, _, _, err := diagnosticInspection("unknown", "monitor.example.com", 8443); err == nil {
		t.Fatal("未知诊断预置必须被拒绝")
	}
}

func TestBearerTokenRequiresBearerSchemeAndSingleToken(t *testing.T) {
	for _, value := range []string{"", "token", "Basic token", "Bearer", "Bearer one two"} {
		if token, ok := bearerToken(value); ok || token != "" {
			t.Fatalf("应拒绝无效诊断认证头 %q", value)
		}
	}
	if token, ok := bearerToken("bearer debug-token"); !ok || token != "debug-token" {
		t.Fatalf("应接受 Bearer 诊断口令，得到 token=%q ok=%v", token, ok)
	}
}

func TestNewTokenReturnsMatchingDigest(t *testing.T) {
	token, digest, err := newToken()
	if err != nil {
		t.Fatalf("生成诊断访问口令失败: %v", err)
	}
	if token == "" || digest != hash(token) || digest == token {
		t.Fatalf("诊断访问口令及摘要不正确: token=%q digest=%q", token, digest)
	}
}

func TestDebugAuthStoresOnlyBearerTokenDigest(t *testing.T) {
	app := &App{}
	var stored string
	handler := app.debugAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stored, _ = r.Context().Value(debugTokenHashKey).(string)
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/runtime-debug/sessions/session/inspect", nil)
	request.Header.Set("Authorization", "Bearer plain-debug-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("Bearer 诊断认证返回 %d", response.Code)
	}
	if stored != hash("plain-debug-token") || stored == "plain-debug-token" {
		t.Fatalf("诊断认证上下文必须只保存口令摘要，得到 %q", stored)
	}
}
