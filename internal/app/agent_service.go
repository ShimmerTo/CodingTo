package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"codingto/internal/applog"
	"codingto/internal/piagent"

	"github.com/wailsapp/wails/v3/pkg/application"
)

const maxConcurrentSessionRuntimes = 4
const defaultModelFirstResponseTimeout = 60 * time.Second

// defaultToolExecutionTimeout bounds how long a single tool execution (for
// now the bash tool, whose commands can otherwise hang forever on an
// unbounded scan or a wedged child process) may run before it is aborted.
const defaultToolExecutionTimeout = 10 * time.Minute

// toolExecutionTimeoutFromConfig converts the global tool-execution timeout
// setting (minutes) into a duration. Zero or out-of-range values fall back to
// the 10 minute default so the watchdog can never be disabled accidentally;
// the ConfigStore layer already clamps to 1..60 minutes.
func toolExecutionTimeoutFromConfig(cfg AppConfig) time.Duration {
	minutes := cfg.ToolExecutionTimeoutMinutes
	if minutes <= 0 {
		minutes = 10
	}
	if minutes > 60 {
		minutes = 60
	}
	return time.Duration(minutes) * time.Minute
}

// AgentService is either the application-level runtime pool (runtimes != nil)
// or one session-scoped Pi runtime. Each conversation gets its own Adapter so
// different conversations can execute concurrently while turns within the same
// conversation remain serialized.
type AgentService struct {
	store                *ConfigStore
	adapter              *piagent.Adapter
	adapterGeneration    uint64
	mu                   sync.Mutex
	prepareMu            sync.Mutex
	sharedPrepareMu      *sync.Mutex
	eventLogMu           sync.Mutex
	runtimes             map[int64]*AgentService
	cancel               context.CancelFunc
	activeMode           string
	activeAgent          string
	activeDataDir        string
	activeDir            string
	activeTools          bool
	activeSessionID      int64
	activeSessionDir     string
	activeSession        string
	activeCatalog        string
	activeProfile        string
	activeSkill          string
	activeSkillStamp     string
	activeStewardToken   string
	pendingStewardToken  string
	stewardPromptPending bool
	execAccumulatedMs    int64
	execTurnStart        time.Time
	preparing            bool
	prepareCanceled      bool
	// pendingRestart holds a deferred restart request that should run only once
	// the agent finishes its current task, so we never kill an in-flight turn
	// (e.g. while materializing a new RTK extension).
	pendingRestart   bool
	pendingReq       PromptRequest
	pendingTools     bool
	activeChangeNode string
	abortFollowUp    bool
	// waitingSubagents 记录会话是否处于"等待后台子 agent"状态：主 agent 回合
	// 已结束（agent_settled）但仍有子 agent 运行，execTurnStart 被保留以维持
	// 忙碌。用户终止时必须由后端强制收尾，否则 abort 只会被 Pi 应答、不会再
	// 有 agent_settled 触发结束（见 forceSettleWaitingLocked）。
	waitingSubagents           bool
	firstResponseTimer         *time.Timer
	firstResponseToken         uint64
	firstResponseNodeID        string
	firstResponseTimeout       time.Duration
	firstResponseTimeoutAction func(sessionID int64, nodeID string, timeout time.Duration)
	// uiWatchdog guards interactive extension UI requests. It is armed when an
	// interactive extension_ui_request is forwarded to the frontend and disarmed
	// as soon as the frontend acknowledges rendering (extension_ui_ack) or answers
	// (extension_ui_response). If it fires, the request is auto-cancelled so Pi is
	// never blocked forever by a dialog the frontend cannot show.
	uiWatchdogTimer *time.Timer
	uiWatchdogID    string
	// toolWatchdog bounds a single tool execution (currently the bash tool). It
	// is armed when a tool_execution_start event arrives and disarmed as soon as
	// the tool finishes (tool_execution_end) or the turn ends. If it fires, the
	// still-running bash command is aborted via Pi's abort_bash RPC so a wedged
	// command cannot stall the agent forever. If the runtime fails to report the
	// tool end promptly, a short escalation grace period kills the Pi process tree.
	toolWatchdogs        map[string]*toolWatchdogState
	toolWatchdogToken    uint64
	toolExecutionTimeout time.Duration
	// toolWatchdogAbortGrace is injectable for tests; production uses the
	// package default. It allows abort_bash to complete normally before the
	// stronger process-tree fallback is used.
	toolWatchdogAbortGrace time.Duration
	// killTreeOverride is test-only dependency injection for watchdog escalation.
	killTreeOverride func()
	runtimeEnv       func(agentID string, sessionID int64) map[string]string
	runtimeRelease   func(agentID string, sessionID int64)
	// sendCommandOverride is test-only dependency injection for event-state
	// tests. Production command delivery continues to use the Pi adapter.
	sendCommandOverride func(json.RawMessage) error
	// emitEventOverride keeps dispatchEvent tests independent of a running Wails
	// application; production events still go through application.Get().Event.
	emitEventOverride func(name string, value any)
	// stewardHooks wires the steward service into event dispatch: permission
	// relay and task-settled reporting. Nil when the steward is disabled.
	stewardHooks *stewardHooks
	// pinnedSessions marks sessions whose runtimes must never be evicted when
	// the runtime pool is full (the resident steward conversation).
	pinnedSessions map[int64]bool
	// idle reclaim for the pinned steward session: armed after a turn settles
	// and disarmed on the next prompt, so the resident Pi process is stopped
	// (session file preserved) when idle for too long.
	idleTimer   *time.Timer
	idleSession int64
	idleTimeout time.Duration
}

func NewAgentService(store *ConfigStore, environment ...func(agentID string, sessionID int64) map[string]string) *AgentService {
	sharedPrepareMu := &sync.Mutex{}
	service := &AgentService{
		store: store, adapter: piagent.NewAdapter(),
		runtimes: map[int64]*AgentService{}, sharedPrepareMu: sharedPrepareMu,
		firstResponseTimeout: defaultModelFirstResponseTimeout,
		toolExecutionTimeout: defaultToolExecutionTimeout,
		pinnedSessions:       map[int64]bool{},
	}
	if len(environment) > 0 {
		service.runtimeEnv = environment[0]
	}
	return service
}

func (s *AgentService) StartPrompt(req PromptRequest) error {
	// Any new prompt cancels a pending idle reclaim so the steward runtime
	// stays alive while work is arriving.
	s.mu.Lock()
	s.disarmIdleReclaimLocked()
	s.mu.Unlock()
	if s.runtimes == nil {
		return s.startPromptSingle(req)
	}
	if len(req.Command) > 0 {
		runtime, err := s.runtimeForCommand(req.SessionID)
		if err != nil {
			return err
		}
		return runtime.startPromptSingle(req)
	}
	if req.SessionID <= 0 {
		return errors.New("create a conversation before sending a prompt")
	}
	runtime, err := s.getOrCreateRuntime(req.SessionID)
	if err != nil {
		return err
	}
	return runtime.startPromptSingle(req)
}

func (s *AgentService) newSessionRuntime() *AgentService {
	return &AgentService{
		store: s.store, adapter: piagent.NewAdapter(),
		sharedPrepareMu: s.sharedPrepareMu, runtimeEnv: s.runtimeEnv, runtimeRelease: s.runtimeRelease,
		firstResponseTimeout: s.firstResponseTimeout, firstResponseTimeoutAction: s.firstResponseTimeoutAction,
		toolExecutionTimeout: s.toolExecutionTimeout,
		stewardHooks:         s.stewardHooks,
	}
}

func (s *AgentService) getOrCreateRuntime(sessionID int64) (*AgentService, error) {
	s.mu.Lock()
	if runtime := s.runtimes[sessionID]; runtime != nil {
		s.mu.Unlock()
		return runtime, nil
	}
	if len(s.runtimes) >= maxConcurrentSessionRuntimes {
		for id, runtime := range s.runtimes {
			// Pinned sessions (the resident steward) are never evicted.
			if runtime.isBusy() || s.pinnedSessions[id] {
				continue
			}
			delete(s.runtimes, id)
			_ = runtime.stopSessionSingle(id)
			break
		}
	}
	if len(s.runtimes) >= maxConcurrentSessionRuntimes {
		s.mu.Unlock()
		return nil, fmt.Errorf("maximum concurrent task limit reached (%d)", maxConcurrentSessionRuntimes)
	}
	runtime := s.newSessionRuntime()
	s.runtimes[sessionID] = runtime
	s.mu.Unlock()
	return runtime, nil
}

func (s *AgentService) runtimeForCommand(sessionID int64) (*AgentService, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sessionID > 0 {
		runtime := s.runtimes[sessionID]
		if runtime == nil {
			return nil, fmt.Errorf("conversation runtime not found: %d", sessionID)
		}
		return runtime, nil
	}
	if len(s.runtimes) == 1 {
		for _, runtime := range s.runtimes {
			return runtime, nil
		}
	}
	return nil, errors.New("session id is required for an agent command")
}

func (s *AgentService) isBusy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.preparing || !s.execTurnStart.IsZero()
}

// sessionExecutionSnapshot is the authoritative in-memory execution state for
// one conversation runtime. The database only freezes ExecDurationMs when a
// turn settles, so consumers that need a live task list must merge this value
// instead of treating the persisted duration/status as real-time state.
type sessionExecutionSnapshot struct {
	Running          bool
	ProcessRunning   bool
	WaitingSubagents bool
	ExecDurationMs   int64
	StartedAt        int64
}

// SessionRuntimeState is the authoritative conversation state returned to the
// frontend. Known distinguishes an idle/unmaterialized conversation from a
// runtime that is currently present in the pool.
type SessionRuntimeState struct {
	Known            bool  `json:"known"`
	Running          bool  `json:"running"`
	ProcessRunning   bool  `json:"processRunning"`
	WaitingSubagents bool  `json:"waitingSubagents"`
	ExecDurationMs   int64 `json:"execDurationMs"`
	StartedAt        int64 `json:"startedAt"`
}

// sessionExecutionSnapshots returns a cheap point-in-time view of every
// materialized conversation runtime. Copy the runtime map before locking
// children so no runtime lock is held together with the pool lock.
func (s *AgentService) sessionExecutionSnapshots() map[int64]sessionExecutionSnapshot {
	result := make(map[int64]sessionExecutionSnapshot)
	if s == nil {
		return result
	}
	if s.runtimes == nil {
		s.mu.Lock()
		if s.activeSessionID > 0 {
			total := s.execAccumulatedMs
			running := s.preparing || !s.execTurnStart.IsZero()
			startedAt := int64(0)
			if !s.execTurnStart.IsZero() {
				total += time.Since(s.execTurnStart).Milliseconds()
				startedAt = s.execTurnStart.UnixMilli()
			}
			result[s.activeSessionID] = sessionExecutionSnapshot{
				Running: running, ProcessRunning: s.adapter.IsRunning(),
				WaitingSubagents: s.waitingSubagents,
				ExecDurationMs:   total, StartedAt: startedAt,
			}
		}
		s.mu.Unlock()
		return result
	}

	s.mu.Lock()
	runtimes := make(map[int64]*AgentService, len(s.runtimes))
	for sessionID, runtime := range s.runtimes {
		runtimes[sessionID] = runtime
	}
	s.mu.Unlock()
	for sessionID, runtime := range runtimes {
		runtime.mu.Lock()
		// A newly allocated runtime has no authoritative state until prompt
		// preparation starts or it is bound to this conversation.
		if runtime.activeSessionID != sessionID && !runtime.preparing {
			runtime.mu.Unlock()
			continue
		}
		total := runtime.execAccumulatedMs
		running := runtime.preparing || !runtime.execTurnStart.IsZero()
		startedAt := int64(0)
		if !runtime.execTurnStart.IsZero() {
			total += time.Since(runtime.execTurnStart).Milliseconds()
			startedAt = runtime.execTurnStart.UnixMilli()
		}
		result[sessionID] = sessionExecutionSnapshot{
			Running: running, ProcessRunning: runtime.adapter.IsRunning(),
			WaitingSubagents: runtime.waitingSubagents,
			ExecDurationMs:   total, StartedAt: startedAt,
		}
		runtime.mu.Unlock()
	}
	return result
}

func (s *AgentService) sessionRuntimeState(sessionID int64) SessionRuntimeState {
	snapshot, ok := s.sessionExecutionSnapshots()[sessionID]
	if !ok {
		return SessionRuntimeState{}
	}
	return SessionRuntimeState{
		Known: true, Running: snapshot.Running, ProcessRunning: snapshot.ProcessRunning,
		WaitingSubagents: snapshot.WaitingSubagents,
		ExecDurationMs:   snapshot.ExecDurationMs, StartedAt: snapshot.StartedAt,
	}
}

func (s *AgentService) startPromptSingle(req PromptRequest) error {
	if len(req.Command) > 0 {
		s.mu.Lock()
		defer s.mu.Unlock()
		if !s.adapter.IsRunning() {
			return errors.New("start a conversation before sending an agent command")
		}
		// Switching sessions while a turn is active can leave Pi without a
		// matching agent_end event and keep the UI locked in its running state.
		if req.Command["type"] == "switch_session" && !s.execTurnStart.IsZero() {
			return errors.New("cannot switch conversations while an agent turn is running")
		}
		commandType := stringValue(req.Command["type"])
		// 用户终止：写 .abort 标记让 bridge 杀掉所有子 agent 进程并写终态，
		// 同时强制收尾可能处于"等待子 agent"状态的会话（forceSettleWaitingLocked）。
		if commandType == "abort" {
			s.handleAbortCommandLocked()
		}
		// extension_ui_ack is a frontend-only acknowledgement that an interactive
		// dialog was rendered. It disarms the watchdog and is never forwarded to
		// Pi, which has no notion of it.
		if commandType == "extension_ui_ack" {
			s.disarmUIWatchdogLocked(stringValue(req.Command["id"]))
			return nil
		}
		// A real answer to the pending dialog also disarms the watchdog before the
		// response is forwarded to Pi.
		if commandType == "extension_ui_response" {
			s.disarmUIWatchdogLocked(stringValue(req.Command["id"]))
		}
		raw, err := json.Marshal(req.Command)
		if err != nil {
			return fmt.Errorf("encode agent command: %w", err)
		}
		return s.adapter.SendCommand(raw)
	}

	req.Message = strings.TrimSpace(req.Message)
	if err := validatePromptContent(req); err != nil {
		return err
	}
	displayMessage := req.Message
	if req.SessionID <= 0 {
		return errors.New("create a conversation before sending a prompt")
	}

	// Reject the common busy case before doing workspace or model I/O. The
	// state is checked again under the lock immediately before touching the
	// adapter so concurrent prompt requests cannot both start a turn.
	s.mu.Lock()
	turnRunning := !s.execTurnStart.IsZero()
	s.mu.Unlock()
	if turnRunning {
		return errors.New("an agent turn is already running")
	}
	prepareMu := &s.prepareMu
	if s.sharedPrepareMu != nil {
		prepareMu = s.sharedPrepareMu
	}
	prepareMu.Lock()
	defer prepareMu.Unlock()
	s.mu.Lock()
	turnRunning = !s.execTurnStart.IsZero()
	if !turnRunning {
		s.preparing = true
		s.prepareCanceled = false
	}
	s.mu.Unlock()
	if turnRunning {
		return errors.New("an agent turn is already running")
	}
	preparing := true
	defer func() {
		if !preparing {
			return
		}
		s.mu.Lock()
		s.preparing = false
		s.prepareCanceled = false
		s.mu.Unlock()
	}()

	// Keep validation and model materialization outside the service lock so abort
	// and agent commands remain responsive while prompt preparation is running.
	cfg := s.store.Get()
	session, exists, err := s.store.Store().SessionByID(req.SessionID)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("conversation not found: %d", req.SessionID)
	}
	if req.SessionPath == "" {
		req.SessionPath = session.SessionPath
	}
	profile, found := cfg.Agent(req.AgentID)
	if !found {
		return fmt.Errorf("agent not found: %s", req.AgentID)
	}
	if req.SkillPath != "" {
		resolved, err := validateAgentSkillPath(profile, req.SkillPath)
		if err != nil {
			return err
		}
		req.SkillPath = resolved
	}
	skillStamp := skillFileSignature(req.SkillPath)
	if req.Provider == "" {
		req.Provider = profile.DefaultProvider
	}
	if req.Model == "" {
		req.Model = profile.DefaultModel
	}
	if req.Provider == "" || req.Model == "" {
		if p, m, ok := profile.ResolveDefaultModel(cfg.Providers); ok {
			req.Provider, req.Model = p, m
		}
	}
	if isOpenAICodexProvider(cfg.Providers, req.Provider) {
		defaultDir, err := piagent.DefaultAgentDir()
		if err != nil {
			applog.Errorf("resolve shared Pi auth directory before prompt: agent=%s: %v", profile.ID, err)
			return errors.New("无法确定 Pi 默认 Agent 目录，请稍后重试")
		}
		if _, ok, err := readChatGPTAuthEntry(defaultDir); err != nil {
			applog.Errorf("read shared ChatGPT credential before prompt: agent=%s: %v", profile.ID, err)
			return errors.New("无法读取 ChatGPT 登录状态")
		} else if !ok {
			return errors.New("尚未登录 ChatGPT，请先在模型页面完成授权")
		}
	}
	if err := piagent.ValidateProviders(cfg.Providers, req.Provider, req.Model); err != nil {
		return err
	}
	if err := validateProviderCredentials(cfg.Providers, req.Provider); err != nil {
		return err
	}
	selectedModel, found := piagent.FindModel(cfg.Providers, req.Provider, req.Model)
	if !found {
		return fmt.Errorf("model not found: %s/%s", req.Provider, req.Model)
	}
	if len(req.Images) > 0 && !selectedModel.SupportsImages() {
		return fmt.Errorf("model %s does not support image input", req.Model)
	}
	if req.ThinkingLevel == "" {
		req.ThinkingLevel = selectedModel.DefaultThinkingLevel
	}
	if !selectedModel.SupportsThinkingLevel(req.ThinkingLevel) {
		return fmt.Errorf("thinking level %q is not supported by model %s", req.ThinkingLevel, req.Model)
	}
	displayImages := append([]ImageInput(nil), req.Images...)
	if req.WorkDir == "" {
		req.WorkDir = cfg.LastEnvironment
	}
	if req.WorkDir == "" {
		req.WorkDir, _ = os.Getwd()
	}
	if info, err := os.Stat(req.WorkDir); err != nil || !info.IsDir() {
		return fmt.Errorf("environment directory does not exist: %s", req.WorkDir)
	}
	if err := piagent.WriteModels(profile.DataDir, cfg.Providers); err != nil {
		return fmt.Errorf("write models.json: %w", err)
	}
	// Propagate the updated model configuration to every other agent so all
	// agents stay in sync after a single-agent "set model" update.
	otherDirs := make([]string, 0, len(cfg.Agents))
	for _, agent := range cfg.Agents {
		if agent.DataDir != "" {
			otherDirs = append(otherDirs, agent.DataDir)
		}
	}
	if err := piagent.SyncModelsToAgents(profile.DataDir, otherDirs); err != nil {
		return fmt.Errorf("sync models.json to agents: %w", err)
	}
	catalog := providerCatalogSignature(cfg.Providers)
	profileSignature := agentRuntimeSignature(profile, cfg)
	agentDataDir := filepath.Clean(profile.DataDir)

	toolsEnabled := selectedModel.SupportsTools()
	// Tool execution timeout is a global runtime setting; apply the effective
	// value (already normalized by ConfigStore.Normalize) to this session's
	// watchdog before the turn starts.
	s.toolExecutionTimeout = toolExecutionTimeoutFromConfig(cfg)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.prepareCanceled {
		s.preparing = false
		s.prepareCanceled = false
		preparing = false
		return errors.New("prompt canceled")
	}
	s.preparing = false
	preparing = false
	if !s.execTurnStart.IsZero() {
		return errors.New("an agent turn is already running")
	}
	if s.adapter.IsRunning() && (s.activeDir != req.WorkDir || s.activeMode != req.Mode || s.activeAgent != profile.ID || s.activeDataDir != agentDataDir || s.activeTools != toolsEnabled || s.activeCatalog != catalog || s.activeProfile != profileSignature || s.activeSkill != req.SkillPath || s.activeSkillStamp != skillStamp || s.activeSessionID != req.SessionID) {
		previousAgent, previousSession := s.activeAgent, s.activeSessionID
		if s.cancel != nil {
			s.cancel()
			s.cancel = nil
		}
		if err := s.adapter.Stop(); err != nil {
			return err
		}
		s.finishExecutionLocked("active")
		if s.runtimeRelease != nil && previousSession > 0 && (previousAgent != profile.ID || previousSession != req.SessionID) {
			s.runtimeRelease(previousAgent, previousSession)
		}
	}
	if !s.adapter.IsRunning() {
		if err := s.startAdapter(req, cfg, profile, toolsEnabled); err != nil {
			return err
		}
	} else if req.SessionPath != "" && req.SessionPath != s.activeSession {
		if err := s.adapter.SendCommand(mustJSON(map[string]any{
			"type":        "switch_session",
			"sessionPath": req.SessionPath,
		})); err != nil {
			return fmt.Errorf("switch conversation: %w", err)
		}
		s.activeSession = req.SessionPath
		s.activeSessionID = session.ID
		s.activeSessionDir = session.SessionDir
		s.execAccumulatedMs = session.ExecDurationMs
	}

	s.activeSessionID = session.ID
	s.activeSessionDir = session.SessionDir
	s.activeSession = req.SessionPath
	s.execAccumulatedMs = session.ExecDurationMs
	turnStartedAt := time.Now()
	changeNodeID, err := beginChangeNode(session.SessionDir, req.WorkDir, req.Message, turnStartedAt.UnixMilli())
	if err != nil {
		return fmt.Errorf("create conversation change node: %w", err)
	}
	s.activeChangeNode = changeNodeID
	// Bridge for the Browser Profile extension: it reads the current change node
	// id so it can scope browser artifacts to the prompt that triggered them.
	if err := os.WriteFile(filepath.Join(session.SessionDir, ".active-change-node"), []byte(changeNodeID), 0o600); err != nil {
		applog.Infof("browser profile: write active change node: %v", err)
	}

	// Archive user attachments for this turn (design §A1). Scoped to the node
	// just created so every uploaded file appears in this turn's manifest with a
	// stable on-disk path. A failed upload aborts the turn before the agent
	// starts.
	var archivedAttachments []ArtifactRef
	if len(req.Attachments) > 0 {
		refs, aerr := archiveAttachments(session.SessionDir, changeNodeID, req.Attachments)
		if aerr != nil {
			_ = finishChangeNode(session.SessionDir, changeNodeID, "error", time.Now().UnixMilli())
			s.activeChangeNode = ""
			s.finishExecutionLocked("active")
			return fmt.Errorf("attachment upload failed: %w", aerr)
		}
		archivedAttachments = refs
		if len(refs) > 0 {
			// Image attachments use the existing inline image channel when they
			// fit. Files that exceed direct-transfer limits remain archived and
			// are described in the manifest with the downgrade reason.
			imgInputs, transfers := imageAttachmentInputs(refs, req.Images, selectedModel.SupportsImages())
			req.Images = append(req.Images, imgInputs...)
			if err := validateImages(req.Images); err != nil {
				_ = finishChangeNode(session.SessionDir, changeNodeID, "error", time.Now().UnixMilli())
				s.activeChangeNode = ""
				s.finishExecutionLocked("active")
				return fmt.Errorf("validate attachment images: %w", err)
			}
			req.Message = appendManifest(req.Message, buildAttachmentManifest(refs, transfers))
		}
	}

	s.execTurnStart = turnStartedAt
	userEvent := map[string]any{
		"type": "user_text", "message": req.Message, "displayMessage": displayMessage,
		"images": displayImages, "attachments": archivedAttachments,
		"changeNodeId": changeNodeID, "_recordedAt": turnStartedAt.UnixMilli(),
	}
	if req.StewardDispatchToken != "" {
		userEvent["_stewardDispatchToken"] = req.StewardDispatchToken
	}
	if err := s.appendEvent(session.SessionDir, userEvent); err != nil {
		_ = finishChangeNode(session.SessionDir, changeNodeID, "error", time.Now().UnixMilli())
		s.activeChangeNode = ""
		s.finishExecutionLocked("active")
		return fmt.Errorf("write conversation log: %w", err)
	}
	if err := s.store.Store().UpdateSession(session.ID, map[string]any{
		"agent_id": req.AgentID, "provider": req.Provider, "model": req.Model, "status": "running",
	}); err != nil {
		_ = finishChangeNode(session.SessionDir, changeNodeID, "error", time.Now().UnixMilli())
		s.activeChangeNode = ""
		s.finishExecutionLocked("active")
		return fmt.Errorf("update conversation: %w", err)
	}
	// Bind the correlation token before writing the prompt command, but do not
	// promote it to the active turn until Pi emits message_start. Keeping the
	// previous turn token active across this boundary prevents a duplicated or
	// delayed settled event from being relabelled with the new generation.
	s.pendingStewardToken = req.StewardDispatchToken
	s.stewardPromptPending = true
	if err := s.sendPrompt(req, selectedModel); err != nil {
		s.pendingStewardToken = ""
		s.stewardPromptPending = false
		_ = finishChangeNode(session.SessionDir, changeNodeID, "error", time.Now().UnixMilli())
		s.activeChangeNode = ""
		s.finishExecutionLocked("active")
		return err
	}
	s.armFirstResponseWatchdogLocked(req.SessionID, changeNodeID)
	if cfg.LastEnvironment != req.WorkDir || cfg.DefaultProvider != req.Provider || cfg.DefaultModel != req.Model {
		cfg.LastEnvironment = req.WorkDir
		cfg.DefaultProvider = req.Provider
		cfg.DefaultModel = req.Model
		_ = s.store.Save(cfg)
	}
	application.Get().Event.Emit("agent:state", map[string]any{
		"running": true, "processRunning": true, "codingToSessionId": req.SessionID,
	})
	return nil
}

func (s *AgentService) sendPrompt(req PromptRequest, model piagent.Model) error {
	setModel, _ := json.Marshal(map[string]string{"type": "set_model", "provider": req.Provider, "modelId": req.Model})
	if err := s.adapter.SendCommand(setModel); err != nil {
		return err
	}
	if model.Reasoning && req.ThinkingLevel != "" {
		setThinking, _ := json.Marshal(map[string]string{"type": "set_thinking_level", "level": req.ThinkingLevel})
		if err := s.adapter.SendCommand(setThinking); err != nil {
			return err
		}
	}
	message := req.Message
	// 强制提示词：对启用的模型，将 PROMPT_FORCE.md 内容追加到用户问题末尾。
	if profile, found := s.store.Get().Agent(req.AgentID); found {
		if profile.ForcedPromptModels[req.Provider+"/"+req.Model] {
			if data, err := os.ReadFile(filepath.Join(profile.DataDir, "PROMPT_FORCE.md")); err == nil {
				if forced := strings.TrimSpace(string(data)); forced != "" {
					message = message + "\n\n" + forced
				}
			}
		}
	}
	prompt := map[string]any{"type": "prompt", "message": message}
	if len(req.Images) > 0 {
		prompt["images"] = req.Images
	}
	raw, _ := json.Marshal(prompt)
	return s.adapter.SendCommand(raw)
}
