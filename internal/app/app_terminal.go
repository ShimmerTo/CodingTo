package app

import (
	"errors"
	"fmt"
	"strings"

	"codingto/internal/applog"
	terminaldomain "codingto/internal/terminal"
)

// TerminalProfile describes one frontend-selectable local or workspace SSH shell.
type TerminalProfile struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
}

// TerminalWorkspace is the shared terminal group resolved for a conversation.
type TerminalWorkspace struct {
	WorkspaceKey string                           `json:"workspaceKey"`
	Root         string                           `json:"root"`
	Profiles     []TerminalProfile                `json:"profiles"`
	Terminals    []terminaldomain.SessionSnapshot `json:"terminals"`
}

// CreateSessionTerminalRequest starts a terminal from a trusted backend profile.
type CreateSessionTerminalRequest struct {
	SessionID int64  `json:"sessionId"`
	ProfileID string `json:"profileId"`
	Columns   int    `json:"columns"`
	Rows      int    `json:"rows"`
}

// TerminalInputRequest sends one bounded input chunk to a terminal.
type TerminalInputRequest struct {
	SessionID  int64  `json:"sessionId"`
	TerminalID string `json:"terminalId"`
	Data       string `json:"data"`
}

// TerminalResizeRequest resizes one terminal in its owning workspace.
type TerminalResizeRequest struct {
	SessionID  int64  `json:"sessionId"`
	TerminalID string `json:"terminalId"`
	Columns    int    `json:"columns"`
	Rows       int    `json:"rows"`
}

// TerminalCloseRequest closes exactly one terminal in its owning workspace.
type TerminalCloseRequest struct {
	SessionID  int64  `json:"sessionId"`
	TerminalID string `json:"terminalId"`
}

type terminalSessionContext struct {
	root        string
	environment *Environment
	config      AppConfig
}

// GetSessionTerminalWorkspace returns profiles and existing terminals shared by
// every conversation that resolves to the same local directory.
func (a *App) GetSessionTerminalWorkspace(sessionID int64) (TerminalWorkspace, error) {
	context, err := a.terminalSessionContext(sessionID)
	if err != nil {
		return TerminalWorkspace{}, a.terminalUserError(err)
	}
	key, sessions, err := a.terminal.Snapshot(context.root)
	if err != nil {
		applog.Errorf("snapshot terminal workspace for session %d: %v", sessionID, err)
		return TerminalWorkspace{}, a.localizedTerminalError("无法读取终端工作区", "Could not load the terminal workspace")
	}
	return TerminalWorkspace{
		WorkspaceKey: key,
		Root:         context.root,
		Profiles:     terminalProfiles(context.config, context.environment),
		Terminals:    sessions,
	}, nil
}

// CreateSessionTerminal starts a selected local or SSH terminal profile.
func (a *App) CreateSessionTerminal(req CreateSessionTerminalRequest) (terminaldomain.SessionSnapshot, error) {
	if len(strings.TrimSpace(req.ProfileID)) > 256 {
		return terminaldomain.SessionSnapshot{}, a.localizedTerminalError("终端参数无效", "Invalid terminal request")
	}
	context, err := a.terminalSessionContext(req.SessionID)
	if err != nil {
		return terminaldomain.SessionSnapshot{}, a.terminalUserError(err)
	}
	spec, ok := resolveTerminalCreateSpec(context.config, context.environment, req.ProfileID, req.Columns, req.Rows)
	if !ok {
		return terminaldomain.SessionSnapshot{}, a.localizedTerminalError("所选终端在当前工作区不可用", "The selected terminal is not available in this workspace")
	}
	snapshot, err := a.terminal.Create(context.root, spec)
	if err != nil {
		applog.Errorf("create %s terminal for session %d: %v", spec.Kind, req.SessionID, err)
		return terminaldomain.SessionSnapshot{}, a.localizedTerminalError("无法启动终端，请检查 Shell 或 SSH 配置", "Could not start the terminal; check the shell or SSH configuration")
	}
	return snapshot, nil
}

// WriteSessionTerminal sends interactive input without logging its content.
func (a *App) WriteSessionTerminal(req TerminalInputRequest) error {
	if strings.TrimSpace(req.TerminalID) == "" || len(req.TerminalID) > 128 || len(req.Data) > 256*1024 {
		return a.localizedTerminalError("终端输入参数无效", "Invalid terminal input")
	}
	context, err := a.terminalSessionContext(req.SessionID)
	if err != nil {
		return a.terminalUserError(err)
	}
	if err := a.terminal.Write(context.root, req.TerminalID, req.Data); err != nil {
		// A queued xterm input can race with a workspace switch or terminal close.
		// It is an expected stale request, not a runtime failure worth a full
		// Wails call stack in the application log.
		if !errors.Is(err, terminaldomain.ErrTerminalNotFound) {
			applog.Errorf("write terminal %s for session %d: %v", req.TerminalID, req.SessionID, err)
		}
		return a.localizedTerminalError("无法写入终端", "Could not write to the terminal")
	}
	return nil
}

// ResizeSessionTerminal updates a terminal's PTY dimensions.
func (a *App) ResizeSessionTerminal(req TerminalResizeRequest) error {
	if strings.TrimSpace(req.TerminalID) == "" || len(req.TerminalID) > 128 {
		return a.localizedTerminalError("终端尺寸参数无效", "Invalid terminal size")
	}
	context, err := a.terminalSessionContext(req.SessionID)
	if err != nil {
		return a.terminalUserError(err)
	}
	if err := a.terminal.Resize(context.root, req.TerminalID, req.Columns, req.Rows); err != nil {
		if !errors.Is(err, terminaldomain.ErrTerminalNotFound) {
			applog.Errorf("resize terminal %s for session %d: %v", req.TerminalID, req.SessionID, err)
		}
		return a.localizedTerminalError("无法调整终端大小", "Could not resize the terminal")
	}
	return nil
}

// CloseSessionTerminal stops and removes one explicitly identified terminal.
func (a *App) CloseSessionTerminal(req TerminalCloseRequest) error {
	if strings.TrimSpace(req.TerminalID) == "" || len(req.TerminalID) > 128 {
		return a.localizedTerminalError("终端关闭参数无效", "Invalid terminal close request")
	}
	context, err := a.terminalSessionContext(req.SessionID)
	if err != nil {
		return a.terminalUserError(err)
	}
	if err := a.terminal.CloseTerminal(context.root, req.TerminalID); err != nil {
		if !errors.Is(err, terminaldomain.ErrTerminalNotFound) {
			applog.Errorf("close terminal %s for session %d: %v", req.TerminalID, req.SessionID, err)
		}
		return a.localizedTerminalError("无法关闭终端", "Could not close the terminal")
	}
	return nil
}

// terminalSessionContext resolves the working directory and environment for a
// terminal workspace. An existing conversation uses its bound workspace; a
// brand-new conversation with no session yet resolves the app's currently
// active workspace, because terminals are a working-directory concern rather
// than a session concern. The active-workspace fallback is limited to sessions
// that do not exist (id < 1) so a missing session never attaches a terminal to
// the wrong workspace.
func (a *App) terminalSessionContext(sessionID int64) (terminalSessionContext, error) {
	cfg := a.store.Get()
	environment := (*Environment)(nil)
	root := ""
	if sessionID >= 1 {
		item, ok, err := a.store.Store().SessionByID(sessionID)
		if err != nil {
			return terminalSessionContext{}, fmt.Errorf("read conversation: %w", err)
		}
		if ok {
			environment = cfg.environmentByID(item.EnvironmentID)
			if environment != nil {
				root = strings.TrimSpace(environment.Path)
			}
			if root == "" {
				if changes, changeErr := readSessionChanges(item.SessionDir); changeErr == nil {
					root = strings.TrimSpace(changes.Root)
				}
			}
		}
	} else {
		// New conversation: use the process-local selection. It starts at
		// ~/.codingto/tempwork and changes without persisting a default workspace.
		runtimeWorkspace := a.runtimeWorkspace()
		environment = cfg.environmentByID(runtimeWorkspace.EnvironmentID)
		root = strings.TrimSpace(runtimeWorkspace.Root)
	}
	if root == "" {
		return terminalSessionContext{}, a.localizedTerminalError("无法定位当前工作区", "Could not resolve the current workspace")
	}
	return terminalSessionContext{root: root, environment: environment, config: cfg}, nil
}

func terminalProfiles(cfg AppConfig, environment *Environment) []TerminalProfile {
	localProfiles := terminaldomain.LocalProfiles()
	profiles := make([]TerminalProfile, 0, len(localProfiles)+4)
	for _, profile := range localProfiles {
		profiles = append(profiles, TerminalProfile{ID: profile.ID, Kind: "local", Title: profile.Title})
	}
	if environment == nil {
		return profiles
	}
	for _, remote := range environment.Remotes {
		sshConfig, ok := findTerminalSSHConfig(cfg.SSHConfigs, remote.SSHConfigID)
		if !ok || strings.TrimSpace(remote.ID) == "" {
			continue
		}
		title := strings.TrimSpace(remote.Name)
		if title == "" {
			title = strings.TrimSpace(sshConfig.Name)
		}
		if title == "" {
			title = sshConfig.Username + "@" + sshConfig.Address
		}
		detail := fmt.Sprintf("%s@%s:%d", sshConfig.Username, sshConfig.Address, sshConfig.Port)
		if strings.TrimSpace(remote.RemotePath) != "" {
			detail += " · " + strings.TrimSpace(remote.RemotePath)
		}
		profiles = append(profiles, TerminalProfile{ID: "ssh:" + remote.ID, Kind: "ssh", Title: title, Detail: detail})
	}
	return profiles
}

func resolveTerminalCreateSpec(cfg AppConfig, environment *Environment, profileID string, columns, rows int) (terminaldomain.CreateSpec, bool) {
	profileID = strings.TrimSpace(profileID)
	if local, ok := terminaldomain.ResolveLocalProfile(profileID); ok {
		return terminaldomain.CreateSpec{
			ProfileID: profileID, Kind: "local", Title: local.Title, Columns: columns, Rows: rows,
			Local: &terminaldomain.LocalSpec{Executable: local.Executable, Args: local.Args, Env: local.Env},
		}, true
	}
	if environment == nil || !strings.HasPrefix(profileID, "ssh:") {
		return terminaldomain.CreateSpec{}, false
	}
	remoteID := strings.TrimPrefix(profileID, "ssh:")
	for _, remote := range environment.Remotes {
		if remote.ID != remoteID {
			continue
		}
		sshConfig, ok := findTerminalSSHConfig(cfg.SSHConfigs, remote.SSHConfigID)
		if !ok {
			return terminaldomain.CreateSpec{}, false
		}
		title := strings.TrimSpace(remote.Name)
		if title == "" {
			title = strings.TrimSpace(sshConfig.Name)
		}
		return terminaldomain.CreateSpec{
			ProfileID: profileID, Kind: "ssh", Title: title, Columns: columns, Rows: rows,
			SSH: &terminaldomain.SSHSpec{
				Address: sshConfig.Address, Port: sshConfig.Port, Username: sshConfig.Username,
				AuthMode: sshConfig.AuthMode, Password: sshConfig.Password, PrivateKey: sshConfig.PrivateKey,
				PrivateKeyPassphrase: sshConfig.PrivateKeyPassphrase, RemotePath: remote.RemotePath,
			},
		}, true
	}
	return terminaldomain.CreateSpec{}, false
}

func findTerminalSSHConfig(configs []SSHConfig, id string) (SSHConfig, bool) {
	for _, config := range configs {
		if config.ID == id && strings.TrimSpace(config.Address) != "" && strings.TrimSpace(config.Username) != "" {
			return config, true
		}
	}
	return SSHConfig{}, false
}

func (a *App) terminalUserError(err error) error {
	applog.Errorf("resolve terminal workspace: %v", err)
	return a.localizedTerminalError("无法解析当前对话的工作区", "Could not resolve this conversation's workspace")
}

func (a *App) localizedTerminalError(chinese, english string) error {
	if strings.HasPrefix(strings.ToLower(a.store.Get().Preferences.Language), "en") {
		return errors.New(english)
	}
	return errors.New(chinese)
}
