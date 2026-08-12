package app

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"codingto/internal/extensions"
	"codingto/internal/piagent"
	"github.com/wailsapp/wails/v3/pkg/application"
)

func (a *App) GetExtensions() extensions.Snapshot {
	snap := a.extensions.Snapshot(a.store.Get().Extensions)
	cfg := a.store.Get()
	if catalog, err := piagent.BuiltinToolCatalog(); err == nil {
		// The steward toolset is reserved for the resident steward agent and is
		// never user-configurable, so it is hidden from the agent settings
		// extension page's fallback catalog as well (BuiltinToolStatuses already
		// filters it out of the per-agent statuses).
		filtered := catalog[:0]
		for _, tool := range catalog {
			if tool.Key == "steward" {
				continue
			}
			filtered = append(filtered, tool)
		}
		snap.BuiltinCatalog = filtered
	} else {
		snap.BuiltinCatalog = []extensions.BuiltinToolStatus{}
	}
	builtins := make(map[string][]extensions.BuiltinToolStatus, len(cfg.Agents))
	for _, agent := range cfg.Agents {
		statuses, err := piagent.BuiltinToolStatuses(agent.DataDir, agent.Builtin)
		if err != nil {
			continue
		}
		builtins[agent.ID] = statuses
	}
	snap.Builtins = builtins

	recommended := make(map[string][]extensions.Status, len(cfg.Agents))
	packages := make(map[string][]extensions.Status, len(cfg.Agents))
	directory := make(map[string][]extensions.Status, len(cfg.Agents))
	mcpServers := make(map[string][]extensions.Status, len(cfg.Agents))
	a.store.EnsureAgentDataDirs(&cfg)
	dcgRuntime := extensions.Status{}
	for _, tool := range snap.Tools {
		if tool.Key == "dcg" {
			dcgRuntime = tool
			break
		}
	}
	for _, agent := range cfg.Agents {
		packageStatuses, err := piagent.InstalledPackageStatuses(agent.DataDir)
		if err != nil {
			packageStatuses = []extensions.Status{}
		}
		packages[agent.ID] = packageStatuses
		unmanagedStatuses, unmanagedErr := piagent.UnmanagedExtensionStatuses(agent.DataDir)
		if unmanagedErr != nil {
			unmanagedStatuses = []extensions.Status{}
		}
		directory[agent.ID] = unmanagedStatuses
		agentMCP, mcpErr := piagent.MCPServerStatuses(agent.DataDir)
		if mcpErr != nil {
			agentMCP = []extensions.Status{}
		}
		mcpServers[agent.ID] = agentMCP

		enabled := agent.Recommended["rtk"] && piagent.RTKMaterialized(agent.DataDir)
		dcgEnabled := agent.Recommended["dcg"] && piagent.DCGMaterialized(agent.DataDir)
		dcgStatus := dcgRuntime
		dcgStatus.Enabled = dcgEnabled
		dcgStatus.Description = "Detects dangerous bash commands and requests approval before execution."
		installed := false
		browserStatusVersion := ""
		if profile, found := cfg.Agent(agent.ID); found {
			installed = piagent.BrowserNativeInstalled(profile.DataDir)
			if installed {
				browserStatusVersion = piagent.BrowserNativeVersion(profile.DataDir)
			}
		}
		browserStatus := extensions.BrowserNativeStatusForAgent(installed)
		browserStatus.Version = browserStatusVersion
		if installed {
			browserStatus.SourcePath = piagent.BrowserNativeDir(agent.DataDir)
		}
		figmaInstalled := agent.Recommended["figma"] && piagent.PiMCPAdapterInstalled(agent.DataDir) && piagent.FigmaMCPConfigured(agent.DataDir)
		figmaStatus := extensions.PiFigmaStatusForAgent(figmaInstalled)
		figmaStatus.Version = piagent.PiMCPAdapterVersion(agent.DataDir)
		if piagent.PiMCPAdapterInstalled(agent.DataDir) {
			figmaStatus.SourcePath = piagent.PiMCPAdapterDir(agent.DataDir)
		}
		recommended[agent.ID] = []extensions.Status{
			extensions.PiPluginsStatusForAgent(packageStatuses),
			extensions.RTKStatusForAgent(enabled),
			dcgStatus,
			browserStatus,
			figmaStatus,
		}
	}
	snap.Recommended = recommended
	snap.Packages = packages
	snap.Directory = directory
	snap.MCP = mcpServers
	return snap
}

// GetAgentExtensions recomputes only one agent's builtin/recommended/package
// statuses. Used after editing a single agent so unrelated agents are not
// re-scanned from disk.
func (a *App) GetAgentExtensions(agentID string) extensions.AgentExtensionStatuses {
	empty := extensions.AgentExtensionStatuses{}
	cfg := a.store.Get()
	agent, found := cfg.Agent(agentID)
	if !found {
		return empty
	}
	a.store.EnsureAgentDataDirs(&cfg)

	builtins, err := piagent.BuiltinToolStatuses(agent.DataDir, agent.Builtin)
	if err != nil {
		builtins = []extensions.BuiltinToolStatus{}
	}

	packageStatuses, err := piagent.InstalledPackageStatuses(agent.DataDir)
	if err != nil {
		packageStatuses = []extensions.Status{}
	}
	unmanagedStatuses, err := piagent.UnmanagedExtensionStatuses(agent.DataDir)
	if err != nil {
		unmanagedStatuses = []extensions.Status{}
	}
	mcpStatuses, err := piagent.MCPServerStatuses(agent.DataDir)
	if err != nil {
		mcpStatuses = []extensions.Status{}
	}

	enabled := agent.Recommended["rtk"] && piagent.RTKMaterialized(agent.DataDir)
	dcgEnabled := agent.Recommended["dcg"] && piagent.DCGMaterialized(agent.DataDir)
	installed := false
	browserStatusVersion := ""
	if profile, found := cfg.Agent(agent.ID); found {
		installed = piagent.BrowserNativeInstalled(profile.DataDir)
		if installed {
			browserStatusVersion = piagent.BrowserNativeVersion(profile.DataDir)
		}
	}
	browserStatus := extensions.BrowserNativeStatusForAgent(installed)
	browserStatus.Version = browserStatusVersion
	if installed {
		browserStatus.SourcePath = piagent.BrowserNativeDir(agent.DataDir)
	}
	figmaInstalled := agent.Recommended["figma"] && piagent.PiMCPAdapterInstalled(agent.DataDir) && piagent.FigmaMCPConfigured(agent.DataDir)
	figmaStatus := extensions.PiFigmaStatusForAgent(figmaInstalled)
	figmaStatus.Version = piagent.PiMCPAdapterVersion(agent.DataDir)
	if piagent.PiMCPAdapterInstalled(agent.DataDir) {
		figmaStatus.SourcePath = piagent.PiMCPAdapterDir(agent.DataDir)
	}
	recommended := []extensions.Status{
		extensions.PiPluginsStatusForAgent(packageStatuses),
		extensions.RTKStatusForAgent(enabled),
		extensions.DCGStatusForAgent(dcgEnabled),
		browserStatus,
		figmaStatus,
	}
	return extensions.AgentExtensionStatuses{
		Builtins:    builtins,
		Recommended: recommended,
		Packages:    packageStatuses,
		Directory:   unmanagedStatuses,
		MCP:         mcpStatuses,
	}
}

type InstallAgentExtensionRequest struct {
	AgentID string `json:"agentId"`
	Command string `json:"command"`
}

type AgentExtensionKeyRequest struct {
	AgentID string `json:"agentId"`
	Key     string `json:"key"`
}

// AgentExtensionResult reports the outcome of a command scoped to an agent.
type AgentExtensionResult struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Command string `json:"command"`
	Output  string `json:"output"`
}

// streamAgentCommand runs a command scoped to an agent while emitting live log
// lines to the frontend through the "install:log" event. This means long-running
// installs (for example downloading Playwright browser binaries) show progress
// instead of appearing frozen. The caller is responsible for emitting the
// "install:start"/"install:done" wrapping events.
func (a *App) streamAgentCommand(agentID, command string) AgentExtensionResult {
	cfg := a.store.Get()
	profile, found := cfg.Agent(agentID)
	if !found {
		return AgentExtensionResult{Success: false, Command: command, Output: "", Message: fmt.Sprintf("agent not found: %s", agentID)}
	}
	a.store.EnsureAgentDataDirs(&cfg)
	profile, _ = cfg.Agent(agentID)
	emit := func(line string) {
		application.Get().Event.Emit("install:log", map[string]any{
			"agentId": agentID,
			"line":    line,
		})
	}
	source, parseErr := piagent.ParsePiInstallCommand(command)
	if parseErr != nil {
		return AgentExtensionResult{Success: false, Command: command, Message: parseErr.Error()}
	}
	if reason := extensions.AgentPackageUnsupportedReason(source, runtime.GOOS); reason != "" {
		return AgentExtensionResult{Success: false, Command: command, Message: reason}
	}
	emit("> " + command)
	out, err := piagent.InstallAgentPackageWithProgress(profile.DataDir, source, emit)
	success := err == nil
	res := AgentExtensionResult{Success: success, Command: command, Output: out, Message: "命令已执行"}
	if err != nil {
		res.Message = fmt.Sprintf("安装执行失败: %v", err)
	}
	return res
}

// InstallAgentExtension runs a pi-install style command scoped to a single
// agent. PI_CODING_AGENT_DIR is pointed at the agent's data directory so the
// extension is installed into that agent only.
func (a *App) InstallAgentExtension(req InstallAgentExtensionRequest) (AgentExtensionResult, error) {
	cfg := a.store.Get()
	if _, found := cfg.Agent(req.AgentID); !found {
		return AgentExtensionResult{}, fmt.Errorf("agent not found: %s", req.AgentID)
	}
	a.store.EnsureAgentDataDirs(&cfg)
	application.Get().Event.Emit("install:start", map[string]any{
		"agentId": req.AgentID,
		"title":   installTerminalName() + " · Agent 扩展安装",
	})
	res := a.streamAgentCommand(req.AgentID, req.Command)
	application.Get().Event.Emit("install:done", map[string]any{
		"agentId": req.AgentID,
		"success": res.Success,
	})
	return res, nil
}

// UninstallAgentExtension removes a Pi extension previously installed into the
// agent's isolated extensions directory.
func (a *App) UninstallAgentExtension(req AgentExtensionKeyRequest) (AgentExtensionResult, error) {
	cfg := a.store.Get()
	profile, found := cfg.Agent(req.AgentID)
	if !found {
		return AgentExtensionResult{}, fmt.Errorf("agent not found: %s", req.AgentID)
	}
	a.store.EnsureAgentDataDirs(&cfg)
	profile, _ = cfg.Agent(req.AgentID)
	statuses, err := piagent.InstalledPackageStatuses(profile.DataDir)
	if err != nil {
		return AgentExtensionResult{}, err
	}
	source := ""
	sourcePath := ""
	if req.Key == "browser-native" || req.Key == "pi-plugins" {
		packageName := "pi-agent-browser-native"
		fallbackSource := "npm:pi-agent-browser-native"
		if req.Key == "pi-plugins" {
			packageName = "@nklisch/pi-plugins"
			fallbackSource = extensions.PiPluginsPackageSource
		}
		for _, status := range statuses {
			if status.Name == packageName || status.Key == fallbackSource {
				source = status.Key
				sourcePath = status.SourcePath
				break
			}
		}
		if source == "" {
			source = fallbackSource
			if req.Key == "browser-native" {
				sourcePath = piagent.BrowserNativeDir(profile.DataDir)
			}
		}
	} else {
		for _, status := range statuses {
			if status.Key == req.Key {
				source = status.Key
				sourcePath = status.SourcePath
				break
			}
		}
		if source == "" {
			return AgentExtensionResult{}, fmt.Errorf("agent package is not configured: %s", req.Key)
		}
	}
	cmd := "pi uninstall " + source
	out, err := piagent.UninstallAgentPackage(profile.DataDir, source)
	if err != nil {
		return AgentExtensionResult{Success: false, Command: cmd, Output: out, Message: fmt.Sprintf("卸载失败: %v", err)}, nil
	}
	// Pi normally removes the installed files together with its settings entry.
	// Clean up a leftover exact package path if an older Pi version only removed
	// the settings entry.
	if sourcePath != "" {
		if rmErr := os.RemoveAll(sourcePath); rmErr != nil {
			out += "\n警告: 无法删除扩展目录: " + rmErr.Error()
		}
	}
	return AgentExtensionResult{Success: true, Command: cmd, Output: out, Message: "已卸载扩展"}, nil
}

// DeleteAgentExtensionDir removes an unmanaged extension directory from the
// agent's extensions folder. Pi auto-discovers every entry under extensions/,
// so an unmanaged extension (for example a manually copied ask-user) is loaded
// even though it never appears in CodingTo's managed lists; this gives the user
// a way to see and remove it. Managed extensions (builtin tools, system
// extensions, RTK bridge) are rejected so the runtime contract cannot be broken
// through this path.
func (a *App) DeleteAgentExtensionDir(req AgentExtensionKeyRequest) (AgentExtensionResult, error) {
	cfg := a.store.Get()
	profile, found := cfg.Agent(req.AgentID)
	if !found {
		return AgentExtensionResult{}, fmt.Errorf("agent not found: %s", req.AgentID)
	}
	key := strings.TrimSpace(req.Key)
	if key == "" || key == "." || key == ".." || strings.ContainsAny(key, "/\\") {
		return AgentExtensionResult{}, fmt.Errorf("invalid extension key: %s", req.Key)
	}
	if piagent.IsManagedExtension(key) {
		return AgentExtensionResult{}, fmt.Errorf("extension %s is managed by CodingTo and cannot be deleted here", key)
	}
	a.store.EnsureAgentDataDirs(&cfg)
	profile, _ = cfg.Agent(req.AgentID)
	target := filepath.Join(profile.DataDir, "extensions", key)
	if info, err := os.Stat(target); err != nil || !info.IsDir() {
		return AgentExtensionResult{}, fmt.Errorf("extension not found: %s", key)
	}
	if err := os.RemoveAll(target); err != nil {
		return AgentExtensionResult{Success: false, Message: fmt.Sprintf("删除失败: %v", err)}, nil
	}
	return AgentExtensionResult{Success: true, Message: "已删除扩展 " + key}, nil
}

func (a *App) ManageExtension(req extensions.ActionRequest) (extensions.ActionResult, error) {
	cfg := a.store.Get()
	if req.Action != "install" && req.Action != "uninstall" {
		return a.extensions.Manage(req, cfg.Extensions)
	}

	operation := "安装"
	if req.Action == "uninstall" {
		operation = "卸载"
	}
	installID := "global:" + req.Action + ":" + req.Key
	displayName := map[string]string{
		"rtk":           "RTK",
		"dcg":           "DCG",
		"agent-browser": "Agent Browser",
		"playwright":    "Playwright",
		"figma":         "Figma",
	}[req.Key]
	if displayName == "" {
		displayName = "全局扩展"
	}
	app := application.Get()
	app.Event.Emit("install:start", map[string]any{
		"installId": installID,
		"scope":     "global",
		"title":     displayName + " 全局" + operation,
	})
	result, err := a.extensions.ManageWithProgress(req, cfg.Extensions, func(line string) {
		app.Event.Emit("install:log", map[string]any{
			"installId": installID,
			"line":      line,
		})
	})
	if err != nil && strings.TrimSpace(result.Output) == "" {
		app.Event.Emit("install:log", map[string]any{
			"installId": installID,
			"line":      err.Error(),
		})
	}
	app.Event.Emit("install:done", map[string]any{
		"installId": installID,
		"success":   err == nil,
	})
	// 安装、卸载等操作会改变全局插件(Plugins 页)与 Agent 扩展状态，
	// 通知前端重新拉取快照，避免列表停留在安装前的旧数据。
	app.Event.Emit("extensions:changed", map[string]any{
		"key":    req.Key,
		"action": req.Action,
	})
	return result, err
}
