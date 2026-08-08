package piagent

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"codingto/internal/extensions"
)

//go:embed all:default_tools
var builtinTools embed.FS

//go:embed all:system_extensions
var systemExtensions embed.FS

var retiredBuiltinTools = []string{"api", "db", "git", "browser-workflow"}

// stewardToolKey is the default_tools directory name of the steward toolset,
// kept in sync with internal/steward.ToolKey (piagent must not import steward,
// which would create an import cycle).
const stewardToolKey = "steward"

// mcpKeyPattern restricts MCP server keys to characters that are safe for
// LLM tool names: letters, digits, underscores and hyphens only.
var mcpKeyPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// BuiltinToolCatalog discovers every bundled extension directory and reads its
// display metadata. default_tools is embedded into the application binary, so
// adding a directory with a valid meta.json automatically makes it available
// to the backend and frontend without updating a hard-coded registry.
func BuiltinToolCatalog() ([]extensions.BuiltinToolStatus, error) {
	entries, err := fs.ReadDir(builtinTools, "default_tools")
	if err != nil {
		return nil, fmt.Errorf("list bundled built-in tools: %w", err)
	}
	catalog := make([]extensions.BuiltinToolStatus, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		var meta struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Version     string `json:"version"`
			Required    bool   `json:"required"`
		}
		raw, err := builtinTools.ReadFile(path.Join("default_tools", name, "meta.json"))
		if err != nil {
			return nil, fmt.Errorf("read built-in tool metadata %s: %w", name, err)
		}
		if err := json.Unmarshal(raw, &meta); err != nil {
			return nil, fmt.Errorf("parse built-in tool metadata %s: %w", name, err)
		}
		catalog = append(catalog, extensions.BuiltinToolStatus{
			Key:            name,
			Name:           meta.Name,
			Description:    meta.Description,
			Required:       meta.Required,
			CurrentVersion: meta.Version,
		})
	}
	return catalog, nil
}

// DefaultBuiltinTools returns the default selection for a newly created agent.
// All extensions currently present under default_tools are installed by
// default; existing agents retain their persisted selections. The steward
// toolset is excluded: it is only meaningful on the resident steward agent,
// which internal/steward force-materializes via MaterializeBuiltinTool and
// injects the RPC endpoint for. Enabling it by default on ordinary agents
// would mislead the model into calling codingto_steward_* tools that fail
// with "RPC 未配置" and hijack unrelated tasks.
func DefaultBuiltinTools() map[string]bool {
	catalog, err := BuiltinToolCatalog()
	if err != nil {
		return map[string]bool{}
	}
	enabled := make(map[string]bool, len(catalog))
	for _, tool := range catalog {
		if tool.Key == stewardToolKey {
			continue
		}
		enabled[tool.Key] = true
	}
	return enabled
}

// RequiredBuiltinTools returns the subset that cannot be disabled because it
// forms part of CodingTo's runtime contract.
func RequiredBuiltinTools() map[string]bool {
	catalog, err := BuiltinToolCatalog()
	if err != nil {
		return map[string]bool{}
	}
	required := make(map[string]bool)
	for _, tool := range catalog {
		if tool.Required {
			required[tool.Key] = true
		}
	}
	return required
}

// MaterializeSystemExtensions copies CodingTo's mandatory, non-user-configurable
// Pi extensions into the managed agent directory. These extensions implement
// runtime invariants (for example transactional file-change capture), so they
// must be present even when the user disables optional built-in tools.
func MaterializeSystemExtensions(piDir string) error {
	targetRoot := filepath.Join(piDir, "extensions")
	if err := os.MkdirAll(targetRoot, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(targetRoot, 0o700); err != nil {
		return err
	}
	err := fs.WalkDir(systemExtensions, "system_extensions", func(sourcePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative := strings.TrimPrefix(filepath.ToSlash(sourcePath), "system_extensions/")
		target := filepath.Join(targetRoot, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if err := os.Chmod(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		content, err := systemExtensions.ReadFile(sourcePath)
		if err != nil {
			return err
		}
		return writeFileIfChanged(target, content)
	})
	if err != nil {
		return fmt.Errorf("materialize system Pi extensions: %w", err)
	}
	return nil
}

// MaterializeBuiltinTool copies a single bundled tool directory into the
// agent's extensions directory, regardless of the agent's enabled set. It is
// for tools a feature must guarantee even when the agent settings do not list
// them — for example the steward toolset on the resident steward agent, which
// is the only delivery path for IM replies.
func MaterializeBuiltinTool(piDir, toolName string) error {
	toolName = strings.TrimSpace(toolName)
	if toolName == "" || strings.ContainsAny(toolName, "/\\..") {
		return fmt.Errorf("invalid builtin tool name %q", toolName)
	}
	sourceRoot := path.Join("default_tools", toolName)
	info, err := fs.Stat(builtinTools, sourceRoot)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("built-in tool %s not found", toolName)
	}
	targetRoot := filepath.Join(piDir, "extensions")
	if err := os.MkdirAll(targetRoot, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(targetRoot, 0o700); err != nil {
		return err
	}
	err = fs.WalkDir(builtinTools, sourceRoot, func(sourcePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative := strings.TrimPrefix(filepath.ToSlash(sourcePath), "default_tools/")
		target := filepath.Join(targetRoot, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if err := os.Chmod(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		content, err := builtinTools.ReadFile(sourcePath)
		if err != nil {
			return err
		}
		return writeFileIfChanged(target, content)
	})
	if err != nil {
		return fmt.Errorf("materialize built-in tool %s: %w", toolName, err)
	}
	return nil
}

// MaterializeBuiltinTools copies the extensions bundled with CodingTo into the
// agent's extensions directory. Pi auto-discovers them via PI_CODING_AGENT_DIR
// at startup, so no explicit --extension flag is needed.
func MaterializeBuiltinTools(piDir string, enabled map[string]bool) error {
	targetRoot := filepath.Join(piDir, "extensions")
	if err := os.MkdirAll(targetRoot, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(targetRoot, 0o700); err != nil {
		return err
	}
	if err := cleanupRetiredBuiltinTools(targetRoot); err != nil {
		return err
	}

	err := fs.WalkDir(builtinTools, "default_tools", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative := strings.TrimPrefix(filepath.ToSlash(path), "default_tools/")
		// Skip tools that are not enabled for this agent. With Pi auto-discovery
		// every materialized tool under extensions/ is loaded, so disabled tools
		// must not be written at all.
		toolName := strings.Split(relative, "/")[0]
		if toolName != "" && !enabled[toolName] {
			return nil
		}
		target := filepath.Join(targetRoot, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if err := os.Chmod(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		content, err := builtinTools.ReadFile(path)
		if err != nil {
			return err
		}
		if err := writeFileIfChanged(target, content); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("materialize built-in Pi extensions: %w", err)
	}
	return nil
}

// RemoveBuiltinTools removes the materialized directories of builtin tools that
// are no longer enabled for the agent. Pi auto-discovers every entry under
// extensions/, so a disabled tool must be physically removed to actually turn
// it off and keep each agent's tool set isolated. User-added and recommended
// extensions are never touched.
func RemoveBuiltinTools(piDir string, enabled map[string]bool) error {
	targetRoot := filepath.Join(piDir, "extensions")
	if err := cleanupRetiredBuiltinTools(targetRoot); err != nil {
		return err
	}
	entries, err := fs.ReadDir(builtinTools, "default_tools")
	if err != nil {
		return fmt.Errorf("list bundled built-in tools: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if enabled[e.Name()] {
			continue
		}
		dest := filepath.Join(targetRoot, e.Name())
		if err := os.RemoveAll(dest); err != nil {
			return fmt.Errorf("remove disabled built-in tool %s: %w", e.Name(), err)
		}
	}
	return nil
}

// managedExtensionKeys returns the set of extension directory names owned by
// CodingTo: builtin tools (default_tools), mandatory system extensions
// (system_extensions) and the RTK Pi bridge. Anything else physically present
// under an agent's extensions/ directory was installed outside CodingTo's
// management and should be surfaced to the user for review and removal.
func managedExtensionKeys() map[string]bool {
	keys := map[string]bool{"rtk": true}
	if catalog, err := BuiltinToolCatalog(); err == nil {
		for _, tool := range catalog {
			keys[tool.Key] = true
		}
	}
	if entries, err := fs.ReadDir(systemExtensions, "system_extensions"); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				keys[entry.Name()] = true
			}
		}
	}
	return keys
}

// IsManagedExtension reports whether the given extensions/ directory name is
// owned by CodingTo (builtin tool, system extension or RTK bridge) and therefore
// must not be deleted through the unmanaged-extension removal path.
func IsManagedExtension(key string) bool {
	return managedExtensionKeys()[strings.TrimSpace(key)]
}

// UnmanagedExtensionStatuses lists the extension directories physically present
// in the agent's extensions/ folder that are not owned by CodingTo. Pi
// auto-discovers every entry under extensions/, so an unmanaged entry (for
// example a manually copied ask-user extension) is still loaded and can block
// execution with interactive UI requests the user never opted into. Surfacing
// them lets the user see and delete them.
func UnmanagedExtensionStatuses(dataDir string) ([]extensions.Status, error) {
	targetRoot := filepath.Join(dataDir, "extensions")
	entries, err := os.ReadDir(targetRoot)
	if os.IsNotExist(err) {
		return []extensions.Status{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read agent extensions directory: %w", err)
	}
	managed := managedExtensionKeys()
	statuses := []extensions.Status{}
	for _, entry := range entries {
		if !entry.IsDir() || managed[entry.Name()] {
			continue
		}
		sourcePath, _ := filepath.Abs(filepath.Join(targetRoot, entry.Name()))
		status := extensions.Status{
			Key:         entry.Name(),
			Name:        entry.Name(),
			Description: entry.Name(),
			Installed:   true,
			Enabled:     true,
			SourcePath:  sourcePath,
		}
		applyUnmanagedManifest(&status, filepath.Join(targetRoot, entry.Name()))
		statuses = append(statuses, status)
	}
	return statuses, nil
}

// applyUnmanagedManifest enriches an unmanaged extension status with display
// metadata from its meta.json or package.json when present.
func applyUnmanagedManifest(status *extensions.Status, dir string) {
	for _, manifestName := range []string{"meta.json", "package.json"} {
		raw, err := os.ReadFile(filepath.Join(dir, manifestName))
		if err != nil {
			continue
		}
		var manifest struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Version     string `json:"version"`
			Homepage    string `json:"homepage"`
		}
		if json.Unmarshal(raw, &manifest) != nil {
			continue
		}
		if strings.TrimSpace(manifest.Name) != "" {
			status.Name = manifest.Name
		}
		if strings.TrimSpace(manifest.Description) != "" {
			status.Description = manifest.Description
		}
		if strings.TrimSpace(manifest.Version) != "" {
			status.Version = manifest.Version
		}
		if strings.TrimSpace(manifest.Homepage) != "" {
			status.Homepage = manifest.Homepage
		}
		return
	}
}

// BuiltinToolStatuses returns the version status of every tool bundled in
// default_tools for the given agent, except the steward toolset: it is an
// internal, non-user-configurable toolset reserved for the resident steward
// agent (force-enabled by internal/steward), so it must never surface in the
// agent settings extension page as a togglable/visible entry. CurrentVersion is
// the version shipped with the application (read from the embedded meta.json);
// InstalledVersion is the version currently materialized into the agent's
// extensions directory (empty when the tool has not been materialized yet).
// This lets the UI surface an update action whenever an installed builtin is
// stale.
func BuiltinToolStatuses(piDir string, enabled map[string]bool) ([]extensions.BuiltinToolStatus, error) {
	statuses, err := BuiltinToolCatalog()
	if err != nil {
		return nil, err
	}
	// Hide the steward toolset from the agent settings UI. The tool itself stays
	// managed (managedExtensionKeys still covers it) so its materialized
	// directory is not mistaken for an unmanaged extension.
	filtered := statuses[:0]
	for _, status := range statuses {
		if status.Key != stewardToolKey {
			filtered = append(filtered, status)
		}
	}
	statuses = filtered
	for index := range statuses {
		status := &statuses[index]
		status.Installed = enabled[status.Key]
		if status.Installed {
			if raw, err := os.ReadFile(filepath.Join(piDir, "extensions", status.Key, "meta.json")); err == nil {
				var installed struct {
					Version string `json:"version"`
				}
				if json.Unmarshal(raw, &installed) == nil {
					status.InstalledVersion = installed.Version
				}
			}
		}
	}
	return statuses, nil
}

// MaterializeRTKExtension copies RTK's globally installed Pi bridge into the
// managed agent's extensions directory. CodingTo sets PI_CODING_AGENT_DIR to the
// agent's data dir, so Pi auto-discovers extensions/rtk/index.ts at startup and
// loads it without an explicit --extension flag.
func MaterializeRTKExtension(piDir, source string) (string, error) {
	content, err := os.ReadFile(source)
	if err != nil {
		return "", fmt.Errorf("read RTK Pi extension: %w", err)
	}
	target := filepath.Join(piDir, "extensions", "rtk", "index.ts")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return "", fmt.Errorf("create RTK extension directory: %w", err)
	}
	if err := os.Chmod(filepath.Dir(target), 0o700); err != nil {
		return "", fmt.Errorf("secure RTK extension directory: %w", err)
	}
	if err := writeFileIfChanged(target, content); err != nil {
		return "", fmt.Errorf("materialize RTK Pi extension: %w", err)
	}
	return target, nil
}

// RTKMaterialized reports whether the RTK Pi bridge has been copied into the
// given agent's extensions directory.
func RTKMaterialized(piDir string) bool {
	info, err := os.Stat(filepath.Join(piDir, "extensions", "rtk", "index.ts"))
	return err == nil && !info.IsDir()
}

// BrowserNativeDir returns the directory where `pi install
// npm:pi-agent-browser-native` materializes the extension for a single agent.
// PI_CODING_AGENT_DIR points Pi's user/global package scope at dataDir, so the
// package remains isolated without using project-local (-l) installation.
func BrowserNativeDir(dataDir string) string {
	return filepath.Join(dataDir, "npm", "node_modules", "pi-agent-browser-native")
}

// BrowserNativeInstalled reports whether pi-agent-browser-native has been
// installed into the agent's isolated user/global package directory.
func BrowserNativeInstalled(dataDir string) bool {
	info, err := os.Stat(BrowserNativeDir(dataDir))
	return err == nil && info.IsDir()
}

// BrowserNativeVersion reads the installed pi-agent-browser-native version from
// the package's own package.json, or "" when it cannot be determined.
func BrowserNativeVersion(dataDir string) string {
	pkgPath := filepath.Join(BrowserNativeDir(dataDir), "package.json")
	data, err := os.ReadFile(pkgPath)
	if err != nil {
		return ""
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(data, &pkg) != nil {
		return ""
	}
	return pkg.Version
}

// PiMCPAdapterDir returns the isolated npm package directory used by the Pi
// Figma extension. PI_CODING_AGENT_DIR makes this Pi user/global package scope
// private to the selected CodingTo agent.
func PiMCPAdapterDir(dataDir string) string {
	return filepath.Join(dataDir, "npm", "node_modules", "pi-mcp-adapter")
}

// PiMCPAdapterInstalled reports whether this agent can bridge MCP servers into
// Pi tools. The global Figma runtime is checked separately by extensions.Manager.
func PiMCPAdapterInstalled(dataDir string) bool {
	info, err := os.Stat(PiMCPAdapterDir(dataDir))
	return err == nil && info.IsDir()
}

// PiMCPAdapterVersion returns the installed adapter package version.
func PiMCPAdapterVersion(dataDir string) string {
	data, err := os.ReadFile(filepath.Join(PiMCPAdapterDir(dataDir), "package.json"))
	if err != nil {
		return ""
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(data, &pkg) != nil {
		return ""
	}
	return pkg.Version
}

// SyncFigmaMCPConfig adds or removes only CodingTo's `figma` server entry in
// the agent-owned mcp.json. Other user-managed MCP servers and settings remain
// untouched. Credentials are environment references, never literal tokens.
func SyncFigmaMCPConfig(dataDir string, enabled bool) error {
	configPath := filepath.Join(dataDir, "mcp.json")
	config := map[string]any{}
	if raw, err := os.ReadFile(configPath); err == nil {
		if len(strings.TrimSpace(string(raw))) > 0 {
			if err := json.Unmarshal(raw, &config); err != nil {
				return fmt.Errorf("parse agent MCP config: %w", err)
			}
		}
	} else if os.IsNotExist(err) && !enabled {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read agent MCP config: %w", err)
	}

	servers, hasServers := config["mcpServers"].(map[string]any)
	if !hasServers && config["mcpServers"] != nil {
		return errors.New("agent MCP config field mcpServers must be an object")
	}
	if servers == nil {
		servers = map[string]any{}
	}
	if enabled {
		servers["figma"] = map[string]any{
			"command":     "figma-developer-mcp",
			"args":        []string{"--stdio"},
			"lifecycle":   "lazy",
			"directTools": true,
			"env": map[string]string{
				"FIGMA_API_KEY":     "$env:CODINGTO_FIGMA_API_KEY",
				"FIGMA_OAUTH_TOKEN": "$env:CODINGTO_FIGMA_OAUTH_TOKEN",
			},
		}
	} else {
		delete(servers, "figma")
	}
	config["mcpServers"] = servers

	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("create agent data directory: %w", err)
	}
	encoded, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("encode agent MCP config: %w", err)
	}
	encoded = append(encoded, '\n')
	if err := writeFileIfChanged(configPath, encoded); err != nil {
		return fmt.Errorf("write agent MCP config: %w", err)
	}
	return nil
}

// FigmaMCPConfigured reports whether CodingTo's server entry exists for the
// agent. It does not inspect or expose credentials.
func FigmaMCPConfigured(dataDir string) bool {
	raw, err := os.ReadFile(filepath.Join(dataDir, "mcp.json"))
	if err != nil {
		return false
	}
	var config struct {
		Servers map[string]json.RawMessage `json:"mcpServers"`
	}
	if json.Unmarshal(raw, &config) != nil {
		return false
	}
	_, ok := config.Servers["figma"]
	return ok
}

// UpsertMCPServer adds or updates one CodingTo-managed MCP server entry in an
// agent's mcp.json while preserving every unrelated server and setting.
func UpsertMCPServer(dataDir, key, command string, args []string) error {
	key = strings.TrimSpace(key)
	command = strings.TrimSpace(command)
	if key == "" || command == "" {
		return errors.New("MCP server key and command are required")
	}
	configPath := filepath.Join(dataDir, "mcp.json")
	config := map[string]any{}
	if raw, err := os.ReadFile(configPath); err == nil {
		if len(strings.TrimSpace(string(raw))) > 0 {
			if err := json.Unmarshal(raw, &config); err != nil {
				return fmt.Errorf("parse agent MCP config: %w", err)
			}
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read agent MCP config: %w", err)
	}
	servers, ok := config["mcpServers"].(map[string]any)
	if !ok && config["mcpServers"] != nil {
		return errors.New("agent MCP config field mcpServers must be an object")
	}
	if servers == nil {
		servers = map[string]any{}
	}
	servers[key] = map[string]any{
		"command":     command,
		"args":        args,
		"lifecycle":   "lazy",
		"directTools": true,
	}
	config["mcpServers"] = servers
	encoded, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("encode agent MCP config: %w", err)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("create agent data directory: %w", err)
	}
	if err := writeFileIfChanged(configPath, append(encoded, '\n')); err != nil {
		return fmt.Errorf("write agent MCP config: %w", err)
	}
	return nil
}

// ManualMCPServerConfig describes a user-supplied MCP server entry that can use
// either stdio (command + args) or remote (URL) transport.
type ManualMCPServerConfig struct {
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	URL     string            `json:"url,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// UpsertManualMCPServer adds or updates a manually configured MCP server entry
// in an agent's mcp.json. It supports both stdio (command+args+env) and remote
// (url) transport types while preserving every unrelated server and setting.
func UpsertManualMCPServer(dataDir, key string, cfg ManualMCPServerConfig) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("MCP server key is required")
	}
	if !mcpKeyPattern.MatchString(key) {
		return fmt.Errorf("MCP server key %q contains invalid characters: only letters, digits, underscores (_) and hyphens (-) are allowed", key)
	}
	if strings.TrimSpace(cfg.Command) == "" && strings.TrimSpace(cfg.URL) == "" {
		return errors.New("either command or url is required for a manual MCP server")
	}
	configPath := filepath.Join(dataDir, "mcp.json")
	config := map[string]any{}
	if raw, err := os.ReadFile(configPath); err == nil {
		if len(strings.TrimSpace(string(raw))) > 0 {
			if err := json.Unmarshal(raw, &config); err != nil {
				return fmt.Errorf("parse agent MCP config: %w", err)
			}
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read agent MCP config: %w", err)
	}
	servers, ok := config["mcpServers"].(map[string]any)
	if !ok && config["mcpServers"] != nil {
		return errors.New("agent MCP config field mcpServers must be an object")
	}
	if servers == nil {
		servers = map[string]any{}
	}
	entry := map[string]any{
		"lifecycle":   "lazy",
		"directTools": true,
	}
	if strings.TrimSpace(cfg.URL) != "" {
		entry["url"] = strings.TrimSpace(cfg.URL)
	} else {
		entry["command"] = strings.TrimSpace(cfg.Command)
		if len(cfg.Args) > 0 {
			entry["args"] = cfg.Args
		}
		if len(cfg.Env) > 0 {
			entry["env"] = cfg.Env
		}
	}
	servers[key] = entry
	config["mcpServers"] = servers
	encoded, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("encode agent MCP config: %w", err)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("create agent data directory: %w", err)
	}
	if err := writeFileIfChanged(configPath, append(encoded, '\n')); err != nil {
		return fmt.Errorf("write agent MCP config: %w", err)
	}
	return nil
}

// RemoveMCPServer deletes one MCP server entry from an agent's mcp.json while
// preserving every unrelated server and setting.
func RemoveMCPServer(dataDir, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("MCP server key is required")
	}
	configPath := filepath.Join(dataDir, "mcp.json")
	raw, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read agent MCP config: %w", err)
	}
	config := map[string]any{}
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := json.Unmarshal(raw, &config); err != nil {
			return fmt.Errorf("parse agent MCP config: %w", err)
		}
	}
	servers, ok := config["mcpServers"].(map[string]any)
	if !ok || servers == nil {
		return nil
	}
	delete(servers, key)
	config["mcpServers"] = servers
	encoded, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("encode agent MCP config: %w", err)
	}
	if err := writeFileIfChanged(configPath, append(encoded, '\n')); err != nil {
		return fmt.Errorf("write agent MCP config: %w", err)
	}
	return nil
}

// MCPServerStatuses reads every server currently configured for one agent,
// including entries managed outside CodingTo.
func MCPServerStatuses(dataDir string) ([]extensions.Status, error) {
	raw, err := os.ReadFile(filepath.Join(dataDir, "mcp.json"))
	if os.IsNotExist(err) {
		return []extensions.Status{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read agent MCP config: %w", err)
	}
	var config struct {
		Servers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
			URL     string   `json:"url"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return nil, fmt.Errorf("parse agent MCP config: %w", err)
	}
	keys := make([]string, 0, len(config.Servers))
	for key := range config.Servers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	statuses := make([]extensions.Status, 0, len(keys))
	for _, key := range keys {
		server := config.Servers[key]
		description := strings.TrimSpace(strings.Join(append([]string{server.Command}, server.Args...), " "))
		if server.URL != "" {
			description = server.URL
		}
		statuses = append(statuses, extensions.Status{
			Key:         key,
			Name:        key,
			Description: description,
			Installed:   true,
			Enabled:     true,
		})
	}
	return statuses, nil
}

// RemoveRTKExtension removes only CodingTo's managed RTK runtime copy.
func RemoveRTKExtension(piDir string) error {
	target := filepath.Join(piDir, "extensions", "rtk", "index.ts")
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove RTK Pi extension: %w", err)
	}
	return nil
}

func writeFileIfChanged(path string, content []byte) error {
	existing, err := os.ReadFile(path)
	if err == nil && string(existing) == string(content) {
		return os.Chmod(path, 0o600)
	}
	return os.WriteFile(path, content, 0o600)
}

func cleanupRetiredBuiltinTools(targetRoot string) error {
	for _, name := range retiredBuiltinTools {
		if err := os.RemoveAll(filepath.Join(targetRoot, name)); err != nil {
			return fmt.Errorf("remove retired built-in tool %s: %w", name, err)
		}
	}
	return nil
}
