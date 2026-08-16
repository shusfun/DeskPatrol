package appconfig

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const (
	DefaultConfigPath = "var/config.json"
	InstallLockName   = ".installed"
)

type Database struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	Name     string `json:"name"`
	SSLMode  string `json:"sslMode"`
}

type Config struct {
	Listen           string   `json:"listen"`
	PublicURL        string   `json:"publicUrl"`
	AgentPublicPort  int      `json:"agentPublicPort"`
	StorageDir       string   `json:"storageDir"`
	GitHubRepository string   `json:"githubRepository"`
	SessionSecret    string   `json:"sessionSecret"`
	PluginToken      string   `json:"pluginToken"`
	MeshLoginKey     string   `json:"meshLoginKey"`
	Database         Database `json:"database"`
}

func Path() string {
	if value := strings.TrimSpace(os.Getenv("DESKPATROL_CONFIG")); value != "" {
		return filepath.Clean(value)
	}
	return DefaultConfigPath
}

func LockPath(path string) string {
	return filepath.Join(filepath.Dir(path), InstallLockName)
}

func NeedsSetup(path string) bool {
	_, err := os.Stat(LockPath(path))
	return errors.Is(err, os.ErrNotExist)
}

func Load(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("读取配置失败: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("解析配置失败: %w", err)
	}
	if err := Validate(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func Validate(cfg Config) error {
	if strings.TrimSpace(cfg.Listen) == "" {
		return errors.New("监听地址不能为空")
	}
	publicURL, err := url.Parse(strings.TrimSpace(cfg.PublicURL))
	if err != nil || publicURL.Scheme != "https" || publicURL.Host == "" || publicURL.Path != "" {
		return errors.New("公网地址必须是无路径的 HTTPS 地址")
	}
	if cfg.AgentPublicPort < 1 || cfg.AgentPublicPort > 65535 {
		return errors.New("Agent 公网端口必须介于 1 和 65535")
	}
	if strings.TrimSpace(cfg.StorageDir) == "" || !filepath.IsAbs(cfg.StorageDir) {
		return errors.New("存储目录必须是绝对路径")
	}
	if !validGitHubRepository(cfg.GitHubRepository) {
		return errors.New("GitHub 仓库必须使用 owner/repository 格式")
	}
	if strings.TrimSpace(cfg.SessionSecret) == "" {
		return errors.New("会话密钥不能为空")
	}
	if len(cfg.PluginToken) < 32 || len(cfg.MeshLoginKey) != 160 {
		return errors.New("MeshCentral 内部凭据不完整")
	}
	if strings.TrimSpace(cfg.Database.Host) == "" || cfg.Database.Port < 1 || cfg.Database.Port > 65535 || strings.TrimSpace(cfg.Database.User) == "" || strings.TrimSpace(cfg.Database.Name) == "" {
		return errors.New("PostgreSQL 配置不完整")
	}
	if cfg.Database.SSLMode != "disable" && cfg.Database.SSLMode != "require" && cfg.Database.SSLMode != "verify-full" {
		return errors.New("PostgreSQL SSL 模式不受支持")
	}
	return nil
}

func NewSessionSecret() (string, error) {
	return NewRandomSecret(32)
}

func NewRandomSecret(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("生成随机密钥失败: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func NewMeshLoginKey() (string, error) {
	value := make([]byte, 80)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("生成 MeshCentral 登录密钥失败: %w", err)
	}
	return fmt.Sprintf("%x", value), nil
}

func WriteAtomic(path string, cfg Config) error {
	if err := Validate(cfg); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("创建配置目录失败: %w", err)
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("生成配置失败: %w", err)
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, raw, 0o600); err != nil {
		return fmt.Errorf("写入临时配置失败: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("保存配置失败: %w", err)
	}
	return nil
}

func WriteLock(path string) error {
	lock := LockPath(path)
	file, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("创建安装锁失败: %w", err)
	}
	return file.Close()
}

func validGitHubRepository(value string) bool {
	parts := strings.Split(strings.TrimSpace(value), "/")
	return len(parts) == 2 && parts[0] != "" && parts[1] != "" && !strings.ContainsAny(value, "?#\\ ")
}
