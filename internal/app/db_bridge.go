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

// resolveDBBridgeBinary 定位 db-security-bridge 可执行文件。
// 与 document_bridge.go 对齐，但不提供统一可执行文件回退：
// 三个数据库 driver 只打进 bridge 二进制，主程序不因此膨胀。
func resolveDBBridgeBinary() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("CODINGTO_DB_BRIDGE_BIN")); configured != "" {
		return validateDBBridgeBinary(configured)
	}
	executableName := "db-security-bridge"
	if runtime.GOOS == "windows" {
		executableName += ".exe"
	}
	candidates := []string{}
	if executable, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(executable), executableName))
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates,
			filepath.Join(cwd, "bin", executableName),
			filepath.Join(cwd, executableName),
		)
	}
	for _, candidate := range candidates {
		if path, err := validateDBBridgeBinary(candidate); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("DB Security Bridge helper 未安装；应与 CodingTo 一同构建并放在应用可执行文件旁")
}

func validateDBBridgeBinary(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("DB Security Bridge helper 不是普通文件")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, absolute, "db-security-bridge", "version", "--json")
	configureDocumentBridgeProcess(command)
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("DB Security Bridge helper 无法执行: %w", err)
	}
	var version struct {
		ProtocolVersion int `json:"protocolVersion"`
	}
	if err := json.Unmarshal(output, &version); err != nil || version.ProtocolVersion != 1 {
		return "", fmt.Errorf("DB Security Bridge helper 协议不兼容")
	}
	return absolute, nil
}
