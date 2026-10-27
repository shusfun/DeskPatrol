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
	"path/filepath"
	"strings"
	"testing"
	"time"

	"deskpatrol/internal/buildinfo"
)

func useUpdatePaths(t *testing.T) (requestDir, stateDir string) {
	t.Helper()
	root := t.TempDir()
	requestDir, stateDir = filepath.Join(root, "update"), filepath.Join(root, "state")
	for _, dir := range []string{requestDir, stateDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	configPath := filepath.Join(root, "updater.conf")
	if err := os.WriteFile(configPath, []byte("REPOSITORY=shusfun/DeskPatrol\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldRequest, oldState, oldConfig, oldVersion := updateRequestDir, updateStateDir, updaterConfigPath, buildinfo.Version
	updateRequestDir, updateStateDir, updaterConfigPath, buildinfo.Version = requestDir, stateDir, configPath, "0.1.23"
	t.Cleanup(func() {
		updateRequestDir, updateStateDir, updaterConfigPath, buildinfo.Version = oldRequest, oldState, oldConfig, oldVersion
	})
	return requestDir, stateDir
}

func postUpdate(t *testing.T, version string) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(map[string]string{"version": version})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/system/update", bytes.NewReader(raw))
	request = request.WithContext(context.WithValue(request.Context(), principalKey, principal{ID: 1, LoginName: "admin"}))
	response := httptest.NewRecorder()
	(&App{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}).postSystemUpdate(response, request)
	return response
}

func TestPostSystemUpdateWritesSingleRequest(t *testing.T) {
	requestDir, _ := useUpdatePaths(t)
	for _, version := range []string{"0.1.23", "0.1.22", "latest"} {
		if response := postUpdate(t, version); response.Code != http.StatusBadRequest {
			t.Fatalf("版本 %s 应被拒绝，实际 %d: %s", version, response.Code, response.Body.String())
		}
	}
	if response := postUpdate(t, "0.1.24"); response.Code != http.StatusAccepted {
		t.Fatalf("更新请求应被接受，实际 %d: %s", response.Code, response.Body.String())
	}
	raw, err := os.ReadFile(filepath.Join(requestDir, "request"))
	if err != nil || string(raw) != "VERSION=0.1.24\n" {
		t.Fatalf("更新请求内容不正确: %q err=%v", raw, err)
	}
	if response := postUpdate(t, "0.1.25"); response.Code != http.StatusConflict {
		t.Fatalf("已有等待中的请求时应返回 409，实际 %d", response.Code)
	}
	entries, _ := os.ReadDir(requestDir)
	if len(entries) != 1 {
		t.Fatalf("不能残留临时请求文件: %v", entries)
	}
}

func TestPostSystemUpdateRejectsWhileRunning(t *testing.T) {
	_, stateDir := useUpdatePaths(t)
	status := "STATE=running\nVERSION=0.1.24\nFROM=0.1.23\nMESSAGE=正在更新\nUPDATED_AT=" + time.Now().UTC().Format(time.RFC3339) + "\n"
	if err := os.WriteFile(filepath.Join(stateDir, "status"), []byte(status), 0o644); err != nil {
		t.Fatal(err)
	}
	if response := postUpdate(t, "0.1.24"); response.Code != http.StatusConflict {
		t.Fatalf("更新执行中应返回 409，实际 %d", response.Code)
	}
	stale := strings.Replace(status, time.Now().UTC().Format(time.RFC3339), time.Now().Add(-3*time.Hour).UTC().Format(time.RFC3339), 1)
	if err := os.WriteFile(filepath.Join(stateDir, "status"), []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	if response := postUpdate(t, "0.1.24"); response.Code != http.StatusAccepted {
		t.Fatalf("过期的 running 状态不应阻塞更新，实际 %d: %s", response.Code, response.Body.String())
	}
}

func TestPostSystemUpdateRequiresUpdater(t *testing.T) {
	useUpdatePaths(t)
	updaterConfigPath = filepath.Join(t.TempDir(), "missing.conf")
	if response := postUpdate(t, "0.1.24"); response.Code != http.StatusPreconditionFailed || !strings.Contains(response.Body.String(), "install.sh") {
		t.Fatalf("未安装更新器时必须返回真实原因，实际 %d: %s", response.Code, response.Body.String())
	}
}

func TestLatestReleaseVersionParsesRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/shusfun/DeskPatrol/releases/latest" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Location", "https://github.com/shusfun/DeskPatrol/releases/tag/v0.1.30")
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()
	oldBase := githubBaseURL
	githubBaseURL = server.URL
	defer func() { githubBaseURL = oldBase }()
	version, err := latestReleaseVersion(context.Background(), http.DefaultClient, "shusfun/DeskPatrol")
	if err != nil || version != "0.1.30" {
		t.Fatalf("最新版本解析不正确: version=%q err=%v", version, err)
	}
	if _, err := latestReleaseVersion(context.Background(), http.DefaultClient, "shusfun/Missing"); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("非跳转响应必须返回真实状态码: %v", err)
	}
}

func TestReadUpdateStatusIgnoresUnknownKeys(t *testing.T) {
	_, stateDir := useUpdatePaths(t)
	if err := os.WriteFile(filepath.Join(stateDir, "status"), []byte("STATE=failed\nMESSAGE=下载失败\nEVIL=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	status, err := readUpdateStatus()
	if err != nil || status["state"] != "failed" || status["message"] != "下载失败" || len(status) != 2 {
		t.Fatalf("状态解析不正确: %#v err=%v", status, err)
	}
}
