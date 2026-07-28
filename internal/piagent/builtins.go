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
	"strings"

	"codingto/internal/extensions"
)

//go:embed all:default_tools
var builtinTools embed.FS

//go:embed all:system_extensions
var systemExtensions embed.FS

var retiredBuiltinTools = []string{"api", "db", "git", "browser-workflow"}

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

// BuiltinToolStatuses returns the version status of every tool bundled in
// default_tools for the given agent. CurrentVersion is the version shipped with
// the application (read from the embedded meta.json); InstalledVersion is the
// version currently materialized into the agent's extensions directory (empty
// when the tool has not been materialized yet). This lets the UI surface an
// update action whenever an installed builtin is stale.
func BuiltinToolStatuses(piDir string, enabled map[string]bool) ([]extensions.BuiltinToolStatus, error) {
	entries, err := fs.ReadDir(builtinTools, "default_tools")
	if err != nil {
		return nil, fmt.Errorf("list bundled built-in tools: %w", err)
	}
	statuses := make([]extensions.BuiltinToolStatus, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		var meta struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Version     string `json:"version"`
		}
		status := extensions.BuiltinToolStatus{Key: name}
		if raw, err := builtinTools.ReadFile(path.Join("default_tools", name, "meta.json")); err == nil {
			_ = json.Unmarshal(raw, &meta)
			status.Name = meta.Name
			status.Description = meta.Description
			status.CurrentVersion = meta.Version
		}
		status.Installed = enabled[name]
		if status.Installed {
			if raw, err := os.ReadFile(filepath.Join(piDir, "extensions", name, "meta.json")); err == nil {
				var installed struct {
					Version string `json:"version"`
				}
				if json.Unmarshal(raw, &installed) == nil {
					status.InstalledVersion = installed.Version
				}
			}
		}
		statuses = append(statuses, status)
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
