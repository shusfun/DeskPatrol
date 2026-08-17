package meshcentral

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }

func TestRunPowerShellUsesLoopbackTokenAndParsesResult(t *testing.T) {
	controller := NewController("", "plugin-secret", t.TempDir())
	controller.CommandURL = "https://127.0.0.1:18129/deskpatrol/run-command"
	controller.HTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("X-DeskPatrol-Plugin-Token") != "plugin-secret" {
			t.Fatal("插件令牌未通过请求头传递")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBufferString(`{"stdout":"ok","stderr":"","exitCode":0,"outputTruncated":false}`)), Header: make(http.Header)}, nil
	})}
	result, err := controller.RunPowerShell(context.Background(), "node//abc", "Get-Date", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if result.Stdout != "ok" || result.ExitCode != 0 {
		t.Fatalf("诊断结果不正确: %#v", result)
	}
}

func TestMeshIdentifiersAreStrict(t *testing.T) {
	controller := NewController("", "", t.TempDir())
	if controller.ControlURL != "ws://127.0.0.1:18129" || controller.AgentControlURL != "wss://127.0.0.1:18130" {
		t.Fatalf("MeshCentral 控制地址不正确: %#v", controller)
	}
	if _, err := controller.AddDeviceGroup(context.Background(), "", ""); err == nil {
		t.Fatal("空设备组名称必须被拒绝")
	}
	if err := controller.MoveToDeviceGroup(context.Background(), "bad", "mesh//ok"); err == nil {
		t.Fatal("错误 NodeID 必须被拒绝")
	}
	if _, err := controller.CreateDesktopShare(context.Background(), "bad", 5); err == nil {
		t.Fatal("错误 NodeID 必须被桌面分享拒绝")
	}
}

func TestDesktopShareURLPatternMatchesPinnedMeshCtrlOutput(t *testing.T) {
	output := "----------\nIdentifier: abc\nType: View Only Desktop\nURL:          https://deskpatrol.example.com/sharing?c=temporary-ticket\n"
	match := sharingURLPattern.FindStringSubmatch(output)
	if len(match) != 2 || match[1] != "https://deskpatrol.example.com/sharing?c=temporary-ticket" {
		t.Fatalf("未解析固定版本 MeshCtrl 的分享地址: %#v", match)
	}
	if sharingURLPattern.MatchString("https://other.example.com/sharing?c=not-a-meshctrl-result") {
		t.Fatal("不得接受非 MeshCtrl URL 字段中的地址")
	}
}

func TestMeshIDPatternMatchesPinnedMeshCtrlDollarOutput(t *testing.T) {
	raw := "xdY7fXe1kE0l$Ew6YQLKoSSxVSMJTWJS8e@xYFTz2EB6SH0g1OY@PQk1d2jTwI8K"
	output := "ok mesh//" + raw
	if value := meshIDPattern.FindString(output); value != "mesh//"+raw {
		t.Fatalf("MeshID 解析失败: %q", value)
	}
}

func TestAgentDownloadIDUsesRawMeshIdentifier(t *testing.T) {
	raw := "xdY7fXe1kE0l$Ew6YQLKoSSxVSMJTWJS8e@xYFTz2EB6SH0g1OY@PQk1d2jTwI8K"
	for _, meshID := range []string{"mesh//" + raw, "mesh/domain/" + raw} {
		value, err := agentDownloadID(meshID)
		if err != nil || value != raw {
			t.Fatalf("AgentDownload ID 转换失败: meshID=%q value=%q err=%v", meshID, value, err)
		}
	}
	for _, meshID := range []string{"", "mesh//short", "mesh//" + strings.Repeat("/", 64), "mesh//" + strings.Repeat(".", 64), "node//" + raw} {
		if _, err := agentDownloadID(meshID); err == nil {
			t.Fatalf("错误 MeshID 必须被拒绝: %q", meshID)
		}
	}
}

func TestAgentDownloadOutputMatchesPinnedMeshCtrlExitOneSuccess(t *testing.T) {
	if err := validateAgentDownloadOutput(`Downloaded 3489752 byte(s) to "meshagent64-DeskPatrol.exe"`, "meshagent64-DeskPatrol.exe", 3489752); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		output   string
		filename string
		size     int64
	}{
		{output: "Unable to connect", filename: "meshagent.exe", size: 1},
		{output: "Unable to connect\n" + `Downloaded 20 byte(s) to "meshagent.exe"`, filename: "meshagent.exe", size: 20},
		{output: `Downloaded 20 byte(s) to "other.exe"`, filename: "meshagent.exe", size: 20},
		{output: `Downloaded 20 byte(s) to "meshagent.exe"`, filename: "meshagent.exe", size: 19},
	} {
		if err := validateAgentDownloadOutput(test.output, test.filename, test.size); err == nil {
			t.Fatalf("错误 AgentDownload 输出必须被拒绝: %#v", test)
		}
	}
}
