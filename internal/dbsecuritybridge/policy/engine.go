package policy

import (
	"fmt"

	"codingto/internal/dbsecurity"
	"codingto/internal/dbsecuritybridge/classify"
)

// AnalyzedStatement 是分类与提取后的语句视图，供策略评估使用。
type AnalyzedStatement struct {
	SQL      string
	Action   dbsecurity.Action
	Tables   []classify.Resource
	HasWhere bool
	HasLimit bool
	// EstimatedRows 是 EXPLAIN 预检的行数估算；-1 表示未估算/不可估算。
	EstimatedRows int64
}

// Analyze 对单条语句做分类与信息提取。
func Analyze(sql string) AnalyzedStatement {
	tokens := classify.Tokenize(sql)
	return AnalyzedStatement{
		SQL:           sql,
		Action:        classify.Classify(sql),
		Tables:        classify.ExtractTables(tokens),
		HasWhere:      classify.HasWhere(tokens),
		HasLimit:      classify.HasLimit(tokens),
		EstimatedRows: -1,
	}
}

// Result 是一条语句的策略裁决。
type Result struct {
	Decision dbsecurity.Effect
	// RuleID/RuleOrigin 记录命中规则，便于审计与确认对话框展示。
	RuleID     string
	RuleOrigin dbsecurity.RuleOrigin
	Reason     string
}

// Engine 按连接 ID 取规则并裁决语句。
type Engine struct {
	rulesByConnection func(connectionID string) []dbsecurity.Rule
}

func NewEngine(rulesByConnection func(connectionID string) []dbsecurity.Rule) *Engine {
	return &Engine{rulesByConnection: rulesByConnection}
}

// Decide 裁决单条语句。仲裁顺序：
//  1. effect 优先级 deny > confirm > allow；
//  2. 同 effect 内 action 更具体（Level 更深）者胜出；
//  3. 同具体度 Override > 预设，仍并列取声明序靠前者。
// 无任何规则命中（或命中规则的条件均不满足）时默认 deny。
func (e *Engine) Decide(connectionID string, stmt AnalyzedStatement) Result {
	rules := e.rulesByConnection(connectionID)

	var best *dbsecurity.Rule
	var conditionFailure *dbsecurity.Rule

	for index := range rules {
		rule := &rules[index]
		if !stmt.Action.Match(rule.Action) {
			continue
		}
		if !resourcesMatch(rule.Resources, stmt.Tables) {
			continue
		}
		if ok, _ := evaluateConditions(rule.Conditions, stmt); !ok {
			if conditionFailure == nil {
				conditionFailure = rule
			}
			continue
		}
		if best == nil || beats(rule, best) {
			best = rule
		}
	}

	if best == nil {
		if conditionFailure != nil {
			_, reason := evaluateConditions(conditionFailure.Conditions, stmt)
			if reason == "" {
				reason = fmt.Sprintf("语句不满足规则 %s 的条件", conditionFailure.ID)
			}
			return Result{
				Decision: dbsecurity.EffectDeny, RuleID: conditionFailure.ID,
				RuleOrigin: conditionFailure.Origin, Reason: reason,
			}
		}
		if stmt.Action == dbsecurity.ActionUnknown {
			return Result{Decision: dbsecurity.EffectDeny, Reason: "无法识别的语句类型，默认拒绝"}
		}
		return Result{Decision: dbsecurity.EffectDeny, Reason: "没有匹配的策略规则，默认拒绝"}
	}

	reason := best.Reason
	if reason == "" {
		reason = fmt.Sprintf("命中规则 %s（%s）", best.ID, best.Effect)
	}
	return Result{Decision: best.Effect, RuleID: best.ID, RuleOrigin: best.Origin, Reason: reason}
}

// beats 判断候选规则是否优于当前最佳规则。
func beats(candidate, current *dbsecurity.Rule) bool {
	cp, cur := dbsecurity.EffectPriority(candidate.Effect), dbsecurity.EffectPriority(current.Effect)
	if cp != cur {
		return cp > cur
	}
	cl, lvl := candidate.Action.Level(), current.Action.Level()
	if cl != lvl {
		return cl > lvl
	}
	if candidate.Origin != current.Origin {
		return candidate.Origin == dbsecurity.OriginOverride
	}
	return false // 完全并列时保留声明序靠前者
}
