package audit

// Event 是一条审计记录。SQL 已脱敏截断；params 永不入审计。
type Event struct {
	Time         string `json:"time"`
	ConnectionID string `json:"connectionId"`
	Action       string `json:"action"` // 协议动作：connections/schema/query/execute/confirm
	SQL          string `json:"sql,omitempty"`
	SQLAction    string `json:"sqlAction,omitempty"` // 分类结果，如 database.write.update
	Decision     string `json:"decision"`            // allow/confirm/deny/error
	Reason       string `json:"reason,omitempty"`
	RuleID       string `json:"ruleId,omitempty"`
	DurationMs   int64  `json:"durationMs,omitempty"`
	RowCount     int    `json:"rowCount,omitempty"`
	Error        string `json:"error,omitempty"`
}

// maxSQLLength 是审计中 SQL 的保留长度上限；超长截断防止审计膨胀。
const maxSQLLength = 1000

// MaskSQL 对 SQL 做脱敏截断（审计专用）。
func MaskSQL(sql string) string {
	runes := []rune(sql)
	if len(runes) <= maxSQLLength {
		return sql
	}
	return string(runes[:maxSQLLength]) + "…"
}
