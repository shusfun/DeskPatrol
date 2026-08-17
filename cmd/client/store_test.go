package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestFrontendErrorQueueKeepsLatestTwoHundredAndClears(t *testing.T) {
	store := &LocalStore{dir: t.TempDir()}
	for index := 0; index < 205; index++ {
		if err := store.AppendFrontendError(fmt.Sprintf(`{"eventId":"%d","message":"token=secret-%d"}`, index, index)); err != nil {
			t.Fatal(err)
		}
	}
	items, err := store.FrontendErrors()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 200 {
		t.Fatalf("异常队列长度应为 200，实际为 %d", len(items))
	}
	if !strings.Contains(string(items[0]), `"eventId":"5"`) || strings.Contains(string(items[0]), "secret-5") {
		t.Fatalf("异常队列裁剪或脱敏不正确: %s", items[0])
	}
	if err := store.ClearFrontendErrors(); err != nil {
		t.Fatal(err)
	}
	items, err = store.FrontendErrors()
	if err != nil || len(items) != 0 {
		t.Fatalf("异常队列未清空: %d %v", len(items), err)
	}
}

func TestClearStateDoesNotRemoveInstallationID(t *testing.T) {
	store := &LocalStore{dir: t.TempDir()}
	installationID, err := store.InstallationID()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(LocalState{DeviceID: "device-1", DeviceToken: "token"}); err != nil {
		t.Fatal(err)
	}
	if err := store.ClearState(); err != nil {
		t.Fatal(err)
	}
	state, err := store.Load()
	if err != nil || state.DeviceID != "" {
		t.Fatalf("本机状态未清除: %#v err=%v", state, err)
	}
	preserved, err := store.InstallationID()
	if err != nil || preserved != installationID {
		t.Fatalf("安装 ID 被错误清除: before=%s after=%s err=%v", installationID, preserved, err)
	}
}
