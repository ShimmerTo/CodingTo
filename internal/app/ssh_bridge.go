package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// resolveSSHBridgeBinary locates a sibling helper and falls back to the main
// executable, which self-dispatches the ssh-security-bridge command.
func resolveSSHBridgeBinary() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("CODINGTO_SSH_BRIDGE_BIN")); configured != "" {
		return validateSSHBridgeBinary(configured)
	}
	executableName := "ssh-security-bridge"
	if runtime.GOOS == "windows" {
		executableName += ".exe"
	}
	candidates := []string{}
	if executable, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(executable), executableName), executable)
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, "bin", executableName), filepath.Join(cwd, executableName))
	}
	for _, candidate := range candidates {
		if path, err := validateSSHBridgeBinary(candidate); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("SSH Security Bridge helper 不可用")
}

func validateSSHBridgeBinary(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("SSH Security Bridge helper 不是普通文件")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, absolute, "ssh-security-bridge", "version", "--json")
	configureDocumentBridgeProcess(command)
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("SSH Security Bridge helper 无法执行: %w", err)
	}
	var version struct {
		ProtocolVersion int `json:"protocolVersion"`
	}
	if err := json.Unmarshal(output, &version); err != nil || version.ProtocolVersion != 1 {
		return "", fmt.Errorf("SSH Security Bridge helper 协议不兼容")
	}
	return absolute, nil
}
