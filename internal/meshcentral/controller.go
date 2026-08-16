package meshcentral

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"deskpatrol/internal/appconfig"
)

const (
	defaultNodePath     = "/opt/deskpatrol/current/node/bin/node"
	defaultMeshCtrlPath = "/opt/deskpatrol/current/meshcentral/node_modules/meshcentral/meshctrl.js"
	defaultControlURL   = "ws://127.0.0.1:18129"
	defaultCommandURL   = "http://127.0.0.1:18129/deskpatrol/run-command"
	maxCommandResponse  = 600 << 10
)

var meshIDPattern = regexp.MustCompile(`mesh/[A-Za-z0-9_+/@=-]+`)
var sharingURLPattern = regexp.MustCompile(`(?m)^URL:\s+(https://[^\s]+/sharing\?[^\s]+)\s*$`)

type Controller struct {
	NodePath     string
	MeshCtrlPath string
	ControlURL   string
	CommandURL   string
	LoginKey     string
	PluginToken  string
	StorageDir   string
	HTTPClient   *http.Client
}

type CommandResult struct {
	Stdout          string `json:"stdout"`
	Stderr          string `json:"stderr"`
	ExitCode        int    `json:"exitCode"`
	OutputTruncated bool   `json:"outputTruncated"`
}

func NewController(loginKey, pluginToken, storageDir string) *Controller {
	return &Controller{
		NodePath: defaultNodePath, MeshCtrlPath: defaultMeshCtrlPath, ControlURL: defaultControlURL,
		CommandURL: defaultCommandURL, LoginKey: loginKey, PluginToken: pluginToken, StorageDir: storageDir,
		HTTPClient: &http.Client{Timeout: 130 * time.Second},
	}
}

func (c *Controller) AddDeviceGroup(ctx context.Context, name, description string) (string, error) {
	if strings.TrimSpace(name) == "" || len(name) > 64 {
		return "", errors.New("MeshCentral 设备组名称不正确")
	}
	output, err := c.runMeshCtrl(ctx, "AddDeviceGroup", "--name", name, "--desc", description)
	if err != nil {
		return "", err
	}
	meshID := meshIDPattern.FindString(output)
	if meshID == "" {
		return "", fmt.Errorf("MeshCentral 创建设备组响应缺少 MeshID: %s", concise(output))
	}
	return meshID, nil
}

func (c *Controller) RemoveDeviceGroup(ctx context.Context, meshID string) error {
	if !validMeshID(meshID) {
		return errors.New("MeshCentral MeshID 不正确")
	}
	_, err := c.runMeshCtrl(ctx, "RemoveDeviceGroup", "--id", meshID)
	return err
}

func (c *Controller) MoveToDeviceGroup(ctx context.Context, nodeID, meshID string) error {
	if !validNodeID(nodeID) || !validMeshID(meshID) {
		return errors.New("MeshCentral NodeID 或 MeshID 不正确")
	}
	_, err := c.runMeshCtrl(ctx, "MoveToDeviceGroup", "--devid", nodeID, "--id", meshID)
	return err
}

func (c *Controller) CreateDesktopShare(ctx context.Context, nodeID string, durationMinutes int) (string, error) {
	if !validNodeID(nodeID) || durationMinutes < 1 || durationMinutes > 15 {
		return "", errors.New("MeshCentral 桌面分享参数不正确")
	}
	output, err := c.runMeshCtrl(ctx, "DeviceSharing", "--id", nodeID, "--add", "DeskPatrol", "--type", "desktop", "--viewonly", "--consent", "none", "--duration", fmt.Sprintf("%d", durationMinutes))
	if err != nil {
		return "", err
	}
	match := sharingURLPattern.FindStringSubmatch(output)
	if len(match) != 2 {
		return "", fmt.Errorf("MeshCentral 桌面分享响应缺少 URL: %s", concise(output))
	}
	return match[1], nil
}

func (c *Controller) DownloadAgent(ctx context.Context, meshID, architecture, destination string) (string, int64, error) {
	if !validMeshID(meshID) {
		return "", 0, errors.New("MeshCentral MeshID 不正确")
	}
	agentType := "4"
	if architecture == "arm64" {
		agentType = "43"
	} else if architecture != "amd64" {
		return "", 0, errors.New("MeshCentral Agent 架构不受支持")
	}
	temporaryDir, err := os.MkdirTemp(c.StorageDir, ".agent-download-")
	if err != nil {
		return "", 0, fmt.Errorf("创建 Agent 下载目录失败: %w", err)
	}
	defer os.RemoveAll(temporaryDir)
	if _, err := c.runMeshCtrlIn(ctx, temporaryDir, "AgentDownload", "--id", meshID, "--type", agentType, "--installflags", "1"); err != nil {
		return "", 0, err
	}
	entries, err := os.ReadDir(temporaryDir)
	if err != nil {
		return "", 0, err
	}
	var source string
	for _, entry := range entries {
		if !entry.IsDir() && entry.Name() != "login-key" {
			if source != "" {
				return "", 0, errors.New("MeshCentral AgentDownload 生成了多个文件")
			}
			source = filepath.Join(temporaryDir, entry.Name())
		}
	}
	if source == "" {
		return "", 0, errors.New("MeshCentral AgentDownload 未生成安装文件")
	}
	raw, err := os.ReadFile(source)
	if err != nil {
		return "", 0, err
	}
	if len(raw) < 1024 || len(raw) > 256<<20 {
		return "", 0, errors.New("MeshCentral Agent 文件大小不正确")
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return "", 0, err
	}
	temporary := destination + ".tmp"
	if err := os.WriteFile(temporary, raw, 0o600); err != nil {
		return "", 0, err
	}
	if err := os.Rename(temporary, destination); err != nil {
		_ = os.Remove(temporary)
		return "", 0, err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), int64(len(raw)), nil
}

func (c *Controller) RunPowerShell(ctx context.Context, nodeID, script string, timeout time.Duration) (CommandResult, error) {
	if !validNodeID(nodeID) {
		return CommandResult{}, errors.New("MeshCentral NodeID 不正确")
	}
	payload, _ := json.Marshal(map[string]any{"nodeId": nodeID, "script": script, "timeoutMs": timeout.Milliseconds()})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.CommandURL, bytes.NewReader(payload))
	if err != nil {
		return CommandResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-DeskPatrol-Plugin-Token", c.PluginToken)
	response, err := c.HTTPClient.Do(req)
	if err != nil {
		return CommandResult{}, fmt.Errorf("调用 MeshCentral 诊断通道失败: %w", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxCommandResponse+1))
	if err != nil {
		return CommandResult{}, err
	}
	if len(raw) > maxCommandResponse {
		return CommandResult{}, errors.New("MeshCentral 诊断通道响应超过限制")
	}
	if response.StatusCode != http.StatusOK {
		return CommandResult{}, fmt.Errorf("MeshCentral 诊断通道返回 HTTP %d: %s", response.StatusCode, concise(string(raw)))
	}
	var result CommandResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return CommandResult{}, fmt.Errorf("解析 MeshCentral 诊断响应失败: %w", err)
	}
	return result, nil
}

func (c *Controller) runMeshCtrl(ctx context.Context, args ...string) (string, error) {
	return c.runMeshCtrlIn(ctx, c.StorageDir, args...)
}

func (c *Controller) runMeshCtrlIn(ctx context.Context, directory string, args ...string) (string, error) {
	if len(c.LoginKey) != appconfig.MeshLoginKeySize {
		return "", errors.New("MeshCentral 登录密钥不正确")
	}
	if _, err := os.Stat(c.NodePath); err != nil {
		return "", fmt.Errorf("MeshCentral Node runtime 不可用: %w", err)
	}
	if _, err := os.Stat(c.MeshCtrlPath); err != nil {
		return "", fmt.Errorf("MeshCentral meshctrl.js 不可用: %w", err)
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	keyFile, err := os.CreateTemp(directory, ".mesh-login-key-")
	if err != nil {
		return "", err
	}
	keyPath := keyFile.Name()
	defer os.Remove(keyPath)
	if err := keyFile.Chmod(0o600); err != nil {
		_ = keyFile.Close()
		return "", err
	}
	if _, err := keyFile.WriteString(c.LoginKey); err != nil {
		_ = keyFile.Close()
		return "", err
	}
	if err := keyFile.Close(); err != nil {
		return "", err
	}
	commandArgs := []string{c.MeshCtrlPath}
	commandArgs = append(commandArgs, args...)
	commandArgs = append(commandArgs, "--url", c.ControlURL, "--loginuser", "admin", "--loginkeyfile", keyPath)
	command := exec.CommandContext(ctx, c.NodePath, commandArgs...)
	command.Dir = directory
	var stdout, stderr limitedBuffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("MeshCentral 命令失败: %w; stdout=%s; stderr=%s", err, concise(stdout.String()), concise(stderr.String()))
	}
	return stdout.String(), nil
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(value []byte) (int, error) {
	original := len(value)
	remaining := (256 << 10) - b.Len()
	if remaining > 0 {
		if len(value) > remaining {
			value = value[:remaining]
		}
		_, _ = b.Buffer.Write(value)
	}
	return original, nil
}

func validMeshID(value string) bool { return strings.HasPrefix(value, "mesh/") && len(value) <= 512 }
func validNodeID(value string) bool { return strings.HasPrefix(value, "node/") && len(value) <= 512 }
func concise(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\n", " "))
	if len(value) > 500 {
		return value[:500]
	}
	return value
}
