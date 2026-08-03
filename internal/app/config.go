package app

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"codingto/internal/extensions"
	"codingto/internal/piagent"
	"codingto/internal/store"
	"codingto/internal/subagentbridge"
)

// Preferences holds UI preferences persisted in tbl_setting.
type Preferences struct {
	Theme        string `json:"theme"`
	Language     string `json:"language"`
	AccentColor  string `json:"accentColor"`
	ChatLayout   string `json:"chatLayout"`
	ShowIdentity bool   `json:"showIdentity"`
	DiffMode     string `json:"diffMode"`
	FontSize     string `json:"fontSize"`
}

// AppConfig is the in-memory shape exchanged with the frontend. It is assembled
// from the normalized database tables on read and split back into tables on
// save; no single JSON blob is persisted.
type AppConfig struct {
	ConfigVersion       int                `json:"configVersion"`
	Preferences         Preferences        `json:"preferences"`
	Providers           []piagent.Provider `json:"providers"`
	DefaultProvider     string             `json:"defaultProvider"`
	DefaultModel        string             `json:"defaultModel"`
	LastEnvironment     string             `json:"lastEnvironment"`
	SessionDir          string             `json:"sessionDir"`
	Extensions          extensions.Config  `json:"extensions"`
	Agents              []AgentProfile     `json:"agents"`
	ActiveAgentID       string             `json:"activeAgentId"`
	Environments        []Environment      `json:"environments"`
	ActiveEnvID         string             `json:"activeEnvId"`
	SSHConfigs          []SSHConfig        `json:"sshConfigs"`
	UserProfile         UserProfile        `json:"userProfile"`
	SubagentConcurrency int                `json:"subagentConcurrency"`
	// ToolExecutionTimeoutMinutes bounds one tool (bash) execution for every
	// agent, in minutes. Zero means the application default (10); the effective
	// value is clamped to 1..60 (one hour) in Normalize.
	ToolExecutionTimeoutMinutes int `json:"toolExecutionTimeoutMinutes"`
	// SystemNotificationEnabled gates desktop system notifications for plan
	// approval requests and conversation completion (default on).
	SystemNotificationEnabled bool `json:"systemNotificationEnabled"`
}

// UserProfile holds the end-user's personal identity shown in the chat UI: a
// display name and an avatar. Avatar is either a data-URL (uploaded image) or an
// emoji/short string; an empty value falls back to a default user icon.
type UserProfile struct {
	Name   string `json:"name"`
	Avatar string `json:"avatar"`
}

// RemoteGitDir is one remote working directory inside an environment: a remote
// server directory plus a reusable SSH connection profile.
type RemoteGitDir struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	RemotePath  string `json:"remotePath"`
	SSHConfigID string `json:"sshConfigId"`
}

// SSHConfig is a reusable password-authenticated SSH connection profile that
// remote working directories reference.
type SSHConfig struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Address  string `json:"address"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	Remark   string `json:"remark"`
}

// Environment (环境) is one workspace: one local directory, one remote server
// directory, and one global SSH profile. Remotes remains a slice for storage
// compatibility, but Normalize limits it to one entry.
type Environment struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Path        string         `json:"path"`
	Description string         `json:"description"`
	Active      bool           `json:"active"`
	Remotes     []RemoteGitDir `json:"remotes"`
}

// AgentProfile is a named Pi workspace configuration. Every agent is a Pi agent
// distinguished by a stable ID and an isolated data directory.
type AgentProfile struct {
	ID                   string               `json:"id"`
	Name                 string               `json:"name"`
	Description          string               `json:"description"`
	DataDir              string               `json:"dataDir"`
	Avatar               string               `json:"avatar"`
	Builtin              map[string]bool      `json:"builtin"`
	Recommended          map[string]bool      `json:"recommended"`
	SubAgents            []string             `json:"subagents"`
	PiTools              map[string]bool      `json:"piTools"`
	DefaultProvider      string               `json:"defaultProvider"`
	DefaultModel         string               `json:"defaultModel"`
	BrowserProfilePolicy BrowserProfilePolicy `json:"browserProfilePolicy"`
	// ForcedPromptModels lists the "provider/model" keys for which the agent's
	// PROMPT_FORCE.md is appended to every user message.
	ForcedPromptModels map[string]bool `json:"forcedPromptModels"`
}

// BrowserProfilePolicy controls the browser visibility used at each distinct
// stage. Values are "headed" or "headless"; Normalize supplies safe defaults
// for agents created before the policy was introduced.
type BrowserProfilePolicy struct {
	ExistingProfileMode   string `json:"existingProfileMode"`
	InteractiveLoginMode  string `json:"interactiveLoginMode"`
	AuthenticatedTaskMode string `json:"authenticatedTaskMode"`
}

func DefaultBrowserProfilePolicy() BrowserProfilePolicy {
	return BrowserProfilePolicy{
		ExistingProfileMode:   "headless",
		InteractiveLoginMode:  "headed",
		AuthenticatedTaskMode: "headless",
	}
}

func (p *BrowserProfilePolicy) Normalize() {
	defaults := DefaultBrowserProfilePolicy()
	if p.ExistingProfileMode != "headed" && p.ExistingProfileMode != "headless" {
		p.ExistingProfileMode = defaults.ExistingProfileMode
	}
	if p.InteractiveLoginMode != "headed" && p.InteractiveLoginMode != "headless" {
		p.InteractiveLoginMode = defaults.InteractiveLoginMode
	}
	if p.AuthenticatedTaskMode != "headed" && p.AuthenticatedTaskMode != "headless" {
		p.AuthenticatedTaskMode = defaults.AuthenticatedTaskMode
	}
}

// parseForcedPromptModels decodes the stored JSON map of "provider/model" keys.
func parseForcedPromptModels(raw string) map[string]bool {
	if raw == "" {
		return map[string]bool{}
	}
	m := map[string]bool{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return map[string]bool{}
	}
	return m
}

func DefaultAgentProfile() AgentProfile {
	return AgentProfile{
		ID: "default", Name: "Default Agent", Description: "General-purpose coding agent",
		Builtin:              piagent.DefaultBuiltinTools(),
		SubAgents:            []string{},
		PiTools:              defaultPiTools(),
		BrowserProfilePolicy: DefaultBrowserProfilePolicy(),
		ForcedPromptModels:   map[string]bool{},
	}
}

func defaultPiTools() map[string]bool {
	return map[string]bool{"read": true, "bash": true, "edit": true, "write": true}
}

// DefaultAgentDataDir returns CodingTo's managed directory for the built-in
// default agent.
func DefaultAgentDataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codingto", "agents", "default"), nil
}

// DefaultSessionDir returns the application-wide directory shared by every
// agent for Pi session files and CodingTo's append-only event logs.
func DefaultSessionDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".codingto", "sessions")
	}
	return filepath.Join(home, ".codingto", "sessions")
}

func (a *AgentProfile) Normalize(index int) {
	if a.ID == "" {
		a.ID = fmt.Sprintf("agent-%d", index+1)
	}
	if a.Name == "" {
		a.Name = fmt.Sprintf("Agent %d", index+1)
	}
	if a.Builtin == nil {
		a.Builtin = map[string]bool{}
	}
	for name := range piagent.RequiredBuiltinTools() {
		a.Builtin[name] = true
	}
	for _, retired := range []string{"api", "db", "git"} {
		delete(a.Builtin, retired)
	}
	if a.Recommended == nil {
		a.Recommended = map[string]bool{}
	}
	if a.SubAgents == nil {
		a.SubAgents = []string{}
	}
	seen := make(map[string]struct{}, len(a.SubAgents))
	subagents := a.SubAgents[:0]
	for _, id := range a.SubAgents {
		id = strings.TrimSpace(id)
		if id == "" || id == a.ID {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		subagents = append(subagents, id)
	}
	a.SubAgents = subagents
	if a.PiTools == nil {
		a.PiTools = map[string]bool{}
	}
	for key, enabled := range defaultPiTools() {
		if _, configured := a.PiTools[key]; !configured {
			a.PiTools[key] = enabled
		}
	}
	for key := range a.PiTools {
		switch key {
		case "read", "bash", "edit", "write":
		default:
			delete(a.PiTools, key)
		}
	}
	a.BrowserProfilePolicy.Normalize()
}

// ResolveDefaultModel returns the agent's configured default provider/model.
// When the agent has not pinned a default, it falls back to the first model of
// the first enabled provider in the supplied list, so the system never assumes
// a specific vendor (e.g. openai) when no model has been chosen. The returned
// booleans report whether a usable default was found.
func (a AgentProfile) ResolveDefaultModel(providers []piagent.Provider) (provider, model string, ok bool) {
	if a.DefaultProvider != "" && a.DefaultModel != "" {
		return a.DefaultProvider, a.DefaultModel, true
	}
	for _, p := range providers {
		if p.Enabled == false {
			continue
		}
		if len(p.Models) == 0 {
			continue
		}
		return p.Name, p.Models[0].ID, true
	}
	return "", "", false
}

func (c AppConfig) Agent(id string) (AgentProfile, bool) {
	if id == "" {
		id = c.ActiveAgentID
	}
	for _, agent := range c.Agents {
		if agent.ID == id {
			return agent, true
		}
	}
	return AgentProfile{}, false
}

func DefaultConfig() AppConfig {
	return AppConfig{
		ConfigVersion:               5,
		Preferences:                 Preferences{Theme: "system", Language: "zh-CN", AccentColor: "#d9a441", ChatLayout: "left", ShowIdentity: true, DiffMode: "unified", FontSize: "small"},
		Providers:                   piagent.DefaultProviders(),
		SessionDir:                  DefaultSessionDir(),
		Extensions:                  extensions.DefaultConfig(),
		SubagentConcurrency:         subagentbridge.DefaultConcurrency,
		SystemNotificationEnabled:   true,
		ToolExecutionTimeoutMinutes: 10,
	}
}

func (c *AppConfig) Normalize() {
	if c.ConfigVersion < 5 {
		c.ConfigVersion = 5
	}
	if c.Preferences.Theme != "light" && c.Preferences.Theme != "dark" && c.Preferences.Theme != "system" {
		c.Preferences.Theme = "system"
	}
	if c.Preferences.Language != "zh-CN" && c.Preferences.Language != "en-US" {
		c.Preferences.Language = "zh-CN"
	}
	if c.Preferences.FontSize != "small" && c.Preferences.FontSize != "medium" && c.Preferences.FontSize != "large" {
		c.Preferences.FontSize = "small"
	}
	if strings.TrimSpace(c.SessionDir) == "" {
		c.SessionDir = DefaultSessionDir()
	}
	c.SessionDir = filepath.Clean(c.SessionDir)
	for i := range c.Providers {
		c.Providers[i].Normalize()
	}
	c.Extensions.Normalize()
	c.SubagentConcurrency = subagentbridge.NormalizeConcurrency(c.SubagentConcurrency)
	// Tool execution timeout: 0 means the 10 minute default; clamp to 1..60
	// minutes so a corrupted value can never disable or unbounded the watchdog.
	if c.ToolExecutionTimeoutMinutes <= 0 {
		c.ToolExecutionTimeoutMinutes = 10
	}
	if c.ToolExecutionTimeoutMinutes > 60 {
		c.ToolExecutionTimeoutMinutes = 60
	}
	activeFound := false
	for i := range c.Agents {
		c.Agents[i].Normalize(i)
		if strings.TrimSpace(c.Agents[i].DefaultProvider) == "" {
			c.Agents[i].DefaultProvider = c.DefaultProvider
		}
		if strings.TrimSpace(c.Agents[i].DefaultModel) == "" {
			c.Agents[i].DefaultModel = c.DefaultModel
		}
		activeFound = activeFound || c.Agents[i].ID == c.ActiveAgentID
	}
	if len(c.Agents) == 0 {
		c.ActiveAgentID = ""
	} else if !activeFound {
		c.ActiveAgentID = c.Agents[0].ID
	}

	for i := range c.SSHConfigs {
		if c.SSHConfigs[i].Port < 1 || c.SSHConfigs[i].Port > 65535 {
			c.SSHConfigs[i].Port = 22
		}
	}

	// A workspace has exactly one remote slot. Keep the first legacy entry when
	// older data contains several remote directories.
	for i := range c.Environments {
		if len(c.Environments[i].Remotes) == 0 {
			c.Environments[i].Remotes = []RemoteGitDir{{}}
		} else if len(c.Environments[i].Remotes) > 1 {
			c.Environments[i].Remotes = c.Environments[i].Remotes[:1]
		}
	}

	// Environments: ensure exactly one active environment when at least one exists.
	if len(c.Environments) > 0 {
		envActive := false
		for _, env := range c.Environments {
			if env.ID == c.ActiveEnvID {
				envActive = true
				break
			}
		}
		if !envActive {
			c.ActiveEnvID = c.Environments[0].ID
		}
	} else {
		c.ActiveEnvID = ""
	}
	// Keep the legacy lastEnvironment in sync with the active environment path so
	// command sessions and agent runs continue to target the right directory.
	if active := c.environmentByID(c.ActiveEnvID); active != nil {
		c.LastEnvironment = active.Path
	}
}

func (c *AppConfig) environmentByID(id string) *Environment {
	if id == "" {
		return nil
	}
	for i := range c.Environments {
		if c.Environments[i].ID == id {
			return &c.Environments[i]
		}
	}
	return nil
}

// ConfigStore is the in-memory cache + persistence boundary. It composes the
// per-table repositories from the store package and assembles the frontend
// AppConfig shape on demand.
type ConfigStore struct {
	mu sync.RWMutex
	st *store.Store
}

func NewConfigStore() (*ConfigStore, error) {
	base, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(base, ".codingto")
	st, err := store.Open(dir)
	if err != nil {
		return nil, err
	}
	s := &ConfigStore{st: st}
	return s, nil
}

func (s *ConfigStore) Get() AppConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.assemble()
}

func (s *ConfigStore) assemble() AppConfig {
	cfg := DefaultConfig()

	setting, err := s.st.GetSetting()
	if err == nil {
		cfg.Preferences.Theme = setting.Theme
		cfg.Preferences.Language = setting.Language
		if setting.AccentColor != "" {
			cfg.Preferences.AccentColor = setting.AccentColor
		}
		if setting.ChatLayout != "" {
			cfg.Preferences.ChatLayout = setting.ChatLayout
		}
		if setting.DiffMode != "" {
			cfg.Preferences.DiffMode = setting.DiffMode
		}
		if setting.FontSize != "" {
			cfg.Preferences.FontSize = setting.FontSize
		}
		cfg.Preferences.ShowIdentity = setting.ShowIdentity
		cfg.DefaultProvider = setting.DefaultProvider
		cfg.DefaultModel = setting.DefaultModel
		cfg.LastEnvironment = setting.LastEnvironment
		cfg.SessionDir = setting.SessionDir
		if setting.Figma != "" {
			var fg extensions.FigmaConfig
			if json.Unmarshal([]byte(setting.Figma), &fg) == nil {
				cfg.Extensions.Figma = fg
			}
		}
		if setting.GlobalMCP != "" {
			_ = json.Unmarshal([]byte(setting.GlobalMCP), &cfg.Extensions.GlobalMCP)
		}
		if setting.GlobalPlugins != "" {
			_ = json.Unmarshal([]byte(setting.GlobalPlugins), &cfg.Extensions.GlobalPlugins)
		}
		cfg.UserProfile = UserProfile{
			Name:   setting.UserName,
			Avatar: setting.UserAvatar,
		}
		cfg.SubagentConcurrency = setting.SubagentConcurrency
		cfg.SystemNotificationEnabled = setting.SystemNotificationEnabled
		cfg.ToolExecutionTimeoutMinutes = setting.ToolExecutionTimeoutMinutes
	}

	providers, err := s.st.ListProviders()
	if err != nil {
		// 仅在查询出错时回退到默认服务商；查询成功但为空表示用户已删除全部。
		cfg.Providers = piagent.DefaultProviders()
	} else {
		cfg.Providers = providers
	}

	agents, err := s.st.ListAgents()
	if err == nil {
		profiles := make([]AgentProfile, 0, len(agents))
		activeID := ""
		for _, agent := range agents {
			var builtin, recommended, piTools map[string]bool
			var subagents []string
			var browserProfilePolicy BrowserProfilePolicy
			_ = json.Unmarshal([]byte(agent.Builtin), &builtin)
			_ = json.Unmarshal([]byte(agent.Recommended), &recommended)
			_ = json.Unmarshal([]byte(agent.Subagents), &subagents)
			_ = json.Unmarshal([]byte(agent.PiTools), &piTools)
			_ = json.Unmarshal([]byte(agent.BrowserProfilePolicy), &browserProfilePolicy)
			profiles = append(profiles, AgentProfile{
				ID:                   agent.ID,
				Name:                 agent.Name,
				Description:          agent.Description,
				DataDir:              agent.DataDir,
				Avatar:               agent.Avatar,
				Builtin:              builtin,
				Recommended:          recommended,
				SubAgents:            subagents,
				PiTools:              piTools,
				DefaultProvider:      agent.DefaultProvider,
				DefaultModel:         agent.DefaultModel,
				BrowserProfilePolicy: browserProfilePolicy,
				ForcedPromptModels:   parseForcedPromptModels(agent.ForcedPromptModels),
			})
			if agent.Active {
				activeID = agent.ID
			}
		}
		if len(profiles) > 0 {
			cfg.Agents = profiles
			cfg.ActiveAgentID = activeID
			if activeID == "" {
				cfg.ActiveAgentID = profiles[0].ID
			}
		}
	}

	sshConfigs, err := s.st.ListSSHConfigs()
	if err == nil {
		cfgs := make([]SSHConfig, 0, len(sshConfigs))
		for _, item := range sshConfigs {
			cfgs = append(cfgs, SSHConfig{
				ID:       item.ID,
				Name:     item.Name,
				Address:  item.Address,
				Port:     item.Port,
				Username: item.Username,
				Password: item.Password,
				Remark:   item.Remark,
			})
		}
		cfg.SSHConfigs = cfgs
	}

	// GitConfigs were removed; environments reference global SSH profiles directly.

	environments, err := s.st.ListEnvironments()
	if err == nil {
		spaces := make([]Environment, 0, len(environments))
		activeEnvID := ""
		for _, item := range environments {
			remotes := []RemoteGitDir{}
			if item.Remotes != "" {
				_ = json.Unmarshal([]byte(item.Remotes), &remotes)
			}
			spaces = append(spaces, Environment{
				ID:          item.ID,
				Name:        item.Name,
				Path:        item.Path,
				Description: item.Description,
				Active:      item.Active,
				Remotes:     remotes,
			})
			if item.Active {
				activeEnvID = item.ID
			}
		}
		cfg.Environments = spaces
		cfg.ActiveEnvID = activeEnvID
		if activeEnvID == "" && len(spaces) > 0 {
			cfg.ActiveEnvID = spaces[0].ID
		}
	}

	cfg.Normalize()
	return cfg
}

func (s *ConfigStore) Save(cfg AppConfig) error {
	s.EnsureAgentDataDirs(&cfg)
	cfg.Normalize()
	if err := ensurePrivateDir(cfg.SessionDir); err != nil {
		return fmt.Errorf("create session directory: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Validate and materialize per-agent extension state before persisting the
	// matching flags, so a broken agent MCP config cannot leave a half-saved UI
	// state behind.
	if err := s.syncRecommendedExtensions(cfg); err != nil {
		return err
	}

	if err := s.st.ReplaceProviders(cfg.Providers); err != nil {
		return err
	}

	figma, _ := json.Marshal(cfg.Extensions.Figma)
	globalMCP, _ := json.Marshal(cfg.Extensions.GlobalMCP)
	globalPlugins, _ := json.Marshal(cfg.Extensions.GlobalPlugins)
	if err := s.st.SaveSetting(store.Setting{
		Theme:                       cfg.Preferences.Theme,
		Language:                    cfg.Preferences.Language,
		AccentColor:                 cfg.Preferences.AccentColor,
		ChatLayout:                  cfg.Preferences.ChatLayout,
		ShowIdentity:                cfg.Preferences.ShowIdentity,
		DiffMode:                    cfg.Preferences.DiffMode,
		FontSize:                    cfg.Preferences.FontSize,
		DefaultProvider:             cfg.DefaultProvider,
		DefaultModel:                cfg.DefaultModel,
		LastEnvironment:             cfg.LastEnvironment,
		SessionDir:                  cfg.SessionDir,
		Figma:                       string(figma),
		GlobalMCP:                   string(globalMCP),
		GlobalPlugins:               string(globalPlugins),
		UserName:                    cfg.UserProfile.Name,
		UserAvatar:                  cfg.UserProfile.Avatar,
		SubagentConcurrency:         cfg.SubagentConcurrency,
		SystemNotificationEnabled:   cfg.SystemNotificationEnabled,
		ToolExecutionTimeoutMinutes: cfg.ToolExecutionTimeoutMinutes,
	}); err != nil {
		return err
	}

	agents := make([]store.Agent, 0, len(cfg.Agents))
	for _, agent := range cfg.Agents {
		builtin, _ := json.Marshal(agent.Builtin)
		recommended, _ := json.Marshal(agent.Recommended)
		subagents, _ := json.Marshal(agent.SubAgents)
		piTools, _ := json.Marshal(agent.PiTools)
		browserProfilePolicy, _ := json.Marshal(agent.BrowserProfilePolicy)
		forcedPromptModels, _ := json.Marshal(agent.ForcedPromptModels)
		agents = append(agents, store.Agent{
			ID:                   agent.ID,
			Name:                 agent.Name,
			DataDir:              agent.DataDir,
			Description:          agent.Description,
			Avatar:               agent.Avatar,
			Builtin:              string(builtin),
			Recommended:          string(recommended),
			Subagents:            string(subagents),
			PiTools:              string(piTools),
			DefaultProvider:      agent.DefaultProvider,
			DefaultModel:         agent.DefaultModel,
			BrowserProfilePolicy: string(browserProfilePolicy),
			ForcedPromptModels:   string(forcedPromptModels),
			Active:               agent.ID == cfg.ActiveAgentID,
		})
	}
	if err := s.st.SaveAgents(agents); err != nil {
		return err
	}
	sshItems := make([]store.SSHConfig, 0, len(cfg.SSHConfigs))
	for _, item := range cfg.SSHConfigs {
		sshItems = append(sshItems, store.SSHConfig{
			ID:       item.ID,
			Name:     item.Name,
			Address:  item.Address,
			Port:     item.Port,
			Username: item.Username,
			Password: item.Password,
			Remark:   item.Remark,
		})
	}
	if err := s.st.SaveSSHConfigs(sshItems); err != nil {
		return err
	}

	// GitConfigs removed; environments are saved below.

	envItems := make([]store.Environment, 0, len(cfg.Environments))
	for _, item := range cfg.Environments {
		remotes, _ := json.Marshal(item.Remotes)
		envItems = append(envItems, store.Environment{
			ID:          item.ID,
			Name:        item.Name,
			Path:        item.Path,
			Description: item.Description,
			Remotes:     string(remotes),
			Active:      item.ID == cfg.ActiveEnvID,
		})
	}
	if err := s.st.SaveEnvironments(envItems); err != nil {
		return err
	}
	return nil
}

func (s *ConfigStore) Dir() string         { return s.st.Dir() }
func (s *ConfigStore) PiDir() string       { return filepath.Join(s.st.Dir(), "pi") }
func (s *ConfigStore) Store() *store.Store { return s.st }

func (s *ConfigStore) EnsureAgentDataDirs(cfg *AppConfig) {
	for i := range cfg.Agents {
		if cfg.Agents[i].DataDir == "" {
			if cfg.Agents[i].ID == "default" {
				cfg.Agents[i].DataDir = filepath.Join(s.Dir(), "agents", "default")
			} else {
				cfg.Agents[i].DataDir = filepath.Join(s.Dir(), "agents", randomAgentDataDirName(cfg.Agents[i].ID))
			}
		}
		_ = ensurePrivateDir(cfg.Agents[i].DataDir)
		syncModelsJSONIfMissing(cfg.Agents[i].DataDir, cfg, cfg.Agents[i].ID)
	}
}

func randomAgentDataDirName(agentID string) string {
	randomBytes := make([]byte, 16)
	if _, err := rand.Read(randomBytes); err != nil {
		fallback := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", agentID, time.Now().UnixNano())))
		copy(randomBytes, fallback[:16])
	}
	return "agent_" + hex.EncodeToString(randomBytes)
}

// syncRecommendedExtensions materializes per-agent runtime state. RTK copies
// its generated bridge; Pi Figma writes only the agent-local MCP server entry.
func (s *ConfigStore) syncRecommendedExtensions(cfg AppConfig) error {
	rtkSource := ""
	for _, agent := range cfg.Agents {
		if agent.DataDir == "" {
			continue
		}
		if agent.Recommended["rtk"] {
			if rtkSource == "" {
				rtkSource = extensions.EnsureRTKPiExtension()
			}
			if rtkSource == "" {
				continue
			}
			_, _ = piagent.MaterializeRTKExtension(agent.DataDir, rtkSource)
		} else {
			_ = piagent.RemoveRTKExtension(agent.DataDir)
		}
		if err := piagent.SyncFigmaMCPConfig(agent.DataDir, agent.Recommended["figma"]); err != nil {
			return fmt.Errorf("sync Pi Figma for %s: %w", agent.Name, err)
		}
	}
	return nil
}

// syncModelsJSONIfMissing copies models.json from a reference agent (or the
// global Pi default) into a newly created agent directory, so a new agent
// starts with the same model configuration as the one the user was already
// using. It only writes when the target has no models.json yet, preserving any
// later per-agent customization.
func syncModelsJSONIfMissing(targetDir string, cfg *AppConfig, agentID string) {
	target := filepath.Join(targetDir, "models.json")
	if _, err := os.Stat(target); err == nil {
		return
	}
	source := referenceModelsPath(cfg, agentID)
	if source == "" {
		return
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return
	}
	_ = os.WriteFile(target, data, 0o600)
	_ = os.Chmod(target, 0o600)
}

// referenceModelsPath returns the path of an existing models.json to inherit
// from when creating a new agent: first the active agent, then the default
// agent, then any other configured agent, and finally the global
// ~/.pi/agent/models.json if present.
func referenceModelsPath(cfg *AppConfig, agentID string) string {
	order := []string{}
	if cfg.ActiveAgentID != "" && cfg.ActiveAgentID != agentID {
		order = append(order, cfg.ActiveAgentID)
	}
	order = append(order, "default")
	for _, agent := range cfg.Agents {
		if agent.ID != agentID {
			order = append(order, agent.ID)
		}
	}

	seen := make(map[string]bool)
	for _, id := range order {
		if seen[id] {
			continue
		}
		seen[id] = true
		for _, agent := range cfg.Agents {
			if agent.ID == id && agent.DataDir != "" {
				candidate := filepath.Join(agent.DataDir, "models.json")
				if _, err := os.Stat(candidate); err == nil {
					return candidate
				}
			}
		}
	}

	if home, err := os.UserHomeDir(); err == nil {
		global := filepath.Join(home, ".pi", "agent", "models.json")
		if _, err := os.Stat(global); err == nil {
			return global
		}
	}
	return ""
}

// EnsureDefaultAgent creates the initial agent only when the profile list is
// empty. Once other agents exist, "default" is a persisted selection rather
// than a permanently protected profile ID.
func (s *ConfigStore) EnsureDefaultAgent() (bool, error) {
	cfg := s.Get()
	if len(cfg.Agents) > 0 {
		return false, nil
	}
	defaultDir, err := DefaultAgentDataDir()
	if err != nil {
		return false, err
	}

	defaultProfile := DefaultAgentProfile()
	defaultProfile.DataDir = defaultDir
	_ = ensurePrivateDir(defaultProfile.DataDir)
	cfg.Agents = append([]AgentProfile{defaultProfile}, cfg.Agents...)
	if cfg.ActiveAgentID == "" {
		cfg.ActiveAgentID = defaultProfile.ID
	}
	if err := s.Save(cfg); err != nil {
		return false, err
	}
	return true, nil
}

// ensurePrivateDir creates a directory that may contain credentials, prompts,
// or model configuration and repairs permissive modes left by older releases.
// Chmod is harmless on Windows and enforces owner-only access on Unix systems.
func ensurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(dir, 0o700)
}

// DeleteAgent explicitly removes an agent. Normal configuration saves only
// upsert agents, preventing a stale or partial UI snapshot from deleting rows.
func (s *ConfigStore) DeleteAgent(id string) (AppConfig, error) {
	cfg := s.Get()
	if len(cfg.Agents) <= 1 {
		return AppConfig{}, errors.New("at least one agent is required")
	}
	if cfg.ActiveAgentID == id {
		return AppConfig{}, errors.New("the default agent cannot be deleted")
	}
	found := false
	remaining := make([]AgentProfile, 0, len(cfg.Agents)-1)
	for _, agent := range cfg.Agents {
		if agent.ID == id {
			found = true
			continue
		}
		remaining = append(remaining, agent)
	}
	if !found {
		return AppConfig{}, fmt.Errorf("agent not found: %s", id)
	}
	if err := s.st.DeleteAgent(id); err != nil {
		return AppConfig{}, err
	}
	cfg.Agents = remaining
	if cfg.ActiveAgentID == id {
		cfg.ActiveAgentID = remaining[0].ID
	}
	if err := s.Save(cfg); err != nil {
		return AppConfig{}, err
	}
	return s.Get(), nil
}
