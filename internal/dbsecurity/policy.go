package dbsecurity

import (
	"fmt"
	"strings"
)

// Effect 是规则的裁决结果。不同 effect 冲突时优先级 deny > confirm > allow。
type Effect string

const (
	EffectAllow   = Effect("allow")
	EffectConfirm = Effect("confirm")
	EffectDeny    = Effect("deny")
)

// EffectPriority 返回 effect 的仲裁优先级，数值越大越优先。
func EffectPriority(e Effect) int {
	switch e {
	case EffectDeny:
		return 3
	case EffectConfirm:
		return 2
	case EffectAllow:
		return 1
	default:
		return 0
	}
}

// RuleOrigin 标记规则来源：预设展开或用户 Override。
type RuleOrigin string

const (
	OriginPreset   = RuleOrigin("preset")
	OriginOverride = RuleOrigin("override")
)

// Condition 是规则生效的静态/预检条件，全部满足规则才参与仲裁。
type Condition struct {
	// RequireWhere 要求 UPDATE/DELETE 语句带 WHERE 子句。
	RequireWhere bool `json:"requireWhere,omitempty"`
	// RequireLimit 要求 SELECT 语句带 LIMIT 子句。
	RequireLimit bool `json:"requireLimit,omitempty"`
	// MaxRows 要求 SELECT 的 EXPLAIN 估算行数不超过该值；无法估算时
	// 条件视为不满足（保守拒绝）。
	MaxRows int `json:"maxRows,omitempty"`
}

func (c Condition) Empty() bool {
	return !c.RequireWhere && !c.RequireLimit && c.MaxRows <= 0
}

// ResourcePattern 限定规则作用的库/表，支持 * 与 ? 通配符。
type ResourcePattern struct {
	Schema string `json:"schema,omitempty"`
	Table  string `json:"table,omitempty"`
}

// Rule 是一条权限策略规则。Resources 为空表示不限资源。
type Rule struct {
	ID         string            `json:"id,omitempty"`
	Action     Action            `json:"action"`
	Effect     Effect            `json:"effect"`
	Reason     string            `json:"reason,omitempty"`
	Conditions Condition         `json:"conditions,omitempty"`
	Resources  []ResourcePattern `json:"resources,omitempty"`
	// Origin 由策略装配时填充，不参与持久化。
	Origin RuleOrigin `json:"-"`
}

func (r Rule) Validate() error {
	if !r.Action.Valid() {
		return fmt.Errorf("规则动作不合法：%q", r.Action)
	}
	switch r.Effect {
	case EffectAllow, EffectConfirm, EffectDeny:
	default:
		return fmt.Errorf("规则效果必须是 allow/confirm/deny：%q", r.Effect)
	}
	return nil
}

// 预设名称。
const (
	PresetSafe        = "safe"
	PresetDevelopment = "development"
	PresetFull        = "full"
	PresetCustom      = "custom"
)

// Policy 是连接级权限策略：一个预设 + 可选 Override 规则。
// 策略只作用于所属连接，不存在全局策略层。
type Policy struct {
	Preset    string `json:"preset"`
	Overrides []Rule `json:"overrides,omitempty"`
}

func (p *Policy) Normalize() {
	p.Preset = strings.ToLower(strings.TrimSpace(p.Preset))
	switch p.Preset {
	case PresetSafe, PresetDevelopment, PresetFull, PresetCustom:
	default:
		p.Preset = PresetSafe
	}
	overrides := make([]Rule, 0, len(p.Overrides))
	for index := range p.Overrides {
		rule := p.Overrides[index]
		if rule.Validate() != nil {
			continue
		}
		if rule.ID == "" {
			rule.ID = fmt.Sprintf("override-%d", index+1)
		}
		overrides = append(overrides, rule)
	}
	p.Overrides = overrides
}

// Rules 展开预设与 Override，得到完整规则列表（含来源标记）。
// 预设规则在前，Override 在后；同级并列时 Override 优先（见 policy 引擎仲裁）。
func (p Policy) Rules() []Rule {
	var presetRules []Rule
	switch p.Preset {
	case PresetDevelopment:
		presetRules = DevelopmentPreset()
	case PresetFull:
		presetRules = FullPreset()
	case PresetCustom:
		presetRules = nil
	default:
		presetRules = SafePreset()
	}
	rules := make([]Rule, 0, len(presetRules)+len(p.Overrides))
	for _, rule := range presetRules {
		rule.Origin = OriginPreset
		rules = append(rules, rule)
	}
	for _, rule := range p.Overrides {
		rule.Origin = OriginOverride
		rules = append(rules, rule)
	}
	return rules
}
