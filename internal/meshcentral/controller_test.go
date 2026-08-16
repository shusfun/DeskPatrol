package meshcentral

import (
	"bytes"
	"context"
	"io"
	"net/http"
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
