package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"codingto/internal/browserworkflow"
	"codingto/internal/extensions"
	"codingto/internal/piagent"
)

const browserManagedSessionIdleTimeoutMS = "900000"

const figmaRoutingPrompt = `Figma integration is enabled. For any figma.com design URL, use a direct tool whose name contains get_figma_data when available; otherwise use the mcp gateway with server "figma" to connect and call get_figma_data. Never use the browser tool to read a Figma design URL.`

func (s *AgentService) startAdapter(req PromptRequest, cfg AppConfig, profile AgentProfile, toolsEnabled bool) error {
	runCtx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	sessionDir := filepath.Join(cfg.SessionDir, fmt.Sprintf("s%d", req.SessionID))
	if err := ensurePrivateDir(sessionDir); err != nil {
		cancel()
		s.cancel = nil
		return fmt.Errorf("create agent session directory: %w", err)
	}
	// 会话级 DCG 开关：仅落盘会话目录内的标记，不动智能体 recommended.dcg
	// 配置。首次发送时前端把 disableDcg 带过来写入标记；运行中切换则由
	// SetSessionDcgDisabled 实时更新同一标记。
	// 注意这里只「写」不「删」：Agent 因扩展重启等场景会重新进入本函数，
	// req.DisableDcg 此时为默认 false，若无条件删除会丢失已持久化的会话级
	// 关闭状态；恢复拦截只允许用户主动调用 SetSessionDcgDisabled(false)。
	if req.DisableDcg {
		if err := writeDcgDisabledMarker(sessionDir, true); err != nil {
			cancel()
			s.cancel = nil
			return fmt.Errorf("write conversation DCG policy marker: %w", err)
		}
	}
	if err := piagent.MaterializeSystemExtensions(profile.DataDir); err != nil {
		cancel()
		s.cancel = nil
		return err
	}
	extra := []string{}
	appendSystemPrompts := []string{}
	agentDataDir := filepath.Clean(profile.DataDir)
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
		appendSystemPrompts = append(appendSystemPrompts, figmaRoutingPrompt)
	}
	// Surface the agent's own isolated Pi data directory in its system prompt.
	// CodingTo launches each agent with PI_CODING_AGENT_DIR pointing at this
	// directory, but the model has no insight into its own environment. Without
	// this hint it guesses the default global agent path (~/.pi/agent) when it
	// introspects extensions or configuration, and reports the wrong agent's
	// data instead of its own.
	appendSystemPrompts = append(appendSystemPrompts, fmt.Sprintf(
		"You are a CodingTo agent. Your isolated Pi data directory is %s (also available as the PI_CODING_AGENT_DIR environment variable). "+
			"Your installed extensions live at %s/extensions. "+
			"When inspecting your own configuration, settings, models, or extensions, always reference %s (or $PI_CODING_AGENT_DIR) and never the default ~/.pi/agent path.",
		agentDataDir, agentDataDir, agentDataDir))
	if len(appendSystemPrompts) > 0 {
		extra = append(extra, "--append-system-prompt", strings.Join(appendSystemPrompts, "\n\n"))
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
	if profile.Recommended["dcg"] {
		if _, err := piagent.MaterializeDCGExtension(profile.DataDir); err != nil {
			return err
		}
	} else {
		_ = piagent.RemoveDCGExtension(profile.DataDir)
	}
	sessionID := ""
	if req.SessionPath == "" {
		sessionID = fmt.Sprintf("codingto-session-%d", req.SessionID)
	}
	agentEnv := agentProcessEnv(cfg, profile)
	agentEnv["CODINGTO_SESSION_DIR"] = sessionDir
	agentEnv["CODINGTO_WORK_DIR"] = req.WorkDir
	// 主 Agent 与子 Agent 的 DCG 扩展都通过该标记文件判断「本次对话是否关闭
	// 命令拦截」，运行中写入即可实时生效，无需重启进程。
	agentEnv["CODINGTO_DCG_DISABLE_MARKER"] = filepath.Join(sessionDir, dcgDisabledMarkerFile)
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
	if profile.Recommended["dcg"] {
		if dcgPath, err := extensions.DCGExecutable(); err == nil {
			agentEnv["CODINGTO_DCG_BIN"] = dcgPath
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
