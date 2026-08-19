package dbsecurity

import "strings"

// Action 是 SQL 语句的层级动作类别，形如 "database.write.update"。
// 策略规则按树形前缀匹配："database.write" 可命中所有 database.write.* 子动作。
type Action string

const (
	ActionRead            = Action("database.read")
	ActionReadSelect      = Action("database.read.select")
	ActionReadMeta        = Action("database.read.meta")
	ActionWrite           = Action("database.write")
	ActionWriteInsert     = Action("database.write.insert")
	ActionWriteUpdate     = Action("database.write.update")
	ActionWriteDelete     = Action("database.write.delete")
	ActionSchema          = Action("database.schema")
	ActionSchemaTable     = Action("database.schema.table")
	ActionSchemaIndex     = Action("database.schema.index")
	ActionSchemaView      = Action("database.schema.view")
	ActionSchemaOther     = Action("database.schema.other")
	ActionAdmin           = Action("database.admin")
	ActionTransaction     = Action("database.transaction")
	ActionExternal        = Action("database.external")
	ActionExternalFile    = Action("database.external.file")
	ActionExternalProgram = Action("database.external.program")
	ActionExternalAttach  = Action("database.external.attach")
	ActionExternalNetwork = Action("database.external.network")
	ActionExternalOther   = Action("database.external.other")
	ActionUnknown         = Action("database.unknown")
)

// topLevelCategories 列出所有二级类别，用于 UI 展示与校验。
var topLevelCategories = []Action{
	ActionRead, ActionWrite, ActionSchema, ActionAdmin,
	ActionTransaction, ActionExternal, ActionUnknown,
}

func TopLevelCategories() []Action { return append([]Action(nil), topLevelCategories...) }

// Level 返回动作层级深度："database" 为 1，"database.read" 为 2，以此类推。
func (a Action) Level() int {
	if a == "" {
		return 0
	}
	return strings.Count(string(a), ".") + 1
}

// Parent 返回上一级动作；顶级动作返回自身。
func (a Action) Parent() Action {
	s := string(a)
	index := strings.LastIndex(s, ".")
	if index <= 0 {
		return a
	}
	return Action(s[:index])
}

// Match 判断动作 a 是否落在 pattern 子树内。pattern 必须按 "." 段边界匹配：
// "database.read" 命中 "database.read.select"，但不命中 "database.readx"。
func (a Action) Match(pattern Action) bool {
	if pattern == "" {
		return false
	}
	if a == pattern {
		return true
	}
	return strings.HasPrefix(string(a), string(pattern)+".")
}

// Valid 判断动作是否以 database 根开头。
func (a Action) Valid() bool {
	return a == "database" || strings.HasPrefix(string(a), "database.")
}
