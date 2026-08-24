package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"codingto/internal/applog"
	"codingto/internal/dbsecurity"
)

// DB Security Gateway 的会话级配置快照：
// 密码只存在于 App 存储与本文件（0600，仅本机 bridge 读取），
// 不经过环境变量、不进协议响应、不进审计。

func dbSnapshotDir(sessionDir string) string {
	return filepath.Join(sessionDir, ".db-security")
}

func dbSnapshotPath(sessionDir string) string {
	return filepath.Join(dbSnapshotDir(sessionDir), "config.json")
}

// dbAuthorizedConnections 返回会话所属工作空间勾选的 DB 连接 ID。
func dbAuthorizedConnections(store *ConfigStore, cfg AppConfig, sessionID int64) []string {
	if sessionID <= 0 {
		return nil
	}
	session, ok, err := store.Store().SessionByID(sessionID)
	if err != nil || !ok {
		return nil
	}
	for _, env := range cfg.Environments {
		if env.ID == session.EnvironmentID {
			return env.DBConnections
		}
	}
	return nil
}

// resolveSSHTunnel 按 SSHConfigID 从全局 SSH 配置解析隧道参数。
// SQLite 为本地文件，忽略隧道；找不到或地址为空时清空隧道（视为无跳板直连）。
func resolveSSHTunnel(sshConfigs []SSHConfig, conn *dbsecurity.ConnectionConfig) {
	if conn.SSHConfigID == "" || conn.Kind == dbsecurity.KindSQLite {
		conn.SSHTunnel = nil
		return
	}
	for _, s := range sshConfigs {
		if s.ID != conn.SSHConfigID {
			continue
		}
		if strings.TrimSpace(s.Address) == "" {
			conn.SSHTunnel = nil
			return
		}
		port := s.Port
		if port <= 0 || port > 65535 {
			port = 22
		}
		conn.SSHTunnel = &dbsecurity.SSHTunnel{
			Address:              s.Address,
			Port:                 port,
			Username:             s.Username,
			AuthMode:             s.AuthMode,
			Password:             s.Password,
			PrivateKey:           s.PrivateKey,
			PrivateKeyPassphrase: s.PrivateKeyPassphrase,
			HostKeyFingerprint:   s.HostKeyFingerprint,
		}
		return
	}
	conn.SSHTunnel = nil
}

// writeDBSnapshot 把勾选范围内的连接（含密码、策略与 SSH 隧道参数）写入
// 会话目录，权限 0600。勾选为空或勾选 ID 均已失效时删除既有快照，
// 返回实际写入的连接数。
func writeDBSnapshot(sessionDir string, db dbsecurity.DBConfig, sshConfigs []SSHConfig, authorized []string) int {
	target := dbSnapshotPath(sessionDir)
	if len(authorized) == 0 {
		_ = os.Remove(target)
		return 0
	}
	allowed := make(map[string]bool, len(authorized))
	for _, id := range authorized {
		allowed[id] = true
	}
	snapshot := dbsecurity.DBConfig{Connections: []dbsecurity.ConnectionConfig{}}
	for _, conn := range db.Connections {
		if !allowed[conn.ID] {
			continue
		}
		resolveSSHTunnel(sshConfigs, &conn)
		snapshot.Connections = append(snapshot.Connections, conn)
	}
	if len(snapshot.Connections) == 0 {
		_ = os.Remove(target)
		return 0
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return 0
	}
	if err := ensurePrivateDir(dbSnapshotDir(sessionDir)); err != nil {
		applog.Warnf("create db snapshot dir: %v", err)
		return 0
	}
	if err := writePrivateFileAtomic(target, raw); err != nil {
		applog.Warnf("write db snapshot: %v", err)
		return 0
	}
	return len(snapshot.Connections)
}

// configureDBSessionEnv 在会话启动时注入 bridge 环境变量。
// 仅当工作空间勾选了 DB 连接且 bridge 二进制可用时注入；
// 未勾选时 TS 工具因缺少环境变量而报 db_disabled。
func configureDBSessionEnv(agentEnv map[string]string, store *ConfigStore, cfg AppConfig, sessionID int64, sessionDir string) {
	authorized := dbAuthorizedConnections(store, cfg, sessionID)
	if len(authorized) == 0 {
		return
	}
	bin, err := resolveDBBridgeBinary()
	if err != nil {
		applog.Warnf("db security bridge unavailable: %v", err)
		return
	}
	if writeDBSnapshot(sessionDir, cfg.Extensions.DB, cfg.SSHConfigs, authorized) == 0 {
		return
	}
	agentEnv["CODINGTO_DB_BRIDGE_BIN"] = bin
	agentEnv["CODINGTO_DB_CONFIG_PATH"] = dbSnapshotPath(sessionDir)
	agentEnv["CODINGTO_SSH_KNOWN_HOSTS"] = knownHostsPath(store.Dir())
}

// refreshActiveDBSnapshot 在 DB 配置或工作空间勾选变更后重写活动会话
// 的快照；bridge 在下次请求时按 mtime 懒重载，无需通知。
func (s *AgentService) refreshActiveDBSnapshot() {
	s.mu.Lock()
	sessionDir, sessionID := s.activeSessionDir, s.activeSessionID
	s.mu.Unlock()
	if sessionDir == "" {
		return
	}
	cfg := s.store.Get()
	writeDBSnapshot(sessionDir, cfg.Extensions.DB, cfg.SSHConfigs, dbAuthorizedConnections(s.store, cfg, sessionID))
}
