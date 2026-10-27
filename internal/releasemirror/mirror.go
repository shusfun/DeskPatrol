// Package releasemirror 对 GitHub Release 下载线路测速并选择最快的一条。
// 镜像只负责传输大文件；manifest 和 SHA256SUMS 始终直连 GitHub 获取，下载结果必须通过其中的 SHA-256 校验。
package releasemirror

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Prefixes 是候选下载线路，空字符串表示直连 github.com。
// 必须与 scripts/install.sh 中的 mirror_prefixes 保持一致（由测试校验）。
var Prefixes = []string{
	"",
	"https://ghfast.top/",
	"https://gh-proxy.com/",
	"https://gh-proxy.org/",
	"https://gh.llkk.cc/",
	"https://ghproxy.net/",
}

// ProbeBytes 是每条线路测速下载的字节数。
const ProbeBytes = 1 << 20

const probeTimeout = 10 * time.Second

// Result 记录一条线路的测速结果。
type Result struct {
	Prefix         string
	BytesPerSecond float64
	Err            error
}

// Name 返回线路的展示名称。
func Name(prefix string) string {
	if prefix == "" {
		return "github.com 直连"
	}
	return strings.TrimSuffix(strings.TrimPrefix(prefix, "https://"), "/")
}

// Select 依次（避免互相抢带宽）用 Range 请求下载 probeURL 的前 ProbeBytes 字节，返回速度最快的线路前缀。
// 所有线路都失败时返回包含每条线路失败原因的错误。
func Select(ctx context.Context, client *http.Client, prefixes []string, probeURL string) (string, []Result, error) {
	results := make([]Result, 0, len(prefixes))
	best := -1
	for _, prefix := range prefixes {
		result := Result{Prefix: prefix}
		result.BytesPerSecond, result.Err = probe(ctx, client, prefix+probeURL)
		results = append(results, result)
		if result.Err == nil && (best < 0 || result.BytesPerSecond > results[best].BytesPerSecond) {
			best = len(results) - 1
		}
	}
	if best < 0 {
		reasons := make([]string, 0, len(results))
		for _, result := range results {
			reasons = append(reasons, fmt.Sprintf("%s: %v", Name(result.Prefix), result.Err))
		}
		return "", results, fmt.Errorf("所有下载线路测速失败（%s）", strings.Join(reasons, "；"))
	}
	return results[best].Prefix, results, nil
}

func probe(ctx context.Context, client *http.Client, target string) (float64, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", ProbeBytes-1))
	started := time.Now()
	response, err := client.Do(req)
	if err != nil {
		return 0, errors.New("连接失败或超时")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusPartialContent && response.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP %d", response.StatusCode)
	}
	written, err := io.Copy(io.Discard, io.LimitReader(response.Body, ProbeBytes))
	if err != nil || written < ProbeBytes {
		return 0, errors.New("测速数据不完整")
	}
	elapsed := time.Since(started).Seconds()
	if elapsed <= 0 {
		elapsed = 1e-6
	}
	return float64(written) / elapsed, nil
}
