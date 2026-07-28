package app

import (
	"context"
	"path/filepath"
)

// RuntimeModule is the narrow registration seam for optional local services.
// Modules own their lifecycle and can contribute private environment variables
// without adding feature-specific fields or methods to AgentService.
type RuntimeModule interface {
	Start(context.Context) error
	Shutdown() error
	AgentEnvironment(agentID string, sessionID int64) map[string]string
}

type runtimeSessionReleaser interface {
	ReleaseAgentSession(agentID string, sessionID int64)
}

type runtimeAgentDataResolverSetter interface {
	SetAgentDataDirResolver(func(agentID string) (string, bool))
}

// registerRuntimeModule installs a module before the Wails application starts.
// Registration stays out of App's exported Wails method surface.
func (a *App) registerRuntimeModule(module RuntimeModule) {
	if module == nil {
		return
	}
	if setter, ok := module.(runtimeAgentDataResolverSetter); ok {
		setter.SetAgentDataDirResolver(a.resolveAgentDataDir)
	}
	a.moduleMu.Lock()
	a.modules = append(a.modules, module)
	a.moduleMu.Unlock()
}

func (a *App) runtimeEnvironment(agentID string, sessionID int64) map[string]string {
	a.moduleMu.Lock()
	modules := append([]RuntimeModule(nil), a.modules...)
	a.moduleMu.Unlock()
	result := map[string]string{}
	for _, module := range modules {
		for key, value := range module.AgentEnvironment(agentID, sessionID) {
			result[key] = value
		}
	}
	return result
}

func (a *App) releaseRuntimeSession(agentID string, sessionID int64) {
	a.moduleMu.Lock()
	modules := append([]RuntimeModule(nil), a.modules...)
	a.moduleMu.Unlock()
	for _, module := range modules {
		if releaser, ok := module.(runtimeSessionReleaser); ok {
			releaser.ReleaseAgentSession(agentID, sessionID)
		}
	}
}

func (a *App) resolveAgentDataDir(agentID string) (string, bool) {
	cfg := a.store.Get()
	a.store.EnsureAgentDataDirs(&cfg)
	profile, ok := cfg.Agent(agentID)
	if !ok || profile.DataDir == "" {
		return "", false
	}
	if absolute, err := filepath.Abs(profile.DataDir); err == nil {
		return absolute, true
	}
	return profile.DataDir, true
}
