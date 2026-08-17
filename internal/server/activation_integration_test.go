package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"deskpatrol/internal/appconfig"
	"deskpatrol/internal/connectionkey"
	"deskpatrol/internal/database"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestConnectionKeyTakeoverDeleteAndRestore(t *testing.T) {
	app, pool, administratorID := integrationTestApp(t)
	first := createIntegrationConnectionKey(t, app, administratorID, "first")
	firstActivation := activateIntegrationClient(t, app, first, "0123456789abcdef0123456789abcdef", http.StatusCreated)
	second := createIntegrationConnectionKey(t, app, administratorID, "second")
	copied := copyIntegrationConnectionKey(t, app, second.ID)
	if copied.ConnectionKey != second.ConnectionKey {
		t.Fatal("未使用连接密钥重复复制后必须保持一致")
	}
	secondActivation := activateIntegrationClient(t, app, copied, "0123456789abcdef0123456789abcdef", http.StatusCreated)
	if secondActivation.DeviceID != firstActivation.DeviceID || secondActivation.DeviceToken == firstActivation.DeviceToken || !secondActivation.AgentSetupRequired {
		t.Fatalf("新密钥未正确接管原设备: first=%#v second=%#v", firstActivation, secondActivation)
	}
	activateIntegrationClient(t, app, first, "0123456789abcdef0123456789abcdef", http.StatusConflict)
	var deviceCount int
	if err := pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM devices WHERE installation_id=$1`, "0123456789abcdef0123456789abcdef").Scan(&deviceCount); err != nil || deviceCount != 1 {
		t.Fatalf("接管后设备记录数不正确: count=%d err=%v", deviceCount, err)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE devices SET node_id='node//integration-node',mesh_id='mesh//integration-mesh',status='online',last_seen_at=NOW() WHERE id=$1`, secondActivation.DeviceID); err != nil {
		t.Fatal(err)
	}

	deleteRequest := httptest.NewRequest(http.MethodDelete, "/api/v1/devices/"+secondActivation.DeviceID, nil)
	deleteRequest.SetPathValue("id", secondActivation.DeviceID)
	deleteRequest = deleteRequest.WithContext(context.WithValue(deleteRequest.Context(), principalKey, principal{ID: administratorID, LoginName: "admin"}))
	deleteResponse := httptest.NewRecorder()
	app.deleteDevice(deleteResponse, deleteRequest)
	if deleteResponse.Code != http.StatusOK {
		t.Fatalf("删除设备返回 %d: %s", deleteResponse.Code, deleteResponse.Body.String())
	}
	statusRequest := httptest.NewRequest(http.MethodGet, "/api/v1/client/status", nil)
	statusRequest.Header.Set("Authorization", "Bearer "+secondActivation.DeviceToken)
	statusResponse := httptest.NewRecorder()
	app.clientStatus(statusResponse, statusRequest)
	if statusResponse.Code != http.StatusGone {
		t.Fatalf("已删除设备凭据应返回 410，实际 %d: %s", statusResponse.Code, statusResponse.Body.String())
	}
	eventRaw, _ := json.Marshal(map[string]any{"type": "agent_heartbeat", "nodeId": "node//integration-node", "meshId": "mesh//integration-mesh", "name": "test-device", "agentType": 4, "agentVersion": 1})
	eventRequest := httptest.NewRequest(http.MethodPost, "/api/internal/meshcentral/events", bytes.NewReader(eventRaw))
	eventRequest.Header.Set("X-DeskPatrol-Plugin-Token", "integration-plugin-token-value-123456")
	eventResponse := httptest.NewRecorder()
	app.meshCentralEvent(eventResponse, eventRequest)
	if eventResponse.Code != http.StatusOK {
		t.Fatalf("已删除设备心跳应被忽略，实际 %d: %s", eventResponse.Code, eventResponse.Body.String())
	}
	third := createIntegrationConnectionKey(t, app, administratorID, "third")
	thirdActivation := activateIntegrationClient(t, app, third, "0123456789abcdef0123456789abcdef", http.StatusCreated)
	if thirdActivation.DeviceID != firstActivation.DeviceID || thirdActivation.AgentSetupRequired {
		t.Fatalf("删除后新密钥必须恢复原设备及 Mesh 身份: first=%#v third=%#v", firstActivation, thirdActivation)
	}
}

type integrationConnectionKey struct {
	ID            string
	ConnectionKey string
	ExpiresAt     time.Time
}

type integrationActivation struct {
	DeviceID           string `json:"deviceId"`
	DeviceName         string `json:"deviceName"`
	DeviceToken        string `json:"deviceToken"`
	AgentSetupRequired bool   `json:"agentSetupRequired"`
}

func integrationTestApp(t *testing.T) (*App, *pgxpool.Pool, int64) {
	t.Helper()
	dsn := os.Getenv("DESKPATROL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置隔离测试数据库 DESKPATROL_TEST_DATABASE_URL")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(context.Background(), database.Schema); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), `TRUNCATE administrators CASCADE`); err != nil {
		t.Fatal(err)
	}
	var administratorID int64
	if err := pool.QueryRow(context.Background(), `INSERT INTO administrators(login_name,password_hash) VALUES('admin','hash') RETURNING id`).Scan(&administratorID); err != nil {
		t.Fatal(err)
	}
	cfg := &appconfig.Config{PublicURL: "https://monitor.example.com", SessionSecret: "integration-session-secret", PluginToken: "integration-plugin-token-value-123456"}
	return &App{config: cfg, pool: pool, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, pool, administratorID
}

func createIntegrationConnectionKey(t *testing.T, app *App, administratorID int64, label string) integrationConnectionKey {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"label": label, "days": 7})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/activation-codes", bytes.NewReader(raw))
	request = request.WithContext(context.WithValue(request.Context(), principalKey, principal{ID: administratorID, LoginName: "admin"}))
	response := httptest.NewRecorder()
	app.createActivationCode(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("创建连接密钥返回 %d: %s", response.Code, response.Body.String())
	}
	var result struct {
		Data integrationConnectionKey `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result.Data
}

func copyIntegrationConnectionKey(t *testing.T, app *App, id string) integrationConnectionKey {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/activation-codes/"+id+"/copy", nil)
	request.SetPathValue("id", id)
	response := httptest.NewRecorder()
	app.copyActivationCode(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("复制连接密钥返回 %d: %s", response.Code, response.Body.String())
	}
	var result struct {
		Data integrationConnectionKey `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result.Data
}

func activateIntegrationClient(t *testing.T, app *App, key integrationConnectionKey, installationID string, expectedStatus int) integrationActivation {
	t.Helper()
	payload, err := connectionkey.Parse(key.ConnectionKey)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]string{"code": payload.ActivationCode, "installationId": installationID, "architecture": "amd64", "deviceName": "test-device"})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/client/activate", bytes.NewReader(raw))
	response := httptest.NewRecorder()
	app.activateClient(response, request)
	if response.Code != expectedStatus {
		t.Fatalf("激活返回 %d，期望 %d: %s", response.Code, expectedStatus, response.Body.String())
	}
	if expectedStatus != http.StatusOK && expectedStatus != http.StatusCreated {
		return integrationActivation{}
	}
	var result struct {
		Data integrationActivation `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result.Data
}
