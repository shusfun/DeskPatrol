package meshcentral

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"deskpatrol/internal/appconfig"
)

func TestWriteConfigPinsSecurityAndDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	input := ConfigInput{PublicURL: "https://monitor.example.com", AgentPublicPort: 8443, StorageDir: "/var/lib/deskpatrol", DatabaseHost: "127.0.0.1", DatabasePort: 5432, DatabaseUser: "deskpatrol", DatabasePass: "secret", DatabaseName: "deskpatrol", LoginKey: strings.Repeat("a", appconfig.MeshLoginKeySize)}
	if err := WriteConfig(path, input); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	settings := value["settings"].(map[string]any)
	if settings["selfUpdate"] != false || settings["noAgentUpdate"] != float64(1) || settings["autoBackup"] != false || settings["agentAliasDNS"] != "monitor.example.com" || settings["tlsOffload"] != "127.0.0.1" {
		t.Fatalf("MeshCentral 安全配置不正确: %#v", settings)
	}
	if settings["datapath"] != "/var/lib/deskpatrol/meshcentral" {
		t.Fatalf("MeshCentral 数据目录不正确: %#v", settings["datapath"])
	}
	if settings["filespath"] != "/var/lib/deskpatrol/meshcentral-files" {
		t.Fatalf("MeshCentral 文件目录不正确: %#v", settings["filespath"])
	}
	if _, exists := settings["dataPath"]; exists {
		t.Fatal("MeshCentral 1.2.5 不读取 dataPath，必须使用 datapath")
	}
	postgres := settings["postgres"].(map[string]any)
	if postgres["database"] != "deskpatrol_mesh" {
		t.Fatalf("MeshCentral 数据库未隔离: %#v", postgres)
	}
}
