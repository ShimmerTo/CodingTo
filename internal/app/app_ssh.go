package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"codingto/internal/applog"
	"codingto/internal/dbsecurity"
	"codingto/internal/dbsecuritybridge/tunnel"
	"codingto/internal/sshsecurity"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// knownHostsFileName 是 TOFU 主机密钥指纹记录文件（位于应用配置目录），
// 主进程测试连接与两个桥接子进程共享同一路径。
const knownHostsFileName = "ssh_known_hosts.json"

// knownHostsPath 返回应用配置目录下的 TOFU 主机指纹记录文件路径。
func knownHostsPath(base string) string { return filepath.Join(base, knownHostsFileName) }

// SSHTestRequest 携带待测试的 SSH 配置；密码/私钥走前端表单值，
// 空密码时由 App 层沿用已存密码（前端不回显 SSH 密码）。
type SSHTestRequest struct {
	Config SSHConfig `json:"config"`
}

type SSHTestResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// SSHKeyFileResult 是「选择密钥文件」的返回：私钥内容读入后填回表单，
// Path 仅用于提示用户当前选中的文件。
type SSHKeyFileResult struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// sshTestTimeout 是「测试连接」的总兜底超时。tunnel.TestConnection 内部
// 的 dialClient 已用 net.DialTimeout + SetDeadline 限制建连与握手，但实测
// 在某些场景（Windows 下静默丢包的地址、握手阶段不回包）仍可能悬挂；
// 这里用 context 再加一层兜底，确保前端必有返回。超时后 goroutine 内的
// 调用会泄漏到 dialClient 自身超时为止，代价远小于前端永远转圈。
const sshTestTimeout = 15 * time.Second

// TestSSHConnection 一次性验证 SSH 配置（账号密码或私钥），
// 仅做握手，不建立常驻隧道，结果直接返回前端卡片就地展示。
func (a *App) TestSSHConnection(req SSHTestRequest) (SSHTestResult, error) {
	cfg := req.Config
	merged := []SSHConfig{cfg}
	mergeSSHCredentials(merged, a.store.Get().SSHConfigs)
	cfg = merged[0]
	if strings.TrimSpace(cfg.Address) == "" {
		return SSHTestResult{OK: false, Message: "服务器地址不能为空"}, nil
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		cfg.Port = 22
	}
	if strings.TrimSpace(cfg.Username) == "" {
		return SSHTestResult{OK: false, Message: "账号不能为空"}, nil
	}
	if cfg.AuthMode != "key" {
		cfg.AuthMode = "password"
	}
	tunnelCfg := dbsecurity.SSHTunnel{
		Address:              cfg.Address,
		Port:                 cfg.Port,
		Username:             cfg.Username,
		AuthMode:             cfg.AuthMode,
		Password:             cfg.Password,
		PrivateKey:           cfg.PrivateKey,
		PrivateKeyPassphrase: cfg.PrivateKeyPassphrase,
		HostKeyFingerprint:   cfg.HostKeyFingerprint,
	}
	applog.Infof("[TestSSHConnection] start: %s@%s:%d mode=%s", cfg.Username, cfg.Address, cfg.Port, cfg.AuthMode)
	ctx, cancel := context.WithTimeout(context.Background(), sshTestTimeout)
	defer cancel()
	type testResult struct{ err error }
	ch := make(chan testResult, 1)
	go func() {
		ch <- testResult{err: tunnel.TestConnection(tunnelCfg, sshsecurity.LoadKnownHosts(filepath.Join(a.store.Dir(), knownHostsFileName)))}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			applog.Warnf("[TestSSHConnection] failed: %v", r.err)
			return SSHTestResult{OK: false, Message: r.err.Error()}, nil
		}
		applog.Infof("[TestSSHConnection] ok: %s:%d", cfg.Address, cfg.Port)
		return SSHTestResult{OK: true, Message: "连接成功"}, nil
	case <-ctx.Done():
		applog.Warnf("[TestSSHConnection] timeout after %s: %s@%s:%d", sshTestTimeout, cfg.Username, cfg.Address, cfg.Port)
		return SSHTestResult{OK: false, Message: "SSH 测试超时（15 秒无响应），请检查网络或服务器是否可达"}, nil
	}
}

// ChooseSSHKeyFile 打开文件选择框并读取私钥内容，避免用户手动粘贴大段密钥。
func (a *App) ChooseSSHKeyFile() (SSHKeyFileResult, error) {
	path, err := application.Get().Dialog.OpenFile().
		SetTitle("Choose SSH private key").
		CanChooseFiles(true).
		CanChooseDirectories(false).
		AttachToWindow(a.window).
		PromptForSingleSelection()
	if err != nil {
		return SSHKeyFileResult{}, err
	}
	if path == "" {
		return SSHKeyFileResult{}, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return SSHKeyFileResult{}, err
	}
	if info.Size() > 4*1024*1024 {
		return SSHKeyFileResult{}, fmt.Errorf("私钥文件过大（>4MB）")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return SSHKeyFileResult{}, err
	}
	content := string(data)
	if strings.TrimSpace(content) == "" {
		return SSHKeyFileResult{}, fmt.Errorf("私钥文件为空")
	}
	return SSHKeyFileResult{Path: path, Content: content}, nil
}
