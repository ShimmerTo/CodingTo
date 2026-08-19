package executor

import (
	"context"
	"database/sql"
	"regexp"
	"strconv"
	"time"

	"codingto/internal/dbsecurity"
)

// pgRowsPattern 匹配 Postgres EXPLAIN 文本计划中的 rows=N 标注。
var pgRowsPattern = regexp.MustCompile(`rows=(\d+)`)

// PreflightRows 对 SELECT 做 EXPLAIN 行数估算。返回估算行数与是否成功。
//
// 安全红线：
//   - 仅接受 SELECT 类语句，调用方须先经分类器确认；
//   - 只使用普通 EXPLAIN，严禁 EXPLAIN ANALYZE（PG 语义下会真实执行）；
//   - SQLite 的 EXPLAIN QUERY PLAN 不产出行数估算，直接返回不可估算。
func PreflightRows(ctx context.Context, db *sql.DB, kind dbsecurity.DBKind, sqlText string, params []any, timeout time.Duration) (int64, bool) {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	switch kind {
	case dbsecurity.KindMySQL:
		return mysqlExplainRows(ctx, db, sqlText, params)
	case dbsecurity.KindPostgres:
		return postgresExplainRows(ctx, db, sqlText, params)
	default:
		return 0, false
	}
}

// mysqlExplainRows 汇总 EXPLAIN 各行 rows 列。
func mysqlExplainRows(ctx context.Context, db *sql.DB, sqlText string, params []any) (int64, bool) {
	rows, err := db.QueryContext(ctx, "EXPLAIN "+sqlText, params...)
	if err != nil {
		return 0, false
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return 0, false
	}
	rowsIndex := -1
	for i, column := range columns {
		if column == "rows" {
			rowsIndex = i
			break
		}
	}
	if rowsIndex < 0 {
		return 0, false
	}

	raw := make([]any, len(columns))
	ptrs := make([]any, len(columns))
	for i := range raw {
		ptrs[i] = &raw[i]
	}

	var total int64
	found := false
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return 0, false
		}
		switch value := raw[rowsIndex].(type) {
		case int64:
			total += value
			found = true
		case []byte:
			if n, convErr := strconv.ParseInt(string(value), 10, 64); convErr == nil {
				total += n
				found = true
			}
		}
	}
	if err := rows.Err(); err != nil {
		return 0, false
	}
	return total, found
}

// postgresExplainRows 解析文本计划首行（顶层节点）的 rows=N。
func postgresExplainRows(ctx context.Context, db *sql.DB, sqlText string, params []any) (int64, bool) {
	rows, err := db.QueryContext(ctx, "EXPLAIN "+sqlText, params...)
	if err != nil {
		return 0, false
	}
	defer rows.Close()

	if !rows.Next() {
		return 0, false
	}
	var line string
	if err := rows.Scan(&line); err != nil {
		return 0, false
	}
	match := pgRowsPattern.FindStringSubmatch(line)
	if match == nil {
		return 0, false
	}
	n, convErr := strconv.ParseInt(match[1], 10, 64)
	if convErr != nil {
		return 0, false
	}
	return n, true
}
