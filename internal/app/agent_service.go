package app

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
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

const browserManagedSessionIdleTimeoutMS = "900000"

const maxConcurrentSessionRuntimes = 4
const defaultModelFirstResponseTimeout = 60 * time.Second

// AgentService is either the application-level runtime pool (runtimes != nil)
// or one session-scoped Pi runtime. Each conversation gets its own Adapter so
// different conversations can execute concurrently while turns within the same
// conversation remain serialized.
type AgentService struct {
	store             *ConfigStore
	adapter           *piagent.Adapter
	mu                sync.Mutex
	prepareMu         sync.Mutex
	sharedPrepareMu   *sync.Mutex
	eventLogMu        sync.Mutex
	runtimes          map[int64]*AgentService
	cancel            context.CancelFunc
	activeMode        string
	activeAgent       string
	activeDataDir     string
	activeDir         string
	activeTools       bool
	activeSessionID   int64
	activeSessionDir  string
	activeSession     string
	activeCatalog     string
	activeProfile     string
	activeSkill       string
	activeSkillStamp  string
	execAccumulatedMs int64
	execTurnStart     time.Time
	preparing         bool
	prepareCanceled   bool
	// pendingRestart holds a deferred restart request that should run only once
	// the agent finishes its current task, so we never kill an in-flight turn
	// (e.g. while materializing a new RTK extension).
	pendingRestart             bool
	pendingReq                 PromptRequest
	pendingTools               bool
	activeChangeNode           string
	firstResponseTimer         *time.Timer
	firstResponseToken         uint64
	firstResponseNodeID        string
	firstResponseTimeout       time.Duration
	firstResponseTimeoutAction func(sessionID int64, nodeID string, timeout time.Duration)
	runtimeEnv                 func(agentID string, sessionID int64) map[string]string
	runtimeRelease             func(agentID string, sessionID int64)
}

func NewAgentService(store *ConfigStore, environment ...func(agentID string, sessionID int64) map[string]string) *AgentService {
	sharedPrepareMu := &sync.Mutex{}
	service := &AgentService{
		store: store, adapter: piagent.NewAdapter(),
		runtimes: map[int64]*AgentService{}, sharedPrepareMu: sharedPrepareMu,
		firstResponseTimeout: defaultModelFirstResponseTimeout,
	}
	if len(environment) > 0 {
		service.runtimeEnv = environment[0]
	}
	return service
}

func (s *AgentService) StartPrompt(req PromptRequest) error {
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
			if !runtime.isBusy() {
				delete(s.runtimes, id)
				_ = runtime.stopSessionSingle(id)
				break
			}
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
		log.Printf("browser profile: write active change node: %v", err)
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
	if err := s.appendEvent(session.SessionDir, map[string]any{
		"type": "user_text", "message": req.Message, "displayMessage": displayMessage,
		"images": displayImages, "attachments": archivedAttachments,
		"changeNodeId": changeNodeID, "_recordedAt": turnStartedAt.UnixMilli(),
	}); err != nil {
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
	if err := s.sendPrompt(req, selectedModel); err != nil {
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

func validatePromptContent(req PromptRequest) error {
	if strings.TrimSpace(req.Message) == "" && len(req.Images) == 0 && len(req.Attachments) == 0 {
		return errors.New("message, images, and attachments are empty")
	}
	return nil
}

func validateImages(images []ImageInput) error {
	// Limits match the attachment guard rails
	// (docs/design/附件上传、输入产物与多模态传递设计.md §6): 50 MB each, 100 MB total.
	const maxBytes = 50 * 1024 * 1024
	const maxTotal = 100 * 1024 * 1024
	if len(images) > 10 {
		return errors.New("send at most 10 images")
	}
	var total int64
	for _, image := range images {
		if image.Type != "image" || !strings.HasPrefix(image.MimeType, "image/") {
			return fmt.Errorf("invalid image attachment: %s", image.Name)
		}
		decoded, err := base64.StdEncoding.DecodeString(image.Data)
		if err != nil {
			return fmt.Errorf("invalid image data: %s", image.Name)
		}
		if len(decoded) > maxBytes {
			return fmt.Errorf("image is larger than 50 MB: %s", image.Name)
		}
		total += int64(len(decoded))
	}
	if total > maxTotal {
		return errors.New("total image size exceeds 100 MB")
	}
	return nil
}

func validateProviderCredentials(providers []piagent.Provider, providerName string) error {
	for _, provider := range providers {
		if provider.Name != providerName {
			continue
		}
		key := strings.TrimSpace(provider.APIKey)
		envName := ""
		if strings.HasPrefix(key, "${") && strings.HasSuffix(key, "}") {
			envName = strings.TrimSuffix(strings.TrimPrefix(key, "${"), "}")
		} else if strings.HasPrefix(key, "$") {
			envName = strings.TrimPrefix(key, "$")
		}
		if envName == "" {
			return nil
		}
		for index, char := range envName {
			if !(char == '_' || char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || index > 0 && char >= '0' && char <= '9') {
				return nil
			}
		}
		if os.Getenv(envName) == "" {
			return fmt.Errorf("API key environment variable %s is not set for provider %s", envName, provider.Name)
		}
		return nil
	}
	return nil
}

func (s *AgentService) startAdapter(req PromptRequest, cfg AppConfig, profile AgentProfile, toolsEnabled bool) error {
	runCtx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	sessionDir := filepath.Join(cfg.SessionDir, fmt.Sprintf("s%d", req.SessionID))
	if err := ensurePrivateDir(sessionDir); err != nil {
		cancel()
		s.cancel = nil
		return fmt.Errorf("create agent session directory: %w", err)
	}
	if err := piagent.MaterializeSystemExtensions(profile.DataDir); err != nil {
		cancel()
		s.cancel = nil
		return err
	}
	extra := []string{}
	if toolsEnabled {
		if err := piagent.MaterializeBuiltinTools(profile.DataDir, profile.Builtin); err != nil {
			return err
		}
		if disabled := disabledPiTools(profile.PiTools); len(disabled) > 0 {
			extra = append(extra, "--exclude-tools", strings.Join(disabled, ","))
		}
	} else {
		extra = append(extra, "--no-builtin-tools")
	}
	if req.SkillPath != "" {
		// The path was validated against this agent before it reaches the Pi CLI.
		extra = append(extra, "--skill", req.SkillPath)
	}
	if _, ok := cfg.Extensions.Figma.ActiveAuthorization(); profile.Recommended["figma"] && cfg.Extensions.Figma.Enabled && ok {
		extra = append(extra, "--append-system-prompt", figmaRoutingPrompt)
	}
	// RTK is a per-agent recommended extension: materialize it only when this
	// agent has it enabled, and remove any stale copy otherwise so a disabled
	// agent never loads it.
	if profile.Recommended["rtk"] {
		if rtkSource := extensions.EnsureRTKPiExtension(); rtkSource != "" {
			if _, err := piagent.MaterializeRTKExtension(profile.DataDir, rtkSource); err != nil {
				return err
			}
		}
	} else {
		_ = piagent.RemoveRTKExtension(profile.DataDir)
	}
	sessionID := ""
	if req.SessionPath == "" {
		sessionID = fmt.Sprintf("codingto-session-%d", req.SessionID)
	}
	agentEnv := agentProcessEnv(cfg, profile)
	agentEnv["CODINGTO_SESSION_DIR"] = sessionDir
	agentEnv["CODINGTO_WORK_DIR"] = req.WorkDir
	if selectedModel, found := piagent.FindModel(cfg.Providers, req.Provider, req.Model); found {
		agentEnv["CODINGTO_MODEL_INPUT_MODALITIES"] = strings.Join(selectedModel.Input, ",")
	}
	if toolsEnabled && profile.Builtin["document"] {
		bridgeBinary, err := resolveDocumentBridgeBinary()
		if err != nil {
			cancel()
			s.cancel = nil
			return err
		}
		agentEnv["CODINGTO_DOCUMENT_BRIDGE_BIN"] = bridgeBinary
	}
	if s.runtimeEnv != nil {
		for key, value := range s.runtimeEnv(profile.ID, req.SessionID) {
			agentEnv[key] = value
		}
	}
	if toolsEnabled {
		if err := s.prepareSubagentRuntime(req, cfg, profile, sessionDir, agentEnv); err != nil {
			cancel()
			s.cancel = nil
			return err
		}
	}
	if err := s.adapter.Start(runCtx, piagent.StartConfig{
		WorkDir: req.WorkDir, SessionDir: sessionDir, Provider: req.Provider, Model: req.Model,
		SessionID:   sessionID,
		SessionPath: req.SessionPath, ExtraArgs: extra, Env: agentEnv,
	}); err != nil {
		cancel()
		s.cancel = nil
		return err
	}
	s.activeDir, s.activeMode, s.activeAgent, s.activeTools = req.WorkDir, req.Mode, profile.ID, toolsEnabled
	s.activeDataDir = filepath.Clean(profile.DataDir)
	s.activeSessionID = req.SessionID
	s.activeSessionDir = sessionDir
	s.activeSession = req.SessionPath
	if session, ok, _ := s.store.Store().SessionByID(req.SessionID); ok {
		s.execAccumulatedMs = session.ExecDurationMs
	}
	s.execTurnStart = time.Time{}
	s.activeCatalog = providerCatalogSignature(cfg.Providers)
	s.activeProfile = agentRuntimeSignature(profile, cfg)
	s.activeSkill = req.SkillPath
	s.activeSkillStamp = skillFileSignature(req.SkillPath)
	go s.forwardEvents(s.adapter, req.SessionID, sessionDir)
	return nil
}

const figmaRoutingPrompt = `Figma integration is enabled. For any figma.com design URL, use a direct tool whose name contains get_figma_data when available; otherwise use the mcp gateway with server "figma" to connect and call get_figma_data. Never use the browser tool to read a Figma design URL.`

// findChromeExecutable returns the path to a full Google Chrome installation if
// present, preferring it over any bundled Chromium-for-Testing binary so the
// browser workflow runs in a real, fully-featured Chrome. Returns "" when no
// known Chrome install is found, letting agent-browser fall back to its own
// discovery.
func findChromeExecutable() string {
	var candidates []string
	switch runtime.GOOS {
	case "windows":
		if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
			candidates = append(candidates, filepath.Join(localAppData, "Google", "Chrome", "Application", "chrome.exe"))
		}
		candidates = append(candidates,
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
		)
	case "darwin":
		if home, err := os.UserHomeDir(); err == nil {
			candidates = append(candidates, filepath.Join(home, "Applications", "Google Chrome.app", "Contents", "MacOS", "Google Chrome"))
		}
		candidates = append(candidates, "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome")
	default:
		candidates = append(candidates,
			"/usr/bin/google-chrome",
			"/usr/bin/google-chrome-stable",
			"/opt/google/chrome/chrome",
		)
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

// findAgentBrowserExecutable resolves the npm shim before the Pi process
// starts. Desktop processes do not always inherit the same command lookup
// behavior as an interactive PowerShell session, so leaving the extension to
// invoke a bare "agent-browser.cmd" can fail even when the CLI is installed.
func findAgentBrowserExecutable() string {
	if configured := strings.TrimSpace(os.Getenv("AGENT_BROWSER_BIN")); configured != "" {
		if absolute, err := filepath.Abs(configured); err == nil {
			return absolute
		}
		return configured
	}
	candidates := []string{"agent-browser"}
	if runtime.GOOS == "windows" {
		candidates = append(candidates, "agent-browser.cmd")
	}
	for _, candidate := range candidates {
		if executable, err := exec.LookPath(candidate); err == nil {
			if absolute, absErr := filepath.Abs(executable); absErr == nil {
				return absolute
			}
			return executable
		}
	}
	return ""
}

func agentProcessEnv(cfg AppConfig, profile AgentProfile) map[string]string {
	agentDataDir := profile.DataDir
	if absolute, err := filepath.Abs(agentDataDir); err == nil {
		agentDataDir = absolute
	}
	agentEnv := map[string]string{
		"PI_CODING_AGENT_DIR": agentDataDir,
		// Keep headed as the ambient agent-browser default for ordinary browser
		// work. Browser Profile overrides it explicitly per stage with
		// --headed true/false according to the Agent's policy.
		"AGENT_BROWSER_HEADED": "1",
		// pi-agent-browser-native applies this value to every managed-session
		// subprocess. Browser Profile also invokes agent-browser directly before
		// handing the same session to the native tool, so both launch paths must
		// inherit an identical value. Otherwise upstream restarts the background
		// browser on the first snapshot and replaces the target tab with
		// about:blank.
		"PI_AGENT_BROWSER_IMPLICIT_SESSION_IDLE_TIMEOUT_MS": browserManagedSessionIdleTimeoutMS,
		"AGENT_BROWSER_IDLE_TIMEOUT_MS":                     browserManagedSessionIdleTimeoutMS,
		"CODINGTO_CREDENTIAL_STORE":                         browserworkflow.CredentialStoreName(),
		"CODINGTO_BROWSER_PROFILE_EXISTING_MODE":            profile.BrowserProfilePolicy.ExistingProfileMode,
		"CODINGTO_BROWSER_PROFILE_LOGIN_MODE":               profile.BrowserProfilePolicy.InteractiveLoginMode,
		"CODINGTO_BROWSER_PROFILE_AUTHENTICATED_MODE":       profile.BrowserProfilePolicy.AuthenticatedTaskMode,
	}
	if chromePath := findChromeExecutable(); chromePath != "" {
		agentEnv["AGENT_BROWSER_EXECUTABLE_PATH"] = chromePath
	}
	if browserPath := findAgentBrowserExecutable(); browserPath != "" {
		agentEnv["AGENT_BROWSER_BIN"] = browserPath
	}
	if executable, err := os.Executable(); err == nil {
		plugins := []map[string]any{}
		ambientPlugins := strings.TrimSpace(os.Getenv("AGENT_BROWSER_PLUGINS"))
		pluginsValid := true
		if ambientPlugins != "" && json.Unmarshal([]byte(ambientPlugins), &plugins) != nil {
			// Preserve an invalid ambient value so CodingTo does not silently erase
			// user configuration. Automatic login will fail safely and fall back to
			// the interactive path until the value is repaired.
			agentEnv["AGENT_BROWSER_PLUGINS"] = ambientPlugins
			pluginsValid = false
		}
		if pluginsValid {
			filtered := plugins[:0]
			for _, plugin := range plugins {
				if plugin["name"] != "codingto-vault" {
					filtered = append(filtered, plugin)
				}
			}
			plugins = append(filtered, map[string]any{
				"name":         "codingto-vault",
				"command":      executable,
				"args":         []string{"credential-provider"},
				"capabilities": []string{"credential.read"},
			})
			if raw, err := json.Marshal(plugins); err == nil {
				agentEnv["AGENT_BROWSER_PLUGINS"] = string(raw)
				if globalBase, berr := browserworkflow.ProfileBaseDir(); berr == nil {
					agentEnv["CODINGTO_AGENT_DATA_DIR"] = globalBase
				}
				if profileDir, berr := browserworkflow.ProfileDir(); berr == nil {
					agentEnv["CODINGTO_BROWSER_PROFILES_DIR"] = profileDir
				}
			}
		}
	}
	if profile.Recommended["rtk"] {
		if rtkPath, err := extensions.RTKExecutable(); err == nil {
			agentEnv["PATH"] = filepath.Dir(rtkPath) + string(os.PathListSeparator) + os.Getenv("PATH")
		}
	}
	if profile.Recommended["figma"] {
		// mcp.json contains only these environment references. Keep both keys
		// explicit so ambient shell credentials can never select another account.
		agentEnv["CODINGTO_FIGMA_API_KEY"] = ""
		agentEnv["CODINGTO_FIGMA_OAUTH_TOKEN"] = ""
		if authorization, ok := cfg.Extensions.Figma.ActiveAuthorization(); cfg.Extensions.Figma.Enabled && ok {
			if authorization.TokenType == "oauth" {
				agentEnv["CODINGTO_FIGMA_OAUTH_TOKEN"] = authorization.Token
			} else {
				agentEnv["CODINGTO_FIGMA_API_KEY"] = authorization.Token
			}
		}
	}
	return agentEnv
}

func providerCatalogSignature(providers []piagent.Provider) string {
	data, err := json.Marshal(providers)
	if err != nil {
		return ""
	}
	return string(data)
}

func skillFileSignature(skillPath string) string {
	if strings.TrimSpace(skillPath) == "" {
		return ""
	}
	raw, err := os.ReadFile(skillPath)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("%x", sum[:])
}

func agentRuntimeSignature(profile AgentProfile, cfg AppConfig) string {
	authorized := make(map[string]struct{}, len(profile.SubAgents))
	for _, key := range profile.SubAgents {
		authorized[key] = struct{}{}
	}
	children := make(map[string]AgentProfile, len(authorized))
	for _, child := range cfg.Agents {
		if _, allowed := authorized[child.ID]; allowed && child.ID != profile.ID {
			children[child.ID] = child
		}
	}
	value := struct {
		Parent   AgentProfile            `json:"parent"`
		Children map[string]AgentProfile `json:"children"`
	}{
		Parent: profile, Children: children,
	}
	data, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(data)
}

func disabledPiTools(configured map[string]bool) []string {
	disabled := []string{}
	for _, key := range []string{"read", "bash", "edit", "write"} {
		if enabled, exists := configured[key]; exists && !enabled {
			disabled = append(disabled, key)
		}
	}
	return disabled
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
	prompt := map[string]any{"type": "prompt", "message": req.Message}
	if len(req.Images) > 0 {
		prompt["images"] = req.Images
	}
	raw, _ := json.Marshal(prompt)
	return s.adapter.SendCommand(raw)
}

func (s *AgentService) armFirstResponseWatchdogLocked(sessionID int64, nodeID string) {
	s.disarmFirstResponseWatchdogLocked()
	timeout := s.firstResponseTimeout
	if timeout <= 0 {
		timeout = defaultModelFirstResponseTimeout
	}
	s.firstResponseToken++
	token := s.firstResponseToken
	s.firstResponseNodeID = nodeID
	s.firstResponseTimer = time.AfterFunc(timeout, func() {
		s.fireFirstResponseWatchdog(token, sessionID, nodeID, timeout)
	})
}

func (s *AgentService) disarmFirstResponseWatchdogLocked() {
	if s.firstResponseTimer != nil {
		s.firstResponseTimer.Stop()
		s.firstResponseTimer = nil
	}
	s.firstResponseNodeID = ""
	s.firstResponseToken++
}

func (s *AgentService) fireFirstResponseWatchdog(token uint64, sessionID int64, nodeID string, timeout time.Duration) {
	s.mu.Lock()
	if token != s.firstResponseToken ||
		s.firstResponseNodeID != nodeID ||
		s.activeSessionID != sessionID ||
		s.activeChangeNode != nodeID ||
		s.execTurnStart.IsZero() {
		s.mu.Unlock()
		return
	}
	s.firstResponseTimer = nil
	s.firstResponseNodeID = ""
	s.firstResponseToken++
	action := s.firstResponseTimeoutAction
	s.mu.Unlock()

	if action != nil {
		action(sessionID, nodeID, timeout)
		return
	}
	s.handleFirstResponseTimeout(sessionID, nodeID, timeout)
}

func firstResponseObserved(event map[string]any) bool {
	switch stringValue(event["type"]) {
	case "message_update", "tool_execution_start", "tool_execution_update", "tool_execution_end",
		"extension_ui_request", "turn_end", "agent_end", "agent_settled", "error":
		return true
	case "message_end":
		return stringValue(mapValue(event["message"])["role"]) == "assistant"
	default:
		return false
	}
}

func (s *AgentService) handleFirstResponseTimeout(sessionID int64, nodeID string, timeout time.Duration) {
	seconds := int(timeout.Round(time.Second) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	message := fmt.Sprintf(
		"Model did not return a response within %d seconds. The stalled agent process was stopped; please retry.",
		seconds,
	)

	s.mu.Lock()
	if s.activeSessionID != sessionID || s.activeChangeNode != nodeID || s.execTurnStart.IsZero() {
		s.mu.Unlock()
		return
	}
	cancel := s.cancel
	s.cancel = nil
	s.pendingRestart = false
	s.activeChangeNode = ""
	sessionDir := s.activeSessionDir
	sessionPath := s.activeSession
	if sessionPath != "" {
		if _, err := os.Stat(sessionPath); errors.Is(err, os.ErrNotExist) {
			s.activeSession = ""
			sessionPath = ""
			if s.store != nil {
				_ = s.store.Store().UpdateSession(sessionID, map[string]any{"session_path": ""})
			}
		}
	}
	s.finishExecutionLocked("active")
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	_ = s.adapter.Stop()
	_ = os.Remove(filepath.Join(sessionDir, ".active-change-node"))

	recordedAt := time.Now().UnixMilli()
	_ = finishChangeNode(sessionDir, nodeID, "timeout", recordedAt)
	summary, err := readChangeSummary(sessionDir, nodeID)
	if err != nil {
		summary = ChangeSummary{
			NodeID: nodeID, Status: "timeout", Files: []FileChangeSummary{},
		}
	}
	events := []map[string]any{
		{
			"type": "agent_end", "messages": []any{}, "errorMessage": message,
			"changeSummary": summary, "changeNodeId": nodeID,
			"codingToSessionId": sessionID, "_recordedAt": recordedAt,
		},
		{
			"type": "error", "code": "model_first_response_timeout", "error": message,
			"changeNodeId": nodeID, "codingToSessionId": sessionID, "_recordedAt": recordedAt,
		},
		{
			"type": "agent_settled", "reason": "model_first_response_timeout",
			"codingToSessionId": sessionID, "_recordedAt": recordedAt,
		},
	}
	for _, event := range events {
		if err := s.appendEvent(sessionDir, event); err != nil {
			log.Printf("[session %d] append first-response timeout event: %v", sessionID, err)
		}
		application.Get().Event.Emit("agent:event", event)
	}
	application.Get().Event.Emit("agent:state", map[string]any{
		"running": false, "processRunning": false,
		"codingToSessionId": sessionID, "error": message,
	})
	log.Printf("[session %d] model first-response timeout after %s; stopped stalled Pi process", sessionID, timeout)
}

// TestModelRequest asks the backend to verify a single model responds through
// the Pi agent runtime without disturbing any running conversation.
type TestModelRequest struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

type TestModelResult struct {
	OK      bool   `json:"ok"`
	Output  string `json:"output,omitempty"`
	Error   string `json:"error,omitempty"`
	Latency int64  `json:"latencyMs,omitempty"`
}

// TestModel spins up an isolated Pi process, runs a trivial prompt, and reports
// whether the model answered. It shares no state with the interactive agent.
func (s *AgentService) TestModel(req TestModelRequest) (TestModelResult, error) {
	log.Printf("[TestModel] start: provider=%q model=%q", req.Provider, req.Model)
	cfg := s.store.Get()
	if req.Provider == "" || req.Model == "" {
		req.Provider, req.Model = cfg.DefaultProvider, cfg.DefaultModel
		log.Printf("[TestModel] empty input, fell back to default: provider=%q model=%q", req.Provider, req.Model)
	}
	if req.Provider == "" || req.Model == "" {
		log.Printf("[TestModel] no provider or model selected")
		return TestModelResult{OK: false, Error: "no provider or model selected"}, nil
	}
	if err := piagent.ValidateProviders(cfg.Providers, req.Provider, req.Model); err != nil {
		log.Printf("[TestModel] ValidateProviders failed: %v", err)
		return TestModelResult{OK: false, Error: err.Error()}, nil
	}
	if err := validateProviderCredentials(cfg.Providers, req.Provider); err != nil {
		log.Printf("[TestModel] provider credentials are unavailable: %v", err)
		return TestModelResult{OK: false, Error: err.Error()}, nil
	}
	selectedModel, found := piagent.FindModel(cfg.Providers, req.Provider, req.Model)
	if !found {
		log.Printf("[TestModel] model not found: %s/%s", req.Provider, req.Model)
		return TestModelResult{OK: false, Error: fmt.Sprintf("model not found: %s/%s", req.Provider, req.Model)}, nil
	}

	// Use a throwaway data dir so the test never touches a real agent session.
	testDir, err := os.MkdirTemp("", "codingto-modeltest-")
	if err != nil {
		return TestModelResult{OK: false, Error: fmt.Sprintf("create temp dir: %v", err)}, nil
	}
	defer os.RemoveAll(testDir)
	if err := piagent.WriteModels(testDir, cfg.Providers); err != nil {
		return TestModelResult{OK: false, Error: fmt.Sprintf("write models.json: %v", err)}, nil
	}
	log.Printf("[TestModel] wrote models.json to %s, starting isolated Pi process", testDir)

	adapter := piagent.NewAdapter()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	start := time.Now()
	if err := adapter.Start(ctx, piagent.StartConfig{
		WorkDir:   testDir,
		Model:     req.Model,
		Provider:  req.Provider,
		ExtraArgs: []string{"--no-builtin-tools"},
		Env:       map[string]string{"PI_CODING_AGENT_DIR": testDir},
	}); err != nil {
		log.Printf("[TestModel] adapter.Start failed after %dms: %v", time.Since(start).Milliseconds(), err)
		return TestModelResult{OK: false, Error: err.Error(), Latency: time.Since(start).Milliseconds()}, nil
	}
	defer adapter.Stop()
	log.Printf("[TestModel] Pi process started after %dms, sending set_model", time.Since(start).Milliseconds())

	if err := adapter.SendCommand(mustJSON(map[string]string{"type": "set_model", "provider": req.Provider, "modelId": req.Model})); err != nil {
		log.Printf("[TestModel] set_model failed: %v", err)
		return TestModelResult{OK: false, Error: err.Error(), Latency: time.Since(start).Milliseconds()}, nil
	}
	if selectedModel.Reasoning {
		if err := adapter.SendCommand(mustJSON(map[string]string{"type": "set_thinking_level", "level": "off"})); err != nil {
			log.Printf("[TestModel] set_thinking_level failed: %v", err)
			return TestModelResult{OK: false, Error: err.Error(), Latency: time.Since(start).Milliseconds()}, nil
		}
	}
	prompt := map[string]any{"type": "prompt", "message": "Reply with the single word OK if you can read this."}
	log.Printf("[TestModel] sending prompt: %q", prompt["message"])
	if err := adapter.SendCommand(mustJSON(prompt)); err != nil {
		log.Printf("[TestModel] send prompt failed: %v", err)
		return TestModelResult{OK: false, Error: err.Error(), Latency: time.Since(start).Milliseconds()}, nil
	}
	log.Printf("[TestModel] prompt sent, waiting for response (timeout 90s)")

	output, err := waitForText(ctx, adapter)
	if err != nil {
		log.Printf("[TestModel] waitForText failed after %dms: %v", time.Since(start).Milliseconds(), err)
		return TestModelResult{OK: false, Error: err.Error(), Latency: time.Since(start).Milliseconds()}, nil
	}
	log.Printf("[TestModel] success after %dms, output=%q", time.Since(start).Milliseconds(), output)
	return TestModelResult{OK: true, Output: output, Latency: time.Since(start).Milliseconds()}, nil
}

// waitForText blocks until the Pi process emits a text delta or the context
// expires, returning the trimmed accumulated text.
func waitForText(ctx context.Context, adapter *piagent.Adapter) (string, error) {
	var buf strings.Builder
	for {
		select {
		case <-ctx.Done():
			log.Printf("[TestModel] waitForText: context done (timeout). buffered=%q", buf.String())
			return buf.String(), fmt.Errorf("model test timed out")
		case evt, ok := <-adapter.Events():
			if !ok {
				msg := buf.String()
				log.Printf("[TestModel] waitForText: events channel closed. buffered=%q", msg)
				if msg == "" {
					if err := adapter.ExitError(); err != nil {
						return "", fmt.Errorf("pi exited: %v", err)
					}
					return "", fmt.Errorf("pi exited before producing output")
				}
				return msg, nil
			}
			var payload struct {
				Type                  string `json:"type"`
				Delta                 string `json:"delta"`
				Text                  string `json:"text"`
				Content               string `json:"content"`
				Command               string `json:"command"`
				Success               *bool  `json:"success"`
				Error                 string `json:"error"`
				AssistantMessageEvent struct {
					Type    string `json:"type"`
					Delta   string `json:"delta"`
					Content string `json:"content"`
					Text    string `json:"text"`
				} `json:"assistantMessageEvent"`
			}
			if err := json.Unmarshal(evt.Raw, &payload); err != nil {
				continue
			}
			// 统一的文本收集：部分模型把回答直接放在 message_start / response 的
			// content、text 字段里，而不以 text_delta 流式增量返回。
			collect := func(text string) {
				if text != "" {
					buf.WriteString(text)
					log.Printf("[TestModel] waitForText: text collected, total len=%d", buf.Len())
				}
			}
			switch payload.Type {
			case "message_update":
				switch payload.AssistantMessageEvent.Type {
				case "text_delta":
					collect(payload.AssistantMessageEvent.Delta)
				case "text_end":
					if buf.Len() == 0 {
						collect(payload.AssistantMessageEvent.Content)
					}
				case "error":
					return buf.String(), errors.New("model stream failed")
				}
			case "text_delta":
				collect(payload.Delta)
			case "message_start", "message_end":
				// reasoning 模型或非流式回答可能在这些事件里直接携带文本。
				if payload.Text != "" {
					collect(payload.Text)
				} else if payload.Content != "" {
					collect(payload.Content)
				} else if payload.AssistantMessageEvent.Content != "" {
					collect(payload.AssistantMessageEvent.Content)
				} else if payload.AssistantMessageEvent.Text != "" {
					collect(payload.AssistantMessageEvent.Text)
				}
			case "agent_end":
				// 本轮对话已结束（模型已回答），不必再等 text_delta 或进程退出。
				log.Printf("[TestModel] waitForText: agent_end received, buffered len=%d", buf.Len())
				if strings.TrimSpace(buf.String()) == "" {
					// 优先返回模型/provider 返回的真实错误（如 404、鉴权失败等）。
					if msg := agentEndErrorMessage(evt.Raw); msg != "" {
						return "", errors.New(msg)
					}
					log.Printf("[TestModel] waitForText: last raw event: %s", string(evt.Raw))
					return "", errors.New("model completed without a text response")
				}
				return buf.String(), nil
			case "response":
				if payload.Success != nil && !*payload.Success {
					if payload.Error == "" {
						payload.Error = payload.Command + " failed"
					}
					return buf.String(), errors.New(payload.Error)
				}
				// 兼容极少数把最终结果放在 response 顶层 text/content 的实现。
				if payload.Text != "" {
					collect(payload.Text)
				} else if payload.Content != "" {
					collect(payload.Content)
				}
			default:
				log.Printf("[TestModel] waitForText: event type=%q", payload.Type)
			}
		}
	}
}

func mustJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}

func (s *AgentService) AbortPrompt(sessionIDs ...int64) error {
	if s.runtimes != nil {
		if len(sessionIDs) == 0 {
			s.mu.Lock()
			runtimes := make([]*AgentService, 0, len(s.runtimes))
			for _, runtime := range s.runtimes {
				runtimes = append(runtimes, runtime)
			}
			s.mu.Unlock()
			if len(runtimes) == 0 {
				return s.abortPromptSingle()
			}
			var result error
			for _, runtime := range runtimes {
				if err := runtime.abortPromptSingle(); err != nil {
					result = errors.Join(result, err)
				}
			}
			return result
		}
		runtime, err := s.runtimeForCommand(sessionIDs[0])
		if err != nil {
			return err
		}
		return runtime.abortPromptSingle()
	}
	return s.abortPromptSingle()
}

func (s *AgentService) abortPromptSingle() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.preparing {
		s.prepareCanceled = true
		return nil
	}
	if !s.adapter.IsRunning() {
		return nil
	}
	// Pi can remain busy after a low-level agent_end event while it retries,
	// compacts, or processes a queued continuation. The UI correctly keeps the
	// stop button visible in those phases, so never discard its abort merely
	// because the execution timer has already been settled by an event.
	raw, _ := json.Marshal(map[string]string{"id": "codingto-abort", "type": "abort"})
	return s.adapter.SendCommand(raw)
}

// agentEndErrorMessage 从 agent_end 事件中提取模型/provider 返回的真实错误。
// Pi 在模型调用失败时会把错误放在 assistant 消息的 errorMessage 字段（或顶层），
// 优先展示它，而不是笼统的 "model completed without a text response"。
func agentEndErrorMessage(raw json.RawMessage) string {
	var p struct {
		ErrorMessage string `json:"errorMessage"`
		Messages     []struct {
			Role         string `json:"role"`
			StopReason   string `json:"stopReason"`
			ErrorMessage string `json:"errorMessage"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return ""
	}
	if p.ErrorMessage != "" {
		return p.ErrorMessage
	}
	for _, m := range p.Messages {
		if m.Role == "assistant" && m.ErrorMessage != "" {
			return m.ErrorMessage
		}
	}
	return ""
}

// StopSession stops and removes exactly one conversation runtime.
func (s *AgentService) StopSession(id int64) error {
	if s.runtimes != nil {
		s.mu.Lock()
		runtime := s.runtimes[id]
		delete(s.runtimes, id)
		s.mu.Unlock()
		if runtime == nil {
			return nil
		}
		return runtime.stopSessionSingle(id)
	}
	return s.stopSessionSingle(id)
}

func (s *AgentService) stopSessionSingle(id int64) error {
	s.mu.Lock()
	if s.activeSessionID != id {
		s.mu.Unlock()
		return nil
	}
	s.pendingRestart = false
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	err := s.adapter.Stop()
	agentID := s.activeAgent
	s.finishExecutionLocked("active")
	s.activeSessionID = 0
	s.activeSessionDir = ""
	s.activeSession = ""
	s.mu.Unlock()
	if s.runtimeRelease != nil {
		s.runtimeRelease(agentID, id)
	}
	application.Get().Event.Emit("agent:state", map[string]any{
		"running": false, "processRunning": false, "codingToSessionId": id,
	})
	return err
}

// Restart stops the running Pi process and respawns it against the same agent
// and session so on-disk changes (e.g. a newly materialized RTK extension) are
// picked up. If the agent is mid-task, the restart is deferred until the current
// turn finishes, to avoid killing an in-flight execution. With no active session
// the stop is enough and the next StartPrompt applies the changes.
func (s *AgentService) Restart() error {
	if s.runtimes != nil {
		s.mu.Lock()
		runtimes := make([]*AgentService, 0, len(s.runtimes))
		for _, runtime := range s.runtimes {
			runtimes = append(runtimes, runtime)
		}
		s.mu.Unlock()
		var result error
		for _, runtime := range runtimes {
			if err := runtime.restartSingle(); err != nil {
				result = errors.Join(result, err)
			}
		}
		return result
	}
	return s.restartSingle()
}

func (s *AgentService) restartSingle() error {
	s.mu.Lock()
	req := s.restartRequestLocked()
	tools := s.activeTools
	busy := !s.execTurnStart.IsZero()
	// If the agent is currently executing a task, defer the respawn until the
	// turn ends. Do not cancel or stop the current Pi process on this path.
	if busy {
		s.pendingRestart = true
		s.pendingReq = req
		s.pendingTools = tools
		s.mu.Unlock()
		application.Get().Event.Emit("agent:restart_deferred", map[string]any{"reason": "busy"})
		return nil
	}
	s.mu.Unlock()
	return s.performRestart(req, tools)
}

// restartRequestLocked captures enough state to recreate the current Pi process
// without injecting a user message. The caller must hold s.mu.
func (s *AgentService) restartRequestLocked() PromptRequest {
	sessionPath := s.activeSession
	if sessionPath == "" && s.activeSessionID > 0 {
		if session, ok, _ := s.store.Store().SessionByID(s.activeSessionID); ok {
			sessionPath = session.SessionPath
		}
	}
	return PromptRequest{
		AgentID:     s.activeAgent,
		Mode:        s.activeMode,
		WorkDir:     s.activeDir,
		SessionID:   s.activeSessionID,
		SessionPath: sessionPath,
		SkillPath:   s.activeSkill,
	}
}

// performRestart stops the current Pi process and recreates it for req. Both an
// immediate restart and a restart deferred until agent_end use this path so the
// frontend always receives the same completion event.
func (s *AgentService) performRestart(req PromptRequest, toolsEnabled bool) error {
	cfg := s.store.Get()
	profile, hasProfile := cfg.Agent(req.AgentID)

	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	stopErr := s.adapter.Stop()
	s.finishExecutionLocked("active")
	s.activeSessionID = 0
	s.activeSessionDir = ""
	s.activeSession = ""

	var restartErr error
	running := false
	switch {
	case stopErr != nil:
		restartErr = stopErr
	case req.SessionPath == "":
		// There is no resumable session yet. Leaving the process stopped is
		// intentional; the next prompt will start it with the latest extensions.
	case !hasProfile:
		restartErr = fmt.Errorf("agent not found: %s", req.AgentID)
	default:
		restartErr = s.startAdapter(req, cfg, profile, toolsEnabled)
		running = restartErr == nil
	}
	s.mu.Unlock()

	// `agent:state.running` represents an active turn in the frontend, not
	// whether the idle Pi subprocess exists. A reload never starts a new turn.
	state := map[string]any{
		"running": false, "processRunning": running, "codingToSessionId": req.SessionID,
	}
	result := map[string]any{"success": restartErr == nil, "processRunning": running}
	if restartErr != nil {
		state["error"] = restartErr.Error()
		result["error"] = restartErr.Error()
	}
	application.Get().Event.Emit("agent:state", state)
	application.Get().Event.Emit("agent:restart_done", result)
	return restartErr
}

func (s *AgentService) Close() error {
	if s.runtimes != nil {
		s.mu.Lock()
		runtimes := make([]*AgentService, 0, len(s.runtimes))
		for _, runtime := range s.runtimes {
			runtimes = append(runtimes, runtime)
		}
		s.runtimes = map[int64]*AgentService{}
		s.mu.Unlock()
		var result error
		for _, runtime := range runtimes {
			if err := runtime.closeSingle(); err != nil {
				result = errors.Join(result, err)
			}
		}
		return result
	}
	return s.closeSingle()
}

func (s *AgentService) closeSingle() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pendingRestart = false
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	err := s.adapter.Stop()
	s.finishExecutionLocked("active")
	return err
}

func (s *AgentService) forwardEvents(adapter *piagent.Adapter, sessionID int64, sessionDir string) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	events := adapter.Events()
	for {
		select {
		case evt, ok := <-events:
			if !ok {
				goto closed
			}
			var payload any
			if err := json.Unmarshal(evt.Raw, &payload); err != nil {
				payload = map[string]any{"type": "raw", "data": string(evt.Raw)}
			}
			if event, ok := payload.(map[string]any); ok {
				recordedAt := time.Now().UnixMilli()
				event["_recordedAt"] = recordedAt
				event["codingToSessionId"] = sessionID
				eventType := stringValue(event["type"])

				s.mu.Lock()
				nodeID := ""
				if s.activeSessionID == sessionID {
					nodeID = s.activeChangeNode
				}
				s.mu.Unlock()
				if nodeID != "" {
					event["changeNodeId"] = nodeID
				}

				var restartReq PromptRequest
				var restartTools bool
				restartAfterEvent := false
				completedNodeID := ""
				s.mu.Lock()
				if event["type"] == "response" && event["command"] == "get_state" {
					if data, ok := event["data"].(map[string]any); ok {
						if path, ok := data["sessionFile"].(string); ok && path != "" {
							if s.activeSessionID == sessionID {
								s.activeSession = path
							}
							if s.pendingRestart && s.pendingReq.SessionID == sessionID {
								s.pendingReq.SessionPath = path
							}
							if sessionID > 0 {
								_ = s.store.Store().UpdateSession(sessionID, map[string]any{"session_path": path})
							}
						}
					}
				}
				if s.activeSessionID == sessionID && s.activeChangeNode != "" {
					willRetry, _ := event["willRetry"].(bool)
					if eventType == "auto_retry_start" || (eventType == "agent_end" && willRetry) {
						s.armFirstResponseWatchdogLocked(sessionID, s.activeChangeNode)
					} else if firstResponseObserved(event) {
						s.disarmFirstResponseWatchdogLocked()
					}
				}
				if eventType == "agent_end" && s.activeSessionID == sessionID {
					willRetry, _ := event["willRetry"].(bool)
					if !willRetry {
						completedNodeID = s.activeChangeNode
						s.activeChangeNode = ""
					}
				}
				// agent_end only marks the end of one low-level run. Pi may
				// continue with an automatic retry, compaction, or queued work.
				// agent_settled is the authoritative end of the session-level
				// operation and therefore the point where a new prompt or
				// deferred restart becomes safe.
				if eventType == "agent_settled" && s.activeSessionID == sessionID {
					s.finishExecutionLocked("active")
					if s.pendingRestart {
						restartReq = s.pendingReq
						if restartReq.SessionPath == "" {
							restartReq.SessionPath = s.activeSession
						}
						restartTools = s.pendingTools
						restartAfterEvent = true
						s.pendingRestart = false
					}
				}
				s.mu.Unlock()
				if completedNodeID != "" {
					status := "completed"
					if agentEndErrorMessage(evt.Raw) != "" {
						status = "error"
					}
					if err := finishChangeNode(sessionDir, completedNodeID, status, recordedAt); err != nil {
						log.Printf("[session %d] finish change node: %v", sessionID, err)
					}
					summary, err := readChangeSummary(sessionDir, completedNodeID)
					if err != nil {
						log.Printf("[session %d] read completed change summary: %v", sessionID, err)
						// Still emit a completion notice for every prompt. The
						// sidebar refresh can resolve the node if only the
						// lightweight summary failed to load.
						summary = ChangeSummary{
							NodeID: completedNodeID, Status: status,
							Files: []FileChangeSummary{},
						}
					}
					event["changeSummary"] = summary
				}
				if err := s.appendEvent(sessionDir, event); err != nil {
					log.Printf("[session %d] append event: %v", sessionID, err)
				}
				if eventType == "tool_execution_update" {
					if subagent := findSubagentEvent(event); subagent != nil {
						subagent["codingToSessionId"] = sessionID
						if nodeID != "" {
							subagent["parentNodeId"] = nodeID
						}
						application.Get().Event.Emit("subagent:event", subagent)
					}
				}
				if documentID, page, preview := documentPreviewRequest(event); preview {
					application.Get().Event.Emit("document:preview", map[string]any{
						"codingToSessionId": sessionID, "documentId": documentID, "page": page,
					})
				}
				if eventType == "agent_settled" && !restartAfterEvent {
					// Streamed usage is per-message and provider-shaped. Session stats
					// are Pi's canonical cumulative token and context-window view.
					_ = adapter.SendCommand(mustJSON(map[string]string{
						"id": "codingto-session-stats", "type": "get_session_stats",
					}))
				}
				application.Get().Event.Emit("agent:event", payload)
				if eventType == "agent_settled" && !restartAfterEvent {
					application.Get().Event.Emit("agent:state", map[string]any{
						"running": false, "processRunning": true,
						"codingToSessionId": sessionID,
					})
				}
				if restartAfterEvent {
					if err := s.performRestart(restartReq, restartTools); err != nil {
						log.Printf("[agent] deferred restart failed: %v", err)
					}
				}
				continue
			}
			application.Get().Event.Emit("agent:event", payload)
		case <-ticker.C:
			s.emitExecProgressFor(sessionID)
		}
	}

closed:
	if adapter.IsRunning() {
		return
	}
	s.mu.Lock()
	interruptedNodeID := ""
	if s.activeSessionID == sessionID {
		interruptedNodeID = s.activeChangeNode
		s.activeChangeNode = ""
	}
	s.finishExecutionLocked("active")
	s.mu.Unlock()
	if interruptedNodeID != "" {
		_ = finishChangeNode(sessionDir, interruptedNodeID, "interrupted", time.Now().UnixMilli())
	}
	state := map[string]any{
		"running": false, "processRunning": false, "codingToSessionId": sessionID,
	}
	if err := adapter.ExitError(); err != nil {
		state["error"] = err.Error()
	}
	application.Get().Event.Emit("agent:state", state)
}

func findSubagentEvent(event map[string]any) map[string]any {
	queue := []any{event}
	for len(queue) > 0 {
		value := queue[0]
		queue = queue[1:]
		switch current := value.(type) {
		case map[string]any:
			if stringValue(current["kind"]) == "subagent_event" {
				result := make(map[string]any, len(current))
				for key, field := range current {
					result[key] = field
				}
				return result
			}
			for _, field := range current {
				queue = append(queue, field)
			}
		case []any:
			queue = append(queue, current...)
		}
		if len(queue) > 256 {
			break
		}
	}
	return nil
}

func (s *AgentService) appendEvent(sessionDir string, event any) error {
	s.eventLogMu.Lock()
	defer s.eventLogMu.Unlock()
	persisted, keep := compactSessionEvent(event)
	if !keep {
		return nil
	}
	return appendSessionEventWithDurability(sessionDir, persisted, sessionEventNeedsSync(persisted))
}

func (s *AgentService) emitExecProgressFor(expectedSessionID int64) {
	s.mu.Lock()
	total := s.execAccumulatedMs
	running := !s.execTurnStart.IsZero()
	if running {
		total += time.Since(s.execTurnStart).Milliseconds()
	}
	sessionID := s.activeSessionID
	s.mu.Unlock()
	if sessionID == 0 || sessionID != expectedSessionID || total == 0 {
		return
	}
	application.Get().Event.Emit("agent:event", map[string]any{
		"type": "exec_progress", "totalMs": total, "running": running,
		"sessionId": sessionID, "codingToSessionId": sessionID,
	})
}

// finishExecutionLocked freezes the current turn duration and persists the
// cumulative value. The caller must hold s.mu.
func (s *AgentService) finishExecutionLocked(status string) {
	s.disarmFirstResponseWatchdogLocked()
	if !s.execTurnStart.IsZero() {
		s.execAccumulatedMs += time.Since(s.execTurnStart).Milliseconds()
		s.execTurnStart = time.Time{}
	}
	if s.activeSessionID > 0 {
		_ = s.store.Store().UpdateSession(s.activeSessionID, map[string]any{
			"status": status, "exec_duration_ms": s.execAccumulatedMs,
		})
	}
}
