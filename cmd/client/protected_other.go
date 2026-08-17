//go:build !windows

package main

import (
	"errors"
	"os"
)

func readProtectedFile(path string) ([]byte, error)    { return os.ReadFile(path) }
func writeProtectedFile(path string, raw []byte) error { return os.WriteFile(path, raw, 0o600) }
func protectDataDirectory(path string) error           { return os.Chmod(path, 0o700) }
func installMeshAgentElevated(string, string) error {
	return errors.New("MeshAgent 安装仅支持 Windows")
}
func meshAgentServiceStatus() string        { return "仅 Windows 支持" }
func meshAgentServiceState() (string, bool) { return "仅 Windows 支持", false }
