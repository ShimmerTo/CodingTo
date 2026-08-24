package app

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"time"

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
	// cleanupMu guards lastCleanup shared with the frontend's one-shot fetch.
	cleanupMu   sync.Mutex
	lastCleanup *SessionCleanupResult

	// shutdownOnce makes ServiceShutdown idempotent: it can be reached from
	// wails' own shutdownServices (when the message loop exits) and from the
	// background shutdown goroutine started by cmd/codingto, but the real
	// cleanup must only run once.
	shutdownOnce sync.Once

	// chatgptMu guards the Agent-scoped ChatGPT/Codex OAuth flow.
	chatgptMu   sync.Mutex
	chatgptFlow *chatgptFlow
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
	// DisableDcg is a conversation-scoped switch: it only stops DCG
	// interception for this conversation by writing the session marker, and
	// never changes the agent's recommended.dcg extension configuration.
	DisableDcg bool `json:"disableDcg,omitempty"`
	// StewardDispatchToken correlates one durable resident-queue event with
	// its lifecycle events. It is internal metadata and never crosses Wails.
	StewardDispatchToken string `json:"-"`
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
	if err := syncAPIDetailMarker(a.store.Dir(), cfg.RecordAPIDetails); err != nil {
		applog.Errorf("sync API detail recording marker at startup: %v", err)
	}
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
		if agent.Recommended["dcg"] {
			if _, err := piagent.MaterializeDCGExtension(agent.DataDir); err != nil {
				applog.Errorf("materialize DCG extension for %s: %v", agent.Name, err)
			}
		} else {
			_ = piagent.RemoveDCGExtension(agent.DataDir)
		}
		if err := piagent.SyncFigmaMCPConfig(agent.DataDir, agent.Recommended["figma"]); err != nil {
			applog.Errorf("sync Pi Figma for %s: %v", agent.Name, err)
		}
	}
	// CodingTo 的 Pi RPC 启动器让所有 Agent 直接使用默认目录中的共享
	// auth.json。清理由旧版本复制到 Agent 目录的 Codex 条目，避免遗留的
	// refresh token 被外部进程误用；损坏文件只记录日志，绝不覆盖。
	go a.removeLegacyChatGPTCredentialsFromAgents()
	a.reconcileOrphanedSubagents()
	// 会话数据自动清理：启动后异步执行，不阻塞窗口渲染；结果供前端拉取展示。
	a.startSessionCleanup()
	go func() {
		a.cleanupModelUsageRetention()
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			a.cleanupModelUsageRetention()
		}
	}()
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
	a.cancelChatGPTFlow()
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
	// 启动时把已保存的 DCG 处置策略与工作目录放行规则同步到运行时产物
	// （策略文件、dcg 用户配置），即使上次退出前同步失败也能自愈。
	if err := a.writeDCGPolicyFile(cfg); err != nil {
		applog.Warnf("write dcg policy file: %v", err)
	}
	if cfg.DCGSettings.WorkspaceAllow {
		if err := syncDCGWorkspaceAllow(cfg); err != nil {
			applog.Warnf("sync dcg workspace allow: %v", err)
		}
	}
	// 凭据隔离：密码与私钥仅存 App 存储与 0600 快照，下发前统一脱敏；
	// 前端保存时空凭据由 SaveConfig 沿用已存值。
	cfg = maskConfigCredentials(cfg)
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
	previous := a.store.Get()
	mergeSSHCredentials(cfg.SSHConfigs, previous.SSHConfigs)
	if err := validateSSHSecurityConfig(cfg.SSHConfigs); err != nil {
		return AppConfig{}, err
	}
	cfg.Normalize()
	// DB 连接密码不回显（GetBootstrap 已脱敏）：提交的空密码沿用已存密码，
	// 避免普通配置保存把既有凭据清空。
	previousPasswords := make(map[string]string, len(previous.Extensions.DB.Connections))
	for _, conn := range previous.Extensions.DB.Connections {
		previousPasswords[conn.ID] = conn.Password
	}
	for i := range cfg.Extensions.DB.Connections {
		conn := &cfg.Extensions.DB.Connections[i]
		if conn.Password == "" {
			conn.Password = previousPasswords[conn.ID]
		}
	}
	enableSubagentExtensionForNewAssignments(&cfg, previous)
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
	// 工作空间 DB 勾选可能随本次保存变更：重写活动会话快照，
	// bridge 在下次请求时按 mtime 懒重载。
	if a.agent != nil {
		a.agent.refreshActiveDBSnapshot()
		a.agent.refreshActiveSSHSnapshot()
	}
	// DCG 处置策略或工作空间列表变化时同步策略文件与 dcg 放行规则。
	a.ensureDCGRuntime(cfg, previous)
	return maskConfigCredentials(a.store.Get()), nil
}

// enableSubagentExtensionForNewAssignments enables the parent-side bridge when
// a user grants an agent access to a new subagent. It intentionally reacts only
// to a new assignment: users may still remove the extension afterwards.
func enableSubagentExtensionForNewAssignments(cfg *AppConfig, previous AppConfig) {
	previousAssignments := make(map[string]map[string]bool, len(previous.Agents))
	for _, agent := range previous.Agents {
		assignments := make(map[string]bool, len(agent.SubAgents))
		for _, id := range agent.SubAgents {
			assignments[id] = true
		}
		previousAssignments[agent.ID] = assignments
	}
	for i := range cfg.Agents {
		agent := &cfg.Agents[i]
		for _, id := range agent.SubAgents {
			if previousAssignments[agent.ID][id] {
				continue
			}
			agent.Builtin["subagent"] = true
			break
		}
	}
}

// enableBuiltinForAgents persists a builtin extension after a feature that
// depends on it has been installed for the selected agents.
func (a *App) enableBuiltinForAgents(agentIDs []string, key string) error {
	wanted := make(map[string]bool, len(agentIDs))
	for _, id := range agentIDs {
		wanted[id] = true
	}
	cfg := a.store.Get()
	found := make(map[string]bool, len(wanted))
	changed := false
	for i := range cfg.Agents {
		agent := &cfg.Agents[i]
		if !wanted[agent.ID] {
			continue
		}
		found[agent.ID] = true
		if agent.Builtin == nil {
			agent.Builtin = map[string]bool{}
		}
		if !agent.Builtin[key] {
			agent.Builtin[key] = true
			changed = true
		}
	}
	for _, id := range agentIDs {
		if !found[id] {
			return fmt.Errorf("agent not found: %s", id)
		}
	}
	if !changed {
		return nil
	}
	_, err := a.SaveConfig(cfg)
	return err
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

// AbortSession is the session-scoped stop boundary used by the frontend. It
// returns the authoritative state after dispatching the abort so callers can
// repair a missed lifecycle event immediately.
func (a *App) AbortSession(id int64) (SessionRuntimeState, error) {
	item, ok, err := a.store.Store().SessionByID(id)
	if err != nil {
		return SessionRuntimeState{}, err
	} else if !ok {
		return SessionRuntimeState{}, fmt.Errorf("conversation not found: %d", id)
	}
	// A live runtime fans out to all children while holding its lifecycle lock.
	// If that runtime has already disappeared, still scan the durable session
	// directory so stale frontend state cannot strand detached child processes.
	if !a.agent.sessionRuntimeState(id).Known {
		abortRunningSubagents(item.SessionDir, id)
	}
	if err := a.agent.AbortPrompt(id); err != nil {
		return a.sessionRuntimeState(id), err
	}
	return a.sessionRuntimeState(id), nil
}

func (a *App) GetSessionRuntimeState(id int64) SessionRuntimeState {
	return a.sessionRuntimeState(id)
}

func (a *App) sessionRuntimeState(id int64) SessionRuntimeState {
	state := a.agent.sessionRuntimeState(id)
	if !state.Known {
		if item, ok, err := a.store.Store().SessionByID(id); err == nil && ok {
			state.ExecDurationMs = item.ExecDurationMs
		}
	}
	return state
}

func (a *App) RestartAgent() error { return a.agent.Restart() }

// SetSessionDcgDisabled toggles DCG interception for a single conversation
// (写入会话目录标记，实时生效），不会修改智能体的 recommended.dcg 配置。
func (a *App) SetSessionDcgDisabled(sessionID int64, disabled bool) error {
	return a.agent.SetSessionDcgDisabled(sessionID, disabled)
}

// GetSessionDcgDisabled reports the conversation-scoped DCG state used to
// restore the bottom security menu when a historical conversation is opened.
func (a *App) GetSessionDcgDisabled(sessionID int64) (bool, error) {
	return a.agent.GetSessionDcgDisabled(sessionID)
}

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

// WindowClose hides the main window while background services keep running.
func (a *App) WindowClose() {
	// The frameless title-bar close button keeps background services running.
	// A full shutdown is available from the system tray menu.
	if a.window != nil {
		a.window.Hide()
	}
}
