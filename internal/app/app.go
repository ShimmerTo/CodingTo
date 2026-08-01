package app

import (
	"context"
	"fmt"
	"runtime"
	"sync"

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
	if a.window != nil {
		a.window.Close()
	}
}
