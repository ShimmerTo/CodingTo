package connection

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"codingto/internal/dbsecurity"
	"codingto/internal/dbsecuritybridge/tunnel"
)

const (
	// DefaultIdleTimeout 是连接池空闲回收阈值：超过该时长无请求的
	// sql.DB 会被整体 Close，避免常驻连接。
	DefaultIdleTimeout = 10 * time.Minute
	// sweepInterval 是空闲回收的检查间隔。bridge 是会话级子进程，
	// 低频扫描即可，不做心跳/轮询探活。
	sweepInterval = 2 * time.Minute
	// pingTimeout 是新建连接时的连通性校验超时。
	pingTimeout = 10 * time.Second
)

type entry struct {
	db          *sql.DB
	tunnel      *tunnel.Tunnel
	fingerprint string
	lastUse     time.Time
}

func (e *entry) close() {
	_ = e.db.Close()
	if e.tunnel != nil {
		e.tunnel.Close()
	}
}

// Manager 按连接 ID 维护 sql.DB 池：懒加载、空闲回收、配置变更后
// 剔除已删除的连接。
type Manager struct {
	mu          sync.Mutex
	entries     map[string]*entry
	idleTimeout time.Duration
	stopCh      chan struct{}
	closed      bool
}

func NewManager() *Manager {
	m := &Manager{
		entries:     make(map[string]*entry),
		idleTimeout: DefaultIdleTimeout,
		stopCh:      make(chan struct{}),
	}
	go m.sweepLoop()
	return m
}

// Get 返回连接对应的 sql.DB；首次或 DSN 变更时懒加载重建，
// 并用 Ping 做一次性连通性校验。配置了 SSHTunnel 的连接会先建立
// SSH 本地端口转发，DSN 目标改为本地监听地址。
func (m *Manager) Get(ctx context.Context, conn dbsecurity.ConnectionConfig) (*sql.DB, error) {
	var tun *tunnel.Tunnel
	var driver, dsn string
	var err error

	if conn.SSHTunnel != nil && conn.Kind != dbsecurity.KindSQLite {
		tun, err = tunnel.Dial(*conn.SSHTunnel, conn.Host, conn.Port)
		if err != nil {
			return nil, err
		}
		driver, dsn, err = conn.DSNFor(tun.Address())
	} else {
		driver, dsn, err = conn.DSN()
	}
	if err != nil {
		if tun != nil {
			tun.Close()
		}
		return nil, err
	}
	fingerprint := driver + "\x00" + dsn

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		if tun != nil {
			tun.Close()
		}
		return nil, fmt.Errorf("连接管理器已关闭")
	}

	if existing, ok := m.entries[conn.ID]; ok && existing.fingerprint == fingerprint {
		// 复用旧池与旧隧道，刚建立的隧道立即回收。
		if tun != nil {
			tun.Close()
		}
		existing.lastUse = time.Now()
		return existing.db, nil
	}
	// DSN/隧道变更：先关闭旧池与旧隧道。
	if existing, ok := m.entries[conn.ID]; ok {
		existing.close()
		delete(m.entries, conn.ID)
	}

	db, err := sql.Open(driver, dsn)
	if err != nil {
		if tun != nil {
			tun.Close()
		}
		return nil, fmt.Errorf("打开数据库连接失败：%w", err)
	}
	// 轻量池配置：Agent 查询为低频串行场景。
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(1)
	db.SetConnMaxIdleTime(m.idleTimeout)

	pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
	err = db.PingContext(pingCtx)
	cancel()
	if err != nil {
		_ = db.Close()
		if tun != nil {
			tun.Close()
		}
		return nil, fmt.Errorf("数据库连接失败：%w", err)
	}

	m.entries[conn.ID] = &entry{db: db, tunnel: tun, fingerprint: fingerprint, lastUse: time.Now()}
	return db, nil
}

// Reconcile 在配置重载后调用：关闭并移除不再存在于配置中的连接池。
func (m *Manager) Reconcile(config dbsecurity.DBConfig) {
	alive := make(map[string]struct{}, len(config.Connections))
	for _, conn := range config.Connections {
		alive[conn.ID] = struct{}{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, e := range m.entries {
		if _, ok := alive[id]; !ok {
			_ = e.db.Close()
			delete(m.entries, id)
		}
	}
}

// Close 关闭所有连接池与回收协程。bridge 退出前调用。
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.closed = true
	close(m.stopCh)
	for id, e := range m.entries {
		_ = e.db.Close()
		delete(m.entries, id)
	}
}

// sweepLoop 定期回收空闲超时的连接池。
func (m *Manager) sweepLoop() {
	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-m.stopCh:
			return
		case now := <-ticker.C:
			m.sweep(now)
		}
	}
}

func (m *Manager) sweep(now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, e := range m.entries {
		if now.Sub(e.lastUse) > m.idleTimeout {
			_ = e.db.Close()
			delete(m.entries, id)
		}
	}
}
