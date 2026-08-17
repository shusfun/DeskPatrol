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
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"deviceId": "device-1", "deviceName": "workstation", "deviceToken": "device-token-value", "agentSetupRequired": true}})
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

func TestDeletedDeviceClearsStateButKeepsInstallationID(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/client/status" {
			t.Fatalf("删除检查请求不正确: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusGone)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "设备已被管理员删除，请重新激活"})
	}))
	defer server.Close()
	app := newTestRuntimeApp(t, server.Client())
	installationID, err := app.store.InstallationID()
	if err != nil {
		t.Fatal(err)
	}
	state := LocalState{ServerURL: server.URL, DeviceID: "device-1", DeviceName: "workstation", DeviceToken: "device-token-value", AgentSetupRequired: true, AgentSetupStatus: agentSetupPending}
	if err := app.store.Save(state); err != nil {
		t.Fatal(err)
	}
	status := app.Status()
	if status.Activated || status.Connection != "设备已删除，请重新激活" {
		t.Fatalf("删除后 Runtime 必须回到激活状态: %#v", status)
	}
	cleared, err := app.store.Load()
	if err != nil || cleared.DeviceID != "" {
		t.Fatalf("删除后本机凭据未清除: %#v err=%v", cleared, err)
	}
	preserved, err := app.store.InstallationID()
	if err != nil || preserved != installationID {
		t.Fatalf("删除后安装 ID 必须保留: before=%s after=%s err=%v", installationID, preserved, err)
	}
}

func TestAgentStateWriterCannotRestoreReplacedCredentials(t *testing.T) {
	app := newTestRuntimeApp(t, http.DefaultClient)
	oldState := LocalState{ServerURL: "https://old.example.com", DeviceID: "device-1", DeviceToken: "old-token", AgentSetupRequired: true, AgentSetupStatus: agentSetupPending}
	newState := LocalState{ServerURL: "https://new.example.com", DeviceID: "device-1", DeviceToken: "new-token", AgentSetupStatus: agentSetupReady}
	if err := app.store.Save(newState); err != nil {
		t.Fatal(err)
	}
	if app.persistAgentSetup(oldState, agentSetupFailed, "旧任务错误", time.Now(), true) {
		t.Fatal("旧 Agent 任务不得覆盖已轮换的设备凭据")
	}
	saved, err := app.store.Load()
	if err != nil || saved.DeviceToken != "new-token" || saved.AgentSetupStatus != agentSetupReady {
		t.Fatalf("新凭据被旧任务覆盖: %#v err=%v", saved, err)
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
