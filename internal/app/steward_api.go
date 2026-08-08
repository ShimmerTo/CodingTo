package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"codingto/internal/browserworkflow"
	"codingto/internal/piagent"
	"codingto/internal/steward"
	"codingto/internal/steward/connectors"
	"codingto/internal/store"
)

// ---- channel API types ----

// PublicBotChannel is the channel shape returned to the frontend (never
// contains secrets).
type PublicBotChannel struct {
	ID             int64             `json:"id"`
	Platform       string            `json:"platform"`
	Name           string            `json:"name"`
	Mode           string            `json:"mode"`
	Config         map[string]string `json:"config"`
	Enabled        bool              `json:"enabled"`
	Status         string            `json:"status"`
	LastError      string            `json:"lastError,omitempty"`
	LastSenderID   string            `json:"lastSenderId,omitempty"`
	LastThreadID   string            `json:"lastThreadId,omitempty"`
	LastReceivedAt int64             `json:"lastReceivedAt,omitempty"`
}

// SaveBotChannelRequest carries the channel form; secrets are stored through
// the encrypted secret store and never returned.
type SaveBotChannelRequest struct {
	ID       int64             `json:"id,omitempty"`
	Platform string            `json:"platform"`
	Name     string            `json:"name"`
	Mode     string            `json:"mode"`
	Config   map[string]string `json:"config"`
	Secrets  map[string]string `json:"secrets"`
	Enabled  bool              `json:"enabled"`
}

// StewardProfileView is the persona configuration exposed to the frontend.
// IdleTimeoutMin / ResidentAlways are intentionally gone: the steward is
// always resident (常驻) and never idle-reclaimed.
type StewardProfileView struct {
	AgentID           string `json:"agentId"`
	Name              string `json:"name"` // 管家名称
	Tone              string `json:"tone"`
	Prompt            string `json:"prompt"`
	Provider          string `json:"provider"`
	Model             string `json:"model"`
	ManageScope       string `json:"manageScope"`       // 'all' | 'butler'
	CompactAfterTurns int    `json:"compactAfterTurns"` // 超过该轮数后压缩上下文
	Enabled           bool   `json:"enabled"`
	// ResidentSessionID 是管家常驻会话的会话 ID。前端用它加载管家会话详情
	// （消息历史）直接展示在"消息"页签中，该会话不进入左侧会话列表。
	ResidentSessionID int64 `json:"residentSessionId"`
}

// ---- stewardControl: AppControl implementation ----

// stewardControl adapts App to steward.AppControl so the steward service can
// drive conversations through the existing runtime. It deliberately lives on
// its own type so the Wails method surface stays unchanged for existing names.
type stewardControl struct {
	app *App
}

// stewardResidentThinkingLevel keeps the resident "总管" in high-thinking
// mode. Dispatched worker conversations still use the normal conversation
// defaults so they behave like manually-created CodingTo sessions.
const stewardResidentThinkingLevel = "high"

// ListSessions maps the store sessions to the steward view.
func (c *stewardControl) ListSessions() ([]steward.SessionView, error) {
	items, err := c.app.store.Store().ListSessions()
	if err != nil {
		return nil, err
	}
	// Session rows intentionally persist only settled execution duration. Merge
	// the runtime pool here so the steward sees the current elapsed time and
	// does not report a stale database "running" flag after the runtime settled.
	runtimeStates := c.app.agent.sessionExecutionSnapshots()
	views := make([]steward.SessionView, 0, len(items))
	for _, item := range items {
		status := item.Status
		duration := item.ExecDurationMs
		runningSince := int64(0)
		activity := ""
		lastEventType := ""
		lastEventAt := int64(0)
		live, hasRuntime := runtimeStates[item.ID]
		// Only inspect candidates that the database or live runtime considers
		// active. The reader is bounded to 256 KiB and therefore independent of
		// the total conversation size.
		if status == "running" || (hasRuntime && live.Running) {
			tail := readSessionTailState(item.SessionDir)
			if tail.Found {
				activity = tail.LastActivity
				lastEventType = tail.LastEventType
				lastEventAt = tail.LastEventAt
				runningSince = tail.StartedAt
			}
		}
		if hasRuntime {
			duration = live.ExecDurationMs
			if live.Running {
				status = "running"
				if lastEventType == "agent_settled" {
					// A live runtime after agent_settled means the main turn is
					// intentionally waiting for detached subagents and their follow-up.
					activity = "主回合已结束，等待后台子任务完成"
				}
				if live.StartedAt > 0 {
					runningSince = live.StartedAt
				}
			} else {
				status = "active"
				runningSince = 0
			}
		} else if status == "running" {
			// The directory tail explains where the last turn stopped, while the
			// absence of a runtime proves no work is executing in this process.
			status = "active"
			runningSince = 0
		}
		views = append(views, steward.SessionView{
			ID: item.ID, AgentID: item.AgentID, EnvironmentID: item.EnvironmentID,
			Title: item.Title, Status: status, ExecDurationMs: duration,
			RunningSince: runningSince, LastActivity: activity,
			LastEventType: lastEventType, LastEventAt: lastEventAt,
			CreateTime: item.CreateTime, UpdateTime: item.UpdateTime,
		})
	}
	return views, nil
}

func (c *stewardControl) CreateSession(agentID, envID, title, provider, model string) (steward.SessionView, error) {
	session, err := c.app.CreateSession(CreateSessionRequest{
		AgentID: agentID, EnvironmentID: envID, Title: title, Provider: provider, Model: model,
	})
	if err != nil {
		return steward.SessionView{}, err
	}
	return steward.SessionView{
		ID: session.ID, AgentID: session.AgentID, EnvironmentID: session.EnvironmentID,
		Title: session.Title, Status: session.Status, ExecDurationMs: session.ExecDurationMs,
		CreateTime: session.CreatedAt, UpdateTime: session.UpdatedAt,
	}, nil
}

func (c *stewardControl) StartPrompt(sessionID int64, message string) error {
	item, ok, err := c.app.store.Store().SessionByID(sessionID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("conversation not found: %d", sessionID)
	}
	workDir := c.environmentPath(item.EnvironmentID)
	thinkingLevel := ""
	if c.app.steward != nil && c.app.steward.IsStewardSession(sessionID) {
		thinkingLevel = stewardResidentThinkingLevel
	}
	return c.app.StartPrompt(PromptRequest{
		AgentID: item.AgentID, SessionID: sessionID, Message: message,
		// The resident steward is deliberately high-thinking so it reliably chooses
		// the steward tools. Dispatched worker tasks keep this empty and therefore
		// resolve to the normal model / working-directory defaults.
		SessionPath: item.SessionPath, WorkDir: workDir, Provider: item.Provider, Model: item.Model,
		ThinkingLevel: thinkingLevel,
	})
}

func (c *stewardControl) StopSession(sessionID int64) error {
	return c.app.agent.StopSession(sessionID)
}

func (c *stewardControl) DeleteSession(sessionID int64) error {
	return c.app.DeleteSession(sessionID)
}

func (c *stewardControl) ListEnvironments() ([]steward.EnvironmentView, error) {
	cfg := c.app.store.Get()
	views := make([]steward.EnvironmentView, 0, len(cfg.Environments))
	for _, env := range cfg.Environments {
		views = append(views, steward.EnvironmentView{
			ID: env.ID, Name: env.Name, Path: env.Path, Description: env.Description, Active: env.Active,
		})
	}
	return views, nil
}

func (c *stewardControl) AddEnvironment(env steward.EnvironmentView) ([]steward.EnvironmentView, error) {
	cfg := c.app.store.Get()
	if env.ID == "" {
		env.ID = "env-" + randomAgentDataDirName("")
	}
	if env.Name == "" {
		return nil, errors.New("environment name is required")
	}
	exists := false
	for i := range cfg.Environments {
		if cfg.Environments[i].Name == env.Name || cfg.Environments[i].ID == env.ID {
			cfg.Environments[i].Path = env.Path
			cfg.Environments[i].Description = env.Description
			exists = true
			break
		}
	}
	if !exists {
		cfg.Environments = append(cfg.Environments, Environment{
			ID: env.ID, Name: env.Name, Path: env.Path, Description: env.Description,
		})
	}
	if err := c.app.store.Save(cfg); err != nil {
		return nil, err
	}
	return c.ListEnvironments()
}

func (c *stewardControl) RemoveEnvironment(envID string) error {
	cfg := c.app.store.Get()
	out := make([]Environment, 0, len(cfg.Environments))
	for _, env := range cfg.Environments {
		if env.ID == envID {
			continue
		}
		out = append(out, env)
	}
	cfg.Environments = out
	if cfg.ActiveEnvID == envID {
		cfg.ActiveEnvID = ""
	}
	return c.app.store.Save(cfg)
}

func (c *stewardControl) ListAgents() ([]steward.AgentView, error) {
	cfg := c.app.store.Get()
	views := make([]steward.AgentView, 0, len(cfg.Agents))
	for _, agent := range cfg.Agents {
		views = append(views, steward.AgentView{
			ID: agent.ID, Name: agent.Name, Description: agent.Description,
			DefaultProvider: agent.DefaultProvider, DefaultModel: agent.DefaultModel,
		})
	}
	return views, nil
}

func (c *stewardControl) ResolveAgent(key string) (steward.AgentView, error) {
	cfg := c.app.store.Get()
	key = strings.TrimSpace(key)
	if key != "" {
		for _, agent := range cfg.Agents {
			if agent.ID == key || strings.EqualFold(agent.Name, key) {
				return steward.AgentView{
					ID: agent.ID, Name: agent.Name, Description: agent.Description,
					DefaultProvider: agent.DefaultProvider, DefaultModel: agent.DefaultModel,
				}, nil
			}
		}
		return steward.AgentView{}, fmt.Errorf("agent not found: %s", key)
	}
	for _, agent := range cfg.Agents {
		if agent.ID == cfg.ActiveAgentID {
			return steward.AgentView{
				ID: agent.ID, Name: agent.Name, Description: agent.Description,
				DefaultProvider: agent.DefaultProvider, DefaultModel: agent.DefaultModel,
			}, nil
		}
	}
	if len(cfg.Agents) == 0 {
		return steward.AgentView{}, errors.New("no agents configured")
	}
	agent := cfg.Agents[0]
	return steward.AgentView{
		ID: agent.ID, Name: agent.Name, Description: agent.Description,
		DefaultProvider: agent.DefaultProvider, DefaultModel: agent.DefaultModel,
	}, nil
}

// AgentDataDir resolves the agent's data directory (extensions/ lives there).
func (c *stewardControl) AgentDataDir(agentID string) (string, bool) {
	return c.app.resolveAgentDataDir(agentID)
}

func (c *stewardControl) AckExtensionUI(sessionID int64, requestID string) error {
	return c.app.agent.StartPrompt(PromptRequest{
		SessionID: sessionID,
		Command:   map[string]any{"type": "extension_ui_ack", "id": requestID},
	})
}

func (c *stewardControl) SendExtensionUIResponse(sessionID int64, requestID string, confirmed *bool, value any) error {
	command := map[string]any{"type": "extension_ui_response", "id": requestID}
	if confirmed != nil {
		command["confirmed"] = *confirmed
	}
	if value != nil {
		command["value"] = value
	}
	return c.app.agent.StartPrompt(PromptRequest{SessionID: sessionID, Command: command})
}

func (c *stewardControl) AckSubagentUI(sessionID int64, runID, requestID string) error {
	return c.app.AckSubagentUI(sessionID, runID, requestID)
}

func (c *stewardControl) PinSession(sessionID int64) error {
	c.app.agent.SetPinnedSession(sessionID, true)
	return nil
}

// CompactSession triggers a Pi compaction on the session's runtime so the
// resident steward history stays bounded. The command is queued through the
// same StartPrompt command path, so it applies to the session's own runtime.
func (c *stewardControl) CompactSession(sessionID int64, instructions string) error {
	command := map[string]any{"type": "compact"}
	if instructions != "" {
		command["customInstructions"] = instructions
	}
	return c.app.agent.StartPrompt(PromptRequest{SessionID: sessionID, Command: command})
}

func (c *stewardControl) RespondSubagentUI(sessionID int64, runID string, answer steward.SubagentUIAnswer) error {
	return c.app.RespondSubagentUI(sessionID, runID, SubagentUIResponse{
		ID: answer.ID, Value: answer.Value, Confirmed: answer.Confirmed, Cancelled: answer.Cancelled,
	})
}

func (c *stewardControl) SaveBrowserProfile(key, targetURL string) (string, error) {
	profile, err := c.app.SaveBrowserProfile(SaveBrowserProfileRequest{
		SaveRequest: browserworkflow.SaveRequest{
			Key: key, TargetURL: targetURL, LoginURL: targetURL, AuthMode: "manual",
		},
	})
	if err != nil {
		return "", err
	}
	return profile.ID, nil
}

func (c *stewardControl) environmentPath(envID string) string {
	if envID == "" {
		return ""
	}
	cfg := c.app.store.Get()
	for _, env := range cfg.Environments {
		if env.ID == envID {
			return env.Path
		}
	}
	return ""
}

// ---- Wails API ----

func (c *stewardControl) service() *steward.Service { return c.app.steward }

func (a *App) ListBotChannels() ([]PublicBotChannel, error) {
	if a.steward == nil {
		return nil, nil
	}
	items, err := a.store.Store().ListBotChannels()
	if err != nil {
		return nil, err
	}
	result := make([]PublicBotChannel, 0, len(items))
	for _, item := range items {
		config, _ := parseChannelConfigMap(item.ConfigJSON)
		status, lastError := item.Status, item.LastError
		if liveStatus, liveError, active := a.steward.ChannelStatus(item.ID); active {
			status, lastError = liveStatus, liveError
		}
		result = append(result, PublicBotChannel{
			ID: item.ID, Platform: item.Platform, Name: item.Name, Mode: item.Mode,
			Config: config, Enabled: item.Enabled, Status: status, LastError: lastError,
			LastSenderID: item.LastSenderID, LastThreadID: item.LastThreadID, LastReceivedAt: item.LastReceivedAt,
		})
	}
	return result, nil
}

func (a *App) SaveBotChannel(req SaveBotChannelRequest) (PublicBotChannel, error) {
	if a.steward == nil {
		return PublicBotChannel{}, errors.New("steward service not available")
	}
	if strings.TrimSpace(req.Name) == "" {
		return PublicBotChannel{}, errors.New("channel name is required")
	}
	platform := strings.TrimSpace(req.Platform)
	if !connectors.Supported(platform) {
		return PublicBotChannel{}, errors.New("unsupported platform: " + platform)
	}
	mode := req.Mode
	if mode == "" {
		switch platform {
		case "wecom":
			mode = "callback"
		default:
			mode = "long"
		}
	}
	configJSON, _ := json.Marshal(req.Config)
	now := time.Now().UnixMilli()
	var channel store.BotChannel
	if req.ID > 0 {
		existing, ok, err := a.store.Store().BotChannelByID(req.ID)
		if err != nil {
			return PublicBotChannel{}, err
		}
		if !ok {
			return PublicBotChannel{}, errors.New("channel not found")
		}
		existing.Platform = platform
		existing.Name = req.Name
		existing.Mode = mode
		existing.ConfigJSON = string(configJSON)
		existing.Enabled = req.Enabled
		existing.UpdatedAt = now
		if err := a.store.Store().UpdateBotChannel(existing.ID, map[string]any{
			"platform": platform, "name": req.Name, "mode": mode, "config_json": string(configJSON),
			"enabled": req.Enabled,
		}); err != nil {
			return PublicBotChannel{}, err
		}
		channel = existing
	} else {
		created, err := a.store.Store().CreateBotChannel(store.BotChannel{
			Platform: platform, Name: req.Name, Mode: mode,
			ConfigJSON: string(configJSON), Enabled: req.Enabled,
		})
		if err != nil {
			return PublicBotChannel{}, err
		}
		channel = created
	}
	// Persist secrets (encrypted) and loopback channel id for injection.
	secrets := req.Secrets
	if platform == "loopback" {
		config := map[string]string{}
		_ = json.Unmarshal([]byte(channel.ConfigJSON), &config)
		config["channelId"] = strconv.FormatInt(channel.ID, 10)
		raw, _ := json.Marshal(config)
		channel.ConfigJSON = string(raw)
		_ = a.store.Store().UpdateBotChannel(channel.ID, map[string]any{"config_json": string(raw)})
	}
	if err := a.stewardSaveSecrets(channel.ID, secrets); err != nil {
		return PublicBotChannel{}, err
	}
	// Apply connection state.
	if req.Enabled {
		if err := a.steward.StartChannel(channel.ID); err != nil {
			_ = a.store.Store().UpdateBotChannel(channel.ID, map[string]any{"status": "error", "last_error": err.Error()})
		}
	} else {
		a.steward.StopChannel(channel.ID)
	}
	return a.channelView(channel.ID)
}

func (a *App) DeleteBotChannel(id int64) error {
	if a.steward == nil {
		return nil
	}
	a.steward.StopChannel(id)
	if err := a.store.Store().DeleteBotChannel(id); err != nil {
		return err
	}
	return a.stewardDeleteSecrets(id)
}

func (a *App) ToggleBotChannel(id int64, enabled bool) error {
	if a.steward == nil {
		return nil
	}
	if enabled {
		if err := a.steward.StartChannel(id); err != nil {
			return err
		}
		return a.store.Store().UpdateBotChannel(id, map[string]any{"enabled": true})
	}
	a.steward.StopChannel(id)
	return a.store.Store().UpdateBotChannel(id, map[string]any{"enabled": false})
}

// TestBotChannel sends a test message through the channel.
func (a *App) TestBotChannel(id int64) error {
	if a.steward == nil {
		return errors.New("steward service not available")
	}
	channel, ok, err := a.store.Store().BotChannelByID(id)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("channel not found")
	}
	if channel.Platform == "loopback" {
		return a.steward.SendToChannel(id, steward.OutboundMessage{Text: "✅ 测试消息：本地渠道连通正常。"})
	}
	// Give a clear reason when the channel is not actually connected, so the
	// frontend can surface it instead of a generic "not connected" error.
	status := channel.Status
	if liveStatus, _, active := a.steward.ChannelStatus(id); active {
		status = liveStatus
	}
	if status != "connected" && status != "connecting" {
		return fmt.Errorf("渠道未连接（当前状态：%s），请先启用渠道后再测试发送", channelStatusLabel(status))
	}
	// An empty destination means "the last sender of this channel". The
	// service resolves the platform-specific target from persisted inbound data,
	// including after a restart.
	return a.steward.SendToChannel(id, steward.OutboundMessage{
		Text: "✅ 测试消息：CodingTo 管家渠道连接正常。",
	})
}

// InjectBotMessage delivers a simulated inbound message to a loopback channel.
func (a *App) InjectBotMessage(channelID int64, text string) error {
	if a.steward == nil {
		return errors.New("steward service not available")
	}
	return connectors.InjectLoopback(channelID, steward.InboundMessage{
		ChannelID: channelID, Platform: steward.PlatformLoopback,
		SenderID: "loopback-user", SenderName: "测试用户", ThreadID: "local", Text: text,
	})
}

func (a *App) GetStewardProfile() (StewardProfileView, error) {
	if a.steward == nil {
		return StewardProfileView{}, nil
	}
	profile := a.steward.Profile()
	return StewardProfileView{
		AgentID: profile.AgentID, Name: profile.Name, Tone: profile.Tone,
		Prompt: profile.Prompt, Provider: profile.Provider, Model: profile.Model,
		ManageScope: profile.ManageScope, CompactAfterTurns: profile.CompactAfterTurns,
		Enabled: profile.Enabled, ResidentSessionID: profile.ResidentSessionID,
	}, nil
}

func (a *App) SaveStewardProfile(req StewardProfileView) (StewardProfileView, error) {
	if a.steward == nil {
		return StewardProfileView{}, errors.New("steward service not available")
	}
	// 默认持续常驻：IdleTimeoutMin/ResidentAlways 已移除，store 层强制常驻。
	profile := store.StewardProfile{
		AgentID: req.AgentID, Name: req.Name, Tone: req.Tone, Prompt: req.Prompt,
		Provider: req.Provider, Model: req.Model, ManageScope: req.ManageScope,
		CompactAfterTurns: req.CompactAfterTurns, Enabled: req.Enabled,
	}
	if err := a.steward.SetProfile(profile); err != nil {
		return StewardProfileView{}, err
	}
	// The selected agent must have the steward built-in tool enabled and
	// materialized, otherwise the resident Pi cannot reach this service.
	agentID := profile.AgentID
	if agentID == "" {
		if agent, err := a.steward.ResolveAgentView(); err == nil {
			agentID = agent.ID
		}
	}
	if agentID != "" {
		_ = a.ensureStewardToolForAgent(agentID)
	}
	// Keep the persistent resident conversation aligned with persona runtime
	// settings. Otherwise changing the selected agent/model only updates the
	// profile row while subsequent prompts continue using the old session data.
	saved := a.steward.Profile()
	if saved.ResidentSessionID > 0 && agentID != "" {
		provider, model := saved.Provider, saved.Model
		cfg := a.store.Get()
		if agent, ok := cfg.Agent(agentID); ok {
			if strings.TrimSpace(provider) == "" {
				provider = agent.DefaultProvider
			}
			if strings.TrimSpace(model) == "" {
				model = agent.DefaultModel
			}
		}
		if provider == "" || model == "" {
			provider, model = cfg.DefaultProvider, cfg.DefaultModel
		}
		if err := a.store.Store().UpdateSession(saved.ResidentSessionID, map[string]any{
			"agent_id": agentID, "provider": provider, "model": model,
		}); err != nil {
			return StewardProfileView{}, err
		}
	}
	return a.GetStewardProfile()
}

// ensureStewardToolForAgent enables the steward built-in tool for the agent and
// materializes it into its data directory so the extension is available.
func (a *App) ensureStewardToolForAgent(agentID string) error {
	cfg := a.store.Get()
	for i := range cfg.Agents {
		if cfg.Agents[i].ID != agentID {
			continue
		}
		if cfg.Agents[i].Builtin == nil {
			cfg.Agents[i].Builtin = map[string]bool{}
		}
		if cfg.Agents[i].Builtin["steward"] {
			return nil
		}
		cfg.Agents[i].Builtin["steward"] = true
		if err := piagent.MaterializeBuiltinTools(cfg.Agents[i].DataDir, cfg.Agents[i].Builtin); err != nil {
			return err
		}
		return a.store.Save(cfg)
	}
	return fmt.Errorf("agent not found: %s", agentID)
}

func (a *App) ListBotTasks() ([]store.BotTask, error) {
	if a.steward == nil {
		return nil, nil
	}
	return a.steward.Tasks(), nil
}

func (a *App) ListStewardPermissions() ([]steward.PublicPermissionView, error) {
	if a.steward == nil {
		return nil, nil
	}
	return a.steward.ListPendingPermissions(), nil
}

// RespondStewardPermission answers a pending permission request from the
// desktop UI (equivalent to the bot replying).
func (a *App) RespondStewardPermission(requestID, answer string) error {
	if a.steward == nil {
		return errors.New("steward service not available")
	}
	_, err := a.steward.AnswerPermission(requestID, answer)
	return err
}

// StewardStopSession ends a bot-managed conversation from the steward page.
func (a *App) StewardStopSession(sessionID int64) error {
	if a.steward == nil {
		return nil
	}
	if err := a.agent.StopSession(sessionID); err != nil {
		return err
	}
	a.steward.FinishTask(sessionID, "aborted", "⏹ 对话已从管家页结束。")
	return nil
}

// StewardDeleteSession deletes a conversation (and its bot task) from the
// steward page.
func (a *App) StewardDeleteSession(sessionID int64) error {
	if a.steward == nil {
		return nil
	}
	if err := a.DeleteSession(sessionID); err != nil {
		return err
	}
	a.steward.DeleteTaskBySession(sessionID)
	return nil
}

// ---- helpers ----

func (a *App) channelView(id int64) (PublicBotChannel, error) {
	item, ok, err := a.store.Store().BotChannelByID(id)
	if err != nil {
		return PublicBotChannel{}, err
	}
	if !ok {
		return PublicBotChannel{}, errors.New("channel not found")
	}
	config, _ := parseChannelConfigMap(item.ConfigJSON)
	status, lastError := item.Status, item.LastError
	if liveStatus, liveError, active := a.steward.ChannelStatus(item.ID); active {
		status, lastError = liveStatus, liveError
	}
	return PublicBotChannel{
		ID: item.ID, Platform: item.Platform, Name: item.Name, Mode: item.Mode,
		Config: config, Enabled: item.Enabled, Status: status, LastError: lastError,
		LastSenderID: item.LastSenderID, LastThreadID: item.LastThreadID, LastReceivedAt: item.LastReceivedAt,
	}, nil
}

func (a *App) stewardSaveSecrets(channelID int64, secrets map[string]string) error {
	return a.stewardSecrets.Merge(channelID, secrets)
}

func (a *App) stewardDeleteSecrets(channelID int64) error {
	return a.stewardSecrets.Delete(channelID)
}

// channelStatusLabel maps the persisted channel status to a Chinese label
// used in user-facing error messages.
func channelStatusLabel(status string) string {
	switch status {
	case "connected":
		return "已连接"
	case "connecting":
		return "连接中"
	case "error":
		return "异常"
	default:
		return "未连接"
	}
}

func parseChannelConfigMap(raw string) (map[string]string, error) {
	if strings.TrimSpace(raw) == "" {
		return map[string]string{}, nil
	}
	var config map[string]string
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		return nil, err
	}
	return config, nil
}
