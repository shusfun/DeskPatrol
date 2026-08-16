//go:build windows

package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func readProtectedFile(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return cryptUnprotect(raw)
}

func writeProtectedFile(path string, raw []byte) error {
	protected, err := cryptProtect(raw)
	if err != nil {
		return err
	}
	return os.WriteFile(path, protected, 0o600)
}

func protectDataDirectory(path string) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	sddl := "D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

func cryptProtect(raw []byte) ([]byte, error) {
	if len(raw) == 0 {
		return nil, errors.New("不能保护空数据")
	}
	in := windows.DataBlob{Size: uint32(len(raw)), Data: &raw[0]}
	var out windows.DataBlob
	if err := windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_LOCAL_MACHINE, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}

func cryptUnprotect(raw []byte) ([]byte, error) {
	if len(raw) == 0 {
		return nil, errors.New("受保护数据为空")
	}
	in := windows.DataBlob{Size: uint32(len(raw)), Data: &raw[0]}
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, 0, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}

func installMeshAgentElevated(downloadURL, expectedSHA256 string) error {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return err
	}
	pipeName := `\\.\pipe\DeskPatrol-` + hex.EncodeToString(random)
	name, _ := windows.UTF16PtrFromString(pipeName)
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;OW)(A;;GA;;;BA)")
	if err != nil {
		return fmt.Errorf("创建命名管道 ACL 失败: %w", err)
	}
	attributes := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}
	handle, err := windows.CreateNamedPipe(name, windows.PIPE_ACCESS_DUPLEX|windows.FILE_FLAG_FIRST_PIPE_INSTANCE, windows.PIPE_TYPE_MESSAGE|windows.PIPE_READMODE_MESSAGE|windows.PIPE_WAIT, 1, 64<<10, 64<<10, 60000, attributes)
	if err != nil {
		return fmt.Errorf("创建一次性命名管道失败: %w", err)
	}
	pipe := os.NewFile(uintptr(handle), pipeName)
	if pipe == nil {
		_ = windows.CloseHandle(handle)
		return errors.New("创建一次性命名管道文件失败")
	}
	defer pipe.Close()
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	helperPath := filepath.Join(filepath.Dir(executable), "DeskPatrolHelper.exe")
	if _, err := os.Stat(helperPath); err != nil {
		return fmt.Errorf("提权 helper 不可用: %w", err)
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	helper, _ := windows.UTF16PtrFromString(helperPath)
	arguments, _ := windows.UTF16PtrFromString(`--pipe "` + pipeName + `"`)
	directory, _ := windows.UTF16PtrFromString(filepath.Dir(executable))
	if err := windows.ShellExecute(0, verb, helper, arguments, directory, windows.SW_HIDE); err != nil {
		return fmt.Errorf("启动提权 helper 失败: %w", err)
	}
	connected := make(chan error, 1)
	go func() {
		err := windows.ConnectNamedPipe(handle, nil)
		if errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
			err = nil
		}
		connected <- err
	}()
	select {
	case err := <-connected:
		if err != nil {
			return fmt.Errorf("提权 helper 连接失败: %w", err)
		}
	case <-time.After(90 * time.Second):
		return errors.New("等待提权 helper 超时或用户取消了 UAC")
	}
	payload, _ := json.Marshal(map[string]string{"downloadUrl": downloadURL, "sha256": expectedSHA256})
	if err := writeFrame(pipe, payload); err != nil {
		return fmt.Errorf("向提权 helper 发送配置失败: %w", err)
	}
	raw, err := readFrame(pipe, 64<<10)
	if err != nil {
		return fmt.Errorf("读取提权 helper 结果失败: %w", err)
	}
	var result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return fmt.Errorf("提权 helper 响应不正确: %w", err)
	}
	if !result.OK {
		return errors.New(firstNonEmpty(result.Error, "MeshAgent 安装失败"))
	}
	return nil
}

func meshAgentServiceStatus() string {
	manager, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return fmt.Sprintf("无法读取服务: %v", err)
	}
	defer windows.CloseServiceHandle(manager)
	name, _ := windows.UTF16PtrFromString("Mesh Agent")
	service, err := windows.OpenService(manager, name, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return "未安装"
		}
		return fmt.Sprintf("无法打开服务: %v", err)
	}
	defer windows.CloseServiceHandle(service)
	var status windows.SERVICE_STATUS
	if err := windows.QueryServiceStatus(service, &status); err != nil {
		return fmt.Sprintf("无法查询服务: %v", err)
	}
	switch status.CurrentState {
	case windows.SERVICE_RUNNING:
		return "运行中"
	case windows.SERVICE_START_PENDING:
		return "正在启动"
	case windows.SERVICE_STOP_PENDING:
		return "正在停止"
	case windows.SERVICE_STOPPED:
		return "已停止"
	default:
		return fmt.Sprintf("状态 %d", status.CurrentState)
	}
}

func writeFrame(writer io.Writer, raw []byte) error {
	if len(raw) == 0 || len(raw) > 64<<10 {
		return errors.New("命名管道消息大小不正确")
	}
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
