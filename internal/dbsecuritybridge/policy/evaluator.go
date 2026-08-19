package policy

import (
	"fmt"
	"strings"

	"codingto/internal/dbsecurity"
	"codingto/internal/dbsecuritybridge/classify"
)

// resourcesMatch 判断语句引用的表是否落在规则的资源范围内。
//   - Resources 为空表示不限资源，直接匹配；
//   - 表提取失败（Tables 为空）的写/读语句一律视为不匹配（保守：让规则
//     落空，走默认拒绝路径，而不是误匹配放行）；
//   - 规则带资源限定时，deny 名单优先：任何一个表不在名单内即不匹配，
//     不允许"部分表命中就放行整条语句"。
func resourcesMatch(patterns []dbsecurity.ResourcePattern, tables []classify.Resource) bool {
	if len(patterns) == 0 {
		return true
	}
	if len(tables) == 0 {
		return false
	}
	for _, table := range tables {
		if !resourceInPatterns(table, patterns) {
			return false
		}
	}
	return true
}

func resourceInPatterns(resource classify.Resource, patterns []dbsecurity.ResourcePattern) bool {
	for _, pattern := range patterns {
		if patternMatch(pattern.Schema, resource.Schema) && patternMatch(pattern.Table, resource.Table) {
			return true
		}
	}
	return false
}

// patternMatch 做大小写不敏感的 * / ? 通配符匹配；空 pattern 视为匹配任意值。
func patternMatch(pattern, value string) bool {
	if pattern == "" {
		return true
	}
	return globMatch(strings.ToLower(pattern), strings.ToLower(value))
}

// globMatch 是逐 rune 的简单通配符匹配（* 匹配任意串，? 匹配单个 rune），
// 支持回溯，复杂度对短标识符足够。
func globMatch(pattern, value string) bool {
	star, match := -1, 0
	p, v := 0, 0
	for v < len(value) {
		switch {
		case p < len(pattern) && (pattern[p] == '?' || pattern[p] == value[v]):
			p++
			v++
		case p < len(pattern) && pattern[p] == '*':
			star, match = p, v
			p++
		case star >= 0:
			match++
			p, v = star+1, match
		default:
			return false
		}
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}

// evaluateConditions 评估规则条件是否全部满足；不满足时返回人类可读原因，
// 供审计与拒绝信息展示。
func evaluateConditions(cond dbsecurity.Condition, stmt AnalyzedStatement) (bool, string) {
	if cond.Empty() {
		return true, ""
	}
	if cond.RequireWhere {
		switch stmt.Action {
		case dbsecurity.ActionWriteUpdate, dbsecurity.ActionWriteDelete:
			if !stmt.HasWhere {
				return false, "语句缺少 WHERE 子句（规则要求 requireWhere）"
			}
		}
	}
	if cond.RequireLimit {
		if stmt.Action == dbsecurity.ActionReadSelect && !stmt.HasLimit {
			return false, "语句缺少 LIMIT 子句（规则要求 requireLimit）"
		}
	}
	if cond.MaxRows > 0 && stmt.Action == dbsecurity.ActionReadSelect {
		if stmt.EstimatedRows < 0 {
			return false, "无法通过 EXPLAIN 估算影响行数，按保守策略拒绝"
		}
		if stmt.EstimatedRows > int64(cond.MaxRows) {
			return false, fmt.Sprintf("EXPLAIN 估算行数 %d 超过上限 %d", stmt.EstimatedRows, cond.MaxRows)
		}
	}
	return true, ""
}
