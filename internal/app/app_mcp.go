package app

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"

	"codingto/internal/applog"
	"codingto/internal/extensions"
	"codingto/internal/piagent"
	"github.com/wailsapp/wails/v3/pkg/application"
)

type GlobalPackageInstallRequest struct {
	Scope   string `json:"scope"`
	Package string `json:"package"`
}

type AgentMCPInstallRequest struct {
	AgentID string `json:"agentId"`
	Package string `json:"package"`
}

type ManualMCPRequest struct {
	Key      string            `json:"key"`
	Command  string            `json:"command"`
	Args     []string          `json:"args"`
	URL      string            `json:"url"`
	Env      map[string]string `json:"env"`
	AgentIDs []string          `json:"agentIds"`
}

type AgentMCPRemoveRequest struct {
	AgentID string `json:"agentId"`
	Key     string `json:"key"`
}

func installTerminalName() string {
	switch runtime.GOOS {
	case "windows":
		return "CMD"
	case "darwin":
		return "Terminal"
	default:
		return "Shell"
	}
}

func upsertGlobalPackage(items []extensions.GlobalPackage, item extensions.GlobalPackage) []extensions.GlobalPackage {
	for index := range items {
		if items[index].Package == item.Package {
			items[index] = item
			return items
		}
	}
	return append(items, item)
}

// InstallGlobalPackage installs and registers a user-supplied npm package in
// either the global MCP or global plugin inventory.
func (a *App) InstallGlobalPackage(req GlobalPackageInstallRequest) (extensions.ActionResult, error) {
	scope := strings.ToLower(strings.TrimSpace(req.Scope))
	if scope != "mcp" && scope != "plugin" {
		return extensions.ActionResult{}, fmt.Errorf("unsupported global package scope: %s", req.Scope)
	}
	packageName := strings.TrimSpace(req.Package)
	if err := extensions.ValidateNPMPackageName(packageName); err != nil {
		return extensions.ActionResult{}, err
	}
	installID := "global:" + scope + ":" + packageName
	app := application.Get()
	app.Event.Emit("install:start", map[string]any{
		"installId": installID,
		"scope":     "global",
		"title":     installTerminalName() + " · 全局安装 · " + packageName,
	})
	app.Event.Emit("install:log", map[string]any{"installId": installID, "line": "> npm install -g " + packageName})
	registration, result, err := a.extensions.InstallGlobalPackage(packageName, func(line string) {
		app.Event.Emit("install:log", map[string]any{"installId": installID, "line": line})
	})
	if err == nil && scope == "mcp" && registration.Command == "" {
		err = fmt.Errorf("npm package %s does not expose an executable MCP command", packageName)
	}
	if err == nil {
		cfg := a.store.Get()
		if scope == "mcp" {
			cfg.Extensions.GlobalMCP = upsertGlobalPackage(cfg.Extensions.GlobalMCP, registration)
		} else {
			cfg.Extensions.GlobalPlugins = upsertGlobalPackage(cfg.Extensions.GlobalPlugins, registration)
		}
		err = a.store.Save(cfg)
	}
	if err != nil {
		app.Event.Emit("install:log", map[string]any{"installId": installID, "line": "ERROR: " + err.Error()})
	}
	app.Event.Emit("install:done", map[string]any{"installId": installID, "success": err == nil})
	app.Event.Emit("extensions:changed", map[string]any{"key": packageName, "action": "install"})
	return result, err
}

// RemoveGlobalPackage uninstalls a user-supplied npm package from the shared
// Node runtime and drops it from the global MCP or plugin inventory. The global
// uninstall is best-effort: if the package is already gone from the global scope
// we still remove the stale registration so the list stays accurate.
func (a *App) RemoveGlobalPackage(req GlobalPackageInstallRequest) (extensions.ActionResult, error) {
	scope := strings.ToLower(strings.TrimSpace(req.Scope))
	if scope != "mcp" && scope != "plugin" {
		return extensions.ActionResult{}, fmt.Errorf("unsupported global package scope: %s", req.Scope)
	}
	packageName := strings.TrimSpace(req.Package)
	if packageName == "" {
		return extensions.ActionResult{}, fmt.Errorf("package name is required")
	}
	if err := extensions.ValidateNPMPackageName(packageName); err != nil {
		return extensions.ActionResult{}, err
	}
	installID := "global:" + scope + ":remove:" + packageName
	app := application.Get()
	app.Event.Emit("install:start", map[string]any{
		"installId": installID,
		"scope":     "global",
		"operation": "uninstall",
		"title":     installTerminalName() + " · 全局卸载 · " + packageName,
	})
	app.Event.Emit("install:log", map[string]any{"installId": installID, "line": "> npm uninstall -g " + packageName})
	result, uninstallErr := a.extensions.UninstallGlobalPackage(packageName, func(line string) {
		app.Event.Emit("install:log", map[string]any{"installId": installID, "line": line})
	})
	if uninstallErr != nil {
		message := "全局插件卸载失败，插件仍保留在列表中，请查看应用日志"
		app.Event.Emit("install:log", map[string]any{"installId": installID, "line": message})
		app.Event.Emit("install:done", map[string]any{"installId": installID, "operation": "uninstall", "success": false})
		result.Message = message
		return result, errors.New(message)
	}
	cfg := a.store.Get()
	if scope == "mcp" {
		cfg.Extensions.GlobalMCP = removeGlobalPackage(cfg.Extensions.GlobalMCP, packageName)
	} else {
		cfg.Extensions.GlobalPlugins = removeGlobalPackage(cfg.Extensions.GlobalPlugins, packageName)
	}
	if err := a.store.Save(cfg); err != nil {
		applog.Errorf("save global package removal %s/%s: %v", scope, packageName, err)
		message := "插件已从系统卸载，但列表状态保存失败，请刷新后重试"
		app.Event.Emit("install:log", map[string]any{"installId": installID, "line": message})
		app.Event.Emit("install:done", map[string]any{"installId": installID, "operation": "uninstall", "success": false})
		app.Event.Emit("extensions:changed", map[string]any{"key": packageName, "action": "uninstall"})
		return extensions.ActionResult{Message: message, Command: result.Command, Output: result.Output}, errors.New(message)
	}
	app.Event.Emit("install:done", map[string]any{"installId": installID, "operation": "uninstall", "success": true})
	app.Event.Emit("extensions:changed", map[string]any{"key": packageName, "action": "uninstall"})
	result.Message = "已从列表移除并卸载"
	return result, nil
}

func removeGlobalPackage(items []extensions.GlobalPackage, name string) []extensions.GlobalPackage {
	filtered := make([]extensions.GlobalPackage, 0, len(items))
	for _, item := range items {
		if item.Package != name {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

// InstallAgentMCP installs the shared MCP runtime, ensures the agent's Pi MCP
// adapter is present, and registers the server in that agent's mcp.json.
func (a *App) InstallAgentMCP(req AgentMCPInstallRequest) (extensions.ActionResult, error) {
	packageName := strings.TrimSpace(req.Package)
	if err := extensions.ValidateNPMPackageName(packageName); err != nil {
		return extensions.ActionResult{}, err
	}
	cfg := a.store.Get()
	agent, found := cfg.Agent(req.AgentID)
	if !found {
		return extensions.ActionResult{}, fmt.Errorf("agent not found: %s", req.AgentID)
	}
	a.store.EnsureAgentDataDirs(&cfg)
	agent, _ = cfg.Agent(req.AgentID)
	installID := "agent-mcp:" + req.AgentID + ":" + packageName
	app := application.Get()
	app.Event.Emit("install:start", map[string]any{
		"installId": installID,
		"agentId":   req.AgentID,
		"scope":     "agent",
		"title":     installTerminalName() + " · Agent MCP 安装 · " + packageName,
	})
	emit := func(line string) {
		app.Event.Emit("install:log", map[string]any{"installId": installID, "agentId": req.AgentID, "line": line})
	}
	emit("> npm install -g " + packageName)
	registration, globalResult, err := a.extensions.InstallGlobalPackage(packageName, emit)
	if err == nil && registration.Command == "" {
		err = fmt.Errorf("npm package %s does not expose an executable MCP command", packageName)
	}
	if err == nil && !piagent.PiMCPAdapterInstalled(agent.DataDir) {
		emit("> pi install npm:pi-mcp-adapter")
		_, err = piagent.InstallAgentPackageWithProgress(agent.DataDir, "npm:pi-mcp-adapter", emit)
	}
	if err == nil {
		key := registration.Command
		if key == "" {
			key = registration.Name
		}
		err = piagent.UpsertMCPServer(agent.DataDir, key, registration.Command, registration.Args)
	}
	if err == nil {
		err = a.store.Save(cfg)
	}
	if err != nil {
		emit("ERROR: " + err.Error())
	}
	app.Event.Emit("install:done", map[string]any{"installId": installID, "agentId": req.AgentID, "success": err == nil})
	app.Event.Emit("extensions:changed", map[string]any{"key": packageName, "action": "install", "agentId": req.AgentID})
	if err != nil {
		return globalResult, err
	}
	globalResult.Message = "Agent MCP installed"
	globalResult.Command += "\npi install npm:pi-mcp-adapter"
	return globalResult, nil
}

// AddManualMCP writes a user-supplied MCP server configuration into one or more
// agents' mcp.json files. It supports both stdio (command+args+env) and remote
// (url) transport types.
func (a *App) AddManualMCP(req ManualMCPRequest) (extensions.ActionResult, error) {
	key := strings.TrimSpace(req.Key)
	if key == "" {
		return extensions.ActionResult{}, fmt.Errorf("MCP server name is required")
	}
	if strings.TrimSpace(req.Command) == "" && strings.TrimSpace(req.URL) == "" {
		return extensions.ActionResult{}, fmt.Errorf("either command or url is required")
	}
	if len(req.AgentIDs) == 0 {
		return extensions.ActionResult{}, fmt.Errorf("at least one target agent is required")
	}
	cfg := a.store.Get()
	installID := "manual-mcp:" + key
	app := application.Get()
	app.Event.Emit("install:start", map[string]any{
		"installId": installID,
		"scope":     "manual-mcp",
		"title":     installTerminalName() + " · 手动 MCP · " + key,
	})
	emit := func(line string) {
		app.Event.Emit("install:log", map[string]any{"installId": installID, "line": line})
	}
	serverCfg := piagent.ManualMCPServerConfig{
		Command: req.Command,
		Args:    req.Args,
		URL:     req.URL,
		Env:     req.Env,
	}
	var lastErr error
	installed := 0
	for _, agentID := range req.AgentIDs {
		agent, found := cfg.Agent(agentID)
		if !found {
			emit("WARN: agent not found: " + agentID)
			continue
		}
		a.store.EnsureAgentDataDirs(&cfg)
		agent, _ = cfg.Agent(agentID)
		if !piagent.PiMCPAdapterInstalled(agent.DataDir) {
			emit("> pi install npm:pi-mcp-adapter (" + agent.Name + ")")
			if _, err := piagent.InstallAgentPackageWithProgress(agent.DataDir, "npm:pi-mcp-adapter", emit); err != nil {
				emit("WARN: pi-mcp-adapter install failed for " + agent.Name + ": " + err.Error())
			}
		}
		emit("> 写入 " + agent.Name + " / mcp.json")
		if err := piagent.UpsertManualMCPServer(agent.DataDir, key, serverCfg); err != nil {
			emit("ERROR: " + agent.Name + ": " + err.Error())
			lastErr = err
			continue
		}
		installed++
	}
	if installed == 0 && lastErr != nil {
		app.Event.Emit("install:done", map[string]any{"installId": installID, "success": false})
		return extensions.ActionResult{}, lastErr
	}
	if err := a.store.Save(cfg); err != nil {
		app.Event.Emit("install:done", map[string]any{"installId": installID, "success": false})
		return extensions.ActionResult{}, err
	}
	app.Event.Emit("install:done", map[string]any{"installId": installID, "success": true})
	app.Event.Emit("extensions:changed", map[string]any{"key": key, "action": "install"})
	return extensions.ActionResult{Message: fmt.Sprintf("MCP server %q added to %d agent(s)", key, installed)}, nil
}

// RemoveAgentMCPServer removes one MCP server entry from a single agent's
// mcp.json. It does not uninstall any npm package.
func (a *App) RemoveAgentMCPServer(req AgentMCPRemoveRequest) (extensions.ActionResult, error) {
	key := strings.TrimSpace(req.Key)
	if key == "" {
		return extensions.ActionResult{}, fmt.Errorf("MCP server key is required")
	}
	cfg := a.store.Get()
	agent, found := cfg.Agent(req.AgentID)
	if !found {
		return extensions.ActionResult{}, fmt.Errorf("agent not found: %s", req.AgentID)
	}
	a.store.EnsureAgentDataDirs(&cfg)
	agent, _ = cfg.Agent(req.AgentID)
	if err := piagent.RemoveMCPServer(agent.DataDir, key); err != nil {
		return extensions.ActionResult{}, err
	}
	app := application.Get()
	app.Event.Emit("extensions:changed", map[string]any{"key": key, "action": "uninstall", "agentId": req.AgentID})
	return extensions.ActionResult{Message: fmt.Sprintf("MCP server %q removed from agent", key)}, nil
}

// SaveFigmaConfig persists the shared authorization catalog. Individual Pi
// agents consume the selected token through their process environment.
func (a *App) SaveFigmaConfig(cfg extensions.FigmaConfig) (extensions.Snapshot, error) {
	cfg.Normalize()
	if authorization, ok := cfg.ActiveAuthorization(); ok {
		if err := extensions.ValidateFigmaAuthorization(context.Background(), authorization); err != nil {
			return a.extensions.Snapshot(a.store.Get().Extensions), fmt.Errorf("validate %s: %w", authorization.Name, err)
		}
	}
	appConfig := a.store.Get()
	appConfig.Extensions.Figma = cfg
	appConfig.Normalize()
	if err := a.store.Save(appConfig); err != nil {
		return extensions.Snapshot{}, err
	}
	return a.extensions.Snapshot(appConfig.Extensions), nil
}
