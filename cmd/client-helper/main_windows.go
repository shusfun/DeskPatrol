//go:build windows

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

func main() {
	pipeName := flag.String("pipe", "", "一次性命名管道")
	flag.Parse()
	if !strings.HasPrefix(*pipeName, `\\.\pipe\DeskPatrol-`) || len(*pipeName) != len(`\\.\pipe\DeskPatrol-`)+32 {
		fail(errors.New("命名管道参数不正确"))
	}
	name, _ := windows.UTF16PtrFromString(*pipeName)
	handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		fail(fmt.Errorf("连接客户端命名管道失败: %w", err))
	}
	pipe := os.NewFile(uintptr(handle), *pipeName)
	if pipe == nil {
		_ = windows.CloseHandle(handle)
		fail(errors.New("打开客户端命名管道失败"))
	}
	defer pipe.Close()
	raw, err := readFrame(pipe, 64<<10)
	if err != nil {
		writeResult(pipe, err)
		return
	}
	var request struct {
		DownloadURL string `json:"downloadUrl"`
		SHA256      string `json:"sha256"`
	}
	if err := json.Unmarshal(raw, &request); err != nil {
		writeResult(pipe, err)
		return
	}
	writeResult(pipe, install(request.DownloadURL, request.SHA256))
}

func install(downloadURL, expectedSHA string) error {
	target, err := url.Parse(downloadURL)
	if err != nil || target.Scheme != "https" || target.Host == "" || !strings.HasPrefix(target.Path, "/api/v1/client/agent/") || target.RawQuery != "" || target.Fragment != "" {
		return errors.New("Agent 下载地址不正确")
	}
	if len(expectedSHA) != 64 {
		return errors.New("Agent SHA-256 不正确")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	response, err := (&http.Client{}).Do(req)
	if err != nil {
		if urlError, ok := err.(*url.Error); ok {
			err = urlError.Err
		}
		return fmt.Errorf("下载 MeshAgent 失败: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("下载 MeshAgent 失败: HTTP %d", response.StatusCode)
	}
	programFiles := os.Getenv("ProgramFiles")
	if programFiles == "" {
		return errors.New("ProgramFiles 环境变量不存在")
	}
	directory := filepath.Join(programFiles, "DeskPatrol")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	temporary := filepath.Join(directory, ".MeshAgent.download")
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o700)
	if err != nil {
		return err
	}
	hasher := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hasher), io.LimitReader(response.Body, (256<<20)+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || written < 1024 || written > 256<<20 {
		_ = os.Remove(temporary)
		return errors.New("写入 MeshAgent 失败或文件大小不正确")
	}
	if hex.EncodeToString(hasher.Sum(nil)) != strings.ToLower(expectedSHA) {
		_ = os.Remove(temporary)
		return errors.New("MeshAgent SHA-256 校验失败")
	}
	finalPath := filepath.Join(directory, "MeshAgent.exe")
	if err := os.Rename(temporary, finalPath); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("保存 MeshAgent 失败: %w", err)
	}
	command := exec.CommandContext(ctx, finalPath, "-fullinstall")
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("安装 MeshAgent 服务失败: %w: %s", err, concise(string(output)))
	}
	return verifyService()
}

func verifyService() error {
	manager, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return err
	}
	defer windows.CloseServiceHandle(manager)
	name, _ := windows.UTF16PtrFromString("Mesh Agent")
	service, err := windows.OpenService(manager, name, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return fmt.Errorf("MeshAgent 服务不存在: %w", err)
	}
	defer windows.CloseServiceHandle(service)
	var status windows.SERVICE_STATUS
	if err := windows.QueryServiceStatus(service, &status); err != nil {
		return err
	}
	if status.CurrentState != windows.SERVICE_RUNNING && status.CurrentState != windows.SERVICE_START_PENDING {
		return fmt.Errorf("MeshAgent 服务状态异常: %d", status.CurrentState)
	}
	return nil
}

func writeResult(pipe io.Writer, operationError error) {
	value := map[string]any{"ok": operationError == nil}
	if operationError != nil {
		value["error"] = operationError.Error()
	}
	raw, _ := json.Marshal(value)
	_ = writeFrame(pipe, raw)
}

func writeFrame(writer io.Writer, raw []byte) error {
	header := []byte{byte(len(raw)), byte(len(raw) >> 8), byte(len(raw) >> 16), byte(len(raw) >> 24)}
	if _, err := writer.Write(header); err != nil {
		return err
	}
	_, err := writer.Write(raw)
	return err
}

func readFrame(reader io.Reader, limit int) ([]byte, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(reader, header); err != nil {
		return nil, err
	}
	size := int(header[0]) | int(header[1])<<8 | int(header[2])<<16 | int(header[3])<<24
	if size <= 0 || size > limit {
		return nil, errors.New("命名管道消息超过限制")
	}
	raw := make([]byte, size)
	_, err := io.ReadFull(reader, raw)
	return raw, err
}

func concise(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\r", " "))
	value = strings.ReplaceAll(value, "\n", " ")
	if len(value) > 1000 {
		return value[:1000]
	}
	return value
}

func fail(err error) { _, _ = fmt.Fprintln(os.Stderr, err); os.Exit(1) }
