package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"codingto/internal/applog"
	"codingto/internal/dbsecurity"
)

// dbTestTimeout 是「测试连接」一次性子进程的总超时。
const dbTestTimeout = 10 * time.Second

type SaveDBConfigRequest struct {
	Connections []dbsecurity.ConnectionConfig `json:"connections"`
}

type DBTestRequest struct {
	Connection dbsecurity.ConnectionConfig `json:"connection"`
}

type DBTestResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// GetDBConfig 返回连接清单与各自的策略；密码一律脱敏为空串。
func (a *App) GetDBConfig() dbsecurity.DBConfig {
	cfg := a.store.Get()
	return cfg.Extensions.DB.Masked()
}

// SaveDBConfig 持久化连接与策略并重写活动会话快照。
// 密码为空串表示沿用已存密码（前端永不回显密码）。
func (a *App) SaveDBConfig(req SaveDBConfigRequest) (dbsecurity.DBConfig, error) {
	cfg := a.store.Get()
	previous := make(map[string]string, len(cfg.Extensions.DB.Connections))
	for _, conn := range cfg.Extensions.DB.Connections {
		previous[conn.ID] = conn.Password
	}
	for i := range req.Connections {
		conn := &req.Connections[i]
		if conn.Password == "" {
			conn.Password = previous[conn.ID]
		}
		if err := conn.Validate(); err != nil {
			return dbsecurity.DBConfig{}, err
		}
	}
	cfg.Extensions.DB = dbsecurity.DBConfig{Connections: req.Connections}.WithoutTunnels()
	if err := a.store.Save(cfg); err != nil {
		return dbsecurity.DBConfig{}, err
	}
	if a.agent != nil {
		a.agent.refreshActiveDBSnapshot()
	}
	return a.store.Get().Extensions.DB.Masked(), nil
}

// TestDBConnection 以一次性 test-connection 子进程验证表单中的连接。
// 表单连接被写入临时 0600 快照（空密码沿用已存密码），测完即删；
// driver 依赖只存在于 bridge 二进制，主程序不加载。
func (a *App) TestDBConnection(req DBTestRequest) (DBTestResult, error) {
	conn := req.Connection
	if conn.Password == "" {
		if existing, ok := a.store.Get().Extensions.DB.ByID(conn.ID); ok {
			conn.Password = existing.Password
		}
	}
	if err := conn.Validate(); err != nil {
		return DBTestResult{OK: false, Message: err.Error()}, nil
	}
	resolveSSHTunnel(a.store.Get().SSHConfigs, &conn)
	bin, err := resolveDBBridgeBinary()
	if err != nil {
		return DBTestResult{OK: false, Message: err.Error()}, nil
	}

	tempDir, err := os.MkdirTemp("", "codingto-dbtest-")
	if err != nil {
		return DBTestResult{}, fmt.Errorf("create temp directory: %w", err)
	}
	defer os.RemoveAll(tempDir)
	configPath := filepath.Join(tempDir, "config.json")
	raw, err := json.Marshal(dbsecurity.DBConfig{Connections: []dbsecurity.ConnectionConfig{conn}})
	if err != nil {
		return DBTestResult{}, fmt.Errorf("marshal test config: %w", err)
	}
	if err := os.WriteFile(configPath, raw, 0o600); err != nil {
		return DBTestResult{}, fmt.Errorf("write test config: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), dbTestTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, bin, "db-security-bridge", "test-connection", "--config", configPath, "--conn", conn.ID)
	command.Env = append(os.Environ(), "CODINGTO_SSH_KNOWN_HOSTS="+knownHostsPath(a.store.Dir()))
	configureDocumentBridgeProcess(command)
	output, _ := command.Output() // 非零退出码也带 JSON 结果

	var result DBTestResult
	if err := json.Unmarshal(output, &result); err != nil {
		if ctx.Err() != nil {
			return DBTestResult{OK: false, Message: "连接测试超时"}, nil
		}
		return DBTestResult{OK: false, Message: "bridge 未返回有效结果"}, nil
	}
	return result, nil
}

// GetDBAuditLogs 读取最近的审计记录（优先活动会话，其次最近的历史会话）。
// 纯按需调用：仅在连接编辑弹窗打开时触发，不做常驻监听。
func (a *App) GetDBAuditLogs(connectionID string, limit int) ([]map[string]any, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}

	auditPath := ""
	if a.agent != nil {
		a.agent.mu.Lock()
		if a.agent.activeSessionDir != "" {
			candidate := dbSnapshotAuditPath(a.agent.activeSessionDir)
			if fileExists(candidate) {
				auditPath = candidate
			}
		}
		a.agent.mu.Unlock()
	}
	if auditPath == "" {
		sessions, err := a.store.Store().ListSessions()
		if err != nil {
			return []map[string]any{}, nil
		}
		// 从最近的会话开始找第一个含审计文件的会话目录。
		checked := 0
		for i := len(sessions) - 1; i >= 0 && checked < 20; i-- {
			checked++
			if sessions[i].SessionDir == "" {
				continue
			}
			candidate := dbSnapshotAuditPath(sessions[i].SessionDir)
			if fileExists(candidate) {
				auditPath = candidate
				break
			}
		}
	}
	if auditPath == "" {
		return []map[string]any{}, nil
	}

	raw, err := os.ReadFile(auditPath)
	if err != nil {
		applog.Warnf("read db audit log: %v", err)
		return []map[string]any{}, nil
	}

	events := []map[string]any{}
	for _, line := range splitLines(raw) {
		var event map[string]any
		if json.Unmarshal(line, &event) != nil {
			continue
		}
		if connectionID != "" {
			if id, _ := event["connectionId"].(string); id != connectionID {
				continue
			}
		}
		events = append(events, event)
	}
	if len(events) > limit {
		events = events[len(events)-limit:]
	}
	return events, nil
}

func dbSnapshotAuditPath(sessionDir string) string {
	return filepath.Join(sessionDir, ".db-security", "audit.jsonl")
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func splitLines(raw []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i := 0; i < len(raw); i++ {
		if raw[i] == '\n' {
			if i > start {
				lines = append(lines, raw[start:i])
			}
			start = i + 1
		}
	}
	if start < len(raw) {
		lines = append(lines, raw[start:])
	}
	return lines
}
