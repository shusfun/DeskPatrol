package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"deskpatrol/internal/buildinfo"
	"deskpatrol/internal/runtimelog"
	"github.com/wailsapp/wails/v3/pkg/application"
)

type RuntimeApp struct {
	mu      sync.RWMutex
	queueMu sync.Mutex
	app     *application.App
	window  application.Window
	store   *LocalStore
	logger  *runtimelog.Logger
}

type RuntimeStatus struct {
	Activated       bool   `json:"activated"`
	ServerURL       string `json:"serverUrl"`
	DeviceID        string `json:"deviceId"`
	DeviceName      string `json:"deviceName"`
	Architecture    string `json:"architecture"`
	ClientVersion   string `json:"clientVersion"`
	MeshAgentStatus string `json:"meshAgentStatus"`
	Connection      string `json:"connection"`
	LastHeartbeat   string `json:"lastHeartbeat"`
	ScreenCount     int    `json:"screenCount"`
	LastError       string `json:"lastError"`
}

type ActivationInput struct {
	ServerURL      string `json:"serverUrl"`
	ActivationCode string `json:"activationCode"`
}

func NewRuntimeApp() (*RuntimeApp, error) {
	store, err := NewLocalStore()
	if err != nil {
		return nil, err
	}
	logger, err := runtimelog.New(store.LogsDir())
	if err != nil {
		return nil, err
	}
	logger.Info("runtime client started version=%s architecture=%s", buildinfo.Version, runtime.GOARCH)
	app := &RuntimeApp{store: store, logger: logger}
	go app.flushFrontendErrors()
	return app, nil
}

func (a *RuntimeApp) SetApplication(app *application.App) { a.mu.Lock(); a.app = app; a.mu.Unlock() }
func (a *RuntimeApp) SetWindow(window application.Window) {
	a.mu.Lock()
	a.window = window
	a.mu.Unlock()
}
func (a *RuntimeApp) ShowWindow() {
	a.mu.RLock()
	window := a.window
	a.mu.RUnlock()
	if window != nil {
		window.Show()
		window.UnMinimise()
		window.Focus()
	}
}

func (a *RuntimeApp) Status() RuntimeStatus {
	state, err := a.store.Load()
	status := RuntimeStatus{Architecture: runtime.GOARCH, ClientVersion: buildinfo.Version, MeshAgentStatus: meshAgentServiceStatus(), Connection: "未激活"}
	if err != nil {
		status.LastError = err.Error()
		return status
	}
	status.Activated = state.DeviceID != ""
	status.ServerURL = state.ServerURL
	status.DeviceID = state.DeviceID
	status.DeviceName = state.DeviceName
	if status.Activated {
		status.Connection = "正在读取 Linux 设备状态"
		if err := a.refreshRemoteStatus(&status, state); err != nil {
			status.Connection = "Linux 服务不可达"
			status.LastError = err.Error()
			a.logger.Error("runtime status request failed error=%s", err)
		}
	}
	return status
}

func (a *RuntimeApp) refreshRemoteStatus(status *RuntimeStatus, state LocalState) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, state.ServerURL+"/api/v1/client/status", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+state.DeviceToken)
	response, err := (&http.Client{}).Do(req)
	if err != nil {
		return fmt.Errorf("读取 Linux 设备状态失败: %w", err)
	}
	defer response.Body.Close()
	var envelope struct {
		Data struct {
			DeviceName  string     `json:"deviceName"`
			Status      string     `json:"status"`
			ScreenCount int        `json:"screenCount"`
			LastSeenAt  *time.Time `json:"lastSeenAt"`
		} `json:"data"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("Linux 状态响应无法读取: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return errors.New(firstNonEmpty(envelope.Error, "Linux 状态接口拒绝请求"))
	}
	status.DeviceName = envelope.Data.DeviceName
	status.ScreenCount = envelope.Data.ScreenCount
	if envelope.Data.LastSeenAt != nil {
		status.LastHeartbeat = envelope.Data.LastSeenAt.Format(time.RFC3339)
	}
	switch envelope.Data.Status {
	case "online":
		status.Connection = "MeshAgent 已连接"
	case "pending":
		status.Connection = "等待 MeshAgent 首次连接"
	case "locked":
		status.Connection = "设备已锁屏"
	default:
		status.Connection = "MeshAgent 离线"
	}
	return nil
}

func (a *RuntimeApp) Logs(query runtimelog.Query) ([]runtimelog.Entry, error) {
	return a.logger.Read(query)
}

func (a *RuntimeApp) ReportFrontendError(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return errors.New("前端异常内容不能为空")
	}
	a.queueMu.Lock()
	err := a.store.AppendFrontendError(raw)
	a.queueMu.Unlock()
	if err != nil {
		return err
	}
	a.logger.Error("runtime frontend error captured")
	go a.flushFrontendErrors()
	return nil
}

func (a *RuntimeApp) Activate(input ActivationInput) (RuntimeStatus, error) {
	serverURL, err := normalizeServerURL(input.ServerURL)
	if err != nil {
		return a.Status(), err
	}
	code := strings.TrimSpace(input.ActivationCode)
	if code == "" {
		return a.Status(), errors.New("激活码不能为空")
	}
	installationID, err := a.store.InstallationID()
	if err != nil {
		return a.Status(), err
	}
	hostname, _ := os.Hostname()
	payload := map[string]string{"code": code, "installationId": installationID, "architecture": runtime.GOARCH, "deviceName": hostname}
	raw, _ := json.Marshal(payload)
	requestContext, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestContext, http.MethodPost, serverURL+"/api/v1/client/activate", bytes.NewReader(raw))
	if err != nil {
		return a.Status(), err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{}).Do(req)
	if err != nil {
		a.logger.Error("activation request failed error=%s", err)
		return a.Status(), fmt.Errorf("连接 DeskPatrol 服务失败: %w", err)
	}
	defer response.Body.Close()
	var envelope struct {
		Data struct {
			DeviceID         string `json:"deviceId"`
			DeviceName       string `json:"deviceName"`
			AgentDownloadURL string `json:"agentDownloadUrl"`
			AgentSHA256      string `json:"agentSha256"`
			DeviceToken      string `json:"deviceToken"`
		} `json:"data"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		return a.Status(), fmt.Errorf("服务响应无法读取: %w", err)
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated {
		a.logger.Error("activation rejected status=%d error=%s", response.StatusCode, envelope.Error)
		return a.Status(), errors.New(firstNonEmpty(envelope.Error, "激活失败"))
	}
	if envelope.Data.DeviceID == "" || envelope.Data.AgentDownloadURL == "" || len(envelope.Data.AgentSHA256) != 64 || envelope.Data.DeviceToken == "" {
		return a.Status(), errors.New("激活响应缺少设备凭据")
	}
	if err := installMeshAgentElevated(envelope.Data.AgentDownloadURL, envelope.Data.AgentSHA256); err != nil {
		return a.Status(), err
	}
	if err := a.store.Save(LocalState{ServerURL: serverURL, DeviceID: envelope.Data.DeviceID, DeviceName: envelope.Data.DeviceName, DeviceToken: envelope.Data.DeviceToken}); err != nil {
		return a.Status(), err
	}
	a.logger.Info("runtime activated deviceId=%s server=%s", envelope.Data.DeviceID, serverURL)
	go a.flushFrontendErrors()
	return a.Status(), nil
}

func (a *RuntimeApp) flushFrontendErrors() {
	a.queueMu.Lock()
	defer a.queueMu.Unlock()
	state, err := a.store.Load()
	if err != nil || state.ServerURL == "" || state.DeviceToken == "" {
		return
	}
	items, err := a.store.FrontendErrors()
	if err != nil || len(items) == 0 {
		return
	}
	raw, _ := json.Marshal(map[string]any{"items": items})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, state.ServerURL+"/api/v1/client/runtime-errors", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+state.DeviceToken)
	response, err := (&http.Client{}).Do(req)
	if err != nil {
		a.logger.Error("runtime frontend error upload failed error=%s", err)
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		a.logger.Error("runtime frontend error upload rejected status=%d", response.StatusCode)
		return
	}
	if err := a.store.ClearFrontendErrors(); err != nil {
		a.logger.Error("runtime frontend error queue cleanup failed error=%s", err)
	}
}

func normalizeServerURL(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("Linux 地址必须是无路径的 HTTPS 地址")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
