//go:build windows

package piagent

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/sys/windows/registry"
)

// InstallNode downloads the latest LTS Node.js installer for Windows and runs it
// silently. After installation the process PATH is refreshed so the npm CLI
// becomes discoverable for the remainder of this run.
func InstallNode(onLog func(string)) error {
	onLog("正在获取 Node.js LTS 版本信息…")
	version, err := latestLTSNodeVersion()
	if err != nil {
		return fmt.Errorf("获取 Node.js 版本失败: %w", err)
	}
	arch := "x64"
	if runtime.GOARCH == "arm64" {
		arch = "arm64"
	}
	msiURL := fmt.Sprintf("https://nodejs.org/dist/%s/node-%s-%s.msi", version, version, arch)
	onLog("下载安装包: " + msiURL)

	tmp, err := os.MkdirTemp("", "codingto-node-*")
	if err != nil {
		return fmt.Errorf("创建临时目录失败: %w", err)
	}
	defer os.RemoveAll(tmp)

	msiPath := filepath.Join(tmp, "node.msi")
	if err := downloadFile(msiURL, msiPath, onLog); err != nil {
		return fmt.Errorf("下载 Node.js 失败: %w", err)
	}

	onLog("运行 Node.js 安装程序（如弹出提示请允许管理员权限）…")
	cmd := exec.Command("msiexec", "/qn", "/i", msiPath)
	configureBackgroundProcess(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		onLog(strings.TrimSpace(string(out)))
		return fmt.Errorf("安装 Node.js 失败: %w", err)
	}
	refreshPath()
	onLog("Node.js 安装完成。")
	return nil
}

func latestLTSNodeVersion() (string, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get("https://nodejs.org/dist/index.json")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	var items []struct {
		Version string `json:"version"`
		LTS     any    `json:"lts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return "", err
	}
	for _, it := range items {
		if it.LTS != nil && it.LTS != false {
			return it.Version, nil
		}
	}
	if len(items) > 0 {
		return items[0].Version, nil
	}
	return "", fmt.Errorf("未找到可用版本")
}

func downloadFile(url, dest string, onLog func(string)) error {
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载失败，状态码 %d", resp.StatusCode)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return err
	}
	return nil
}

// refreshPath merges the machine and user PATH values from the registry into the
// current process environment so the freshly installed Node.js becomes findable
// by exec.LookPath for the rest of this run.
func refreshPath() {
	parts := make([]string, 0, 2)
	collect := func(root registry.Key, path string) {
		k, err := registry.OpenKey(root, path, registry.QUERY_VALUE)
		if err != nil {
			return
		}
		defer k.Close()
		if v, _, err := k.GetStringValue("Path"); err == nil && v != "" {
			parts = append(parts, v)
		}
	}
	collect(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`)
	collect(registry.CURRENT_USER, `Environment`)

	combined := strings.Join(parts, string(os.PathListSeparator))
	if existing := os.Getenv("PATH"); existing != "" {
		combined = existing + string(os.PathListSeparator) + combined
	}
	const nodeDir = `C:\Program Files\nodejs`
	if !strings.Contains(combined, nodeDir) {
		combined = combined + string(os.PathListSeparator) + nodeDir
	}
	_ = os.Setenv("PATH", combined)
}
