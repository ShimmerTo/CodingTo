package extensions

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"codingto/internal/applog"
)

// FigmaPackage is installed once as a global runtime dependency. Individual Pi
// agents connect to it through their own pi-mcp-adapter stdio client.
const FigmaPackage = "figma-developer-mcp"

// AgentBrowserPackage is the shared browser runtime used by every isolated
// pi-agent-browser-native extension.
const AgentBrowserPackage = "agent-browser"

// PlaywrightPackage is the globally installed Playwright toolkit shared by all
// agents. It is installed once (npm -g plus a Chromium download) and only its
// version is surfaced on per-agent extension cards.
const PlaywrightPackage = "playwright"

// playwrightDownloadMirror mirrors the default used for agent commands in
// internal/piagent/command.go: the official Playwright CDN can be extremely
// slow or blocked on some networks.
const playwrightDownloadMirror = "https://cdn.npmmirror.com/binaries/playwright"

type Config struct {
	Figma         FigmaConfig     `json:"figma"`
	GlobalMCP     []GlobalPackage `json:"globalMcp"`
	GlobalPlugins []GlobalPackage `json:"globalPlugins"`
}

type GlobalPackage struct {
	Package string   `json:"package"`
	Name    string   `json:"name"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

// FigmaConfig holds the global Figma runtime and authorization settings.
// Agents only persist a reference/enable flag; tokens never enter agent dirs.
type FigmaConfig struct {
	Enabled               bool                 `json:"enabled"`
	ActiveAuthorizationID string               `json:"activeAuthorizationId"`
	Authorizations        []FigmaAuthorization `json:"authorizations"`
	// APIKey is retained only long enough to migrate the former single-token
	// configuration. Normalize moves it into Authorizations and clears it.
	APIKey string `json:"apiKey,omitempty"`
}

// FigmaAuthorization is one named Figma identity. PAT is the normal local-tool
// flow; OAuth accepts an access token obtained by an external OAuth client.
type FigmaAuthorization struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Token     string `json:"token"`
	TokenType string `json:"tokenType"`
}

type Status struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Homepage    string `json:"homepage"`
	InstallHint string `json:"installHint"`
	Installed   bool   `json:"installed"`
	Enabled     bool   `json:"enabled"`
	Running     bool   `json:"running"`
	Version     string `json:"version"`
	PID         int    `json:"pid"`
	SourcePath  string `json:"sourcePath"`
	Error       string `json:"error"`
}

const PiPluginsPackageSource = "npm:@nklisch/pi-plugins"

// AgentPackageUnsupportedReason returns a user-facing explanation when a
// recommended Pi package must not be installed on the target operating system.
// Keep this guard in the backend as well as the UI so future automatic install
// paths cannot accidentally install an unsupported package.
func AgentPackageUnsupportedReason(source, goos string) string {
	normalizedSource := strings.TrimSpace(source)
	isPiPlugins := normalizedSource == PiPluginsPackageSource ||
		strings.HasPrefix(normalizedSource, PiPluginsPackageSource+"@")
	if isPiPlugins && strings.EqualFold(strings.TrimSpace(goos), "windows") {
		return "Windows 暂不支持 Pi 插件市场"
	}
	return ""
}

// BuiltinToolStatus reports the bundled (current) versus installed version of a
// builtin tool for a single agent, so the UI can show a version and offer an
// update when the installed copy is stale relative to default_tools.
type BuiltinToolStatus struct {
	Key              string `json:"key"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	Required         bool   `json:"required"`
	Installed        bool   `json:"installed"`
	CurrentVersion   string `json:"currentVersion"`
	InstalledVersion string `json:"installedVersion"`
}

type Snapshot struct {
	Tools          []Status                       `json:"tools"`
	Figma          FigmaStatus                    `json:"figma"`
	BuiltinCatalog []BuiltinToolStatus            `json:"builtinCatalog"`
	Builtins       map[string][]BuiltinToolStatus `json:"builtins"`
	// Recommended holds per-agent recommended extension status keyed by agent ID
	// (e.g. RTK). Each agent enables them independently in its own data dir.
	Recommended map[string][]Status `json:"recommended"`
	// Packages is the complete per-agent inventory persisted by `pi install`.
	// Unlike Recommended, it is not limited to extensions known by CodingTo.
	Packages map[string][]Status `json:"packages"`
	// Directory lists, per agent, the extension directories physically present
	// under extensions/ that are not owned by CodingTo (unmanaged). These are
	// auto-discovered by Pi and may include blocking extensions the user is not
	// aware of, so the UI surfaces them for review and removal.
	Directory map[string][]Status `json:"directory"`
	MCP       map[string][]Status `json:"mcp"`
	// GlobalMCP and GlobalPlugins are user-installed npm packages registered in
	// their corresponding global scope.
	GlobalMCP     []Status `json:"globalMcp"`
	GlobalPlugins []Status `json:"globalPlugins"`
}

// AgentExtensionStatuses holds the extension statuses for a single agent,
// returned by a targeted refresh that avoids re-scanning every agent.
type AgentExtensionStatuses struct {
	Builtins    []BuiltinToolStatus `json:"builtins"`
	Recommended []Status            `json:"recommended"`
	Packages    []Status            `json:"packages"`
	Directory   []Status            `json:"directory"`
	MCP         []Status            `json:"mcp"`
}

// FigmaStatus reports whether the Figma MCP package is available, authorized and
// running so the UI can present a single coherent management card.
type FigmaStatus struct {
	Installed               bool   `json:"installed"`
	Enabled                 bool   `json:"enabled"`
	Running                 bool   `json:"running"`
	PID                     int    `json:"pid"`
	HasToken                bool   `json:"hasToken"`
	Version                 string `json:"version"`
	AuthorizationCount      int    `json:"authorizationCount"`
	ActiveAuthorizationName string `json:"activeAuthorizationName"`
}

type ActionRequest struct {
	Key    string `json:"key"`
	Action string `json:"action"`
}

type ActionResult struct {
	Message string `json:"message"`
	Command string `json:"command"`
	Output  string `json:"output"`
}

// fileLogger is the small subset of the shared application logger the
// manager needs. The concrete type is applog's *gstool.GsSlog; an interface
// keeps the extensions package free of the logging implementation.
type fileLogger interface {
	Infof(format string, args ...any)
	Errof(format string, args ...any)
}

type Manager struct {
	log fileLogger

	// cached detection results so repeated Snapshot() calls don't re-run slow
	// external commands (npx/npm/etc.) on every UI refresh.
	detectMu     sync.Mutex
	figmaCache   *binaryDetect
	figmaCacheAt time.Time
	// agent-browser can be a Node shim, so cache its version probe too.
	agentBrowserCache   *binaryDetect
	agentBrowserCacheAt time.Time
	// playwright is a Node shim as well; probing it spawns node, so cache it.
	playwrightCache   *binaryDetect
	playwrightCacheAt time.Time
}

type binaryDetect struct {
	installed bool
	version   string
}

// detectionCacheTTL bounds how long a cached detection result stays fresh.
const detectionCacheTTL = 60 * time.Second

// detectionTimeout bounds how long a single detection command may block.
const detectionTimeout = 8 * time.Second

var npmPackageNamePattern = regexp.MustCompile(`^(?:@[A-Za-z0-9._-]+/)?[A-Za-z0-9._-]+$`)

func NewManager() *Manager {
	return &Manager{
		log: applog.Get(),
	}
}

func DefaultConfig() Config {
	return Config{
		Figma: FigmaConfig{
			Enabled:        false,
			Authorizations: []FigmaAuthorization{},
		},
		GlobalMCP:     []GlobalPackage{},
		GlobalPlugins: []GlobalPackage{},
	}
}

func (c *Config) Normalize() {
	c.Figma.Normalize()
	c.GlobalMCP = normalizeGlobalPackages(c.GlobalMCP)
	c.GlobalPlugins = normalizeGlobalPackages(c.GlobalPlugins)
}

func normalizeGlobalPackages(packages []GlobalPackage) []GlobalPackage {
	seen := map[string]bool{}
	result := make([]GlobalPackage, 0, len(packages))
	for _, item := range packages {
		item.Package = strings.TrimSpace(item.Package)
		item.Name = strings.TrimSpace(item.Name)
		item.Command = strings.TrimSpace(item.Command)
		if item.Package == "" || seen[item.Package] {
			continue
		}
		if item.Name == "" {
			item.Name = item.Package
		}
		if item.Args == nil {
			item.Args = []string{}
		}
		seen[item.Package] = true
		result = append(result, item)
	}
	return result
}

func (c *FigmaConfig) Normalize() {
	legacyToken := strings.TrimSpace(c.APIKey)
	c.APIKey = ""
	if c.Authorizations == nil {
		c.Authorizations = []FigmaAuthorization{}
	}
	if legacyToken != "" && len(c.Authorizations) == 0 {
		c.Authorizations = append(c.Authorizations, FigmaAuthorization{
			ID: "figma-default", Name: "Default", Token: legacyToken, TokenType: "pat",
		})
	}

	seen := make(map[string]bool, len(c.Authorizations))
	normalized := make([]FigmaAuthorization, 0, len(c.Authorizations))
	for index, authorization := range c.Authorizations {
		authorization.ID = strings.TrimSpace(authorization.ID)
		authorization.Name = strings.TrimSpace(authorization.Name)
		authorization.Token = strings.TrimSpace(authorization.Token)
		authorization.TokenType = strings.ToLower(strings.TrimSpace(authorization.TokenType))
		if authorization.ID == "" {
			authorization.ID = fmt.Sprintf("figma-%d", index+1)
		}
		if authorization.Name == "" {
			authorization.Name = fmt.Sprintf("Figma %d", index+1)
		}
		if authorization.TokenType != "oauth" {
			authorization.TokenType = "pat"
		}
		if authorization.Token == "" || seen[authorization.ID] {
			continue
		}
		seen[authorization.ID] = true
		normalized = append(normalized, authorization)
	}
	c.Authorizations = normalized
	if _, ok := c.ActiveAuthorization(); !ok {
		if len(c.Authorizations) > 0 {
			c.ActiveAuthorizationID = c.Authorizations[0].ID
		} else {
			c.ActiveAuthorizationID = ""
			c.Enabled = false
		}
	}
}

func (c FigmaConfig) ActiveAuthorization() (FigmaAuthorization, bool) {
	for _, authorization := range c.Authorizations {
		if authorization.ID == c.ActiveAuthorizationID && strings.TrimSpace(authorization.Token) != "" {
			return authorization, true
		}
	}
	return FigmaAuthorization{}, false
}

func (m *Manager) Snapshot(cfg Config) Snapshot {
	cfg.Normalize()

	figmaInstalled, figmaVersion := m.cachedFigma()
	activeAuthorization, hasAuthorization := cfg.Figma.ActiveAuthorization()
	figmaStatus := FigmaStatus{
		Installed:               figmaInstalled,
		Enabled:                 cfg.Figma.Enabled,
		Running:                 false,
		PID:                     0,
		HasToken:                hasAuthorization,
		Version:                 figmaVersion,
		AuthorizationCount:      len(cfg.Figma.Authorizations),
		ActiveAuthorizationName: activeAuthorization.Name,
	}

	return Snapshot{
		Tools:         []Status{RTKGlobalStatus(), m.agentBrowserStatus(), m.playwrightStatus()},
		Figma:         figmaStatus,
		GlobalMCP:     GlobalPackageStatuses(cfg.GlobalMCP),
		GlobalPlugins: GlobalPackageStatuses(cfg.GlobalPlugins),
	}
}

func ValidateNPMPackageName(packageName string) error {
	packageName = strings.TrimSpace(packageName)
	if packageName == "" || !npmPackageNamePattern.MatchString(packageName) {
		return fmt.Errorf("invalid npm package name: %s", packageName)
	}
	return nil
}

// InstallGlobalPackage installs one npm package into the shared Node runtime
// and returns the registration metadata needed to list it later. MCP packages
// use the discovered npm bin as their default stdio command.
func (m *Manager) InstallGlobalPackage(packageName string, onLine func(string)) (GlobalPackage, ActionResult, error) {
	packageName = strings.TrimSpace(packageName)
	if err := ValidateNPMPackageName(packageName); err != nil {
		m.errf("install global package %s: invalid name: %v", packageName, err)
		return GlobalPackage{}, ActionResult{}, err
	}
	npm, err := npmExecutable()
	if err != nil {
		m.errf("install global package %s: npm not found", packageName)
		return GlobalPackage{}, ActionResult{}, errors.New("npm was not found; install Node.js first")
	}
	commandText := "npm install -g " + packageName
	output, err := runUnboundedWithProgress(onLine, npm, "install", "-g", packageName)
	if err != nil {
		m.errf("install global package %s failed: %v", packageName, err)
		return GlobalPackage{}, ActionResult{Message: "Global package installation failed", Command: commandText, Output: output}, err
	}
	registration, metaErr := globalPackageRegistration(npm, packageName)
	if metaErr != nil {
		m.errf("install global package %s: metadata read failed: %v", packageName, metaErr)
		return GlobalPackage{}, ActionResult{Message: "Package installed but metadata could not be read", Command: commandText, Output: output}, metaErr
	}
	m.infof("global package installed: %s", packageName)
	return registration, ActionResult{Message: "Global package installed", Command: commandText, Output: output}, nil
}

// UninstallGlobalPackage removes one npm package from the shared Node runtime.
// Failures are surfaced to the caller so callers can decide whether to drop the
// registration anyway (e.g. a package already gone from the global scope).
func (m *Manager) UninstallGlobalPackage(packageName string, onLine func(string)) (ActionResult, error) {
	packageName = strings.TrimSpace(packageName)
	if err := ValidateNPMPackageName(packageName); err != nil {
		m.errf("uninstall global package %s: invalid name: %v", packageName, err)
		return ActionResult{}, err
	}
	npm, err := npmExecutable()
	if err != nil {
		m.errf("uninstall global package %s: npm not found", packageName)
		return ActionResult{}, errors.New("npm was not found; install Node.js first")
	}
	commandText := "npm uninstall -g " + packageName
	output, err := runUnboundedWithProgress(onLine, npm, "uninstall", "-g", packageName)
	if err != nil {
		m.errf("uninstall global package %s failed: %v", packageName, err)
		return ActionResult{Message: "Global package uninstall failed", Command: commandText, Output: output}, err
	}
	m.infof("global package uninstalled: %s", packageName)
	return ActionResult{Message: "Global package uninstalled", Command: commandText, Output: output}, nil
}

func globalPackageRegistration(npm, packageName string) (GlobalPackage, error) {
	root, err := run(npm, "root", "-g")
	if err != nil {
		return GlobalPackage{}, fmt.Errorf("locate global npm packages: %w", err)
	}
	packageDir := filepath.Join(strings.TrimSpace(root), filepath.FromSlash(packageName))
	raw, err := os.ReadFile(filepath.Join(packageDir, "package.json"))
	if err != nil {
		return GlobalPackage{}, fmt.Errorf("read installed package metadata: %w", err)
	}
	var manifest struct {
		Name string          `json:"name"`
		Bin  json.RawMessage `json:"bin"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return GlobalPackage{}, fmt.Errorf("parse installed package metadata: %w", err)
	}
	command := packageCommand(manifest.Bin, packageName)
	return GlobalPackage{
		Package: packageName,
		Name:    strings.TrimSpace(manifest.Name),
		Command: command,
		Args:    []string{},
	}, nil
}

func packageCommand(raw json.RawMessage, packageName string) string {
	base := packageName
	if slash := strings.LastIndex(base, "/"); slash >= 0 {
		base = base[slash+1:]
	}
	var command string
	if json.Unmarshal(raw, &command) == nil && strings.TrimSpace(command) != "" {
		return base
	}
	var commands map[string]string
	if json.Unmarshal(raw, &commands) == nil && len(commands) > 0 {
		if _, ok := commands[base]; ok {
			return base
		}
		keys := make([]string, 0, len(commands))
		for key := range commands {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		return keys[0]
	}
	return ""
}

func GlobalPackageStatuses(packages []GlobalPackage) []Status {
	statuses := make([]Status, 0, len(packages))
	globalRoot := ""
	if npm, err := npmExecutable(); err == nil {
		if root, rootErr := run(npm, "root", "-g"); rootErr == nil {
			globalRoot = strings.TrimSpace(root)
		}
	}
	for _, item := range packages {
		status := Status{
			Key:         item.Package,
			Name:        item.Name,
			Description: item.Command,
			InstallHint: "npm install -g " + item.Package,
		}
		if globalRoot != "" {
			source := filepath.Join(globalRoot, filepath.FromSlash(item.Package))
			if raw, readErr := os.ReadFile(filepath.Join(source, "package.json")); readErr == nil {
				var manifest struct {
					Version string `json:"version"`
				}
				if json.Unmarshal(raw, &manifest) == nil {
					status.Installed = true
					status.Enabled = true
					status.Version = manifest.Version
					status.SourcePath = source
				}
			}
		}
		statuses = append(statuses, status)
	}
	return statuses
}

// RTKGlobalStatus reports the shared RTK binary. Agent-specific state is the
// generated Pi bridge and is reported separately in Recommended.
func RTKGlobalStatus() Status {
	installed, version := detectRTK()
	sourcePath := ""
	if path, err := RTKExecutable(); err == nil {
		sourcePath, _ = filepath.Abs(path)
	}
	return Status{
		Key:         "rtk",
		Name:        "RTK (Rust Token Killer)",
		Description: "RTK filters and compresses terminal command output before it reaches the LLM context — a single Rust binary, 100+ supported commands, under 10ms overhead.",
		Homepage:    "https://github.com/rtk-ai/rtk",
		InstallHint: rtkInstallHint(),
		Installed:   installed,
		Enabled:     installed,
		Version:     version,
		SourcePath:  sourcePath,
	}
}

// AgentBrowserStatus reports the globally shared browser runtime. The Pi
// wrapper itself remains installed independently for each agent.
func (m *Manager) agentBrowserStatus() Status {
	installed, version := m.cachedAgentBrowser()
	sourcePath := ""
	if installed {
		if path, err := exec.LookPath(AgentBrowserPackage); err == nil {
			sourcePath, _ = filepath.Abs(path)
		}
	}
	return Status{
		Key:         AgentBrowserPackage,
		Name:        "Agent Browser",
		Description: "Browser automation runtime for AI agents; drives Chromium to navigate, interact, and extract pages.",
		Homepage:    "https://agent-browser.dev/",
		InstallHint: "npm install -g agent-browser",
		Installed:   installed,
		Enabled:     installed,
		Version:     version,
		SourcePath:  sourcePath,
	}
}

// playwrightStatus reports the globally installed Playwright toolkit. It is a
// shared, agent-independent plugin: agents (and the browser-native extension)
// only ever read its version, while installation and updates are managed from
// the global Plugins page.
func (m *Manager) playwrightStatus() Status {
	installed, version := m.cachedPlaywright()
	sourcePath := ""
	if installed {
		if path, err := playwrightExecutable(); err == nil {
			sourcePath, _ = filepath.Abs(path)
		}
	}
	return Status{
		Key:         PlaywrightPackage,
		Name:        "Playwright",
		Description: "Global browser automation toolkit; ships the Chromium runtime shared by browser extensions across all agents.",
		Homepage:    "https://playwright.dev/",
		InstallHint: "npm install -g playwright && playwright install chromium",
		Installed:   installed,
		Enabled:     installed,
		Version:     version,
		SourcePath:  sourcePath,
	}
}

func (m *Manager) cachedPlaywright() (bool, string) {
	m.detectMu.Lock()
	defer m.detectMu.Unlock()
	if m.playwrightCache != nil && time.Since(m.playwrightCacheAt) < detectionCacheTTL {
		return m.playwrightCache.installed, m.playwrightCache.version
	}
	installed, version := detectPlaywright()
	m.playwrightCache = &binaryDetect{installed: installed, version: version}
	m.playwrightCacheAt = time.Now()
	return installed, version
}

// detectPlaywright probes the global playwright CLI. `playwright --version`
// prints "Version 1.62.0", so the prefix is stripped for a clean display.
func detectPlaywright() (bool, string) {
	path, err := playwrightExecutable()
	if err != nil {
		return false, ""
	}
	output, err := run(path, "--version")
	if err != nil {
		return false, ""
	}
	if line, _, ok := strings.Cut(strings.TrimSpace(output), "\n"); ok {
		output = line
	}
	version := strings.TrimSpace(output)
	version = strings.TrimSpace(strings.TrimPrefix(version, "Version"))
	return true, version
}

func playwrightExecutable() (string, error) {
	names := []string{PlaywrightPackage}
	if runtime.GOOS == "windows" {
		names = []string{PlaywrightPackage + ".cmd", PlaywrightPackage + ".exe", PlaywrightPackage}
	}
	for _, name := range names {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", exec.ErrNotFound
}

func (m *Manager) cachedAgentBrowser() (bool, string) {
	m.detectMu.Lock()
	defer m.detectMu.Unlock()
	if m.agentBrowserCache != nil && time.Since(m.agentBrowserCacheAt) < detectionCacheTTL {
		return m.agentBrowserCache.installed, m.agentBrowserCache.version
	}
	installed, version := detectVersion(AgentBrowserPackage)
	m.agentBrowserCache = &binaryDetect{installed: installed, version: version}
	m.agentBrowserCacheAt = time.Now()
	return installed, version
}

// RTKStatusForAgent reports RTK's per-agent status. The rtk binary is shared at
// the PATH level, while enabled reflects whether this specific agent has the RTK
// Pi bridge active (recommended flag on and materialized in its data dir).
func RTKStatusForAgent(enabled bool) Status {
	installed, version := detectRTK()
	sourcePath := ""
	if path, err := RTKExecutable(); err == nil {
		sourcePath, _ = filepath.Abs(path)
	}
	return Status{
		Key:         "rtk",
		Name:        "RTK (Rust Token Killer)",
		Description: "Filters and compresses terminal command output before it reaches the agent.",
		Homepage:    "https://github.com/rtk-ai/rtk",
		InstallHint: rtkInstallHint(),
		Installed:   installed,
		Enabled:     enabled,
		Version:     version,
		SourcePath:  sourcePath,
	}
}

// BrowserNativeStatusForAgent describes the Pi Agent Browser Native recommended
// extension for the given agent. Installation is driven by `pi install
// npm:pi-agent-browser-native`, which lands the extension in the agent's
// isolated extensions directory when PI_CODING_AGENT_DIR is set.
func BrowserNativeStatusForAgent(installed bool) Status {
	return Status{
		Key:         "browser-native",
		Name:        "Pi Agent Browser Native",
		Description: "Wraps the agent-browser automation runtime as a native Pi tool so the agent can drive a real browser.",
		Homepage:    "https://github.com/fitchmultz/pi-agent-browser-native",
		InstallHint: "pi install npm:pi-agent-browser-native",
		Installed:   installed,
	}
}

// PiPluginsStatusForAgent promotes the Pi-native plugin marketplace from the
// generic package inventory into CodingTo's recommended extension list.
func PiPluginsStatusForAgent(packages []Status) Status {
	status := Status{
		Key:         "pi-plugins",
		Name:        "Pi Plugins",
		Description: "Adds marketplace discovery and plugin lifecycle management for the current agent.",
		Homepage:    "https://github.com/nklisch/pi-plugins",
		InstallHint: "pi install " + PiPluginsPackageSource,
	}
	for _, installed := range packages {
		if installed.Key != PiPluginsPackageSource && installed.Name != "@nklisch/pi-plugins" {
			continue
		}
		status.Installed = installed.Installed
		status.Enabled = installed.Enabled
		status.Version = installed.Version
		status.SourcePath = installed.SourcePath
		status.Error = installed.Error
		break
	}
	return status
}

// PiFigmaStatusForAgent describes the per-agent MCP bridge. The shared Figma
// runtime and authorization remain visible on the global MCP page.
func PiFigmaStatusForAgent(installed bool) Status {
	return Status{
		Key:         "figma",
		Name:        "Pi Figma",
		Description: "Connects Figma design files to this agent through an MCP adapter so it can read layers, components, and styles.",
		Homepage:    "https://github.com/nicobailon/pi-mcp-adapter",
		Installed:   installed,
		Enabled:     installed,
	}
}

func (m *Manager) Manage(req ActionRequest, cfg Config) (ActionResult, error) {
	return m.ManageWithProgress(req, cfg, nil)
}

// infof/errf write into the shared application log file at ~/.codingto/logs.
// The shared logger is owned by applog; it can be nil only when the desktop
// app failed to initialize file logging, so both helpers are nil-safe.
func (m *Manager) infof(format string, args ...any) {
	if m.log != nil {
		m.log.Infof(format, args...)
	}
}

func (m *Manager) errf(format string, args ...any) {
	if m.log != nil {
		m.log.Errof(format, args...)
	}
}

// ManageWithProgress performs the same action as Manage and reports command
// output as it is produced. Global installs can take several minutes, so the
// application uses this callback to keep the install log dialog responsive.
func (m *Manager) ManageWithProgress(req ActionRequest, cfg Config, onLine func(string)) (ActionResult, error) {
	result, err := m.manage(req, cfg, onLine)
	if err != nil {
		m.errf("extension %s/%s failed: %v", req.Key, req.Action, err)
		return result, err
	}
	m.infof("extension %s/%s ok: %s", req.Key, req.Action, result.Message)
	return result, nil
}

func (m *Manager) manage(req ActionRequest, cfg Config, onLine func(string)) (ActionResult, error) {
	switch req.Key {
	case "rtk":
		if req.Action == "install" {
			output, err := installRTK(onLine)
			if err != nil {
				return ActionResult{Message: "RTK installation failed", Output: output}, err
			}
			return ActionResult{Message: "RTK installed", Output: output}, nil
		}
	case AgentBrowserPackage:
		if req.Action == "install" {
			output, err := m.installAgentBrowser(onLine)
			if err != nil {
				return ActionResult{Message: "Agent Browser installation failed", Output: output}, err
			}
			return ActionResult{Message: "Agent Browser installed", Output: output}, nil
		}
	case PlaywrightPackage:
		if req.Action == "install" {
			output, err := m.installPlaywright(onLine)
			if err != nil {
				return ActionResult{Message: "Playwright installation failed", Output: output}, err
			}
			return ActionResult{Message: "Playwright installed", Output: output}, nil
		}
	case "figma":
		switch req.Action {
		case "install":
			output, err := m.installFigma(onLine)
			if err != nil {
				return ActionResult{Message: "Figma MCP installation failed", Output: output}, err
			}
			return ActionResult{Message: "Figma MCP installed", Output: output}, nil
		}
	}
	return ActionResult{}, fmt.Errorf("unsupported extension action: %s/%s", req.Key, req.Action)
}

func installRTK(onLine func(string)) (string, error) {
	switch runtime.GOOS {
	case "windows":
		winget, err := exec.LookPath("winget")
		if err != nil {
			return "", errors.New("winget was not found; install RTK from the rtk-ai/rtk releases page")
		}
		return runUnboundedWithProgress(onLine, winget, "install", "--id", "rtk-ai.rtk", "--exact", "--silent", "--accept-source-agreements", "--accept-package-agreements")
	case "darwin":
		brew, err := exec.LookPath("brew")
		if err != nil {
			return "", errors.New("Homebrew was not found; install RTK from the rtk-ai/rtk releases page")
		}
		return runUnboundedWithProgress(onLine, brew, "install", "rtk")
	default:
		sh, err := exec.LookPath("sh")
		if err != nil {
			return "", errors.New("sh was not found; install RTK from the rtk-ai/rtk releases page")
		}
		return runUnboundedWithProgress(onLine, sh, "-c", "curl -fsSL https://raw.githubusercontent.com/rtk-ai/rtk/refs/heads/master/install.sh | sh")
	}
}

func (m *Manager) installAgentBrowser(onLine func(string)) (string, error) {
	npm, err := npmExecutable()
	if err != nil {
		return "", errors.New("npm was not found; install Node.js to install Agent Browser")
	}
	output, err := runUnboundedWithProgress(onLine, npm, "install", "-g", AgentBrowserPackage)
	if err != nil {
		return output, fmt.Errorf("unable to install %s: %w", AgentBrowserPackage, err)
	}
	m.detectMu.Lock()
	m.agentBrowserCache = nil
	m.agentBrowserCacheAt = time.Time{}
	m.detectMu.Unlock()
	return strings.TrimSpace(output), nil
}

// installPlaywright installs the Playwright CLI globally and then downloads the
// Chromium runtime it drives. The browser download honors an existing
// PLAYWRIGHT_DOWNLOAD_HOST override and otherwise falls back to the same mirror
// the agent commands use, because the official CDN is unreliable in some
// regions.
func (m *Manager) installPlaywright(onLine func(string)) (string, error) {
	npm, err := npmExecutable()
	if err != nil {
		return "", errors.New("npm was not found; install Node.js to install Playwright")
	}
	output, err := runUnboundedWithProgress(onLine, npm, "install", "-g", PlaywrightPackage)
	if err != nil {
		return output, fmt.Errorf("unable to install %s: %w", PlaywrightPackage, err)
	}

	playwright, err := playwrightExecutable()
	if err != nil {
		return output, fmt.Errorf("playwright CLI was not found after npm install: %w", err)
	}
	env := os.Environ()
	if strings.TrimSpace(os.Getenv("PLAYWRIGHT_DOWNLOAD_HOST")) == "" {
		env = append(env, "PLAYWRIGHT_DOWNLOAD_HOST="+playwrightDownloadMirror)
	}
	browserOutput, err := runUnboundedWithProgressEnv(onLine, env, playwright, "install", "chromium")
	combined := strings.TrimSpace(output + "\n" + browserOutput)
	if err != nil {
		return combined, fmt.Errorf("unable to download Chromium for Playwright: %w", err)
	}

	m.detectMu.Lock()
	m.playwrightCache = nil
	m.playwrightCacheAt = time.Time{}
	m.detectMu.Unlock()
	return combined, nil
}

func (m *Manager) Close() error {
	m.infof("extensions manager closed")
	return nil
}

// detectFigma reports whether the globally installed Figma runtime is on PATH.
func detectFigma() (bool, string) {
	path, err := figmaExecutable()
	if err != nil {
		return false, ""
	}
	output, err := run(path, "--version")
	if err != nil {
		return false, ""
	}
	version := strings.TrimSpace(output)
	firstLine := version
	if idx := strings.Index(version, "\n"); idx >= 0 {
		firstLine = version[:idx]
	}
	return true, strings.TrimSpace(firstLine)
}

// cachedFigma returns the figma availability probe, reusing a recent result so
// repeated Snapshot() calls don't re-run the slow npx command every time.
func (m *Manager) cachedFigma() (bool, string) {
	m.detectMu.Lock()
	defer m.detectMu.Unlock()
	if m.figmaCache != nil && time.Since(m.figmaCacheAt) < detectionCacheTTL {
		return m.figmaCache.installed, m.figmaCache.version
	}
	installed, version := detectFigma()
	m.figmaCache = &binaryDetect{installed: installed, version: version}
	m.figmaCacheAt = time.Now()
	return installed, version
}

// installFigma installs the shared runtime once. It deliberately does not start
// a stdio server: each enabled Pi agent owns that process and its pipes.
func (m *Manager) installFigma(onLine func(string)) (string, error) {
	npm, err := npmExecutable()
	if err != nil {
		return "", errors.New("npm was not found; install Node.js to install the Figma runtime")
	}
	output, err := runUnboundedWithProgress(onLine, npm, "install", "-g", FigmaPackage)
	if err != nil {
		return output, fmt.Errorf("unable to install %s: %w", FigmaPackage, err)
	}
	m.detectMu.Lock()
	m.figmaCache = nil
	m.figmaCacheAt = time.Time{}
	m.detectMu.Unlock()
	return strings.TrimSpace(output), nil
}

func npmExecutable() (string, error) {
	names := []string{"npm"}
	if runtime.GOOS == "windows" {
		names = []string{"npm.cmd", "npm.exe", "npm"}
	}
	for _, name := range names {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", exec.ErrNotFound
}

func figmaExecutable() (string, error) {
	names := []string{FigmaPackage}
	if runtime.GOOS == "windows" {
		names = []string{FigmaPackage + ".cmd", FigmaPackage + ".exe", FigmaPackage}
	}
	for _, name := range names {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", exec.ErrNotFound
}

func detectVersion(binary string) (bool, string) {
	if _, err := exec.LookPath(binary); err != nil {
		return false, ""
	}
	output, _ := run(binary, "--version")
	if line, _, ok := strings.Cut(strings.TrimSpace(output), "\n"); ok {
		output = line
	}
	return true, strings.TrimSpace(output)
}

// run executes a command with a bounded timeout so a slow or stuck external
// tool (e.g. npx downloading a package) can't block the whole panel refresh.
func run(binary string, args ...string) (string, error) {
	return runTimeout(detectionTimeout, binary, args...)
}

// runUnboundedWithProgress merges stdout and stderr, streams complete lines to
// onLine, and still returns the full output for the existing result contract.
func runUnboundedWithProgress(onLine func(string), binary string, args ...string) (string, error) {
	return runUnboundedWithProgressEnv(onLine, nil, binary, args...)
}

// runUnboundedWithProgressEnv is runUnboundedWithProgress with an optional
// environment override, used when an install step needs mirror variables.
func runUnboundedWithProgressEnv(onLine func(string), env []string, binary string, args ...string) (string, error) {
	cmd := exec.Command(binary, args...)
	if env != nil {
		cmd.Env = env
	}
	if runtime.GOOS == "windows" {
		cmd.SysProcAttr = windowsHiddenProcessAttributes()
	}

	reader, writer := io.Pipe()
	cmd.Stdout = writer
	cmd.Stderr = writer

	var output bytes.Buffer
	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			output.WriteString(line)
			output.WriteByte('\n')
			if onLine != nil {
				onLine(line)
			}
		}
	}()

	if err := cmd.Start(); err != nil {
		_ = writer.Close()
		<-scanDone
		return "", err
	}
	waitErr := cmd.Wait()
	_ = writer.Close()
	<-scanDone
	_ = reader.Close()

	text := strings.TrimSpace(output.String())
	if waitErr != nil {
		return text, fmt.Errorf("%s: %w", text, waitErr)
	}
	return text, nil
}

func runTimeout(timeout time.Duration, binary string, args ...string) (string, error) {
	var cmd *exec.Cmd
	if timeout > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		cmd = exec.CommandContext(ctx, binary, args...)
	} else {
		cmd = exec.Command(binary, args...)
	}
	if runtime.GOOS == "windows" {
		cmd.SysProcAttr = windowsHiddenProcessAttributes()
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return strings.TrimSpace(string(output)), fmt.Errorf("%s: %w", strings.TrimSpace(string(output)), err)
	}
	return strings.TrimSpace(string(output)), nil
}

func rtkInstallHint() string {
	switch runtime.GOOS {
	case "windows":
		return "winget install --id rtk-ai.rtk --exact"
	case "darwin":
		return "brew install rtk"
	default:
		return "curl -fsSL https://raw.githubusercontent.com/rtk-ai/rtk/refs/heads/master/install.sh | sh"
	}
}

// RTKInstalled reports whether the rtk binary is discoverable on PATH.
func RTKInstalled() bool {
	_, err := RTKExecutable()
	return err == nil
}

// RTKExecutable locates RTK on PATH and in the standard user-level locations
// used by WinGet, Cargo, and the official install script.
func RTKExecutable() (string, error) {
	names := []string{"rtk"}
	if runtime.GOOS == "windows" {
		names = []string{"rtk.exe", "rtk.cmd", "rtk"}
	}
	for _, name := range names {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	home, _ := os.UserHomeDir()
	candidates := []string{filepath.Join(home, ".local", "bin", "rtk")}
	if runtime.GOOS == "windows" {
		candidates = []string{
			filepath.Join(home, ".local", "bin", "rtk.exe"),
			filepath.Join(home, ".cargo", "bin", "rtk.exe"),
		}
		if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
			candidates = append([]string{filepath.Join(localAppData, "Microsoft", "WinGet", "Links", "rtk.exe")}, candidates...)
		}
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", exec.ErrNotFound
}

func detectRTK() (bool, string) {
	path, err := RTKExecutable()
	if err != nil {
		return false, ""
	}
	output, _ := run(path, "--version")
	if line, _, ok := strings.Cut(strings.TrimSpace(output), "\n"); ok {
		output = line
	}
	return true, strings.TrimSpace(output)
}

// EnsureRTKPiExtension returns the path to RTK's Pi bridge source, generating it
// on demand when it does not yet exist. RTK only ships a global generator
// (`rtk init -g --agent pi`), so CodingTo runs it once to produce the source and
// then materializes a per-agent copy elsewhere. Returns an empty string when the
// rtk binary is unavailable or the source could not be created.
func EnsureRTKPiExtension() string {
	if path := FindRTKPiExtension(); path != "" {
		return path
	}
	if !RTKInstalled() {
		return ""
	}
	rtk, err := RTKExecutable()
	if err != nil {
		return ""
	}
	if _, err := run(rtk, "init", "-g", "--agent", "pi"); err != nil {
		return ""
	}
	return FindRTKPiExtension()
}

// FindRTKPiExtension locates the global TypeScript extension created by
// `rtk init -g --agent pi`. CodingTo passes it explicitly because each managed
// agent uses its own PI_CODING_AGENT_DIR and would not otherwise inherit files
// from Pi's default global directory.
func FindRTKPiExtension() string {
	if explicit := strings.TrimSpace(os.Getenv("RTK_PI_EXTENSION")); explicit != "" {
		if info, err := os.Stat(filepath.Clean(explicit)); err == nil && !info.IsDir() {
			path, _ := filepath.Abs(explicit)
			return path
		}
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	candidate := filepath.Join(home, ".pi", "agent", "extensions", "rtk.ts")
	if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
		path, _ := filepath.Abs(candidate)
		return path
	}
	return ""
}
