package bridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"codingto/internal/dbsecurity"
	"codingto/internal/dbsecuritybridge/audit"
	"codingto/internal/dbsecuritybridge/classify"
	"codingto/internal/dbsecuritybridge/config"
	"codingto/internal/dbsecuritybridge/connection"
	"codingto/internal/dbsecuritybridge/executor"
	"codingto/internal/dbsecuritybridge/policy"
	"codingto/internal/dbsecuritybridge/protocol"
)

const (
	// confirmTTL 是确认 token 的有效期；超时作废，必须重新发起。
	confirmTTL = 5 * time.Minute
	// preflightTimeout 是 EXPLAIN 预检的独立超时。
	preflightTimeout = 15 * time.Second
)

// identifierPattern 校验直接拼入 SQL 的标识符（仅 MySQL SHOW 语句使用），
// 其他位置一律走参数化。
var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]{0,127}$`)

// plannedStatement 是分类并通过策略门控后待执行的语句。
type plannedStatement struct {
	SQL    string
	Params []any
	Action dbsecurity.Action
}

// pendingConfirm 是等待用户确认的执行计划；token 一次性、超时作废。
type pendingConfirm struct {
	mode      string // "query" | "execute"
	connID    string
	plans     []plannedStatement
	createdAt time.Time
}

// Service 实现 protocol.Handler：动作分发 + 策略门控 + 确认编排。
type Service struct {
	snapshot *config.Snapshot
	manager  *connection.Manager
	recorder *audit.Recorder
	engine   *policy.Engine
	rulesFor func(connectionID string) []dbsecurity.Rule

	mu      sync.Mutex
	pending map[string]*pendingConfirm
}

// New 以快照路径初始化服务。审计目录取 sessionDir/.db-security
// （快照位于 sessionDir/.db-security/config.json）。
func New(configPath string) (*Service, error) {
	sessionDir := filepath.Dir(filepath.Dir(configPath))
	recorder, err := audit.NewRecorder(sessionDir)
	if err != nil {
		return nil, fmt.Errorf("初始化审计失败：%w", err)
	}
	snapshot := config.NewSnapshot(configPath)
	s := &Service{
		snapshot: snapshot,
		manager:  connection.NewManager(),
		recorder: recorder,
		pending:  map[string]*pendingConfirm{},
	}
	s.rulesFor = func(connectionID string) []dbsecurity.Rule {
		cfg, cfgErr := snapshot.Config()
		if cfgErr != nil {
			return nil
		}
		conn, ok := cfg.ByID(connectionID)
		if !ok {
			return nil
		}
		return conn.Policy.Rules()
	}
	s.engine = policy.NewEngine(s.rulesFor)
	return s, nil
}

// Close 释放连接池与审计文件。
func (s *Service) Close() {
	s.manager.Close()
	s.recorder.Close()
}

// Handle 分发协议动作。
func (s *Service) Handle(ctx context.Context, requestID, action string, params json.RawMessage) (any, error) {
	switch action {
	case "connections":
		return s.listConnections()
	case "schema":
		return s.schema(ctx, params)
	case "query", "execute":
		var req sqlRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, protocol.Errorf("bad_request", "参数解析失败：%v", err)
		}
		return s.processSQL(ctx, action, req)
	case "confirm":
		var req struct {
			Token    string `json:"token"`
			Decision string `json:"decision"`
		}
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, protocol.Errorf("bad_request", "参数解析失败：%v", err)
		}
		return s.confirm(ctx, req.Token, req.Decision)
	default:
		return nil, protocol.Errorf("bad_request", "未知动作：%s", action)
	}
}

type sqlRequest struct {
	ConnectionID string `json:"connectionId"`
	SQL          string `json:"sql"`
	Params       []any  `json:"params"`
}

// listConnections 返回脱敏连接清单（无密码、无策略细节）。
func (s *Service) listConnections() (any, error) {
	cfg, err := s.snapshot.Config()
	if err != nil {
		return nil, protocol.Errorf("config_error", "%v", err)
	}
	list := make([]map[string]any, 0, len(cfg.Connections))
	for _, conn := range cfg.Connections {
		list = append(list, map[string]any{
			"id":       conn.ID,
			"name":     conn.Name,
			"kind":     conn.Kind,
			"address":  addressOf(conn),
			"database": conn.Database,
			"preset":   conn.Policy.Preset,
		})
	}
	return map[string]any{"connections": list}, nil
}

func addressOf(conn dbsecurity.ConnectionConfig) string {
	if conn.Kind == dbsecurity.KindSQLite {
		return conn.Path
	}
	return fmt.Sprintf("%s:%d", conn.Host, conn.Port)
}

// schema 把结构查询翻译成各引擎的只读 SQL，再走统一策略门控执行。
func (s *Service) schema(ctx context.Context, params json.RawMessage) (any, error) {
	var req struct {
		ConnectionID string `json:"connectionId"`
		Table        string `json:"table"`
	}
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, protocol.Errorf("bad_request", "参数解析失败：%v", err)
	}
	cfg, err := s.snapshot.Config()
	if err != nil {
		return nil, protocol.Errorf("config_error", "%v", err)
	}
	conn, ok := cfg.ByID(req.ConnectionID)
	if !ok {
		return nil, protocol.Errorf("connection_not_found", "连接不存在：%s", req.ConnectionID)
	}

	sqlText, sqlParams, err := schemaSQL(conn.Kind, req.Table)
	if err != nil {
		return nil, protocol.Errorf("bad_request", "%v", err)
	}
	return s.processSQL(ctx, "query", sqlRequest{ConnectionID: conn.ID, SQL: sqlText, Params: sqlParams})
}

// schemaSQL 生成方言对应的结构查询语句。表名要么走参数化，
// 要么通过严格标识符校验后才允许拼接。
func schemaSQL(kind dbsecurity.DBKind, table string) (string, []any, error) {
	if table != "" && !identifierPattern.MatchString(table) {
		return "", nil, fmt.Errorf("表名含非法字符：%q", table)
	}
	switch kind {
	case dbsecurity.KindMySQL:
		if table == "" {
			return "SHOW TABLES", nil, nil
		}
		return fmt.Sprintf("SHOW FULL COLUMNS FROM `%s`", table), nil, nil
	case dbsecurity.KindPostgres:
		if table == "" {
			return "SELECT table_schema, table_name FROM information_schema.tables " +
				"WHERE table_schema NOT IN ('pg_catalog','information_schema') " +
				"ORDER BY table_schema, table_name", nil, nil
		}
		return "SELECT column_name, data_type, is_nullable, column_default " +
				"FROM information_schema.columns WHERE table_name = $1 ORDER BY ordinal_position",
			[]any{table}, nil
	case dbsecurity.KindSQLite:
		if table == "" {
			return "SELECT name, type FROM sqlite_master " +
				"WHERE type IN ('table','view') AND name NOT LIKE 'sqlite_%' ORDER BY name", nil, nil
		}
		return `SELECT name, type, "notnull", dflt_value, pk FROM pragma_table_info(?) ORDER BY cid`,
			[]any{table}, nil
	}
	return "", nil, fmt.Errorf("不支持的数据库类型：%q", kind)
}

// confirmSummary 是返回给 TS 工具的确认请求描述。
type confirmStatement struct {
	SQL    string `json:"sql"`
	Action string `json:"action"`
	RuleID string `json:"ruleId,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// processSQL 是 query/execute 的统一管线：拆分 → 分类 → 预检 → 裁决 →
// 执行或生成确认 token。任一条 deny 则整批拒绝。
func (s *Service) processSQL(ctx context.Context, mode string, req sqlRequest) (any, error) {
	cfg, err := s.snapshot.Config()
	if err != nil {
		return nil, protocol.Errorf("config_error", "%v", err)
	}
	conn, ok := cfg.ByID(req.ConnectionID)
	if !ok {
		return nil, protocol.Errorf("connection_not_found", "连接不存在：%s", req.ConnectionID)
	}

	stmtTexts := classify.SplitStatements(req.SQL)
	if len(stmtTexts) == 0 {
		return nil, protocol.Errorf("bad_request", "SQL 为空")
	}
	if len(req.Params) > 0 && len(stmtTexts) > 1 {
		return nil, protocol.Errorf("bad_request", "params 仅支持单条语句")
	}

	needPreflight := false
	for _, rule := range s.rulesFor(conn.ID) {
		if rule.Conditions.MaxRows > 0 {
			needPreflight = true
			break
		}
	}

	plans := make([]plannedStatement, 0, len(stmtTexts))
	confirms := make([]confirmStatement, 0)
	var firstConfirmReason string

	for index, text := range stmtTexts {
		stmt := policy.Analyze(text)
		isRead := stmt.Action.Match(dbsecurity.ActionRead)
		if mode == "query" && !isRead {
			return nil, protocol.Errorf("bad_request",
				"query 只接受只读语句：%s（分类为 %s），请改用 execute", summarize(text), stmt.Action)
		}
		if mode == "execute" && isRead {
			return nil, protocol.Errorf("bad_request",
				"execute 不接受只读语句 %s，请改用 query", summarize(text))
		}

		var stmtParams []any
		if index == 0 {
			stmtParams = req.Params
		}

		if needPreflight && stmt.Action == dbsecurity.ActionReadSelect {
			if db, dbErr := s.manager.Get(ctx, conn); dbErr == nil {
				if est, okEst := executor.PreflightRows(ctx, db, conn.Kind, text, stmtParams, preflightTimeout); okEst {
					stmt.EstimatedRows = est
				}
			}
		}

		result := s.engine.Decide(conn.ID, stmt)
		switch result.Decision {
		case dbsecurity.EffectDeny:
			s.record(conn.ID, mode, text, string(stmt.Action), "deny", result.Reason, result.RuleID, 0, 0, "")
			return nil, protocol.Errorf("policy_denied", "%s", result.Reason)
		case dbsecurity.EffectConfirm:
			confirms = append(confirms, confirmStatement{
				SQL: audit.MaskSQL(text), Action: string(stmt.Action),
				RuleID: result.RuleID, Reason: result.Reason,
			})
			if firstConfirmReason == "" {
				firstConfirmReason = result.Reason
			}
		}
		plans = append(plans, plannedStatement{SQL: text, Params: stmtParams, Action: stmt.Action})
	}

	if len(confirms) > 0 {
		token, tokenErr := newToken()
		if tokenErr != nil {
			return nil, protocol.Errorf("internal_error", "生成确认令牌失败")
		}
		s.mu.Lock()
		s.sweepExpiredLocked()
		s.pending[token] = &pendingConfirm{mode: mode, connID: conn.ID, plans: plans, createdAt: time.Now()}
		s.mu.Unlock()
		for _, c := range confirms {
			s.record(conn.ID, mode, c.SQL, c.Action, "confirm", c.Reason, c.RuleID, 0, 0, "")
		}
		return map[string]any{
			"needsConfirm": map[string]any{
				"token":        token,
				"decision":     "confirm",
				"connectionId": conn.ID,
				"reason":       firstConfirmReason,
				"statements":   confirms,
				"expiresInMs":  confirmTTL.Milliseconds(),
			},
		}, nil
	}

	outcome, execErr := s.runStatements(ctx, conn, mode, plans)
	for _, plan := range plans {
		if execErr != nil {
			s.record(conn.ID, mode, plan.SQL, string(plan.Action), "error", "", "", 0, 0, execErr.Error())
		} else {
			s.record(conn.ID, mode, plan.SQL, string(plan.Action), "allow", "", "", 0, 0, "")
		}
	}
	if execErr != nil {
		return nil, protocol.Errorf("execution_failed", "%v", execErr)
	}
	return outcome, nil
}

// confirm 处理用户对确认请求的裁决。token 一次性；allow 执行暂存计划，
// deny 直接丢弃并审计。
func (s *Service) confirm(ctx context.Context, token, decision string) (any, error) {
	s.mu.Lock()
	s.sweepExpiredLocked()
	pending := s.pending[token]
	delete(s.pending, token)
	s.mu.Unlock()

	if pending == nil {
		return nil, protocol.Errorf("invalid_token", "确认令牌无效或已过期")
	}
	cfg, err := s.snapshot.Config()
	if err != nil {
		return nil, protocol.Errorf("config_error", "%v", err)
	}
	conn, ok := cfg.ByID(pending.connID)
	if !ok {
		return nil, protocol.Errorf("connection_not_found", "连接不存在：%s", pending.connID)
	}

	switch decision {
	case "deny":
		for _, plan := range pending.plans {
			s.record(conn.ID, pending.mode, plan.SQL, string(plan.Action), "deny", "用户在确认对话框中拒绝", "", 0, 0, "")
		}
		return map[string]any{"denied": true}, nil
	case "allow":
	default:
		return nil, protocol.Errorf("bad_request", "confirm decision 必须是 allow 或 deny：%q", decision)
	}

	outcome, execErr := s.runStatements(ctx, conn, pending.mode, pending.plans)
	for _, plan := range pending.plans {
		if execErr != nil {
			s.record(conn.ID, pending.mode, plan.SQL, string(plan.Action), "error", "用户确认后执行失败", "", 0, 0, execErr.Error())
		} else {
			s.record(conn.ID, pending.mode, plan.SQL, string(plan.Action), "allow", "用户确认后执行", "", 0, 0, "")
		}
	}
	if execErr != nil {
		return nil, protocol.Errorf("execution_failed", "%v", execErr)
	}
	return outcome, nil
}

// runStatements 执行已通过策略门控的语句。
// query：逐条 RunQuery（行数截断）；execute：默认包在事务内串行，
// 语句自带 BEGIN/COMMIT 时不额外包事务。
func (s *Service) runStatements(ctx context.Context, conn dbsecurity.ConnectionConfig, mode string, plans []plannedStatement) (map[string]any, error) {
	db, err := s.manager.Get(ctx, conn)
	if err != nil {
		return nil, err
	}
	timeout := time.Duration(conn.QueryTimeout()) * time.Second

	if mode == "query" {
		results := make([]map[string]any, 0, len(plans))
		for _, plan := range plans {
			qr, qErr := executor.RunQuery(ctx, db, conn.Kind, plan.SQL, plan.Params, timeout, conn.RowLimit())
			if qErr != nil {
				return nil, fmt.Errorf("%s：%w", summarize(plan.SQL), qErr)
			}
			results = append(results, map[string]any{
				"sql": audit.MaskSQL(plan.SQL), "columns": qr.Columns, "rows": qr.Rows,
				"rowCount": qr.RowCount, "truncated": qr.Truncated, "durationMs": qr.DurationMs,
			})
		}
		return map[string]any{"statements": results}, nil
	}

	txControlled := false
	for _, plan := range plans {
		if plan.Action == dbsecurity.ActionTransaction {
			txControlled = true
			break
		}
	}

	results := make([]map[string]any, 0, len(plans))
	if txControlled {
		for _, plan := range plans {
			er, eErr := executor.RunExec(ctx, db, plan.SQL, plan.Params, timeout)
			if eErr != nil {
				return nil, fmt.Errorf("%s：%w", summarize(plan.SQL), eErr)
			}
			results = append(results, execEntry(plan.SQL, er))
		}
		return map[string]any{"statements": results, "transaction": false}, nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("开启事务失败：%w", err)
	}
	for _, plan := range plans {
		er, eErr := executor.RunExec(ctx, tx, plan.SQL, plan.Params, timeout)
		if eErr != nil {
			_ = tx.Rollback()
			return nil, fmt.Errorf("%s：%w", summarize(plan.SQL), eErr)
		}
		results = append(results, execEntry(plan.SQL, er))
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("提交事务失败：%w", err)
	}
	return map[string]any{"statements": results, "transaction": true}, nil
}

func execEntry(sqlText string, er *executor.ExecResult) map[string]any {
	return map[string]any{
		"sql": audit.MaskSQL(sqlText), "affectedRows": er.AffectedRows,
		"affectedKnown": er.AffectedKnown, "durationMs": er.DurationMs,
	}
}

// record 写一条审计。
func (s *Service) record(connID, action, sqlText, sqlAction, decision, reason, ruleID string, durationMs int64, rowCount int, errMsg string) {
	s.recorder.Record(audit.Event{
		ConnectionID: connID,
		Action:       action,
		SQL:          audit.MaskSQL(sqlText),
		SQLAction:    sqlAction,
		Decision:     decision,
		Reason:       reason,
		RuleID:       ruleID,
		DurationMs:   durationMs,
		RowCount:     rowCount,
		Error:        errMsg,
	})
}

// sweepExpiredLocked 清理过期 token；调用方须持有 s.mu。
func (s *Service) sweepExpiredLocked() {
	now := time.Now()
	for token, p := range s.pending {
		if now.Sub(p.createdAt) > confirmTTL {
			delete(s.pending, token)
		}
	}
}

func newToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// summarize 生成 SQL 摘要用于错误信息。
func summarize(sqlText string) string {
	runes := []rune(sqlText)
	if len(runes) > 80 {
		return string(runes[:80]) + "…"
	}
	return string(runes)
}
