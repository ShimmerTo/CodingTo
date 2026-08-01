package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"codingto/internal/piagent"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// GetPiVersion returns the installed Pi Agent version.
func (a *App) GetPiVersion() string {
	v, err := piagent.Version()
	if err != nil {
		return ""
	}
	return v
}

// GetUpdateLog reads the project changelog (update.md) and returns its raw
// content for display in the settings UI. It prefers the copy shipped next to
// the executable (placed there by the build tasks) and falls back to the
// working directory so the log can be hot-updated during development.
func (a *App) GetUpdateLog() string {
	// 优先读取可执行文件所在目录的 update.md（发布产物由构建流程放置）。
	if exePath, err := os.Executable(); err == nil {
		if data, err := os.ReadFile(filepath.Join(filepath.Dir(exePath), "update.md")); err == nil {
			return string(data)
		}
	}
	// 回退到工作目录，便于开发期热更新日志。
	wd, err := os.Getwd()
	if err != nil {
		return "# 无法读取更新日志\n（获取工作目录失败）"
	}
	data, err := os.ReadFile(filepath.Join(wd, "update.md"))
	if err != nil {
		return "# 未找到更新日志\n（update.md 不存在于工作目录）"
	}
	return string(data)
}

// CheckPiUpdate compares the installed Pi Agent version with the latest
// published npm version.
func (a *App) CheckPiUpdate() PiUpdateStatus {
	installed, err := piagent.Version()
	if err != nil {
		return PiUpdateStatus{Error: err.Error()}
	}
	latest, err := piagent.LatestVersion()
	if err != nil {
		return PiUpdateStatus{Installed: installed, Error: "无法获取最新版本：" + err.Error()}
	}
	return PiUpdateStatus{
		Installed: installed,
		Latest:    latest,
		Available: latest != "" && latest != installed,
	}
}

// UpdatePi updates the Pi Agent to the latest version, streaming progress via
// piagent:start / piagent:log / piagent:done events.
func (a *App) UpdatePi() error {
	app := application.Get()
	app.Event.Emit("piagent:start", map[string]any{"title": "Pi Agent 更新"})
	_, err := piagent.InstallWithProgress(func(line string) {
		app.Event.Emit("piagent:log", map[string]any{"line": line})
	})
	app.Event.Emit("piagent:done", map[string]any{"success": err == nil})
	return err
}

// InstallPi installs the Pi CLI, verifying that it is discoverable, then
// initializes the first agent in the same operation. When npm is missing it
// first bootstraps Node.js (which bundles npm), so a fresh machine can install
// Pi Agent end-to-end from this single entry point.
func (a *App) InstallPi() (Bootstrap, error) {
	a.piInstall.Lock()
	defer a.piInstall.Unlock()

	app := application.Get()
	installID := fmt.Sprintf("pi-%d", time.Now().UnixNano())
	app.Event.Emit("install:start", map[string]any{"installId": installID, "title": "Pi Agent 安装"})
	success := true
	onLog := func(line string) {
		app.Event.Emit("install:log", map[string]any{"installId": installID, "line": line})
	}
	defer func() {
		app.Event.Emit("install:done", map[string]any{"installId": installID, "success": success})
	}()

	if !piagent.NpmInstalled() {
		onLog("未检测到 npm，需要先安装 Node.js（npm 随 Node.js 一同安装）…")
		if err := piagent.InstallNode(onLog); err != nil {
			onLog("Node.js 安装失败：" + err.Error())
			success = false
			return Bootstrap{}, err
		}
		if !piagent.NpmInstalled() {
			onLog("Node.js 已安装，但仍未找到 npm，请重启 CodingTo 后重试")
			success = false
			return Bootstrap{}, errors.New("npm 仍不可用，请重启程序后重试")
		}
		onLog("Node.js 安装完成。")
	}

	onLog("开始安装 Pi Agent…")
	if _, err := piagent.InstallWithProgress(onLog); err != nil {
		onLog("Pi Agent 安装失败：" + err.Error())
		success = false
		return Bootstrap{}, err
	}
	if _, err := a.store.EnsureDefaultAgent(); err != nil {
		success = false
		return Bootstrap{}, fmt.Errorf("initialize default agent: %w", err)
	}
	onLog("Pi Agent 安装完成。")
	return a.GetBootstrap(), nil
}
