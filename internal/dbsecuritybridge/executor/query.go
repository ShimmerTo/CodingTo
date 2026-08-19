package executor

import (
	"context"
	"database/sql"
	"encoding/base64"
	"time"
	"unicode/utf8"

	"codingto/internal/dbsecurity"
)

// QueryResult 是 SELECT 执行结果。行数被截断时 Truncated 为 true，
// 防止结果撑爆 Agent 上下文。
type QueryResult struct {
	Columns    []string          `json:"columns"`
	Rows       [][]any           `json:"rows"`
	RowCount   int               `json:"rowCount"`
	Truncated  bool              `json:"truncated"`
	DurationMs int64             `json:"durationMs"`
	Kind       dbsecurity.DBKind `json:"-"`
}

// RunQuery 执行只读语句并返回截断后的结果集。
// 语句级超时由 timeout 控制；结果最多读取 maxRows+1 行以判定截断。
func RunQuery(ctx context.Context, db *sql.DB, kind dbsecurity.DBKind, sqlText string, params []any, timeout time.Duration, maxRows int) (*QueryResult, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if maxRows <= 0 {
		maxRows = 500
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	rows, err := db.QueryContext(ctx, sqlText, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	result := &QueryResult{Columns: columns, Rows: [][]any{}}
	raw := make([]any, len(columns))
	ptrs := make([]any, len(columns))
	for i := range raw {
		ptrs[i] = &raw[i]
	}

	for rows.Next() {
		if len(result.Rows) > maxRows {
			// 已读到 maxRows+1 行，确定截断；不再继续拉取。
			result.Truncated = true
			break
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make([]any, len(columns))
		for i := range raw {
			row[i] = normalizeValue(raw[i])
			raw[i] = nil // Scan 复用缓冲，显式置空避免串值
		}
		result.Rows = append(result.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(result.Rows) > maxRows {
		result.Rows = result.Rows[:maxRows]
		result.Truncated = true
	}
	result.RowCount = len(result.Rows)
	result.DurationMs = time.Since(start).Milliseconds()
	result.Kind = kind
	return result, nil
}

// normalizeValue 把 driver 返回值转成 JSON 友好的形式。
func normalizeValue(v any) any {
	switch value := v.(type) {
	case nil:
		return nil
	case []byte:
		if utf8.Valid(value) {
			return string(value)
		}
		return "base64:" + base64.StdEncoding.EncodeToString(value)
	case time.Time:
		return value.Format(time.RFC3339Nano)
	default:
		return value
	}
}
