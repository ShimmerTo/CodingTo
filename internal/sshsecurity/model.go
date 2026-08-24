// Package sshsecurity defines the declarative SSH capability and policy model.
package sshsecurity

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Effect is the policy decision for one capability.
type Effect string

const (
	// EffectAllow executes without interactive confirmation.
	EffectAllow Effect = "allow"
	// EffectAsk requires an explicit user decision before execution.
	EffectAsk Effect = "ask"
	// EffectDeny blocks execution.
	EffectDeny Effect = "deny"
)

// ParamType is one of the supported strongly typed capability parameters.
type ParamType string

const (
	// ParamInt is a bounded base-10 integer.
	ParamInt ParamType = "int"
	// ParamString is a bounded shell-quoted string.
	ParamString ParamType = "string"
	// ParamEnum accepts one configured literal value.
	ParamEnum ParamType = "enum"
	// ParamRemotePath is a bounded remote path passed as one argv token.
	ParamRemotePath ParamType = "remote_path"
	// ParamContainerName accepts a Docker-compatible container identifier.
	ParamContainerName ParamType = "container_name"
	// ParamGitRef accepts a non-option Git reference.
	ParamGitRef ParamType = "git_ref"
	// ParamServiceName accepts a systemd-compatible service identifier.
	ParamServiceName ParamType = "service_name"
	// ParamSearchPattern is a bounded shell-quoted search expression.
	ParamSearchPattern ParamType = "search_pattern"
)

const (
	// PresetSafe allows reads, asks for operations, and denies destructive capabilities.
	PresetSafe = "safe"
	// PresetDevelopment additionally allows Git fetch.
	PresetDevelopment = "development"
	// PresetFull allows routine operations while retaining hard-deny defaults.
	PresetFull = "full"
	// PresetCustom denies everything until an override explicitly opens it.
	PresetCustom = "custom"
)

var (
	capabilityNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(?:\.[a-z][a-z0-9_]*)+$`)
	ruleCapabilityPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(?:\.[a-z][a-z0-9_]*)*$`)
	paramNamePattern      = regexp.MustCompile(`^[a-z][a-zA-Z0-9_]*$`)
	executablePattern     = regexp.MustCompile(`^[A-Za-z0-9_./-]+$`)
	placeholderPattern    = regexp.MustCompile(`^\{([a-z][a-zA-Z0-9_]*)\}$`)
	containerNamePattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	gitRefPattern         = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/@{}+~-]{0,254}$`)
	serviceNamePattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.@-]{0,127}$`)
)

// ParamSpec describes one typed placeholder accepted by a capability.
type ParamSpec struct {
	Type      ParamType `json:"type"`
	Required  bool      `json:"required,omitempty"`
	Min       int64     `json:"min,omitempty"`
	Max       int64     `json:"max,omitempty"`
	MaxLength int       `json:"maxLength,omitempty"`
	Values    []string  `json:"values,omitempty"`
}

// Capability is an argv template. Every placeholder must occupy a whole arg.
type Capability struct {
	Name           string               `json:"name"`
	Group          string               `json:"group"`
	Description    string               `json:"description,omitempty"`
	Executable     string               `json:"executable"`
	Args           []string             `json:"args"`
	Params         map[string]ParamSpec `json:"params,omitempty"`
	Permission     Effect               `json:"permission"`
	TimeoutSeconds int                  `json:"timeoutSeconds,omitempty"`
}

// Rule overrides one capability or capability-tree prefix in a policy mode.
type Rule struct {
	ID         string `json:"id,omitempty"`
	Capability string `json:"capability"`
	Effect     Effect `json:"effect"`
	Reason     string `json:"reason,omitempty"`
}

// Policy selects a built-in mode and optional deterministic overrides.
type Policy struct {
	Preset    string `json:"preset"`
	Overrides []Rule `json:"overrides,omitempty"`
}

// Resource is one workspace-authorized SSH target in a session snapshot.
type Resource struct {
	ID                   string       `json:"id"`
	Name                 string       `json:"name"`
	Address              string       `json:"address"`
	Port                 int          `json:"port"`
	Username             string       `json:"username"`
	AuthMode             string       `json:"authMode"`
	Password             string       `json:"password,omitempty"`
	PrivateKey           string       `json:"privateKey,omitempty"`
	PrivateKeyPassphrase string       `json:"privateKeyPassphrase,omitempty"`
	HostKeyFingerprint   string       `json:"hostKeyFingerprint,omitempty"`
	WorkDir              string       `json:"workDir,omitempty"`
	Policy               Policy       `json:"policy"`
	CustomCapabilities   []Capability `json:"customCapabilities,omitempty"`
}

// Config is the complete session-scoped SSH bridge snapshot.
type Config struct {
	Resources []Resource `json:"resources"`
}

// DefaultPolicy returns the conservative operational default.
func DefaultPolicy() Policy {
	return Policy{Preset: PresetSafe, Overrides: []Rule{}}
}

// Normalize validates a policy mode and discards malformed overrides.
func (p *Policy) Normalize() {
	p.Preset = strings.ToLower(strings.TrimSpace(p.Preset))
	switch p.Preset {
	case PresetSafe, PresetDevelopment, PresetFull, PresetCustom:
	default:
		p.Preset = PresetSafe
	}
	seen := map[string]int{}
	overrides := make([]Rule, 0, len(p.Overrides))
	for index, rule := range p.Overrides {
		rule.Capability = strings.ToLower(strings.TrimSpace(rule.Capability))
		rule.Reason = strings.TrimSpace(rule.Reason)
		if !ruleCapabilityPattern.MatchString(rule.Capability) || !validEffect(rule.Effect) {
			continue
		}
		if rule.Capability == "shell.raw" && rule.Effect == EffectAllow {
			rule.Effect = EffectAsk
		}
		if rule.ID == "" {
			rule.ID = fmt.Sprintf("override-%d", index+1)
		}
		if existing, duplicate := seen[rule.Capability]; duplicate {
			// Legacy configurations could contain conflicting rules for the exact
			// same capability. Collapse them deterministically with deny > ask >
			// allow rather than making array order a security decision.
			if effectPriority(rule.Effect) > effectPriority(overrides[existing].Effect) {
				overrides[existing] = rule
			}
			continue
		}
		seen[rule.Capability] = len(overrides)
		overrides = append(overrides, rule)
	}
	p.Overrides = overrides
}

// Validate checks policy size and override syntax before persistence.
func (p Policy) Validate() error {
	preset := strings.ToLower(strings.TrimSpace(p.Preset))
	if preset != "" && preset != PresetSafe && preset != PresetDevelopment && preset != PresetFull && preset != PresetCustom {
		return fmt.Errorf("SSH 策略模式不合法：%q", p.Preset)
	}
	if len(p.Overrides) > 128 {
		return fmt.Errorf("SSH 策略 Override 最多 128 条")
	}
	seenIDs := map[string]bool{}
	seenCapabilities := map[string]bool{}
	for _, rule := range p.Overrides {
		capability := strings.ToLower(strings.TrimSpace(rule.Capability))
		if !ruleCapabilityPattern.MatchString(capability) {
			return fmt.Errorf("规则能力名不合法：%q", rule.Capability)
		}
		if seenCapabilities[capability] {
			return fmt.Errorf("同一 SSH 能力不能配置多条 Override：%s", capability)
		}
		seenCapabilities[capability] = true
		if !validEffect(rule.Effect) {
			return fmt.Errorf("规则权限必须是 allow/ask/deny：%q", rule.Effect)
		}
		if len([]rune(rule.Reason)) > 512 {
			return fmt.Errorf("规则原因不能超过 512 字符")
		}
		if rule.ID != "" {
			if seenIDs[rule.ID] {
				return fmt.Errorf("SSH 策略 Override ID 重复：%s", rule.ID)
			}
			seenIDs[rule.ID] = true
		}
	}
	return nil
}

// Normalize validates a resource and its custom capability inventory.
func (r *Resource) Normalize() {
	r.ID = strings.TrimSpace(r.ID)
	r.Name = strings.TrimSpace(r.Name)
	r.Address = strings.TrimSpace(r.Address)
	r.Username = strings.TrimSpace(r.Username)
	r.HostKeyFingerprint = NormalizeHostKeyFingerprint(r.HostKeyFingerprint)
	r.WorkDir = strings.TrimSpace(r.WorkDir)
	if r.Port < 1 || r.Port > 65535 {
		r.Port = 22
	}
	if r.AuthMode != "key" {
		r.AuthMode = "password"
	}
	r.Policy.Normalize()
	seen := map[string]bool{}
	custom := make([]Capability, 0, len(r.CustomCapabilities))
	for _, capability := range r.CustomCapabilities {
		if len(custom) >= 64 {
			break
		}
		capability.Normalize()
		if capability.Validate(true) != nil || seen[capability.Name] {
			continue
		}
		seen[capability.Name] = true
		custom = append(custom, capability)
	}
	r.CustomCapabilities = custom
}

// Normalize validates the resource inventory and removes duplicate IDs.
func (c *Config) Normalize() {
	seen := map[string]bool{}
	resources := make([]Resource, 0, len(c.Resources))
	for _, resource := range c.Resources {
		resource.Normalize()
		if resource.ID == "" || resource.Address == "" || resource.Username == "" || seen[resource.ID] {
			continue
		}
		seen[resource.ID] = true
		resources = append(resources, resource)
	}
	c.Resources = resources
}

// ByID resolves one resource without exposing credentials in list responses.
func (c Config) ByID(id string) (Resource, bool) {
	for _, resource := range c.Resources {
		if resource.ID == id {
			return resource, true
		}
	}
	return Resource{}, false
}

// Normalize canonicalizes a capability before validation or persistence.
func (c *Capability) Normalize() {
	c.Name = strings.ToLower(strings.TrimSpace(c.Name))
	c.Group = strings.ToLower(strings.TrimSpace(c.Group))
	c.Description = strings.TrimSpace(c.Description)
	c.Executable = strings.TrimSpace(c.Executable)
	if c.Args == nil {
		c.Args = []string{}
	}
	if c.Params == nil {
		c.Params = map[string]ParamSpec{}
	}
	if c.TimeoutSeconds <= 0 {
		c.TimeoutSeconds = 30
	}
	if c.TimeoutSeconds > 300 {
		c.TimeoutSeconds = 300
	}
}

// Validate rejects malformed templates and unbounded parameter definitions.
func (c Capability) Validate(custom bool) error {
	if !capabilityNamePattern.MatchString(c.Name) {
		return fmt.Errorf("能力名称不合法：%q", c.Name)
	}
	if custom {
		root := strings.SplitN(c.Name, ".", 2)[0]
		switch root {
		case "system", "git", "docker", "service", "shell", "ssh":
			return fmt.Errorf("自定义能力不能占用内置命名空间 %q", root)
		}
		if err := validateCustomExecutable(c.Executable, c.Args); err != nil {
			return err
		}
	}
	if c.Group == "" || !regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`).MatchString(c.Group) {
		return fmt.Errorf("能力分组不合法：%q", c.Group)
	}
	if len([]rune(c.Description)) > 512 {
		return fmt.Errorf("能力说明不能超过 512 字符")
	}
	if !validEffect(c.Permission) {
		return fmt.Errorf("能力权限必须是 allow/ask/deny：%q", c.Permission)
	}
	if c.Name == "shell.raw" {
		return nil
	}
	if len(c.Executable) == 0 || len(c.Executable) > 128 || !executablePattern.MatchString(c.Executable) || strings.Contains(c.Executable, "..") {
		return fmt.Errorf("固定可执行文件不合法：%q", c.Executable)
	}
	if len(c.Args) > 32 {
		return fmt.Errorf("命令参数模板最多 32 项")
	}
	used := map[string]bool{}
	for _, arg := range c.Args {
		if len(arg) > 512 || strings.ContainsAny(arg, "\x00\r\n") {
			return fmt.Errorf("命令参数模板含非法内容")
		}
		if match := placeholderPattern.FindStringSubmatch(arg); len(match) == 2 {
			if match[1] == "resourceWorkDir" {
				continue
			}
			if _, ok := c.Params[match[1]]; !ok {
				return fmt.Errorf("占位符 %q 未声明参数类型", match[1])
			}
			used[match[1]] = true
		} else if strings.ContainsAny(arg, "{}") {
			return fmt.Errorf("占位符必须独占一个参数：%q", arg)
		}
	}
	for name, spec := range c.Params {
		if !paramNamePattern.MatchString(name) || !used[name] {
			return fmt.Errorf("参数 %q 未被模板正确引用", name)
		}
		if err := spec.Validate(); err != nil {
			return fmt.Errorf("参数 %s：%w", name, err)
		}
	}
	return nil
}

// Validate checks one strongly typed parameter specification.
func (p ParamSpec) Validate() error {
	switch p.Type {
	case ParamInt:
		if p.Min > p.Max || p.Max == 0 {
			return fmt.Errorf("int 必须声明有效 min/max")
		}
	case ParamString, ParamRemotePath, ParamSearchPattern:
		if p.MaxLength <= 0 || p.MaxLength > 4096 {
			return fmt.Errorf("字符串类型必须声明 1..4096 的 maxLength")
		}
	case ParamEnum:
		if len(p.Values) == 0 || len(p.Values) > 64 {
			return fmt.Errorf("enum 必须声明 1..64 个 values")
		}
		for _, value := range p.Values {
			if value == "" || len([]rune(value)) > 128 || strings.ContainsAny(value, "\x00\r\n") {
				return fmt.Errorf("enum value 必须为 1..128 字符且不能包含换行或 NUL")
			}
		}
	case ParamContainerName, ParamGitRef, ParamServiceName:
	default:
		return fmt.Errorf("不支持的类型 %q", p.Type)
	}
	return nil
}

// ValidateValue validates and canonicalizes one Agent-supplied value.
func (p ParamSpec) ValidateValue(value any) (string, error) {
	if value == nil {
		if p.Required {
			return "", fmt.Errorf("参数不能为空")
		}
		return "", nil
	}
	text := strings.TrimSpace(fmt.Sprint(value))
	if text == "" {
		if p.Required {
			return "", fmt.Errorf("参数不能为空")
		}
		return "", nil
	}
	if strings.ContainsAny(text, "\x00\r\n") {
		return "", fmt.Errorf("参数不能包含换行或 NUL")
	}
	switch p.Type {
	case ParamInt:
		n, err := strconv.ParseInt(text, 10, 64)
		if err != nil || n < p.Min || n > p.Max {
			return "", fmt.Errorf("整数必须在 %d..%d 范围内", p.Min, p.Max)
		}
		return strconv.FormatInt(n, 10), nil
	case ParamEnum:
		for _, allowed := range p.Values {
			if text == allowed {
				return text, nil
			}
		}
		return "", fmt.Errorf("值不在允许列表中")
	case ParamContainerName:
		if !containerNamePattern.MatchString(text) {
			return "", fmt.Errorf("容器名格式不合法")
		}
	case ParamGitRef:
		if !gitRefPattern.MatchString(text) || strings.HasPrefix(text, "-") {
			return "", fmt.Errorf("Git 引用格式不合法")
		}
	case ParamServiceName:
		if !serviceNamePattern.MatchString(text) || strings.HasPrefix(text, "-") {
			return "", fmt.Errorf("服务名格式不合法")
		}
	case ParamString, ParamRemotePath, ParamSearchPattern:
		if len([]rune(text)) > p.MaxLength {
			return "", fmt.Errorf("参数长度不能超过 %d", p.MaxLength)
		}
		if p.Type == ParamRemotePath && strings.HasPrefix(text, "-") {
			return "", fmt.Errorf("路径不能以 - 开头")
		}
	default:
		return "", fmt.Errorf("不支持的参数类型")
	}
	return text, nil
}

// ResolveEffect applies the selected mode followed by the most specific override.
func ResolveEffect(policy Policy, capability Capability) (Effect, string) {
	policy.Normalize()
	effect := presetEffect(policy.Preset, capability)
	reason := ""
	bestDepth := -1
	for _, rule := range policy.Overrides {
		if !matchesPrefix(rule.Capability, capability.Name) {
			continue
		}
		depth := strings.Count(rule.Capability, ".")
		if depth >= bestDepth {
			bestDepth = depth
			effect = rule.Effect
			reason = rule.Reason
		}
	}
	if capability.Name == "shell.raw" && effect == EffectAllow {
		effect = EffectAsk
	}
	if capability.Name == "docker.rm" || capability.Name == "docker.exec" {
		effect = EffectDeny
	}
	return effect, reason
}

func presetEffect(preset string, capability Capability) Effect {
	if preset == PresetCustom {
		return EffectDeny
	}
	if capability.Permission == EffectDeny {
		if preset == PresetFull && capability.Name == "shell.raw" {
			return EffectAsk
		}
		return EffectDeny
	}
	if preset == PresetFull && capability.Permission == EffectAsk && !strings.HasPrefix(capability.Name, "docker.rm") && !strings.HasPrefix(capability.Name, "docker.exec") {
		return EffectAllow
	}
	if preset == PresetDevelopment && capability.Name == "git.fetch" {
		return EffectAllow
	}
	return capability.Permission
}

func matchesPrefix(prefix, name string) bool {
	return prefix == name || strings.HasPrefix(name, prefix+".")
}

func validEffect(effect Effect) bool {
	return effect == EffectAllow || effect == EffectAsk || effect == EffectDeny
}

func effectPriority(effect Effect) int {
	switch effect {
	case EffectDeny:
		return 3
	case EffectAsk:
		return 2
	case EffectAllow:
		return 1
	default:
		return 0
	}
}

func executableBase(value string) string {
	value = strings.TrimSpace(value)
	if index := strings.LastIndexAny(value, `/\\`); index >= 0 {
		value = value[index+1:]
	}
	return strings.TrimSuffix(strings.ToLower(value), ".exe")
}

func validateCustomExecutable(executable string, args []string) error {
	base := executableBase(executable)
	shells := map[string]bool{
		"sh": true, "bash": true, "dash": true, "zsh": true, "fish": true,
		"cmd": true, "powershell": true, "pwsh": true, "env": true,
	}
	if shells[base] {
		return fmt.Errorf("自定义能力不能使用命令解释器 %q", executable)
	}
	interpreters := map[string]bool{"python": true, "python3": true, "node": true, "perl": true, "ruby": true, "php": true}
	if interpreters[base] {
		for _, arg := range args {
			if arg == "-c" || arg == "-e" || arg == "--eval" {
				return fmt.Errorf("自定义能力不能通过 %s 解释执行代码", executable)
			}
		}
	}
	// Wrappers such as sudo/doas are allowed for fixed commands, but must not
	// smuggle a shell interpreter into the argv template.
	for _, arg := range args {
		if shells[executableBase(arg)] {
			return fmt.Errorf("自定义能力参数不能调用命令解释器 %q", arg)
		}
	}
	return nil
}

// SortedParamNames returns stable parameter ordering for UI and tool metadata.
func SortedParamNames(params map[string]ParamSpec) []string {
	names := make([]string, 0, len(params))
	for name := range params {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
