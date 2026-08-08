package app

import (
	"context"
	"fmt"
	"runtime"
	"sync"

	"codingto/internal/applog"
	"codingto/internal/browserworkflow"
	"codingto/internal/extensions"
	"codingto/internal/piagent"
	"codingto/internal/steward"
	"codingto/internal/steward/connectors"
	"codingto/internal/subagentbridge"
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
	// steward is the always-on bot-relay service (nil when construction fails
	// so the desktop app keeps working without it).
	steward        *steward.Service
	stewardSecrets *steward.SecretStore
	// windowCloseHook wires the frameless window's close button to the
	// application-level shutdown flow owned by cmd/codingto (see WindowClose).
	windowCloseHook func()
	// shutdownOnce makes ServiceShutdown idempotent: it can be reached from
	// wails' own shutdownServices (when the message loop exits) and from the
	// background shutdown goroutine started by cmd/codingto, but the real
	// cleanup must only run once.
	shutdownOnce sync.Once
}

// SetWindowCloseHook registers the shutdown entry point used by WindowClose.
// cmd/codingto injects a handler that shows the "正在关闭中" overlay and runs
// ServiceShutdown off the UI main thread.
func (a *App) SetWindowCloseHook(fn func()) {
	a.windowCloseHook = fn
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
	extensions := extensions.NewManager()
	result := &App{
		store:      store,
		extensions: extensions,
	}
	result.agent = NewAgentService(store, result.runtimeEnvironment)
	result.agent.runtimeRelease = result.releaseRuntimeSession
	for _, module := range modules {
		result.registerRuntimeModule(module)
	}
	result.initSteward()
	if err := result.sanitizeStewardToolset(); err != nil {
		applog.Errorf("sanitize steward toolset: %v", err)
	}
	return result, nil
}

// initSteward builds the steward service, registers it as a runtime module and
// wires its hooks into AgentService. Failures are logged but never abort app
// startup.
func (a *App) initSteward() {
	secrets, err := steward.NewSecretStore(a.store.Dir())
	if err != nil {
		applog.Errorf("steward: init secret store: %v", err)
		return
	}
	control := &stewardControl{app: a}
	service := steward.NewService(a.store.Store(), secrets, control, connectors.Factories(), emitStewardEvent)
	a.steward = service
	a.stewardSecrets = secrets
	a.registerRuntimeModule(service)
	service.ResolveStewardAgent()
	a.agent.SetStewardHooks(&stewardHooks{
		isBotManaged:            service.IsBotManaged,
		relayPermission:         service.RelayPermission,
		relaySubagentPermission: service.RelaySubagentPermission,
		onAgentEvent:            service.OnAgentEvent,
		onTaskSettled:           a.stewardsOnTaskSettled,
	})
}

// stewardsOnTaskSettled reports a settled bot-managed task. The resident
// steward conversation is always resident and is never idle-reclaimed, so no
// reclaim timer is armed here.
func (a *App) stewardsOnTaskSettled(sessionID int64, event map[string]any) {
	if a.steward == nil {
		return
	}
	a.steward.OnTaskSettled(sessionID, event)
}

// emitStewardEvent forwards steward events to the frontend through Wails.
func emitStewardEvent(name string, value any) {
	if app := application.Get(); app != nil && app.Event != nil {
		app.Event.Emit(name, value)
	}
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
			applog.Errorf("materialize built-in tools for %s: %v", agent.Name, err)
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
			applog.Errorf("sync Pi Figma for %s: %v", agent.Name, err)
		}
	}
	a.reconcileOrphanedSubagents()
	applog.Infof("CodingTo %s started", appVersion)
	return nil
}

// reconcileOrphanedSubagents marks subagent runs left as "running" on disk by
// a previous process as aborted. Subagent bridges and their child Pi processes
// cannot survive a CodingTo restart, so any run still recorded as running at
// startup is orphaned: its follow-up result can never be delivered, and keeping
// it as running would leave the session stuck in the "waiting for subagents"
// state forever (see runningSubagentCount).
func (a *App) reconcileOrphanedSubagents() {
	sessions, err := a.store.Store().ListSessions()
	if err != nil {
		applog.Errorf("reconcile orphaned subagents: list sessions: %v", err)
		return
	}
	for _, session := range sessions {
		if session.SessionDir == "" {
			continue
		}
		count, err := subagentbridge.ReconcileOrphanedRuns(session.SessionDir)
		if err != nil {
			applog.Errorf("reconcile orphaned subagents for session %d (%s): %v", session.ID, session.SessionDir, err)
			continue
		}
		if count > 0 {
			applog.Infof("reconcile: session %d: marked %d orphaned subagent run(s) aborted", session.ID, count)
		}
	}
}

func (a *App) ServiceShutdown() error {
	// Guard against concurrent/repeated invocation: wails' deferred
	// shutdownServices and the background shutdown goroutine started by
	// cmd/codingto can both reach this. Only the first call performs the real
	// cleanup; later calls return its error, so agent processes are killed
	// exactly once (no duplicate taskkill, no double log close).
	var err error
	a.shutdownOnce.Do(func() { err = a.shutdown() })
	return err
}

func (a *App) shutdown() error {
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
	applog.Infof("CodingTo %s shutting down", appVersion)
	// Flush and close the daily log file last so shutdown diagnostics are
	// persisted before the process exits.
	applog.Close()
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
	// The steward toolset is reserved for the resident steward agent only: it is
	// force-enabled (and persisted) on that agent, and stripped from every other
	// agent so a plain config save cannot re-enable or resurrect it elsewhere.
	stewardAgentID := ""
	if a.steward != nil {
		stewardAgentID = a.steward.ResolvedAgentID()
	}
	for i := range cfg.Agents {
		agent := &cfg.Agents[i]
		if err := piagent.WriteModels(agent.DataDir, cfg.Providers); err != nil {
			return AppConfig{}, fmt.Errorf("write models for %s: %w", agent.Name, err)
		}
		// Materialize the builtin tools this agent enables into its isolated data
		// directory, and physically remove the directories of disabled tools (Pi
		// auto-discovers extensions/, so a disabled tool must be removed to turn
		// it off). Doing this here means toggling a tool in the agent settings
		// page writes the files immediately instead of only at session start.
		// The steward toolset persists as enabled only on the steward agent; on
		// every other agent it is deleted from Builtin (and thus from disk).
		if agent.ID == stewardAgentID {
			agent.Builtin[steward.ToolKey] = true
		} else {
			delete(agent.Builtin, steward.ToolKey)
		}
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

// sanitizeStewardToolset enforces that the steward toolset is enabled on the
// resident steward agent only. It strips steward from every other agent's
// persisted Builtin set and physically removes extensions/steward so Pi
// auto-discovery never loads the codingto_steward_* tools there; the steward
// agent itself is force-enabled and materialized. Running once at startup
// migrates existing configurations created while steward was enabled by
// default on every agent.
func (a *App) sanitizeStewardToolset() error {
	cfg := a.store.Get()
	a.store.EnsureAgentDataDirs(&cfg)
	stewardAgentID := ""
	if a.steward != nil {
		stewardAgentID = a.steward.ResolvedAgentID()
	}
	changed := false
	for i := range cfg.Agents {
		agent := &cfg.Agents[i]
		if agent.ID == stewardAgentID {
			if !agent.Builtin[steward.ToolKey] {
				agent.Builtin[steward.ToolKey] = true
				changed = true
			}
		} else if _, ok := agent.Builtin[steward.ToolKey]; ok {
			delete(agent.Builtin, steward.ToolKey)
			changed = true
		}
		if err := piagent.MaterializeBuiltinTools(agent.DataDir, agent.Builtin); err != nil {
			return fmt.Errorf("materialize built-in tools for %s: %w", agent.Name, err)
		}
		if err := piagent.RemoveBuiltinTools(agent.DataDir, agent.Builtin); err != nil {
			return fmt.Errorf("remove disabled built-in tools for %s: %w", agent.Name, err)
		}
	}
	if !changed {
		return nil
	}
	if err := a.store.Save(cfg); err != nil {
		return fmt.Errorf("persist steward toolset sanitization: %w", err)
	}
	return nil
}

// DeleteAgent explicitly deletes one non-default agent and returns the updated
// configuration. Regular SaveConfig calls never infer deletions from omissions.
func (a *App) DeleteAgent(id string) (AppConfig, error) {
	return a.store.DeleteAgent(id)
}

func (a *App) StartPrompt(req PromptRequest) error { return a.agent.StartPrompt(req) }
func (a *App) AbortPrompt() error                  { return a.agent.AbortPrompt() }
func (a *App) RestartAgent() error                 { return a.agent.Restart() }

// TestModel verifies a single model can be invoked through the Pi agent runtime.
func (a *App) TestModel(req TestModelRequest) (TestModelResult, error) {
	return a.agent.TestModel(req)
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
	// 前端 frameless 窗口的关闭按钮走这里。旧实现直接 application.Quit()，
	// 但 wails v3 在 Windows 上会"先同步执行 OnShutdown/ServiceShutdown，
	// 再销毁窗口"：清理逻辑（steward 渠道关闭等最多可阻塞 3 秒）跑在主线程
	// 上，导致点关闭后界面卡住 3 秒没反应。cmd/codingto 现在注入
	// windowCloseHook，走"立即显示正在关闭蒙层 + 后台异步清理"的退出流程，
	// 界面即时反馈、清理不阻塞 UI。未注入时（理论上不会发生）回退到 Quit。
	if a.windowCloseHook != nil {
		a.windowCloseHook()
		return
	}
	if app := application.Get(); app != nil {
		app.Quit()
	}
}
