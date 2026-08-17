package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"deskpatrol/internal/connectionkey"
	"deskpatrol/internal/runtimelog"
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

func TestActivatePersistsCredentialsBeforeAgentSetup(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/client/activate" {
			t.Fatalf("激活阶段不应请求 %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"deviceId": "device-1", "deviceName": "workstation", "deviceToken": "device-token-value"}})
	}))
	defer server.Close()
	app := newTestRuntimeApp(t, server.Client())
	app.service = func() (string, bool) { return "运行中", true }
	key, err := connectionkey.Build(server.URL, "activation-code")
	if err != nil {
		t.Fatal(err)
	}
	status, err := app.Activate(ActivationInput{ConnectionKey: key})
	if err != nil {
		t.Fatal(err)
	}
	if !status.Activated || status.AgentSetupStatus != agentSetupPending {
		t.Fatalf("激活必须先成功并进入 Agent 待准备状态: %#v", status)
	}
	state, err := app.store.Load()
	if err != nil || state.DeviceID != "device-1" || state.DeviceToken != "device-token-value" {
		t.Fatalf("激活凭据未先保存: %#v err=%v", state, err)
	}
}

func TestAgentSetupRetriesWithoutClearingActivation(t *testing.T) {
	var installCalls atomic.Int32
	var serviceReady atomic.Bool
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/client/agent/prepare" || r.Header.Get("Authorization") != "Bearer device-token-value" {
			t.Fatalf("Agent 准备请求不正确: %s %s", r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"agentDownloadUrl": server.URL + "/api/v1/client/agent/download-ticket-value",
			"agentSha256":      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"agentSize":        2048,
		}})
	}))
	defer server.Close()
	app := newTestRuntimeApp(t, server.Client())
	app.service = func() (string, bool) { return "测试服务", serviceReady.Load() }
	app.installer = func(string, string) error {
		if installCalls.Add(1) == 1 {
			return errors.New("用户取消了 UAC")
		}
		serviceReady.Store(true)
		return nil
	}
	state := LocalState{ServerURL: server.URL, DeviceID: "device-1", DeviceName: "workstation", DeviceToken: "device-token-value", AgentSetupStatus: agentSetupPending}
	if err := app.store.Save(state); err != nil {
		t.Fatal(err)
	}
	originalDelays := agentRetryDelays
	agentRetryDelays = []time.Duration{time.Millisecond}
	t.Cleanup(func() { agentRetryDelays = originalDelays })
	app.runAgentSetup()
	saved, err := app.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if saved.DeviceID != state.DeviceID || saved.DeviceToken != state.DeviceToken || saved.AgentSetupStatus != agentSetupReady || installCalls.Load() != 2 {
		t.Fatalf("Agent 重试不得清除激活且最终应就绪: %#v calls=%d", saved, installCalls.Load())
	}
}

func TestAgentDownloadURLMustStayOnDeploymentHost(t *testing.T) {
	for _, value := range []string{
		"https://monitor.example.com/api/v1/client/agent/ticket-value-1234567890",
		"https://monitor.example.com/api/v1/client/agent/ticket-value-1234567890?source=github",
		"https://github.com/api/v1/client/agent/ticket-value-1234567890",
	} {
		valid := validAgentDownloadURL("https://monitor.example.com", value)
		if value == "https://monitor.example.com/api/v1/client/agent/ticket-value-1234567890" && !valid {
			t.Fatalf("部署地址内的 Agent URL 应有效: %s", value)
		}
		if value != "https://monitor.example.com/api/v1/client/agent/ticket-value-1234567890" && valid {
			t.Fatalf("外部或带查询参数的 Agent URL 必须拒绝: %s", value)
		}
	}
}

func newTestRuntimeApp(t *testing.T, client *http.Client) *RuntimeApp {
	t.Helper()
	directory := t.TempDir()
	logger, err := runtimelog.New(filepath.Join(directory, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	return &RuntimeApp{store: &LocalStore{dir: directory}, logger: logger, http: client, installer: func(string, string) error { return nil }, service: func() (string, bool) { return "运行中", true }, agentWake: make(chan struct{}, 1)}
}
