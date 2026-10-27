package releasemirror

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

func probeServer(t *testing.T, status int, delay time.Duration, size int) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		w.WriteHeader(status)
		_, _ = w.Write(make([]byte, size))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestSelectPicksFastestWorkingPrefix(t *testing.T) {
	slow := probeServer(t, http.StatusPartialContent, 200*time.Millisecond, ProbeBytes)
	fast := probeServer(t, http.StatusPartialContent, 0, ProbeBytes)
	broken := probeServer(t, http.StatusTooManyRequests, 0, 10)
	short := probeServer(t, http.StatusPartialContent, 0, 100)
	prefixes := []string{broken.URL + "/", slow.URL + "/", fast.URL + "/", short.URL + "/"}
	chosen, results, err := Select(context.Background(), http.DefaultClient, prefixes, "release.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if chosen != fast.URL+"/" {
		t.Fatalf("应选择最快线路 %s，实际 %s", fast.URL, chosen)
	}
	if len(results) != 4 || results[0].Err == nil || results[3].Err == nil {
		t.Fatalf("失败线路必须记录原因: %#v", results)
	}
}

func TestSelectReportsEveryFailure(t *testing.T) {
	broken := probeServer(t, http.StatusBadGateway, 0, 0)
	_, _, err := Select(context.Background(), http.DefaultClient, []string{broken.URL + "/"}, "x")
	if err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("全部失败时必须返回每条线路原因: %v", err)
	}
}

func TestPrefixesMatchInstallScript(t *testing.T) {
	raw, err := os.ReadFile("../../scripts/install.sh")
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`(?s)mirror_prefixes=\((.*?)\)`).FindSubmatch(raw)
	if match == nil {
		t.Fatal("install.sh 缺少 mirror_prefixes")
	}
	var scripted []string
	for _, value := range regexp.MustCompile(`"([^"]*)"`).FindAllSubmatch(match[1], -1) {
		scripted = append(scripted, string(value[1]))
	}
	if strings.Join(scripted, ",") != strings.Join(Prefixes, ",") {
		t.Fatalf("install.sh 与 Go 的下载线路不一致:\nscript=%v\ngo=%v", scripted, Prefixes)
	}
}
