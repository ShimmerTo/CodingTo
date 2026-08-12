//go:build darwin

package piagent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// InstallNode downloads the official universal macOS Node.js package and asks
// macOS for administrator approval to install it. The package installs node and
// npm into /usr/local/bin, which prepareCommandPath exposes to Finder-launched
// CodingTo processes.
func InstallNode(onLog func(string)) error {
	onLog("正在获取 Node.js LTS 版本信息…")
	version, err := latestLTSNodeVersion()
	if err != nil {
		return fmt.Errorf("获取 Node.js 版本失败: %w", err)
	}
	pkgURL := fmt.Sprintf("https://nodejs.org/dist/%s/node-%s.pkg", version, version)
	onLog("下载安装包: " + pkgURL)

	tmp, err := os.MkdirTemp("", "codingto-node-*")
	if err != nil {
		return fmt.Errorf("创建临时目录失败: %w", err)
	}
	defer os.RemoveAll(tmp)

	pkgPath := filepath.Join(tmp, "node.pkg")
	if err := downloadFile(pkgURL, pkgPath, onLog); err != nil {
		return fmt.Errorf("下载 Node.js 失败: %w", err)
	}

	onLog("正在请求管理员权限安装 Node.js…")
	const script = `on run argv
do shell script "/usr/sbin/installer -pkg " & quoted form of item 1 of argv & " -target /" with administrator privileges
end run`
	cmd := exec.Command("/usr/bin/osascript", "-e", script, pkgPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message != "" {
			onLog(message)
		}
		return fmt.Errorf("安装 Node.js 失败: %w", err)
	}
	if !NodeInstalled() || !NpmInstalled() {
		return fmt.Errorf("Node.js 安装完成，但未在 /usr/local/bin 中找到 node/npm")
	}
	onLog("Node.js 安装完成。")
	return nil
}
