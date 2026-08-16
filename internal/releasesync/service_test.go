package releasesync

import (
	"bytes"
	"strings"
	"testing"
)

func TestValidateArtifactRejectsPathsAndBadDigest(t *testing.T) {
	if err := validateArtifact(Artifact{Filename: "../client.exe", Platform: "windows", Architecture: "amd64", Size: 1, SHA256: strings.Repeat("a", 64)}); err == nil {
		t.Fatal("应拒绝目录穿越文件名")
	}
	if err := validateArtifact(Artifact{Filename: "client.exe", Platform: "windows", Architecture: "amd64", Size: 1, SHA256: "bad"}); err == nil {
		t.Fatal("应拒绝无效 SHA-256")
	}
}

func TestCopyWithProgressReportsDeltas(t *testing.T) {
	raw := bytes.Repeat([]byte("x"), (2<<20)+17)
	var output bytes.Buffer
	var progress int64
	written, err := copyWithProgress(&output, bytes.NewReader(raw), func(delta int64) { progress += delta })
	if err != nil {
		t.Fatal(err)
	}
	if written != int64(len(raw)) || progress != written || !bytes.Equal(output.Bytes(), raw) {
		t.Fatalf("下载进度必须按增量上报: written=%d progress=%d", written, progress)
	}
}

func TestSafeErrorRemovesRemoteURL(t *testing.T) {
	value := safeError(assertError("GET https://github.com/private/release?token=secret failed"))
	if strings.Contains(value, "github.com") || strings.Contains(value, "token") {
		t.Fatalf("远程 URL 未过滤: %s", value)
	}
}

type assertError string

func (e assertError) Error() string { return string(e) }
