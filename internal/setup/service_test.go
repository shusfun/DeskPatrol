package setup

import (
	"strings"
	"testing"

	"deskpatrol/internal/appconfig"
)

func validRequest() Request {
	return Request{PublicURL: "https://monitor.example.com", AgentPublicPort: 8443, StorageDir: "/var/lib/deskpatrol", GitHubRepository: "owner/repository", Database: appconfig.Database{Host: "127.0.0.1", Port: 5432, User: "deskpatrol", Name: "deskpatrol", SSLMode: "require"}, Admin: Admin{LoginName: "admin", Password: "long-random-password"}}
}

func TestValidateRequiresSixCharacterAdminPassword(t *testing.T) {
	req := validRequest()
	req.Admin.Password = "12345"
	if err := Validate(req); err == nil || !strings.Contains(err.Error(), "6") {
		t.Fatalf("应拒绝少于 6 位的密码，得到 %v", err)
	}
	req.Admin.Password = "123456"
	if err := Validate(req); err != nil {
		t.Fatalf("应接受 6 位密码，得到 %v", err)
	}
}

func TestValidateRejectsManagementAndAgentPortCollision(t *testing.T) {
	req := validRequest()
	req.AgentPublicPort = 443
	if err := Validate(req); err == nil {
		t.Fatal("应拒绝 Agent 与管理端端口冲突")
	}
}

func TestDefaultsUseNonTLSLoopbackPostgreSQL(t *testing.T) {
	defaults, err := Defaults("/tmp/deskpatrol/config.json")
	if err != nil {
		t.Fatalf("生成默认配置失败: %v", err)
	}
	if defaults.Database.Host != "127.0.0.1" || defaults.Database.SSLMode != "disable" {
		t.Fatalf("本机 PostgreSQL 默认值不正确: %#v", defaults.Database)
	}
}

func TestDefaultStorageDirUsesLinuxPersistentDirectory(t *testing.T) {
	directory, err := defaultStorageDir("linux", "/etc/deskpatrol/config.json")
	if err != nil {
		t.Fatalf("生成 Linux 默认存储目录失败: %v", err)
	}
	if directory != "/var/lib/deskpatrol" {
		t.Fatalf("Linux 默认存储目录不正确: %s", directory)
	}
}

func TestDefaultStorageDirUsesConfigDirectoryOutsideLinux(t *testing.T) {
	directory, err := defaultStorageDir("darwin", "/workspace/DeskPatrol/var/config.json")
	if err != nil {
		t.Fatalf("生成开发环境默认存储目录失败: %v", err)
	}
	if directory != "/workspace/DeskPatrol/var/lib/deskpatrol" {
		t.Fatalf("开发环境默认存储目录不正确: %s", directory)
	}
}
