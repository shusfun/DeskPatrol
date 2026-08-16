package appconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validConfig() Config {
	return Config{Listen: "127.0.0.1:18123", PublicURL: "https://monitor.example.com", AgentPublicPort: 8443, StorageDir: "/var/lib/deskpatrol", GitHubRepository: "owner/repository", SessionSecret: "session-secret", PluginToken: "plugin-token-value-with-at-least-32-characters", MeshLoginKey: strings.Repeat("a", 160), Database: Database{Host: "127.0.0.1", Port: 5432, User: "deskpatrol", Name: "deskpatrol", SSLMode: "require"}}
}

func TestValidateRejectsNonHTTPSPublicURL(t *testing.T) {
	cfg := validConfig()
	cfg.PublicURL = "http://monitor.example.com"
	if err := Validate(cfg); err == nil {
		t.Fatal("应拒绝非 HTTPS 公网地址")
	}
}

func TestWriteAtomicAndInstallLock(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.json")
	if err := WriteAtomic(path, validConfig()); err != nil {
		t.Fatal(err)
	}
	if NeedsSetup(path) != true {
		t.Fatal("写配置不应隐式创建安装锁")
	}
	if err := WriteLock(path); err != nil {
		t.Fatal(err)
	}
	if NeedsSetup(path) {
		t.Fatal("安装锁创建后不应继续进入 Setup")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("配置权限不正确: %v %v", info, err)
	}
}
