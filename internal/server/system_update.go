package server

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"deskpatrol/internal/buildinfo"
	"deskpatrol/internal/releasesync"
)

// 自更新的特权部分由 root 运行的 deskpatrol-update.service 完成：
// 后端只能把目标版本写入 updateRequestDir/request，root 更新器读取后从 GitHub 校验并执行新版 install.sh。
// 更新器的仓库地址和状态都放在 root 拥有的路径下，后端只读。
var (
	updateRequestDir  = "/var/lib/deskpatrol/update"
	updateStateDir    = "/var/lib/deskpatrol-update"
	updaterConfigPath = "/etc/deskpatrol-updater.conf"
	githubBaseURL     = "https://github.com"
)

// 超过该时长仍为 running 的状态视为更新器异常退出，允许重新发起更新。
const staleUpdateAfter = 2 * time.Hour

var updateStatusKeys = map[string]string{"STATE": "state", "VERSION": "version", "FROM": "from", "MESSAGE": "message", "UPDATED_AT": "updatedAt"}

func updaterRepository() (string, error) {
	file, err := os.Open(updaterConfigPath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	scanner := bufio.NewScanner(io.LimitReader(file, 4096))
	for scanner.Scan() {
		if value, ok := strings.CutPrefix(strings.TrimSpace(scanner.Text()), "REPOSITORY="); ok && regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`).MatchString(value) {
			return value, nil
		}
	}
	return "", errors.New("更新器配置缺少有效的 REPOSITORY")
}

func readUpdateStatus() (map[string]string, error) {
	raw, err := os.ReadFile(filepath.Join(updateStateDir, "status"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	status := make(map[string]string)
	for _, line := range strings.Split(string(raw), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if name, known := updateStatusKeys[key]; ok && known {
			status[name] = value
		}
	}
	if status["state"] == "" {
		return nil, errors.New("更新状态文件格式不正确")
	}
	return status, nil
}

func updateRequestPending() bool {
	_, err := os.Lstat(filepath.Join(updateRequestDir, "request"))
	return err == nil
}

func updateRunning(status map[string]string) bool {
	if status == nil || status["state"] != "running" {
		return false
	}
	updated, err := time.Parse(time.RFC3339, status["updatedAt"])
	return err != nil || time.Since(updated) < staleUpdateAfter
}

func readUpdateLog() string {
	file, err := os.Open(filepath.Join(updateStateDir, "update.log"))
	if err != nil {
		return ""
	}
	defer file.Close()
	const limit = 32 << 10
	if info, err := file.Stat(); err == nil && info.Size() > limit {
		_, _ = file.Seek(-limit, io.SeekEnd)
	}
	raw, _ := io.ReadAll(io.LimitReader(file, limit))
	return string(raw)
}

func (a *App) getSystemUpdate(w http.ResponseWriter, r *http.Request) {
	repository, repoErr := updaterRepository()
	_, dirErr := os.Stat(updateRequestDir)
	reason := ""
	switch {
	case errors.Is(repoErr, os.ErrNotExist) || errors.Is(dirErr, os.ErrNotExist):
		reason = "服务器尚未安装自更新组件，请先通过 SSH 运行一次 v0.1.23 或更新版本的 install.sh"
	case repoErr != nil:
		reason = fmt.Sprintf("读取更新器配置失败: %v", repoErr)
	case dirErr != nil:
		reason = fmt.Sprintf("读取更新请求目录失败: %v", dirErr)
	}
	if repository == "" {
		if cfg, _, err := a.snapshot(); err == nil {
			repository = cfg.GitHubRepository
		}
	}
	status, err := readUpdateStatus()
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("读取更新状态失败: %w", err))
		return
	}
	if updateRequestPending() && !updateRunning(status) {
		status = map[string]string{"state": "pending", "message": "已提交更新请求，等待更新器启动"}
	}
	var statusValue any
	if status != nil {
		statusValue = status
	}
	writeJSON(w, http.StatusOK, map[string]any{"currentVersion": buildinfo.Version, "available": reason == "", "unavailableReason": reason, "repository": repository, "status": statusValue, "log": readUpdateLog()})
}

func latestReleaseVersion(ctx context.Context, client *http.Client, repository string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, fmt.Sprintf("%s/%s/releases/latest", githubBaseURL, repository), nil)
	if err != nil {
		return "", err
	}
	noRedirect := *client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := noRedirect.Do(req)
	if err != nil {
		return "", errors.New("连接 GitHub 失败或超时")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusFound && response.StatusCode != http.StatusMovedPermanently {
		return "", fmt.Errorf("GitHub 返回 HTTP %d", response.StatusCode)
	}
	location, err := url.Parse(response.Header.Get("Location"))
	if err != nil {
		return "", errors.New("GitHub 返回的跳转地址不正确")
	}
	match := regexp.MustCompile(`/releases/tag/v(\d+\.\d+\.\d+)$`).FindStringSubmatch(location.Path)
	if match == nil {
		return "", errors.New("GitHub 最新 Release 的 tag 不是 vX.Y.Z 格式")
	}
	return match[1], nil
}

func (a *App) getLatestSystemVersion(w http.ResponseWriter, r *http.Request) {
	repository, err := updaterRepository()
	if err != nil {
		cfg, _, snapErr := a.snapshot()
		if snapErr != nil {
			writeError(w, http.StatusPreconditionFailed, snapErr)
			return
		}
		repository = cfg.GitHubRepository
	}
	version, err := latestReleaseVersion(r.Context(), http.DefaultClient, repository)
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Errorf("查询最新版本失败: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"version": version})
}

func (a *App) postSystemUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Version string `json:"version"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	version := strings.TrimSpace(req.Version)
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(version) {
		writeError(w, http.StatusBadRequest, errors.New("版本必须使用 x.y.z 格式"))
		return
	}
	if releasesync.CompareVersions(version, buildinfo.Version) <= 0 {
		writeError(w, http.StatusBadRequest, fmt.Errorf("目标版本必须高于当前版本 %s", buildinfo.Version))
		return
	}
	if _, err := updaterRepository(); err != nil {
		writeError(w, http.StatusPreconditionFailed, errors.New("服务器尚未安装自更新组件，请先通过 SSH 运行一次 v0.1.23 或更新版本的 install.sh"))
		return
	}
	status, err := readUpdateStatus()
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("读取更新状态失败: %w", err))
		return
	}
	if updateRunning(status) {
		writeError(w, http.StatusConflict, errors.New("已有更新正在执行"))
		return
	}
	// 先写临时文件再硬链接到 request，保证 systemd 路径单元看到的永远是完整内容，且并发请求只有一个成功。
	temporary, err := os.CreateTemp(updateRequestDir, ".request-*")
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("创建更新请求失败: %w", err))
		return
	}
	defer os.Remove(temporary.Name())
	_, writeErr := temporary.WriteString("VERSION=" + version + "\n")
	if closeErr := temporary.Close(); writeErr != nil || closeErr != nil {
		writeError(w, http.StatusInternalServerError, errors.New("写入更新请求失败"))
		return
	}
	if err := os.Link(temporary.Name(), filepath.Join(updateRequestDir, "request")); err != nil {
		if errors.Is(err, os.ErrExist) {
			writeError(w, http.StatusConflict, errors.New("已有更新请求等待执行"))
			return
		}
		writeError(w, http.StatusInternalServerError, fmt.Errorf("提交更新请求失败: %w", err))
		return
	}
	user := r.Context().Value(principalKey).(principal)
	a.logger.Info("system update requested", "administrator", user.LoginName, "from", buildinfo.Version, "to", version)
	writeJSON(w, http.StatusAccepted, map[string]string{"state": "pending", "version": version})
}
