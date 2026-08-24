package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"codingto/internal/applog"
	"codingto/internal/sshsecurity"
)

// validateSSHSecurityConfig rejects malformed policies and templates at the
// Wails save boundary instead of silently dropping them during normalization.
func validateSSHSecurityConfig(profiles []SSHConfig) error {
	profileIDs := map[string]bool{}
	for _, profile := range profiles {
		profileID := strings.TrimSpace(profile.ID)
		if profileID == "" || len(profileID) > 128 || strings.ContainsAny(profileID, "\x00\r\n") {
			return fmt.Errorf("SSH 配置 ID 不合法")
		}
		if profileIDs[profileID] {
			return fmt.Errorf("SSH 配置 ID 重复：%s", profileID)
		}
		profileIDs[profileID] = true
		if err := sshsecurity.ValidateHostKeyFingerprint(profile.HostKeyFingerprint); err != nil {
			return fmt.Errorf("SSH 配置 %q：%w", profile.Name, err)
		}
		if err := profile.Policy.Validate(); err != nil {
			return fmt.Errorf("SSH 配置 %q：%w", profile.Name, err)
		}
		if len(profile.CustomCapabilities) > 64 {
			return fmt.Errorf("SSH 配置 %q：自定义能力最多 64 个", profile.Name)
		}
		seen := map[string]bool{}
		for _, source := range profile.CustomCapabilities {
			capability := source
			capability.Normalize()
			if err := capability.Validate(true); err != nil {
				return fmt.Errorf("SSH 配置 %q：%w", profile.Name, err)
			}
			if seen[capability.Name] {
				return fmt.Errorf("SSH 配置 %q：自定义能力重名：%s", profile.Name, capability.Name)
			}
			seen[capability.Name] = true
		}
	}
	return nil
}

// mergeSSHCredentials restores masked credentials from the persisted profiles.
// A newly supplied private key owns its passphrase, including an intentionally
// empty passphrase; an empty private key means the frontend did not replace it.
func mergeSSHCredentials(profiles []SSHConfig, previous []SSHConfig) {
	stored := make(map[string]SSHConfig, len(previous))
	for _, profile := range previous {
		stored[profile.ID] = profile
	}
	for index := range profiles {
		old, ok := stored[profiles[index].ID]
		if !ok {
			continue
		}
		if profiles[index].Password == "" {
			profiles[index].Password = old.Password
		}
		if profiles[index].PrivateKey == "" {
			profiles[index].PrivateKey = old.PrivateKey
			if profiles[index].PrivateKeyPassphrase == "" {
				profiles[index].PrivateKeyPassphrase = old.PrivateKeyPassphrase
			}
		}
	}
}

// maskConfigCredentials returns a frontend-safe copy of the application config.
func maskConfigCredentials(cfg AppConfig) AppConfig {
	cfg.Extensions.DB = cfg.Extensions.DB.Masked()
	cfg.SSHConfigs = append([]SSHConfig(nil), cfg.SSHConfigs...)
	for index := range cfg.SSHConfigs {
		cfg.SSHConfigs[index].Password = ""
		cfg.SSHConfigs[index].PrivateKey = ""
		cfg.SSHConfigs[index].PrivateKeyPassphrase = ""
	}
	return cfg
}

func sshSnapshotDir(sessionDir string) string {
	return filepath.Join(sessionDir, ".ssh-security")
}

func sshSnapshotPath(sessionDir string) string {
	return filepath.Join(sshSnapshotDir(sessionDir), "config.json")
}

// sshAuthorizedResources returns only SSH profiles linked to the session workspace.
func sshAuthorizedResources(store *ConfigStore, cfg AppConfig, sessionID int64) []sshsecurity.Resource {
	if sessionID <= 0 {
		return nil
	}
	session, ok, err := store.Store().SessionByID(sessionID)
	if err != nil {
		applog.Warnf("resolve SSH session authorization: %v", err)
		return nil
	}
	if !ok {
		return nil
	}
	var environment *Environment
	for index := range cfg.Environments {
		if cfg.Environments[index].ID == session.EnvironmentID {
			environment = &cfg.Environments[index]
			break
		}
	}
	if environment == nil {
		return nil
	}
	sshByID := make(map[string]SSHConfig, len(cfg.SSHConfigs))
	for _, profile := range cfg.SSHConfigs {
		sshByID[profile.ID] = profile
	}
	resources := make([]sshsecurity.Resource, 0, len(environment.Remotes))
	seen := map[string]bool{}
	for _, remote := range environment.Remotes {
		profile, exists := sshByID[remote.SSHConfigID]
		if !exists || strings.TrimSpace(profile.Address) == "" {
			continue
		}
		resourceID := strings.TrimSpace(remote.ID)
		if resourceID == "" {
			resourceID = profile.ID
		}
		if seen[resourceID] {
			continue
		}
		seen[resourceID] = true
		name := strings.TrimSpace(remote.Name)
		if name == "" {
			name = profile.Name
		}
		resources = append(resources, sshsecurity.Resource{
			ID: resourceID, Name: name, Address: profile.Address, Port: profile.Port,
			Username: profile.Username, AuthMode: profile.AuthMode, Password: profile.Password,
			PrivateKey: profile.PrivateKey, PrivateKeyPassphrase: profile.PrivateKeyPassphrase,
			HostKeyFingerprint: profile.HostKeyFingerprint,
			WorkDir:            remote.RemotePath, Policy: profile.Policy, CustomCapabilities: profile.CustomCapabilities,
		})
	}
	return resources
}

// writeSSHSnapshot writes credentials and policy to a private session-only file.
func writeSSHSnapshot(sessionDir string, resources []sshsecurity.Resource) int {
	target := sshSnapshotPath(sessionDir)
	if len(resources) == 0 {
		if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
			applog.Warnf("remove SSH security snapshot: %v", err)
		}
		return 0
	}
	snapshot := sshsecurity.Config{Resources: resources}
	snapshot.Normalize()
	if len(snapshot.Resources) == 0 {
		return 0
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		applog.Warnf("marshal SSH security snapshot: %v", err)
		return 0
	}
	if err := ensurePrivateDir(sshSnapshotDir(sessionDir)); err != nil {
		applog.Warnf("create SSH security snapshot dir: %v", err)
		return 0
	}
	if err := writePrivateFileAtomic(target, raw); err != nil {
		applog.Warnf("write SSH security snapshot: %v", err)
		return 0
	}
	return len(snapshot.Resources)
}

// configureSSHSessionEnv enables the bridge only for workspace-linked resources.
func configureSSHSessionEnv(agentEnv map[string]string, store *ConfigStore, cfg AppConfig, sessionID int64, sessionDir string) {
	resources := sshAuthorizedResources(store, cfg, sessionID)
	if len(resources) == 0 {
		return
	}
	bridgeBinary, err := resolveSSHBridgeBinary()
	if err != nil {
		applog.Warnf("SSH security bridge unavailable: %v", err)
		return
	}
	if writeSSHSnapshot(sessionDir, resources) == 0 {
		return
	}
	agentEnv["CODINGTO_SSH_BRIDGE_BIN"] = bridgeBinary
	agentEnv["CODINGTO_SSH_CONFIG_PATH"] = sshSnapshotPath(sessionDir)
	agentEnv["CODINGTO_SSH_KNOWN_HOSTS"] = knownHostsPath(store.Dir())
}

// refreshActiveSSHSnapshot applies SSH profile or workspace changes to the active session.
func (s *AgentService) refreshActiveSSHSnapshot() {
	s.mu.Lock()
	sessionDir, sessionID := s.activeSessionDir, s.activeSessionID
	s.mu.Unlock()
	if sessionDir == "" {
		return
	}
	cfg := s.store.Get()
	writeSSHSnapshot(sessionDir, sshAuthorizedResources(s.store, cfg, sessionID))
}
