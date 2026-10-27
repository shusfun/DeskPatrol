package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeviceGroupLifecycle(t *testing.T) {
	app, pool, administratorID := integrationTestApp(t)
	if _, err := pool.Exec(context.Background(), `TRUNCATE device_groups CASCADE`); err != nil {
		t.Fatal(err)
	}
	key := createIntegrationConnectionKey(t, app, administratorID, "group-device")
	activation := activateIntegrationClient(t, app, key, "fedcba9876543210fedcba9876543210", http.StatusCreated)

	call := func(handler http.HandlerFunc, method, path, pathID string, body any, expectedStatus int) []byte {
		t.Helper()
		var reader *bytes.Reader
		if body == nil {
			reader = bytes.NewReader(nil)
		} else {
			raw, _ := json.Marshal(body)
			reader = bytes.NewReader(raw)
		}
		request := httptest.NewRequest(method, path, reader)
		if pathID != "" {
			request.SetPathValue("id", pathID)
		}
		request = request.WithContext(context.WithValue(request.Context(), principalKey, principal{ID: administratorID, LoginName: "admin"}))
		response := httptest.NewRecorder()
		handler(response, request)
		if response.Code != expectedStatus {
			t.Fatalf("%s %s 返回 %d，期望 %d: %s", method, path, response.Code, expectedStatus, response.Body.String())
		}
		return response.Body.Bytes()
	}
	var created struct {
		Data struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(call(app.createDeviceGroup, http.MethodPost, "/api/v1/device-groups", "", map[string]string{"name": " 销售组 "}, http.StatusCreated), &created); err != nil {
		t.Fatal(err)
	}
	if created.Data.Name != "销售组" || !groupIDPattern.MatchString(created.Data.ID) {
		t.Fatalf("创建分组结果不正确: %#v", created.Data)
	}
	groupID := created.Data.ID
	call(app.createDeviceGroup, http.MethodPost, "/api/v1/device-groups", "", map[string]string{"name": "销售组"}, http.StatusConflict)
	call(app.createDeviceGroup, http.MethodPost, "/api/v1/device-groups", "", map[string]string{"name": " "}, http.StatusBadRequest)
	call(app.renameDeviceGroup, http.MethodPut, "/api/v1/device-groups/"+groupID, groupID, map[string]string{"name": "商务BD"}, http.StatusOK)
	call(app.renameDeviceGroup, http.MethodPut, "/api/v1/device-groups/missing", "missing", map[string]string{"name": "x"}, http.StatusNotFound)

	devicePath := "/api/v1/devices/" + activation.DeviceID + "/group"
	call(app.putDeviceGroup, http.MethodPut, devicePath, activation.DeviceID, map[string]any{"groupId": groupID}, http.StatusOK)
	call(app.putDeviceGroup, http.MethodPut, devicePath, activation.DeviceID, map[string]any{"groupId": "00000000-0000-4000-8000-000000000099"}, http.StatusNotFound)
	var storedGroup *string
	if err := pool.QueryRow(context.Background(), `SELECT group_id::text FROM devices WHERE id=$1`, activation.DeviceID).Scan(&storedGroup); err != nil || storedGroup == nil || *storedGroup != groupID {
		t.Fatalf("设备分组未持久化: group=%v err=%v", storedGroup, err)
	}

	call(app.putWallLayout, http.MethodPut, "/api/v1/wall-layout", "", map[string]any{"tileCount": 9, "deviceOrder": []string{}, "collapsedGroups": []string{groupID, ungroupedKey}}, http.StatusOK)
	call(app.putWallLayout, http.MethodPut, "/api/v1/wall-layout", "", map[string]any{"tileCount": 9, "deviceOrder": []string{}, "collapsedGroups": []string{"bad"}}, http.StatusBadRequest)

	call(app.deleteDeviceGroup, http.MethodDelete, "/api/v1/device-groups/"+groupID, groupID, nil, http.StatusOK)
	call(app.deleteDeviceGroup, http.MethodDelete, "/api/v1/device-groups/"+groupID, groupID, nil, http.StatusNotFound)
	if err := pool.QueryRow(context.Background(), `SELECT group_id::text FROM devices WHERE id=$1`, activation.DeviceID).Scan(&storedGroup); err != nil || storedGroup != nil {
		t.Fatalf("删除分组后设备应归入未分组: group=%v err=%v", storedGroup, err)
	}
	var layout struct {
		Data struct {
			CollapsedGroups []string `json:"collapsedGroups"`
		} `json:"data"`
	}
	if err := json.Unmarshal(call(app.getWallLayout, http.MethodGet, "/api/v1/wall-layout", "", nil, http.StatusOK), &layout); err != nil {
		t.Fatal(err)
	}
	if len(layout.Data.CollapsedGroups) != 1 || layout.Data.CollapsedGroups[0] != ungroupedKey {
		t.Fatalf("删除分组后必须清理折叠状态: %v", layout.Data.CollapsedGroups)
	}
	call(app.putDeviceGroup, http.MethodPut, devicePath, activation.DeviceID, map[string]any{"groupId": nil}, http.StatusOK)
}
