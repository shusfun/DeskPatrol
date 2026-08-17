package releasesync

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompareVersions(t *testing.T) {
	for _, test := range []struct {
		left, right string
		want        int
	}{
		{"0.1.3", "0.1.2", 1},
		{"1.2.0", "1.2.0", 0},
		{"1.9.0", "1.10.0", -1},
	} {
		if got := compareVersions(test.left, test.right); got != test.want {
			t.Fatalf("compareVersions(%q, %q) = %d, want %d", test.left, test.right, got, test.want)
		}
	}
}

func TestCleanupOlderReleaseDirectories(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"0.1.2", "0.1.3", ".staging-job", ".backup-job", "notes"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	service := &Service{}
	if err := service.cleanupOlderReleaseDirectories(root, "0.1.3"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"0.1.3", "notes"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("应保留 %s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "0.1.2")); !os.IsNotExist(err) {
		t.Fatalf("旧版本目录未清理: %v", err)
	}
	for _, name := range []string{".staging-job", ".backup-job"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("Release 工作目录 %s 未清理: %v", name, err)
		}
	}
}

func TestCleanupMissingReleaseDirectoryIsEmpty(t *testing.T) {
	service := &Service{}
	if err := service.cleanupOlderReleaseDirectories(filepath.Join(t.TempDir(), "releases"), ""); err != nil {
		t.Fatal(err)
	}
}

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
