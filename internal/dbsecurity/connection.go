package dbsecurity

import (
	"fmt"
	"net/url"
	"strings"
)

// DBKind 标识数据库类型。
type DBKind string

const (
	KindMySQL    = DBKind("mysql")
	KindPostgres = DBKind("postgres")
	KindSQLite   = DBKind("sqlite")
)

func (k DBKind) Valid() bool {
	switch k {
	case KindMySQL, KindPostgres, KindSQLite:
		return true
	}
	return false
}

// SSHTunnel 是到跳板机的 SSH 本地端口转发参数，由 SSHConfigID 引用的全局
// SSH 配置在写会话快照 / 测试配置时解析生成。只出现在 App 存储解析后的
// 0600 快照与临时测试配置中，永不面向前端/Agent 输出。
// AuthMode 与全局 SSH 配置一致："password" 用 Password，"key" 用
// PrivateKey（PEM，可选 PrivateKeyPassphrase）。
type SSHTunnel struct {
	Address              string `json:"address,omitempty"`
	Port                 int    `json:"port,omitempty"`
	Username             string `json:"username,omitempty"`
	AuthMode             string `json:"authMode,omitempty"`
	Password             string `json:"password,omitempty"`
	PrivateKey           string `json:"privateKey,omitempty"`
	PrivateKeyPassphrase string `json:"privateKeyPassphrase,omitempty"`
}

// ConnectionConfig 是一条数据库连接及其连接级权限策略。
// Password 只存在于 App 存储与 0600 快照文件中，任何面向前端/Agent 的
// 返回都必须先经 Masked 脱敏。
type ConnectionConfig struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Kind     DBKind `json:"kind"`
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	Database string `json:"database,omitempty"`
	// Path 仅 SQLite 使用：数据库文件路径。
	Path     string `json:"path,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	// SSLMode 仅 Postgres 使用，默认 disable。
	SSLMode string `json:"sslMode,omitempty"`
	// SSHConfigID 引用全局 SSH 配置（跳板机），用于通过 SSH 隧道访问
	// 内网数据库；仅 MySQL/PostgreSQL 生效，SQLite 忽略。
	SSHConfigID string `json:"sshConfigId,omitempty"`
	// SSHTunnel 是 SSHConfigID 解析后的隧道参数，仅由 App 层写入会话快照
	// 与临时测试配置，不参与存储与前端往返。
	SSHTunnel *SSHTunnel `json:"sshTunnel,omitempty"`
	// Policy 是连接级策略（preset + override），只在该连接上生效。
	Policy Policy `json:"policy"`
	// QueryTimeoutSeconds 是语句级超时，0 表示使用默认 30s。
	QueryTimeoutSeconds int `json:"queryTimeoutSeconds,omitempty"`
	// MaxRows 是查询结果行数上限，0 表示使用默认 500。
	MaxRows int `json:"maxRows,omitempty"`
}

func (c ConnectionConfig) Validate() error {
	if strings.TrimSpace(c.ID) == "" {
		return fmt.Errorf("连接 ID 不能为空")
	}
	if strings.TrimSpace(c.Name) == "" {
		return fmt.Errorf("连接名称不能为空")
	}
	if !c.Kind.Valid() {
		return fmt.Errorf("不支持的数据库类型：%q", c.Kind)
	}
	switch c.Kind {
	case KindSQLite:
		if strings.TrimSpace(c.Path) == "" {
			return fmt.Errorf("SQLite 连接必须提供数据库文件路径")
		}
	default:
		if strings.TrimSpace(c.Host) == "" {
			return fmt.Errorf("主机地址不能为空")
		}
		if c.Port < 1 || c.Port > 65535 {
			return fmt.Errorf("端口必须在 1-65535 之间")
		}
	}
	return nil
}

// QueryTimeout 返回生效的语句超时（秒）。
func (c ConnectionConfig) QueryTimeout() int {
	if c.QueryTimeoutSeconds <= 0 {
		return 30
	}
	if c.QueryTimeoutSeconds > 600 {
		return 600
	}
	return c.QueryTimeoutSeconds
}

// RowLimit 返回生效的结果行数上限。
func (c ConnectionConfig) RowLimit() int {
	if c.MaxRows <= 0 {
		return 500
	}
	if c.MaxRows > 5000 {
		return 5000
	}
	return c.MaxRows
}

// Address 返回数据库目标地址 host:port；端口缺失时按类型使用默认端口，
// SQLite 返回文件路径。
func (c ConnectionConfig) Address() string {
	switch c.Kind {
	case KindPostgres:
		return hostPort(c.Host, c.Port, 5432)
	case KindSQLite:
		return c.Path
	default:
		return hostPort(c.Host, c.Port, 3306)
	}
}

// DSN 构造 driver 名称与连接串（目标地址取 Address）。密码只在此处拼装进
// 连接串，不进入日志、协议响应或审计记录。
func (c ConnectionConfig) DSN() (driver string, dsn string, err error) {
	return c.DSNFor(c.Address())
}

// DSNFor 使用给定目标地址（可为 SSH 隧道的本地监听地址）构造连接串。
func (c ConnectionConfig) DSNFor(address string) (driver string, dsn string, err error) {
	switch c.Kind {
	case KindMySQL:
		dsn = fmt.Sprintf("%s:%s@tcp(%s)/%s?timeout=10s&parseTime=true&interpolateParams=true",
			c.Username, c.Password, address, c.Database)
		return "mysql", dsn, nil
	case KindPostgres:
		sslMode := strings.TrimSpace(c.SSLMode)
		if sslMode == "" {
			sslMode = "disable"
		}
		u := url.URL{
			Scheme: "postgres",
			Host:   address,
			Path:   "/" + c.Database,
		}
		if c.Username != "" {
			u.User = url.UserPassword(c.Username, c.Password)
		}
		query := url.Values{}
		query.Set("sslmode", sslMode)
		query.Set("connect_timeout", "10")
		u.RawQuery = query.Encode()
		return "postgres", u.String(), nil
	case KindSQLite:
		if strings.TrimSpace(c.Path) == "" {
			return "", "", fmt.Errorf("SQLite 连接缺少文件路径")
		}
		return "sqlite", c.Path, nil
	}
	return "", "", fmt.Errorf("不支持的数据库类型：%q", c.Kind)
}

func hostPort(host string, port, fallback int) string {
	if port <= 0 {
		port = fallback
	}
	return fmt.Sprintf("%s:%d", host, port)
}

// DBConfig 是全局 DB 资产：连接清单（每条自带 preset + override）。
type DBConfig struct {
	Connections []ConnectionConfig `json:"connections"`
}

func (c *DBConfig) Normalize() {
	connections := make([]ConnectionConfig, 0, len(c.Connections))
	seen := make(map[string]struct{}, len(c.Connections))
	for index := range c.Connections {
		conn := c.Connections[index]
		if strings.TrimSpace(conn.ID) == "" {
			conn.ID = fmt.Sprintf("db-%d", index+1)
		}
		if _, dup := seen[conn.ID]; dup {
			continue
		}
		if conn.Validate() != nil {
			continue
		}
		conn.Policy.Normalize()
		// SSHTunnel 保留：它是 App 层由 SSHConfigID 解析后写入快照/测试配置
		// 的合法输入，bridge 加载快照后必须能读到它才能建立隧道。
		// 存储流如需剔除派生隧道字段，显式调用 WithoutTunnels。
		seen[conn.ID] = struct{}{}
		connections = append(connections, conn)
	}
	c.Connections = connections
}

// WithoutTunnels 返回去掉派生隧道字段的副本，用于 App 持久化存储：
// 隧道参数只在写快照时由 SSHConfigID 重新解析，不落库、不回显前端。
func (c DBConfig) WithoutTunnels() DBConfig {
	copy := DBConfig{Connections: make([]ConnectionConfig, 0, len(c.Connections))}
	for _, conn := range c.Connections {
		conn.SSHTunnel = nil
		copy.Connections = append(copy.Connections, conn)
	}
	return copy
}

// ByID 按 ID 查找连接。
func (c DBConfig) ByID(id string) (ConnectionConfig, bool) {
	for _, conn := range c.Connections {
		if conn.ID == id {
			return conn, true
		}
	}
	return ConnectionConfig{}, false
}

// Masked 返回脱敏副本：密码一律置空，隧道参数一并剥离（SSHConfigID 保留，
// 由 App 层在写快照时重新解析），用于任何面向前端/Agent 的输出。
func (c DBConfig) Masked() DBConfig {
	masked := DBConfig{Connections: make([]ConnectionConfig, 0, len(c.Connections))}
	for _, conn := range c.Connections {
		conn.Password = ""
		conn.SSHTunnel = nil
		masked.Connections = append(masked.Connections, conn)
	}
	return masked
}
