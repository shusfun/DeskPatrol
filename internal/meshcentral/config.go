package meshcentral

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"deskpatrol/internal/appconfig"
)

type ConfigInput struct {
	PublicURL       string
	AgentPublicPort int
	StorageDir      string
	DatabaseHost    string
	DatabasePort    int
	DatabaseUser    string
	DatabasePass    string
	DatabaseName    string
	LoginKey        string
}

func WriteConfig(path string, input ConfigInput) error {
	publicURL, err := url.Parse(input.PublicURL)
	if err != nil || publicURL.Hostname() == "" {
		return fmt.Errorf("MeshCentral 公网地址不正确")
	}
	value := map[string]any{
		"settings": map[string]any{
			"cert":                     publicURL.Hostname(),
			"autoBackup":               false,
			"loginCookieEncryptionKey": input.LoginKey,
			"port":                     18129,
			"portBind":                 "127.0.0.1",
			"aliasPort":                443,
			"redirPort":                0,
			"agentPort":                18124,
			"agentPortBind":            "127.0.0.1",
			"agentAliasPort":           input.AgentPublicPort,
			"agentAliasDNS":            publicURL.Hostname(),
			"agentPortTls":             false,
			"trustedProxy":             "127.0.0.1",
			"tlsOffload":               "127.0.0.1",
			"WANonly":                  true,
			"selfUpdate":               false,
			"noAgentUpdate":            1,
			"temporaryAgentUpdate":     false,
			"webRTC":                   false,
			"allowHighQualityDesktop":  false,
			"desktopMultiplex":         true,
			"datapath":                 filepath.Join(input.StorageDir, "meshcentral"),
			"filespath":                filepath.Join(input.StorageDir, "meshcentral-files"),
			"plugins":                  map[string]any{"enabled": true, "list": []string{"deskpatrol"}},
			"postgres": map[string]any{
				"host": input.DatabaseHost, "port": input.DatabasePort, "user": input.DatabaseUser,
				"password": input.DatabasePass, "database": input.DatabaseName + "_mesh", "createdatabase": true,
			},
		},
		"domains": map[string]any{
			"": map[string]any{
				"title": "DeskPatrol Mesh", "newAccounts": false,
				"certUrl":               "https://127.0.0.1:18130",
				"desktop":               map[string]any{"viewonly": true, "disableconnectall": true},
				"localSessionRecording": false, "sessionRecording": false,
			},
		},
	}
	return writeJSONAtomic(path, value)
}

func WriteDeploymentFiles(configDirectory string, cfg appconfig.Config) error {
	meshConfigPath := filepath.Join(configDirectory, "meshcentral-config.json")
	if err := WriteConfig(meshConfigPath, ConfigInput{
		PublicURL: cfg.PublicURL, AgentPublicPort: cfg.AgentPublicPort, StorageDir: cfg.StorageDir,
		DatabaseHost: cfg.Database.Host, DatabasePort: cfg.Database.Port, DatabaseUser: cfg.Database.User,
		DatabasePass: cfg.Database.Password, DatabaseName: cfg.Database.Name, LoginKey: cfg.MeshLoginKey,
	}); err != nil {
		return fmt.Errorf("写入 MeshCentral 配置失败: %w", err)
	}
	if err := WriteEnvironment(filepath.Join(configDirectory, "meshcentral.env"), cfg.PluginToken); err != nil {
		return fmt.Errorf("写入 MeshCentral 插件环境失败: %w", err)
	}
	return nil
}

func WriteEnvironment(path, token string) error {
	if len(token) < 32 {
		return fmt.Errorf("MeshCentral 插件令牌不正确")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary := path + ".tmp"
	content := []byte("DESKPATROL_PLUGIN_TOKEN=" + token + "\nDESKPATROL_CALLBACK_URL=http://127.0.0.1:18123/api/internal/meshcentral/events\n")
	if err := os.WriteFile(temporary, content, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func writeJSONAtomic(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}
