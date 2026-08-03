package app

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"codingto/internal/piagent"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// updateLogMD 是随二进制一起编译的更新日志（internal/app/update.md）。
// 不再依赖运行时工作目录或 exe 旁边的文件，避免“update.md 不在工作目录”问题。
//
//go:embed update.md
var updateLogMD string

// GetPiVersion returns the installed Pi Agent version.
func (a *App) GetPiVersion() string {
	v, err := piagent.Version()
	if err != nil {
		return ""
	}
	return v
}

// GetUpdateLog returns the project changelog for display in the settings UI.
// The log is embedded into the binary at build time (internal/app/update.md),
// so it works regardless of the working directory. A file named update.md
// placed next to the executable still takes precedence, allowing packaged
// builds to ship a newer log without rebuilding.
func (a *App) GetUpdateLog() string {
	// 可执行文件旁的 update.md（如有）优先，便于发布包覆盖最新日志。
	if exePath, err := os.Executable(); err == nil {
		if data, err := os.ReadFile(filepath.Join(filepath.Dir(exePath), "update.md")); err == nil {
			return string(data)
		}
	}
	if updateLogMD == "" {
		return "# 暂无更新日志"
	}
	return updateLogMD
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
