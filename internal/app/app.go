package app

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"codingto/internal/browserworkflow"
	"codingto/internal/extensions"
	"codingto/internal/piagent"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// App is the Wails service boundary. It owns transport-specific concerns and
// delegates long-running agent work to AgentService.
type App struct {
	store      *ConfigStore
	agent      *AgentService
	extensions *extensions.Manager
	window     *application.WebviewWindow
	piInstall  sync.Mutex
	moduleMu   sync.Mutex
	modules    []RuntimeModule
}

type Bootstrap struct {
	Config          AppConfig          `json:"config"`
	ProviderPresets []piagent.Provider `json:"providerPresets"`
	OS              string             `json:"os"`
	PiInstalled     bool               `json:"piInstalled"`
	PiPath          string             `json:"piPath"`
	ConfigDir       string             `json:"configDir"`
	Version         string             `json:"version"`
}

// PiUpdateStatus reports the result of a Pi Agent update check.
type PiUpdateStatus struct {
	Installed string `json:"installed"`
	Latest    string `json:"latest"`
	Available bool   `json:"available"`
	Error     string `json:"error,omitempty"`
}

type ImageInput struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Data     string `json:"data"`
	MimeType string `json:"mimeType"`
}

type PromptRequest struct {
	AgentID       string            `json:"agentId"`
	Message       string            `json:"message"`
	Provider      string            `json:"provider"`
	Model         string            `json:"model"`
	ThinkingLevel string            `json:"thinkingLevel"`
	SkillPath     string            `json:"skillPath,omitempty"`
	Images        []ImageInput      `json:"images"`
	Attachments   []AttachmentInput `json:"attachments,omitempty"`
	WorkDir       string            `json:"workDir"`
	Mode          string            `json:"mode"`
	SessionID     int64             `json:"sessionId,omitempty"`
	SessionPath   string            `json:"sessionPath,omitempty"`
	Command       map[string]any    `json:"command,omitempty"`
}

// SaveBrowserProfileRequest keeps agent selection at the Wails boundary while
// SaveBrowserProfileRequest carries the profile fields. Profiles live in a
// single global directory shared by every agent, so no agent id is required.
// Password values are passed directly to the platform credential store and are
// never returned.
type SaveBrowserProfileRequest struct {
	browserworkflow.SaveRequest
}

func NewApp(modules ...RuntimeModule) (*App, error) {
	store, err := NewConfigStore()
	if err != nil {
		return nil, err
	}
	if err := store.Store().RecoverSessions(); err != nil {
		return nil, fmt.Errorf("recover sessions: %w", err)
	}
	if _, err := store.EnsureDefaultAgent(); err != nil {
		return nil, fmt.Errorf("initialize default agent: %w", err)
	}
	extensions := extensions.NewManager(store.Dir())
	result := &App{
		store:      store,
		extensions: extensions,
	}
	result.agent = NewAgentService(store, result.runtimeEnvironment)
	result.agent.runtimeRelease = result.releaseRuntimeSession
	for _, module := range modules {
		result.registerRuntimeModule(module)
	}
	return result, nil
}

func (a *App) SetWindow(window *application.WebviewWindow) { a.window = window }

func (a *App) ServiceStartup(ctx context.Context, _ application.ServiceOptions) error {
	a.moduleMu.Lock()
	modules := append([]RuntimeModule(nil), a.modules...)
	a.moduleMu.Unlock()
	started := make([]RuntimeModule, 0, len(modules))
	for _, module := range modules {
		if err := module.Start(ctx); err != nil {
			for index := len(started) - 1; index >= 0; index-- {
				_ = started[index].Shutdown()
			}
			return fmt.Errorf("start runtime module: %w", err)
		}
		started = append(started, module)
	}
	cfg := a.store.Get()
	// Keep each agent's materialized builtin tools in sync with the bundled
	// meta.json on every launch. This is what populates the installed version
	// reported by GetExtensions, so the UI can show the "current" version and an
	// update action whenever it lags behind the bundled "latest" version.
	rtkSource := ""
	for _, agent := range cfg.Agents {
		if err := piagent.MaterializeBuiltinTools(agent.DataDir, agent.Builtin); err != nil {
			fmt.Printf("materialize built-in tools for %s: %v\n", agent.Name, err)
		}
		// Keep each agent's RTK copy in sync with its recommended flag on launch.
		if agent.Recommended["rtk"] {
			if rtkSource == "" {
				rtkSource = extensions.EnsureRTKPiExtension()
			}
			if rtkSource != "" {
				_, _ = piagent.MaterializeRTKExtension(agent.DataDir, rtkSource)
			}
		} else {
			_ = piagent.RemoveRTKExtension(agent.DataDir)
		}
		if err := piagent.SyncFigmaMCPConfig(agent.DataDir, agent.Recommended["figma"]); err != nil {
			fmt.Printf("sync Pi Figma for %s: %v\n", agent.Name, err)
		}
	}
	return nil
}

func (a *App) ServiceShutdown() error {
	agentErr := a.agent.Close()
	a.moduleMu.Lock()
	modules := append([]RuntimeModule(nil), a.modules...)
	a.moduleMu.Unlock()
	var moduleErr error
	for index := len(modules) - 1; index >= 0; index-- {
		if err := modules[index].Shutdown(); err != nil && moduleErr == nil {
			moduleErr = err
		}
	}
	extensionErr := a.extensions.Close()
	if agentErr != nil {
		return agentErr
	}
	if moduleErr != nil {
		return moduleErr
	}
	return extensionErr
}

func (a *App) GetBootstrap() Bootstrap {
	path, installed := piagent.FindExecutable()
	// Keep bootstrap self-healing: ensure the default agent exists even if Pi
	// became available after the application started.
	_, _ = a.store.EnsureDefaultAgent()
	cfg := a.store.Get()
	return Bootstrap{
		Config:          cfg,
		ProviderPresets: piagent.ProviderPresets(),
		OS:              runtime.GOOS,
		PiInstalled:     installed,
		PiPath:          path,
		ConfigDir:       a.store.Dir(),
		Version:         appVersion,
	}
}

// GetPiVersion returns the installed Pi Agent version.
func (a *App) GetPiVersion() string {
	v, err := piagent.Version()
	if err != nil {
		return ""
	}
	return v
}

// GetUpdateLog reads the project changelog (update.md) from the working
// directory and returns its raw content for display in the settings UI.
func (a *App) GetUpdateLog() string {
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

func (a *App) GetExtensions() extensions.Snapshot {
	snap := a.extensions.Snapshot(a.store.Get().Extensions)
	cfg := a.store.Get()
	builtins := make(map[string][]extensions.BuiltinToolStatus, len(cfg.Agents))
	for _, agent := range cfg.Agents {
		statuses, err := piagent.BuiltinToolStatuses(agent.DataDir, agent.Builtin)
		if err != nil {
			continue
		}
		builtins[agent.ID] = statuses
	}
	snap.Builtins = builtins

	recommended := make(map[string][]extensions.Status, len(cfg.Agents))
	packages := make(map[string][]extensions.Status, len(cfg.Agents))
	a.store.EnsureAgentDataDirs(&cfg)
	for _, agent := range cfg.Agents {
		packageStatuses, err := piagent.InstalledPackageStatuses(agent.DataDir)
		if err != nil {
			packageStatuses = []extensions.Status{}
		}
		packages[agent.ID] = packageStatuses

		enabled := agent.Recommended["rtk"] && piagent.RTKMaterialized(agent.DataDir)
		installed := false
		browserStatusVersion := ""
		if profile, found := cfg.Agent(agent.ID); found {
			installed = piagent.BrowserNativeInstalled(profile.DataDir)
			if installed {
				browserStatusVersion = piagent.BrowserNativeVersion(profile.DataDir)
			}
		}
		browserStatus := extensions.BrowserNativeStatusForAgent(installed)
		browserStatus.Version = browserStatusVersion
		if installed {
			browserStatus.SourcePath = piagent.BrowserNativeDir(agent.DataDir)
		}
		figmaInstalled := agent.Recommended["figma"] && piagent.PiMCPAdapterInstalled(agent.DataDir) && piagent.FigmaMCPConfigured(agent.DataDir)
		figmaStatus := extensions.PiFigmaStatusForAgent(figmaInstalled)
		figmaStatus.Version = piagent.PiMCPAdapterVersion(agent.DataDir)
		if piagent.PiMCPAdapterInstalled(agent.DataDir) {
			figmaStatus.SourcePath = piagent.PiMCPAdapterDir(agent.DataDir)
		}
		recommended[agent.ID] = []extensions.Status{
			extensions.RTKStatusForAgent(enabled),
			browserStatus,
			figmaStatus,
		}
	}
	snap.Recommended = recommended
	snap.Packages = packages
	return snap
}

// GetAgentExtensions recomputes only one agent's builtin/recommended/package
// statuses. Used after editing a single agent so unrelated agents are not
// re-scanned from disk.
func (a *App) GetAgentExtensions(agentID string) extensions.AgentExtensionStatuses {
	empty := extensions.AgentExtensionStatuses{}
	cfg := a.store.Get()
	agent, found := cfg.Agent(agentID)
	if !found {
		return empty
	}
	a.store.EnsureAgentDataDirs(&cfg)

	builtins, err := piagent.BuiltinToolStatuses(agent.DataDir, agent.Builtin)
	if err != nil {
		builtins = []extensions.BuiltinToolStatus{}
	}

	packageStatuses, err := piagent.InstalledPackageStatuses(agent.DataDir)
	if err != nil {
		packageStatuses = []extensions.Status{}
	}

	enabled := agent.Recommended["rtk"] && piagent.RTKMaterialized(agent.DataDir)
	installed := false
	browserStatusVersion := ""
	if profile, found := cfg.Agent(agent.ID); found {
		installed = piagent.BrowserNativeInstalled(profile.DataDir)
		if installed {
			browserStatusVersion = piagent.BrowserNativeVersion(profile.DataDir)
		}
	}
	browserStatus := extensions.BrowserNativeStatusForAgent(installed)
	browserStatus.Version = browserStatusVersion
	if installed {
		browserStatus.SourcePath = piagent.BrowserNativeDir(agent.DataDir)
	}
	figmaInstalled := agent.Recommended["figma"] && piagent.PiMCPAdapterInstalled(agent.DataDir) && piagent.FigmaMCPConfigured(agent.DataDir)
	figmaStatus := extensions.PiFigmaStatusForAgent(figmaInstalled)
	figmaStatus.Version = piagent.PiMCPAdapterVersion(agent.DataDir)
	if piagent.PiMCPAdapterInstalled(agent.DataDir) {
		figmaStatus.SourcePath = piagent.PiMCPAdapterDir(agent.DataDir)
	}
	recommended := []extensions.Status{
		extensions.RTKStatusForAgent(enabled),
		browserStatus,
		figmaStatus,
	}
	return extensions.AgentExtensionStatuses{
		Builtins:    builtins,
		Recommended: recommended,
		Packages:    packageStatuses,
	}
}

// ListBrowserProfiles returns the global, non-secret profile metadata shared by
// every agent. An optional target URL limits the result to profiles whose
// allowed origins include that origin.
func (a *App) ListBrowserProfiles(targetURL string) ([]browserworkflow.Profile, error) {
	base, err := browserworkflow.ProfileBaseDir()
	if err != nil {
		return nil, err
	}
	return browserworkflow.List(base, targetURL)
}

// SaveBrowserProfile stores metadata in the global browser profile directory
// and, when requested, protects the credential with the operating system
// credential store. The response contains metadata only.
func (a *App) SaveBrowserProfile(req SaveBrowserProfileRequest) (browserworkflow.Profile, error) {
	base, err := browserworkflow.ProfileBaseDir()
	if err != nil {
		return browserworkflow.Profile{}, err
	}
	return browserworkflow.Save(base, req.SaveRequest)
}

// DeleteBrowserProfile removes one global persistent browser session, including
// its protected credential file.
func (a *App) DeleteBrowserProfile(profileID string) error {
	if profileID == "" {
		return fmt.Errorf("profile id is required")
	}
	base, err := browserworkflow.ProfileBaseDir()
	if err != nil {
		return err
	}
	return browserworkflow.Delete(base, profileID)
}

// RenameBrowserProfile updates the display name of one persistent browser
// session in the global profile store. The immutable key and stored credentials
// are untouched.
func (a *App) RenameBrowserProfile(profileID, newName string) (browserworkflow.Profile, error) {
	if profileID == "" {
		return browserworkflow.Profile{}, fmt.Errorf("profile id is required")
	}
	base, err := browserworkflow.ProfileBaseDir()
	if err != nil {
		return browserworkflow.Profile{}, err
	}
	return browserworkflow.Rename(base, profileID, newName)
}

type InstallAgentExtensionRequest struct {
	AgentID string `json:"agentId"`
	Command string `json:"command"`
}

type AgentExtensionKeyRequest struct {
	AgentID string `json:"agentId"`
	Key     string `json:"key"`
}

// AgentExtensionResult reports the outcome of a command scoped to an agent.
type AgentExtensionResult struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Command string `json:"command"`
	Output  string `json:"output"`
}

// streamAgentCommand runs a command scoped to an agent while emitting live log
// lines to the frontend through the "install:log" event. This means long-running
// installs (for example downloading Playwright browser binaries) show progress
// instead of appearing frozen. The caller is responsible for emitting the
// "install:start"/"install:done" wrapping events.
func (a *App) streamAgentCommand(agentID, command string) AgentExtensionResult {
	cfg := a.store.Get()
	profile, found := cfg.Agent(agentID)
	if !found {
		return AgentExtensionResult{Success: false, Command: command, Output: "", Message: fmt.Sprintf("agent not found: %s", agentID)}
	}
	a.store.EnsureAgentDataDirs(&cfg)
	profile, _ = cfg.Agent(agentID)
	emit := func(line string) {
		application.Get().Event.Emit("install:log", map[string]any{
			"agentId": agentID,
			"line":    line,
		})
	}
	out, err := piagent.RunAgentCommandWithProgress(profile.DataDir, command, emit)
	success := err == nil
	res := AgentExtensionResult{Success: success, Command: command, Output: out, Message: "命令已执行"}
	if err != nil {
		res.Message = fmt.Sprintf("安装执行失败: %v", err)
	}
	return res
}

// InstallAgentExtension runs a pi-install style command scoped to a single
// agent. PI_CODING_AGENT_DIR is pointed at the agent's data directory so the
// extension is installed into that agent only.
func (a *App) InstallAgentExtension(req InstallAgentExtensionRequest) (AgentExtensionResult, error) {
	cfg := a.store.Get()
	if _, found := cfg.Agent(req.AgentID); !found {
		return AgentExtensionResult{}, fmt.Errorf("agent not found: %s", req.AgentID)
	}
	a.store.EnsureAgentDataDirs(&cfg)
	application.Get().Event.Emit("install:start", map[string]any{
		"agentId": req.AgentID,
		"title":   "扩展安装",
	})
	res := a.streamAgentCommand(req.AgentID, req.Command)
	application.Get().Event.Emit("install:done", map[string]any{
		"agentId": req.AgentID,
		"success": res.Success,
	})
	return res, nil
}

// UninstallAgentExtension removes a Pi extension previously installed into the
// agent's isolated extensions directory.
func (a *App) UninstallAgentExtension(req AgentExtensionKeyRequest) (AgentExtensionResult, error) {
	cfg := a.store.Get()
	profile, found := cfg.Agent(req.AgentID)
	if !found {
		return AgentExtensionResult{}, fmt.Errorf("agent not found: %s", req.AgentID)
	}
	a.store.EnsureAgentDataDirs(&cfg)
	profile, _ = cfg.Agent(req.AgentID)
	statuses, err := piagent.InstalledPackageStatuses(profile.DataDir)
	if err != nil {
		return AgentExtensionResult{}, err
	}
	source := ""
	sourcePath := ""
	if req.Key == "browser-native" {
		for _, status := range statuses {
			if status.Name == "pi-agent-browser-native" {
				source = status.Key
				sourcePath = status.SourcePath
				break
			}
		}
		if source == "" {
			source = "npm:pi-agent-browser-native"
			sourcePath = piagent.BrowserNativeDir(profile.DataDir)
		}
	} else {
		for _, status := range statuses {
			if status.Key == req.Key {
				source = status.Key
				sourcePath = status.SourcePath
				break
			}
		}
		if source == "" {
			return AgentExtensionResult{}, fmt.Errorf("agent package is not configured: %s", req.Key)
		}
	}
	cmd := "pi uninstall " + source
	out, err := piagent.UninstallAgentPackage(profile.DataDir, source)
	if err != nil {
		return AgentExtensionResult{Success: false, Command: cmd, Output: out, Message: fmt.Sprintf("卸载失败: %v", err)}, nil
	}
	// Pi normally removes the installed files together with its settings entry.
	// Clean up a leftover exact package path if an older Pi version only removed
	// the settings entry.
	if sourcePath != "" {
		if rmErr := os.RemoveAll(sourcePath); rmErr != nil {
			out += "\n警告: 无法删除扩展目录: " + rmErr.Error()
		}
	}
	return AgentExtensionResult{Success: true, Command: cmd, Output: out, Message: "已卸载扩展"}, nil
}

func (a *App) ManageExtension(req extensions.ActionRequest) (extensions.ActionResult, error) {
	cfg := a.store.Get()
	if req.Action != "install" {
		return a.extensions.Manage(req, cfg.Extensions)
	}

	installID := "global:" + req.Key
	title := map[string]string{
		"rtk":           "RTK 全局安装",
		"agent-browser": "Agent Browser 全局安装",
		"playwright":    "Playwright 全局安装",
		"figma":         "Figma 全局安装",
	}[req.Key]
	if title == "" {
		title = "全局扩展安装"
	}
	app := application.Get()
	app.Event.Emit("install:start", map[string]any{
		"installId": installID,
		"scope":     "global",
		"title":     title,
	})
	result, err := a.extensions.ManageWithProgress(req, cfg.Extensions, func(line string) {
		app.Event.Emit("install:log", map[string]any{
			"installId": installID,
			"line":      line,
		})
	})
	if err != nil && strings.TrimSpace(result.Output) == "" {
		app.Event.Emit("install:log", map[string]any{
			"installId": installID,
			"line":      err.Error(),
		})
	}
	app.Event.Emit("install:done", map[string]any{
		"installId": installID,
		"success":   err == nil,
	})
	// 安装/启用等操作会改变全局插件(Plugins 页)与 Agent 扩展状态，
	// 通知前端重新拉取快照，避免列表停留在安装前的旧数据。
	app.Event.Emit("extensions:changed", map[string]any{
		"key":    req.Key,
		"action": req.Action,
	})
	return result, err
}

// SaveFigmaConfig persists the shared authorization catalog. Individual Pi
// agents consume the selected token through their process environment.
func (a *App) SaveFigmaConfig(cfg extensions.FigmaConfig) (extensions.Snapshot, error) {
	cfg.Normalize()
	if authorization, ok := cfg.ActiveAuthorization(); ok {
		if err := extensions.ValidateFigmaAuthorization(context.Background(), authorization); err != nil {
			return a.extensions.Snapshot(a.store.Get().Extensions), fmt.Errorf("validate %s: %w", authorization.Name, err)
		}
	}
	appConfig := a.store.Get()
	appConfig.Extensions.Figma = cfg
	appConfig.Normalize()
	if err := a.store.Save(appConfig); err != nil {
		return extensions.Snapshot{}, err
	}
	return a.extensions.Snapshot(appConfig.Extensions), nil
}

func (a *App) SaveConfig(cfg AppConfig) (AppConfig, error) {
	cfg.Normalize()
	a.store.EnsureAgentDataDirs(&cfg)
	seenAgents := make(map[string]bool, len(cfg.Agents))
	for _, agent := range cfg.Agents {
		if seenAgents[agent.ID] {
			return AppConfig{}, fmt.Errorf("duplicate agent id: %s", agent.ID)
		}
		seenAgents[agent.ID] = true
	}
	if err := piagent.ValidateProviders(cfg.Providers, cfg.DefaultProvider, cfg.DefaultModel); err != nil {
		return AppConfig{}, err
	}
	// Validate and materialize the runtime configuration before committing the
	// application configuration, so a failed Pi write does not persist a config
	// that cannot be used by the next prompt.
	for _, agent := range cfg.Agents {
		if err := piagent.WriteModels(agent.DataDir, cfg.Providers); err != nil {
			return AppConfig{}, fmt.Errorf("write models for %s: %w", agent.Name, err)
		}
		// Materialize the builtin tools this agent enables into its isolated data
		// directory, and physically remove the directories of disabled tools (Pi
		// auto-discovers extensions/, so a disabled tool must be removed to turn
		// it off). Doing this here means toggling a tool in the agent settings
		// page writes the files immediately instead of only at session start.
		if err := piagent.MaterializeBuiltinTools(agent.DataDir, agent.Builtin); err != nil {
			return AppConfig{}, fmt.Errorf("materialize built-in tools for %s: %w", agent.Name, err)
		}
		if err := piagent.RemoveBuiltinTools(agent.DataDir, agent.Builtin); err != nil {
			return AppConfig{}, fmt.Errorf("remove disabled built-in tools for %s: %w", agent.Name, err)
		}
	}
	if err := a.store.Save(cfg); err != nil {
		return AppConfig{}, err
	}
	return a.store.Get(), nil
}

// DeleteAgent explicitly deletes one non-default agent and returns the updated
// configuration. Regular SaveConfig calls never infer deletions from omissions.
func (a *App) DeleteAgent(id string) (AppConfig, error) {
	return a.store.DeleteAgent(id)
}

func (a *App) ChooseImages() ([]ImageInput, error) {
	paths, err := application.Get().Dialog.OpenFile().
		SetTitle("Choose images").
		CanChooseFiles(true).
		CanChooseDirectories(false).
		AddFilter("Images", "*.png;*.jpg;*.jpeg;*.webp;*.gif").
		AttachToWindow(a.window).
		PromptForMultipleSelection()
	if err != nil {
		return nil, err
	}
	if len(paths) > 10 {
		return nil, errors.New("select at most 10 images")
	}
	images := make([]ImageInput, 0, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if len(data) > 20*1024*1024 {
			return nil, fmt.Errorf("image is larger than 20 MB: %s", filepath.Base(path))
		}
		mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
		images = append(images, ImageInput{
			Name: filepath.Base(path), Type: "image",
			Data: base64.StdEncoding.EncodeToString(data), MimeType: mimeType,
		})
	}
	return images, nil
}

func (a *App) ChooseWorkspace() (string, error) {
	return application.Get().Dialog.OpenFile().
		SetTitle("Choose workspace").
		CanChooseFiles(false).
		CanChooseDirectories(true).
		CanCreateDirectories(true).
		AttachToWindow(a.window).
		PromptForSingleSelection()
}

func (a *App) ChooseSessionDir() (string, error) {
	return application.Get().Dialog.OpenFile().
		SetTitle("Choose session storage directory").
		CanChooseFiles(false).
		CanChooseDirectories(true).
		CanCreateDirectories(true).
		AttachToWindow(a.window).
		PromptForSingleSelection()
}

func (a *App) StartPrompt(req PromptRequest) error { return a.agent.StartPrompt(req) }
func (a *App) AbortPrompt() error                  { return a.agent.AbortPrompt() }
func (a *App) RestartAgent() error                 { return a.agent.Restart() }

// TestModel verifies a single model can be invoked through the Pi agent runtime.
func (a *App) TestModel(req TestModelRequest) (TestModelResult, error) {
	return a.agent.TestModel(req)
}

// agentFileWhitelist lists the only files the UI may read or write inside an
// agent's data directory. This keeps the surface narrow (no arbitrary file
// access) while supporting the per-agent prompt file (AGENTS.md) that Pi loads
// by default.
var agentFileWhitelist = map[string]bool{
	"AGENTS.md": true,
}

// agentFilePath resolves the absolute path for filename inside the data
// directory of the agent identified by agentId. It returns an error when the
// agent is unknown or the filename is not whitelisted.
func (a *App) agentFilePath(agentId, filename string) (string, error) {
	if !agentFileWhitelist[filename] {
		return "", fmt.Errorf("file not editable: %s", filename)
	}
	cfg := a.store.Get()
	profile, found := cfg.Agent(agentId)
	if !found {
		return "", fmt.Errorf("agent not found: %s", agentId)
	}
	// Make sure an empty dataDir resolves to the managed default and the
	// directory exists before we touch files inside it.
	a.store.EnsureAgentDataDirs(&cfg)
	profile, found = cfg.Agent(agentId)
	if !found || profile.DataDir == "" {
		return "", fmt.Errorf("agent data directory unavailable: %s", agentId)
	}
	return filepath.Join(profile.DataDir, filename), nil
}

// ReadAgentFile returns the contents of a whitelisted file inside an agent's
// data directory. A missing file yields an empty string rather than an error,
// so the UI can edit and create the file from scratch.
func (a *App) ReadAgentFile(agentId, filename string) (string, error) {
	path, err := a.agentFilePath(agentId, filename)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read %s: %w", filename, err)
	}
	return string(data), nil
}

// WriteAgentFile writes content to a whitelisted file inside an agent's data
// directory, creating the directory if needed.
func (a *App) WriteAgentFile(agentId, filename, content string) error {
	path, err := a.agentFilePath(agentId, filename)
	if err != nil {
		return err
	}
	if err := ensurePrivateDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("create agent directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", filename, err)
	}
	return os.Chmod(path, 0o600)
}

func (a *App) WindowMinimise() {
	if a.window != nil {
		a.window.Minimise()
	}
}

func (a *App) WindowToggleMaximise() {
	if a.window != nil {
		a.window.ToggleMaximise()
	}
}

func (a *App) WindowClose() {
	if a.window != nil {
		a.window.Close()
	}
}
