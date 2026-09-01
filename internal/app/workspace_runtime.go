package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// WorkspaceRuntime describes the process-local workspace selected for a new conversation.
type WorkspaceRuntime struct {
	EnvironmentID string `json:"environmentId,omitempty"`
	Root          string `json:"root"`
}

// DefaultWorkDir returns the non-persisted working directory used on startup.
func DefaultWorkDir() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return filepath.Join(".codingto", "tempwork")
	}
	return filepath.Join(home, ".codingto", "tempwork")
}

func ensureDefaultWorkDir() (string, error) {
	root, err := filepath.Abs(DefaultWorkDir())
	if err != nil {
		return "", err
	}
	root = filepath.Clean(root)
	if err := ensurePrivateDir(root); err != nil {
		return "", err
	}
	return root, nil
}

func configuredStartupWorkspace(cfg AppConfig, fallback string) (string, string) {
	if len(cfg.Environments) == 0 {
		return "", fallback
	}
	first := cfg.Environments[0]
	root := strings.TrimSpace(first.Path)
	if root == "" {
		return "", fallback
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", fallback
	}
	absolute = filepath.Clean(absolute)
	info, err := os.Stat(absolute)
	if err != nil || !info.IsDir() {
		return "", fallback
	}
	return first.ID, absolute
}

// ActivateWorkspace changes only the process-local workspace used by a new,
// unsent conversation. It never persists a default workspace.
func (a *App) ActivateWorkspace(environmentID string) (WorkspaceRuntime, error) {
	a.workspaceActivateMu.Lock()
	defer a.workspaceActivateMu.Unlock()

	environmentID = strings.TrimSpace(environmentID)
	root := a.defaultWorkDir
	if environmentID != "" {
		cfg := a.store.Get()
		environment := cfg.environmentByID(environmentID)
		if environment == nil || strings.TrimSpace(environment.Path) == "" {
			return WorkspaceRuntime{}, errors.New("workspace not found")
		}
		root = strings.TrimSpace(environment.Path)
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return WorkspaceRuntime{}, errors.New("could not resolve the workspace")
	}
	absolute = filepath.Clean(absolute)
	info, err := os.Stat(absolute)
	if err != nil || !info.IsDir() {
		return WorkspaceRuntime{}, errors.New("workspace directory does not exist")
	}

	a.workspaceMu.Lock()
	a.currentEnvironmentID = environmentID
	a.currentWorkDir = absolute
	a.workspaceMu.Unlock()
	if a.gitMonitor != nil {
		a.gitMonitor.Ensure(absolute)
	}
	return WorkspaceRuntime{EnvironmentID: environmentID, Root: absolute}, nil
}

func (a *App) runtimeWorkspace() WorkspaceRuntime {
	a.workspaceMu.RLock()
	defer a.workspaceMu.RUnlock()
	root := a.currentWorkDir
	if root == "" {
		root = a.defaultWorkDir
	}
	return WorkspaceRuntime{EnvironmentID: a.currentEnvironmentID, Root: root}
}
