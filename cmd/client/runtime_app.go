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
	"deskpatrol/internal/connectionkey"
	"deskpatrol/internal/runtimelog"
	"deskpatrol/internal/security"
	"github.com/wailsapp/wails/v3/pkg/application"
)

const (
	agentSetupPending    = "pending"
	agentSetupPreparing  = "preparing"
	agentSetupInstalling = "installing"
	agentSetupReady      = "ready"
	agentSetupFailed     = "failed"
)

var agentRetryDelays = []time.Duration{5 * time.Second, 15 * time.Second, 30 * time.Second, time.Minute, 5 * time.Minute}

var errRemoteDeviceDeleted = errors.New("设备已被管理员删除，请重新激活")

type RuntimeApp struct {
	mu        sync.RWMutex
	stateMu   sync.Mutex
	queueMu   sync.Mutex
	agentMu   sync.Mutex
	app       *application.App
	window    application.Window
	store     *LocalStore
	logger    *runtimelog.Logger
	http      *http.Client
	installer func(string, string) error
	service   func() (string, bool)
	agentRun  bool
	agentWake chan struct{}
}

type RuntimeStatus struct {
	Activated        bool   `json:"activated"`
	ServerURL        string `json:"serverUrl"`
	DeviceID         string `json:"deviceId"`
	DeviceName       string `json:"deviceName"`
	Architecture     string `json:"architecture"`
	ClientVersion    string `json:"clientVersion"`
	MeshAgentStatus  string `json:"meshAgentStatus"`
	AgentSetupStatus string `json:"agentSetupStatus"`
	AgentSetupError  string `json:"agentSetupError"`
	AgentNextRetryAt string `json:"agentNextRetryAt"`
	Connection       string `json:"connection"`
	LastHeartbeat    string `json:"lastHeartbeat"`
	ScreenCount      int    `json:"screenCount"`
	LastError        string `json:"lastError"`
}

type ActivationInput struct {
	ConnectionKey string `json:"connectionKey"`
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
	app := &RuntimeApp{
		store: store, logger: logger, http: &http.Client{}, installer: installMeshAgentElevated,
		service: meshAgentServiceState, agentWake: make(chan struct{}, 1),
	}
	logger.Info("runtime client started version=%s architecture=%s", buildinfo.Version, runtime.GOARCH)
	go app.flushFrontendErrors()
	if state, loadErr := app.loadState(); loadErr == nil && state.DeviceID != "" {
		time.AfterFunc(time.Second, app.startAgentSetup)
	}
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
	state, err := a.loadState()
	if err != nil {
		return RuntimeStatus{Architecture: runtime.GOARCH, ClientVersion: buildinfo.Version, MeshAgentStatus: meshAgentServiceStatus(), Connection: "未激活", LastError: err.Error()}
	}
	meshStatus, serviceReady := a.service()
	if state.DeviceID != "" && serviceReady && !state.AgentSetupRequired && state.AgentSetupStatus != agentSetupReady {
		if a.persistAgentSetup(state, agentSetupReady, "", time.Time{}, false) {
			state.AgentSetupStatus = agentSetupReady
			state.AgentSetupError = ""
			state.AgentNextRetryAt = ""
		}
	}
	status := a.localStatus(state, meshStatus)
	if !status.Activated {
		return status
	}
	if err := a.refreshRemoteStatus(&status, state, status.AgentSetupStatus == agentSetupReady); err != nil {
		if errors.Is(err, errRemoteDeviceDeleted) {
			if clearErr := a.clearActivation(state); clearErr != nil {
				status.LastError = clearErr.Error()
				return status
			}
			a.signalAgentSetup()
			return RuntimeStatus{Architecture: runtime.GOARCH, ClientVersion: buildinfo.Version, MeshAgentStatus: meshStatus, Connection: "设备已删除，请重新激活", LastError: errRemoteDeviceDeleted.Error()}
		}
		if status.AgentSetupStatus == agentSetupReady {
			status.Connection = "Linux 服务不可达"
		}
		if status.LastError == "" {
			status.LastError = err.Error()
		}
		a.logger.Error("runtime status request failed error=%s", security.Redact(err.Error()))
	}
	return status
}

func (a *RuntimeApp) localStatus(state LocalState, meshStatus string) RuntimeStatus {
	setupStatus := normalizeAgentSetupStatus(state.AgentSetupStatus)
	status := RuntimeStatus{
		Activated: state.DeviceID != "", ServerURL: state.ServerURL, DeviceID: state.DeviceID, DeviceName: state.DeviceName,
		Architecture: runtime.GOARCH, ClientVersion: buildinfo.Version, MeshAgentStatus: meshStatus,
		AgentSetupStatus: setupStatus, AgentSetupError: state.AgentSetupError, AgentNextRetryAt: state.AgentNextRetryAt,
		Connection: "未激活",
	}
	if !status.Activated {
		return status
	}
	switch setupStatus {
	case agentSetupPreparing:
		status.Connection = "设备已激活，正在准备 MeshAgent"
	case agentSetupInstalling:
		status.Connection = "设备已激活，正在安装 MeshAgent"
	case agentSetupReady:
		status.Connection = "正在读取 Linux 设备状态"
	case agentSetupFailed:
		status.Connection = "设备已激活，MeshAgent 待完成"
		status.LastError = state.AgentSetupError
	default:
		status.Connection = "设备已激活，等待 MeshAgent 准备"
	}
	return status
}

func normalizeAgentSetupStatus(value string) string {
	switch value {
	case agentSetupPending, agentSetupPreparing, agentSetupInstalling, agentSetupReady, agentSetupFailed:
		return value
	default:
		return agentSetupPending
	}
}

func (a *RuntimeApp) refreshRemoteStatus(status *RuntimeStatus, state LocalState, updateConnection bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, state.ServerURL+"/api/v1/client/status", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+state.DeviceToken)
	response, err := a.http.Do(req)
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
		if response.StatusCode == http.StatusGone {
			return errRemoteDeviceDeleted
		}
		return errors.New(firstNonEmpty(envelope.Error, "Linux 状态接口拒绝请求"))
	}
	status.DeviceName = envelope.Data.DeviceName
	status.ScreenCount = envelope.Data.ScreenCount
	if envelope.Data.LastSeenAt != nil {
		status.LastHeartbeat = envelope.Data.LastSeenAt.Format(time.RFC3339)
	}
	if !updateConnection {
		return nil
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
	connection, err := connectionkey.Parse(input.ConnectionKey)
	if err != nil {
		return a.Status(), err
	}
	installationID, err := a.store.InstallationID()
	if err != nil {
		return a.Status(), err
	}
	hostname, _ := os.Hostname()
	payload := map[string]string{"code": connection.ActivationCode, "installationId": installationID, "architecture": runtime.GOARCH, "deviceName": hostname}
	raw, _ := json.Marshal(payload)
	requestContext, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestContext, http.MethodPost, connection.ServerURL+"/api/v1/client/activate", bytes.NewReader(raw))
	if err != nil {
		return a.Status(), err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := a.http.Do(req)
	if err != nil {
		a.logger.Error("activation request failed error=%s", security.Redact(err.Error()))
		return a.Status(), fmt.Errorf("连接 DeskPatrol 服务失败: %w", err)
	}
	defer response.Body.Close()
	var envelope struct {
		Data struct {
			DeviceID           string `json:"deviceId"`
			DeviceName         string `json:"deviceName"`
			DeviceToken        string `json:"deviceToken"`
			AgentSetupRequired bool   `json:"agentSetupRequired"`
		} `json:"data"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		return a.Status(), fmt.Errorf("服务响应无法读取: %w", err)
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated {
		a.logger.Error("activation rejected status=%d error=%s", response.StatusCode, security.Redact(envelope.Error))
		return a.Status(), errors.New(firstNonEmpty(envelope.Error, "激活失败"))
	}
	if envelope.Data.DeviceID == "" || envelope.Data.DeviceName == "" || envelope.Data.DeviceToken == "" {
		return a.Status(), errors.New("激活响应缺少设备凭据")
	}
	setupStatus := agentSetupPending
	if !envelope.Data.AgentSetupRequired {
		if _, ready := a.service(); ready {
			setupStatus = agentSetupReady
		}
	}
	state := LocalState{ServerURL: connection.ServerURL, DeviceID: envelope.Data.DeviceID, DeviceName: envelope.Data.DeviceName, DeviceToken: envelope.Data.DeviceToken, AgentSetupRequired: envelope.Data.AgentSetupRequired, AgentSetupStatus: setupStatus}
	if err := a.saveState(state); err != nil {
		return a.Status(), err
	}
	a.logger.Info("runtime activated deviceId=%s server=%s", envelope.Data.DeviceID, connection.ServerURL)
	go a.flushFrontendErrors()
	if setupStatus != agentSetupReady {
		time.AfterFunc(time.Second, a.startAgentSetup)
	}
	meshStatus, _ := a.service()
	return a.localStatus(state, meshStatus), nil
}

func (a *RuntimeApp) RetryAgentSetup() error {
	state, err := a.loadState()
	if err != nil {
		return err
	}
	if state.DeviceID == "" || state.ServerURL == "" || state.DeviceToken == "" {
		return errors.New("设备尚未激活")
	}
	a.agentMu.Lock()
	running := a.agentRun
	a.agentMu.Unlock()
	if running {
		a.signalAgentSetup()
	} else {
		a.startAgentSetup()
	}
	return nil
}

func (a *RuntimeApp) startAgentSetup() {
	state, err := a.loadState()
	if err != nil || state.DeviceID == "" || state.ServerURL == "" || state.DeviceToken == "" {
		return
	}
	a.agentMu.Lock()
	if a.agentRun {
		a.agentMu.Unlock()
		return
	}
	select {
	case <-a.agentWake:
	default:
	}
	a.agentRun = true
	a.agentMu.Unlock()
	go func() {
		defer func() {
			a.agentMu.Lock()
			a.agentRun = false
			a.agentMu.Unlock()
		}()
		a.runAgentSetup()
	}()
}

func (a *RuntimeApp) signalAgentSetup() {
	select {
	case a.agentWake <- struct{}{}:
	default:
	}
}

func (a *RuntimeApp) runAgentSetup() {
	attempt := 0
	for {
		state, err := a.loadState()
		if err != nil || state.DeviceID == "" || state.ServerURL == "" || state.DeviceToken == "" {
			return
		}
		if _, ready := a.service(); ready && !state.AgentSetupRequired {
			a.persistAgentSetup(state, agentSetupReady, "", time.Time{}, false)
			return
		}
		err = a.prepareAndInstallAgent(state)
		if err == nil {
			a.persistAgentSetup(state, agentSetupReady, "", time.Time{}, false)
			return
		}
		if errors.Is(err, errRemoteDeviceDeleted) {
			_ = a.clearActivation(state)
			return
		}
		delay := agentRetryDelays[min(attempt, len(agentRetryDelays)-1)]
		attempt++
		nextRetry := time.Now().Add(delay)
		safeError := security.Redact(err.Error())
		if !a.persistAgentSetup(state, agentSetupFailed, safeError, nextRetry, state.AgentSetupRequired) {
			return
		}
		a.logger.Error("runtime agent setup failed deviceId=%s error=%s", state.DeviceID, safeError)
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-a.agentWake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			attempt = 0
		}
	}
}

func (a *RuntimeApp) prepareAndInstallAgent(state LocalState) error {
	if !a.persistAgentSetup(state, agentSetupPreparing, "", time.Time{}, state.AgentSetupRequired) {
		return errRemoteDeviceDeleted
	}
	requestContext, cancel := context.WithTimeout(context.Background(), 135*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestContext, http.MethodPost, state.ServerURL+"/api/v1/client/agent/prepare", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+state.DeviceToken)
	response, err := a.http.Do(req)
	if err != nil {
		return fmt.Errorf("请求 Linux Agent 准备失败: %w", err)
	}
	defer response.Body.Close()
	var envelope struct {
		Data struct {
			AgentDownloadURL string `json:"agentDownloadUrl"`
			AgentSHA256      string `json:"agentSha256"`
			AgentSize        int64  `json:"agentSize"`
		} `json:"data"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("Agent 准备响应无法读取: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == http.StatusGone {
			return errRemoteDeviceDeleted
		}
		return errors.New(firstNonEmpty(envelope.Error, "Linux Agent 准备失败"))
	}
	if !validAgentDownloadURL(state.ServerURL, envelope.Data.AgentDownloadURL) || len(envelope.Data.AgentSHA256) != 64 || envelope.Data.AgentSize < 1024 || envelope.Data.AgentSize > 256<<20 {
		return errors.New("Agent 准备响应不正确")
	}
	if !a.persistAgentSetup(state, agentSetupInstalling, "", time.Time{}, state.AgentSetupRequired) {
		return errRemoteDeviceDeleted
	}
	if err := a.installer(envelope.Data.AgentDownloadURL, envelope.Data.AgentSHA256); err != nil {
		return err
	}
	if _, ready := a.service(); !ready {
		return errors.New("MeshAgent 安装完成后服务未正常运行")
	}
	return nil
}

func validAgentDownloadURL(serverURL, downloadURL string) bool {
	server, err := url.Parse(serverURL)
	if err != nil || server.Scheme != "https" || server.Host == "" || server.Path != "" || server.RawQuery != "" || server.Fragment != "" {
		return false
	}
	download, err := url.Parse(downloadURL)
	if err != nil || download.Scheme != server.Scheme || !strings.EqualFold(download.Host, server.Host) || !strings.HasPrefix(download.Path, "/api/v1/client/agent/") || download.RawQuery != "" || download.Fragment != "" {
		return false
	}
	return len(strings.TrimPrefix(download.Path, "/api/v1/client/agent/")) >= 20
}

func (a *RuntimeApp) persistAgentSetup(expected LocalState, status, setupError string, nextRetry time.Time, setupRequired bool) bool {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	state, err := a.store.Load()
	if err != nil || state.DeviceID != expected.DeviceID || state.DeviceToken != expected.DeviceToken {
		return false
	}
	state.AgentSetupRequired = setupRequired
	state.AgentSetupStatus = status
	state.AgentSetupError = setupError
	if nextRetry.IsZero() {
		state.AgentNextRetryAt = ""
	} else {
		state.AgentNextRetryAt = nextRetry.Format(time.RFC3339)
	}
	if err := a.store.Save(state); err != nil {
		a.logger.Error("runtime agent state save failed error=%s", security.Redact(err.Error()))
		return false
	}
	return true
}

func (a *RuntimeApp) clearActivation(expected LocalState) error {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	state, err := a.store.Load()
	if err != nil {
		return err
	}
	if state.DeviceID != expected.DeviceID || state.DeviceToken != expected.DeviceToken {
		return nil
	}
	return a.store.ClearState()
}

func (a *RuntimeApp) loadState() (LocalState, error) {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	return a.store.Load()
}

func (a *RuntimeApp) saveState(state LocalState) error {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	return a.store.Save(state)
}

func (a *RuntimeApp) flushFrontendErrors() {
	a.queueMu.Lock()
	defer a.queueMu.Unlock()
	state, err := a.loadState()
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
	response, err := a.http.Do(req)
	if err != nil {
		a.logger.Error("runtime frontend error upload failed error=%s", security.Redact(err.Error()))
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		a.logger.Error("runtime frontend error upload rejected status=%d", response.StatusCode)
		return
	}
	if err := a.store.ClearFrontendErrors(); err != nil {
		a.logger.Error("runtime frontend error queue cleanup failed error=%s", security.Redact(err.Error()))
	}
}

func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
