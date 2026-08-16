package setup

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"

	"deskpatrol/internal/appconfig"
	"deskpatrol/internal/database"
	"deskpatrol/internal/meshcentral"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

var (
	identifierPattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{1,62}$`)
	installMu         sync.Mutex
)

type Admin struct {
	LoginName string `json:"loginName"`
	Password  string `json:"password"`
}

type Request struct {
	PublicURL        string             `json:"publicUrl"`
	AgentPublicPort  int                `json:"agentPublicPort"`
	StorageDir       string             `json:"storageDir"`
	GitHubRepository string             `json:"githubRepository"`
	Database         appconfig.Database `json:"database"`
	Admin            Admin              `json:"admin"`
}

type Result struct {
	Installed bool `json:"installed"`
}

func Defaults(configPath string) (Request, error) {
	storageDir, err := defaultStorageDir(runtime.GOOS, configPath)
	if err != nil {
		return Request{}, err
	}
	return Request{
		PublicURL:        "https://deskpatrol.example.com",
		AgentPublicPort:  8443,
		StorageDir:       storageDir,
		GitHubRepository: "owner/deskpatrol",
		Database:         appconfig.Database{Host: "127.0.0.1", Port: 5432, User: "deskpatrol", Name: "deskpatrol", SSLMode: "disable"},
		Admin:            Admin{LoginName: "admin"},
	}, nil
}

func defaultStorageDir(goos, configPath string) (string, error) {
	if goos == "linux" {
		return "/var/lib/deskpatrol", nil
	}
	absoluteConfigPath, err := filepath.Abs(configPath)
	if err != nil {
		return "", fmt.Errorf("解析配置文件绝对路径失败: %w", err)
	}
	return filepath.Join(filepath.Dir(absoluteConfigPath), "lib", "deskpatrol"), nil
}

func Validate(req Request) error {
	parsed, err := url.Parse(strings.TrimSpace(req.PublicURL))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Path != "" {
		return errors.New("公网地址必须是无路径的 HTTPS 地址")
	}
	if req.AgentPublicPort < 1 || req.AgentPublicPort > 65535 {
		return errors.New("Agent 公网端口必须介于 1 和 65535")
	}
	if !filepath.IsAbs(req.StorageDir) {
		return errors.New("存储目录必须是绝对路径")
	}
	if strings.TrimSpace(req.GitHubRepository) == "" {
		return errors.New("GitHub 仓库不能为空")
	}
	if !identifierPattern.MatchString(req.Database.User) || !identifierPattern.MatchString(req.Database.Name) {
		return errors.New("PostgreSQL 用户名和数据库名格式不正确")
	}
	if net.ParseIP(req.Database.Host) == nil && !validHostname(req.Database.Host) {
		return errors.New("PostgreSQL 主机名格式不正确")
	}
	if req.Database.Port < 1 || req.Database.Port > 65535 {
		return errors.New("PostgreSQL 端口不正确")
	}
	if req.Database.SSLMode != "disable" && req.Database.SSLMode != "require" && req.Database.SSLMode != "verify-full" {
		return errors.New("PostgreSQL SSL 模式不受支持")
	}
	if !identifierPattern.MatchString(req.Admin.LoginName) {
		return errors.New("管理员账号格式不正确")
	}
	if len(req.Admin.Password) < 6 || len(req.Admin.Password) > 128 {
		return errors.New("管理员密码长度必须介于 6 和 128")
	}
	return nil
}

func TestDatabase(ctx context.Context, cfg appconfig.Database) error {
	pool, err := database.Open(ctx, cfg)
	if err != nil {
		return err
	}
	pool.Close()
	return nil
}

func Install(ctx context.Context, configPath string, req Request) (Result, error) {
	installMu.Lock()
	defer installMu.Unlock()
	if !appconfig.NeedsSetup(configPath) {
		return Result{}, errors.New("系统已完成初始化")
	}
	if err := Validate(req); err != nil {
		return Result{}, err
	}
	pool, err := database.Open(ctx, req.Database)
	if err != nil {
		return Result{}, err
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("开始安装事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, database.Schema); err != nil {
		return Result{}, fmt.Errorf("初始化数据库结构失败: %w", err)
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(req.Admin.Password), bcrypt.DefaultCost)
	if err != nil {
		return Result{}, fmt.Errorf("生成管理员密码摘要失败: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO administrators(login_name,password_hash) VALUES($1,$2)`, req.Admin.LoginName, string(passwordHash)); err != nil {
		return Result{}, fmt.Errorf("创建管理员失败: %w", err)
	}
	sessionSecret, err := appconfig.NewSessionSecret()
	if err != nil {
		return Result{}, err
	}
	pluginToken, err := appconfig.NewRandomSecret(32)
	if err != nil {
		return Result{}, err
	}
	meshLoginKey, err := appconfig.NewMeshLoginKey()
	if err != nil {
		return Result{}, err
	}
	cfg := appconfig.Config{Listen: "127.0.0.1:18123", PublicURL: strings.TrimRight(req.PublicURL, "/"), AgentPublicPort: req.AgentPublicPort, StorageDir: filepath.Clean(req.StorageDir), GitHubRepository: strings.TrimSpace(req.GitHubRepository), SessionSecret: sessionSecret, PluginToken: pluginToken, MeshLoginKey: meshLoginKey, Database: req.Database}
	if err := os.MkdirAll(cfg.StorageDir, 0o700); err != nil {
		return Result{}, fmt.Errorf("创建存储目录失败: %w", err)
	}
	if err := appconfig.WriteAtomic(configPath, cfg); err != nil {
		return Result{}, err
	}
	configDirectory := filepath.Dir(configPath)
	meshConfigPath := filepath.Join(configDirectory, "meshcentral-config.json")
	meshEnvironmentPath := filepath.Join(configDirectory, "meshcentral.env")
	if err := meshcentral.WriteConfig(meshConfigPath, meshcentral.ConfigInput{PublicURL: cfg.PublicURL, AgentPublicPort: cfg.AgentPublicPort, StorageDir: cfg.StorageDir, DatabaseHost: cfg.Database.Host, DatabasePort: cfg.Database.Port, DatabaseUser: cfg.Database.User, DatabasePass: cfg.Database.Password, DatabaseName: cfg.Database.Name, LoginKey: cfg.MeshLoginKey}); err != nil {
		_ = os.Remove(configPath)
		return Result{}, fmt.Errorf("写入 MeshCentral 配置失败: %w", err)
	}
	if err := meshcentral.WriteEnvironment(meshEnvironmentPath, cfg.PluginToken); err != nil {
		_ = os.Remove(configPath)
		_ = os.Remove(meshConfigPath)
		return Result{}, fmt.Errorf("写入 MeshCentral 插件环境失败: %w", err)
	}
	cleanupFiles := true
	defer func() {
		if cleanupFiles {
			_ = os.Remove(configPath)
			_ = os.Remove(appconfig.LockPath(configPath))
			_ = os.Remove(meshConfigPath)
			_ = os.Remove(meshEnvironmentPath)
		}
	}()
	if err := appconfig.WriteLock(configPath); err != nil {
		return Result{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Result{}, fmt.Errorf("提交安装事务失败: %w", err)
	}
	cleanupFiles = false
	return Result{Installed: true}, nil
}

func RunMigrations(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, database.Schema)
	return err
}

func validHostname(value string) bool {
	if len(value) < 1 || len(value) > 253 {
		return false
	}
	for _, part := range strings.Split(value, ".") {
		if part == "" || len(part) > 63 || strings.HasPrefix(part, "-") || strings.HasSuffix(part, "-") {
			return false
		}
		for _, char := range part {
			if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '-' {
				return false
			}
		}
	}
	return true
}
