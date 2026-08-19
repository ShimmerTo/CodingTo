package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"codingto/internal/applog"
	"codingto/internal/extensions"
)

const (
	// dcgPolicyFileName carries the severity -> action map consumed by the dcg
	// bridge extension on every bash call (path injected as
	// CODINGTO_DCG_POLICY_FILE), so policy changes apply without restarting agents.
	dcgPolicyFileName = "dcg_policy.json"
	// dcgWorkspaceAllowReason / dcgWorkspaceAllowAddedBy tag the [[allow]]
	// entries managed by CodingTo so sync can replace them without touching
	// user-defined entries. dcg supports pack wildcards ("pack_id:*") but no
	// full-rule wildcard, so each enabled pack gets its own entry.
	dcgWorkspaceAllowReason  = "codingto-workspace-allow"
	dcgWorkspaceAllowAddedBy = "codingto"
	// dcgAllowlistFileName sits next to the user config file selected by
	// selectDCGUserConfigSource; dcg reads allowlist entries from that location.
	dcgAllowlistFileName = "allowlist.toml"
)

// dcgSeverityLevels are the levels dcg reports for matched rules.
var dcgSeverityLevels = []string{"critical", "high", "medium", "low"}

// normalizeDCGSettings keeps only known severity levels and actions.
func normalizeDCGSettings(settings DCGSettings) DCGSettings {
	out := DCGSettings{WorkspaceAllow: settings.WorkspaceAllow}
	for _, level := range dcgSeverityLevels {
		action := strings.ToLower(strings.TrimSpace(settings.SeverityPolicy[level]))
		switch action {
		case "allow", "ask", "deny":
			if out.SeverityPolicy == nil {
				out.SeverityPolicy = map[string]string{}
			}
			out.SeverityPolicy[level] = action
		}
	}
	return out
}

// GetDCGSettings returns CodingTo's dcg disposition policy.
func (a *App) GetDCGSettings() DCGSettings {
	return a.store.Get().DCGSettings
}

// SaveDCGSettings persists the policy and refreshes its runtime artifacts: the
// bridge policy file and the dcg user-config workspace allow rules. The config
// is saved even when the dcg sync fails (e.g. dcg not installed); the returned
// error only reports the sync problem to the UI.
func (a *App) SaveDCGSettings(settings DCGSettings) (DCGSettings, error) {
	normalized := normalizeDCGSettings(settings)
	cfg := a.store.Get()
	wsAllowChanged := cfg.DCGSettings.WorkspaceAllow != normalized.WorkspaceAllow
	cfg.DCGSettings = normalized
	if err := a.store.Save(cfg); err != nil {
		return DCGSettings{}, err
	}
	if err := a.writeDCGPolicyFile(cfg); err != nil {
		return DCGSettings{}, err
	}
	// Only sync workspace allow rules when the switch actually changed.
	// Pure severity-policy edits (e.g. changing "ask" to "deny") should
	// not require a working dcg binary.
	if wsAllowChanged {
		if err := syncDCGWorkspaceAllow(cfg); err != nil {
			return normalized, err
		}
	}
	return normalized, nil
}

// writeDCGPolicyFile writes the severity -> action map for the dcg bridge. The
// write is skipped when the on-disk content already matches, so unrelated
// config saves do not touch the file.
func (a *App) writeDCGPolicyFile(cfg AppConfig) error {
	return writeDCGPolicyFileAt(a.store.Dir(), cfg)
}

// defaultDCGAction is the disposition applied when no explicit policy is set:
// medium/low are allowed by default, critical/high ask for confirmation.
func defaultDCGAction(level string) string {
	if level == "medium" || level == "low" {
		return "allow"
	}
	return "ask"
}

func writeDCGPolicyFileAt(dir string, cfg AppConfig) error {
	policy := map[string]string{}
	for _, level := range dcgSeverityLevels {
		action := cfg.DCGSettings.SeverityPolicy[level]
		if action == "" {
			action = defaultDCGAction(level)
		}
		policy[level] = action
	}
	raw, err := json.Marshal(policy)
	if err != nil {
		return fmt.Errorf("encode dcg policy: %w", err)
	}
	path := filepath.Join(dir, dcgPolicyFileName)
	if existing, readErr := os.ReadFile(path); readErr == nil && string(existing) == string(raw) {
		return nil
	}
	return os.WriteFile(path, raw, 0o600)
}

// ensureDCGRuntime refreshes the policy file and, when the workspace allow
// inputs changed, the dcg user-config allow rules. Failures are logged only so
// an unrelated config save is never blocked by a dcg problem.
func (a *App) ensureDCGRuntime(cfg, previous AppConfig) {
	if err := a.writeDCGPolicyFile(cfg); err != nil {
		applog.Warnf("write dcg policy file: %v", err)
		return
	}
	if !dcgRuntimeInputChanged(cfg, previous) {
		return
	}
	if err := syncDCGWorkspaceAllow(cfg); err != nil {
		applog.Warnf("sync dcg workspace allow: %v", err)
	}
}

func dcgRuntimeInputChanged(cfg, previous AppConfig) bool {
	if cfg.DCGSettings.WorkspaceAllow != previous.DCGSettings.WorkspaceAllow {
		return true
	}
	if !cfg.DCGSettings.WorkspaceAllow && !previous.DCGSettings.WorkspaceAllow {
		return false
	}
	return workspacePaths(cfg) != workspacePaths(previous)
}

func workspacePaths(cfg AppConfig) string {
	parts := make([]string, 0, len(cfg.Environments))
	for _, env := range cfg.Environments {
		parts = append(parts, strings.TrimSpace(env.Path))
	}
	return strings.Join(parts, "\x00")
}

// syncDCGWorkspaceAllow rebuilds the CodingTo-managed [[allow]] entries in the
// dcg user allowlist. When the switch is on, every enabled pack gets a
// wildcard entry scoped to each workspace directory (the directory itself plus
// its recursive glob); when off, the managed entries are removed while
// user-defined entries stay untouched. dcg 0.11 matches these entries against
// both the process working directory and absolute paths in the command.
func syncDCGWorkspaceAllow(cfg AppConfig) error {
	binary, err := extensions.DCGExecutable()
	if err != nil {
		return errors.New("dcg executable was not found; install dcg to sync workspace allow rules")
	}
	_, sources, err := runDCGConfig(binary, "")
	if err != nil {
		return fmt.Errorf("discover dcg configuration: %w", err)
	}
	configPath, _, err := selectDCGUserConfigSource(sources)
	if err != nil {
		return err
	}
	allowlistPath := filepath.Join(filepath.Dir(configPath), dcgAllowlistFileName)

	content := ""
	if raw, readErr := os.ReadFile(allowlistPath); readErr == nil {
		content = string(raw)
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return fmt.Errorf("read dcg allowlist: %w", readErr)
	}

	values, err := parseDCGConfig(content)
	if err != nil {
		return err
	}
	var packs []string
	if cfg.DCGSettings.WorkspaceAllow {
		packs, err = enabledDCGPackIDs(binary)
		if err != nil {
			return err
		}
	}
	applyWorkspaceAllowEntries(values, cfg, packs, time.Now())
	next, err := formatDCGConfig(values)
	if err != nil {
		return err
	}
	if next == content {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(allowlistPath), 0o700); err != nil {
		return fmt.Errorf("create dcg allowlist directory: %w", err)
	}
	if err := os.WriteFile(allowlistPath, []byte(next), 0o600); err != nil {
		return fmt.Errorf("write dcg allowlist: %w", err)
	}
	if err := validateDCGAllowlist(binary); err != nil {
		_ = os.WriteFile(allowlistPath, []byte(content), 0o600)
		return err
	}
	return nil
}

// applyWorkspaceAllowEntries rebuilds the CodingTo-managed [[allow]] entries in
// the parsed dcg user allowlist: entries tagged with the CodingTo reason are
// dropped, and when the switch is on one wildcard entry per enabled pack is
// added for every workspace directory. added_at from the previous entry is
// reused when rule and paths are unchanged so an identical policy round-trips
// to the same file content and skips the disk write.
func applyWorkspaceAllowEntries(values map[string]any, cfg AppConfig, packs []string, now time.Time) {
	entries, _ := values["allow"].([]any)
	kept := make([]any, 0, len(entries))
	legacy := map[string]string{}
	for _, item := range entries {
		entry, ok := item.(map[string]any)
		if !ok {
			kept = append(kept, item)
			continue
		}
		if reason, _ := entry["reason"].(string); reason != dcgWorkspaceAllowReason {
			kept = append(kept, item)
			continue
		}
		if addedAt, _ := entry["added_at"].(string); addedAt != "" {
			legacy[dcgAllowEntryKey(entry)] = addedAt
		}
	}
	if !cfg.DCGSettings.WorkspaceAllow {
		values["allow"] = kept
		return
	}
	nowStr := now.UTC().Format(time.RFC3339)
	for _, env := range cfg.Environments {
		dir := filepath.Clean(strings.TrimSpace(env.Path))
		if dir == "" || dir == "." {
			continue
		}
		paths := []any{dir, dir + string(filepath.Separator) + "**"}
		for _, pack := range packs {
			entry := map[string]any{
				"rule":     pack + ":*",
				"reason":   dcgWorkspaceAllowReason,
				"added_by": dcgWorkspaceAllowAddedBy,
				"paths":    paths,
			}
			if addedAt := legacy[dcgAllowEntryKey(entry)]; addedAt != "" {
				entry["added_at"] = addedAt
			} else {
				entry["added_at"] = nowStr
			}
			kept = append(kept, entry)
		}
	}
	values["allow"] = kept
}

func dcgAllowEntryKey(entry map[string]any) string {
	rule, _ := entry["rule"].(string)
	paths, _ := entry["paths"].([]any)
	parts := make([]string, 0, len(paths)+1)
	parts = append(parts, rule)
	for _, p := range paths {
		if s, ok := p.(string); ok {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\x00")
}

// enabledDCGPackIDs returns the pack IDs dcg currently evaluates. Only these
// packs can trigger a decision, so only their wildcard entries are needed to
// cover every dangerous command inside a workspace.
func enabledDCGPackIDs(binary string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "packs", "--format", "json")
	configureDCGProcess(command)
	command.Env = append(os.Environ(), "DCG_NO_COLOR=1", "DCG_NO_UPDATE_CHECK=1")
	raw, err := command.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, errors.New("enumerating dcg packs timed out")
	}
	if err != nil {
		message := strings.TrimSpace(string(raw))
		if message == "" {
			message = err.Error()
		}
		return nil, errors.New(message)
	}
	var result struct {
		Packs []struct {
			ID      string `json:"id"`
			Enabled bool   `json:"enabled"`
		} `json:"packs"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("parse dcg packs output: %w", err)
	}
	ids := make([]string, 0, len(result.Packs))
	for _, pack := range result.Packs {
		if pack.Enabled && strings.TrimSpace(pack.ID) != "" {
			ids = append(ids, strings.TrimSpace(pack.ID))
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// validateDCGAllowlist asks dcg to validate the user allowlist after a write.
// dcg discovers the allowlist from its default user config location (DCG_CONFIG
// does not affect allowlist discovery), so the file just written is validated;
// the caller restores the previous content on failure.
func validateDCGAllowlist(binary string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "allowlist", "validate", "--user")
	configureDCGProcess(command)
	command.Env = append(os.Environ(), "DCG_NO_COLOR=1", "DCG_NO_UPDATE_CHECK=1")
	raw, err := command.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return errors.New("allowlist validation timed out")
	}
	if err != nil {
		message := strings.TrimSpace(string(raw))
		if message == "" {
			message = err.Error()
		}
		return errors.New(message)
	}
	return nil
}
