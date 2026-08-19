package executor

import (
	"context"
	"database/sql"
	"time"
)

// ExecResult 是写入/DDL 执行结果。
type ExecResult struct {
	AffectedRows int64 `json:"affectedRows"`
	// AffectedKnown 为 false 时表示驱动不支持返回影响行数（此时为 -1）。
	AffectedKnown bool  `json:"affectedKnown"`
	DurationMs    int64 `json:"durationMs"`
}

// Execer 抽象 *sql.DB 与 *sql.Tx 的执行能力。
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// RunExec 执行写入/DDL/事务语句。语句级超时与查询一致。
func RunExec(ctx context.Context, db Execer, sqlText string, params []any, timeout time.Duration) (*ExecResult, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	res, err := db.ExecContext(ctx, sqlText, params...)
	if err != nil {
		return nil, err
	}
	result := &ExecResult{AffectedRows: -1}
	if affected, err := res.RowsAffected(); err == nil {
		result.AffectedRows = affected
		result.AffectedKnown = true
	}
	result.DurationMs = time.Since(start).Milliseconds()
	return result, nil
}
