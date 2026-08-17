package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"deskpatrol/internal/security"
)

type LocalState struct {
	ServerURL          string `json:"serverUrl"`
	DeviceID           string `json:"deviceId"`
	DeviceName         string `json:"deviceName"`
	DeviceToken        string `json:"deviceToken"`
	AgentSetupRequired bool   `json:"agentSetupRequired"`
	AgentSetupStatus   string `json:"agentSetupStatus"`
	AgentSetupError    string `json:"agentSetupError"`
	AgentNextRetryAt   string `json:"agentNextRetryAt"`
}

type LocalStore struct{ dir string }

func NewLocalStore() (*LocalStore, error) {
	dir, err := localDataDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("创建客户端数据目录失败: %w", err)
	}
	if err := protectDataDirectory(dir); err != nil {
		return nil, fmt.Errorf("限制客户端数据目录权限失败: %w", err)
	}
	return &LocalStore{dir: dir}, nil
}

func (s *LocalStore) Load() (LocalState, error) {
	raw, err := readProtectedFile(filepath.Join(s.dir, "state.dat"))
	if errors.Is(err, os.ErrNotExist) {
		return LocalState{}, nil
	}
	if err != nil {
		return LocalState{}, fmt.Errorf("读取本机状态失败: %w", err)
	}
	var state LocalState
	if err := json.Unmarshal(raw, &state); err != nil {
		return LocalState{}, fmt.Errorf("解析本机状态失败: %w", err)
	}
	return state, nil
}

func (s *LocalStore) Save(state LocalState) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return writeProtectedFile(filepath.Join(s.dir, "state.dat"), raw)
}

func (s *LocalStore) ClearState() error {
	err := os.Remove(filepath.Join(s.dir, "state.dat"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s *LocalStore) InstallationID() (string, error) {
	path := filepath.Join(s.dir, "installation-id.dat")
	raw, err := readProtectedFile(path)
	if err == nil && len(raw) == 32 {
		return string(raw), nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	id := hex.EncodeToString(value)
	if err := writeProtectedFile(path, []byte(id)); err != nil {
		return "", err
	}
	return id, nil
}

func (s *LocalStore) LogsDir() string { return filepath.Join(s.dir, "logs") }

func (s *LocalStore) AppendFrontendError(raw string) error {
	path := filepath.Join(s.dir, "frontend-errors.dat")
	items := make([]json.RawMessage, 0)
	if content, err := readProtectedFile(path); err == nil {
		if err := json.Unmarshal(content, &items); err != nil {
			return fmt.Errorf("读取前端异常队列失败: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	filtered := security.Redact(strings.TrimSpace(raw))
	if !json.Valid([]byte(filtered)) {
		return errors.New("前端异常内容不是有效 JSON")
	}
	items = append(items, json.RawMessage(filtered))
	if len(items) > 200 {
		items = items[len(items)-200:]
	}
	content, err := json.Marshal(items)
	if err != nil {
		return err
	}
	return writeProtectedFile(path, content)
}

func (s *LocalStore) FrontendErrors() ([]json.RawMessage, error) {
	path := filepath.Join(s.dir, "frontend-errors.dat")
	content, err := readProtectedFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return []json.RawMessage{}, nil
	}
	if err != nil {
		return nil, err
	}
	var items []json.RawMessage
	if err := json.Unmarshal(content, &items); err != nil {
		return nil, fmt.Errorf("读取前端异常队列失败: %w", err)
	}
	return items, nil
}

func (s *LocalStore) ClearFrontendErrors() error {
	err := os.Remove(filepath.Join(s.dir, "frontend-errors.dat"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func localDataDir() (string, error) {
	if runtime.GOOS == "windows" {
		programData := os.Getenv("ProgramData")
		if programData == "" {
			return "", errors.New("ProgramData 环境变量不存在")
		}
		return filepath.Join(programData, "DeskPatrol"), nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "DeskPatrol"), nil
}
