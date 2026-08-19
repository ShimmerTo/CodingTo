package tunnel

import (
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"codingto/internal/applog"
	"codingto/internal/dbsecurity"
)

// dialTimeout 是 SSH 握手与建链总超时；建立失败立即返回，不悬挂。
const dialTimeout = 10 * time.Second

// Tunnel 是到跳板机的 SSH 本地端口转发：本地 127.0.0.1 随机端口监听，
// 每个进入连接经 SSH 通道转发到目标数据库地址。生命周期与所属连接池一致，
// 空闲回收或 bridge 退出时整体关闭，不做常驻心跳/轮询。
type Tunnel struct {
	client *ssh.Client
	ln     net.Listener
	target string
	addr   string

	mu     sync.Mutex
	closed bool
}

// dialClient 建立到跳板机的 SSH 连接，并让「TCP 建连 + SSH 握手」整体受
// dialTimeout 约束：仅靠 ClientConfig.Timeout 只限制 TCP 建连，握手阶段若
// 服务端收包后不回包会无限悬挂（表现为前端「测试连接」永远转圈）。
// 握手完成后清除 deadline，长生命周期隧道不受影响。
// 各阶段写 applog，便于从 ~/.codingto/logs/codingto/YYYY/MM/DD.log 定位卡死点。
func dialClient(cfg dbsecurity.SSHTunnel, clientCfg *ssh.ClientConfig) (*ssh.Client, error) {
	sshAddr := net.JoinHostPort(cfg.Address, strconv.Itoa(tunnelPort(cfg.Port)))
	start := time.Now()
	applog.Infof("[ssh-dial] dialtcp start: %s", sshAddr)
	conn, err := net.DialTimeout("tcp", sshAddr, dialTimeout)
	if err != nil {
		applog.Warnf("[ssh-dial] dialtcp failed after %s: %v", time.Since(start), err)
		return nil, err
	}
	applog.Infof("[ssh-dial] dialtcp ok after %s, starting handshake", time.Since(start))
	_ = conn.SetDeadline(time.Now().Add(dialTimeout))
	c, chans, reqs, err := ssh.NewClientConn(conn, sshAddr, clientCfg)
	if err != nil {
		applog.Warnf("[ssh-dial] handshake failed after %s: %v", time.Since(start), err)
		_ = conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	applog.Infof("[ssh-dial] handshake ok after %s", time.Since(start))
	return ssh.NewClient(c, chans, reqs), nil
}

// Dial 建立到跳板机的 SSH 连接并在本地随机端口监听，返回隧道。
// cfg 为跳板机参数（Address/Port/Username/AuthMode/凭据），dbHost/dbPort
// 为内网数据库目标。
func Dial(cfg dbsecurity.SSHTunnel, dbHost string, dbPort int) (*Tunnel, error) {
	if dbPort <= 0 {
		return nil, fmt.Errorf("SSH 隧道目标数据库端口无效：%d", dbPort)
	}
	authMethod, err := authMethodFor(cfg)
	if err != nil {
		return nil, err
	}
	clientCfg := &ssh.ClientConfig{
		User: cfg.Username,
		Auth: []ssh.AuthMethod{authMethod},
		// 与 App 内 SSH 配置一致，不维护主机指纹：跳板机场景下不做
		// 主机密钥校验。
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         dialTimeout,
	}
	client, err := dialClient(cfg, clientCfg)
	if err != nil {
		return nil, fmt.Errorf("SSH 连接跳板机失败：%w", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("SSH 隧道本地监听失败：%w", err)
	}
	t := &Tunnel{
		client: client,
		ln:     ln,
		target: net.JoinHostPort(dbHost, strconv.Itoa(dbPort)),
		addr:   ln.Addr().String(),
	}
	go t.acceptLoop()
	return t, nil
}

// TestConnection 验证到跳板机的 SSH 连接是否可用：握手成功即关闭并返回 nil。
// 供 App 的「测试连接」按钮调用，一次性执行，不建立常驻隧道。
func TestConnection(cfg dbsecurity.SSHTunnel) error {
	authMethod, err := authMethodFor(cfg)
	if err != nil {
		return err
	}
	clientCfg := &ssh.ClientConfig{
		User:            cfg.Username,
		Auth:            []ssh.AuthMethod{authMethod},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         dialTimeout,
	}
	client, err := dialClient(cfg, clientCfg)
	if err != nil {
		return fmt.Errorf("SSH 连接失败：%w", err)
	}
	_ = client.Close()
	return nil
}

// authMethodFor 按认证模式构造 ssh.AuthMethod：password 用密码，
// key 解析 PEM 私钥（支持 passphrase 加密）。
func authMethodFor(cfg dbsecurity.SSHTunnel) (ssh.AuthMethod, error) {
	if cfg.AuthMode == "key" {
		key := []byte(strings.TrimSpace(cfg.PrivateKey))
		if len(key) == 0 {
			return nil, fmt.Errorf("SSH 私钥为空")
		}
		var signer ssh.Signer
		var err error
		if cfg.PrivateKeyPassphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(key, []byte(cfg.PrivateKeyPassphrase))
		} else {
			signer, err = ssh.ParsePrivateKey(key)
		}
		if err != nil {
			return nil, fmt.Errorf("解析 SSH 私钥失败：%w", err)
		}
		return ssh.PublicKeys(signer), nil
	}
	return ssh.Password(cfg.Password), nil
}

func tunnelPort(p int) int {
	if p <= 0 || p > 65535 {
		return 22
	}
	return p
}

// Address 返回本地监听地址（127.0.0.1:port），用作 DSN 的目标 host:port。
func (t *Tunnel) Address() string { return t.addr }

func (t *Tunnel) isClosed() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closed
}

func (t *Tunnel) acceptLoop() {
	for {
		local, err := t.ln.Accept()
		if err != nil {
			if t.isClosed() {
				return
			}
			// 瞬时错误：低频退避后继续，不视为致命。
			time.Sleep(50 * time.Millisecond)
			continue
		}
		go t.forward(local)
	}
}

func (t *Tunnel) forward(local net.Conn) {
	remote, err := t.client.Dial("tcp", t.target)
	if err != nil {
		_ = local.Close()
		return
	}
	go copyAndClose(remote, local)
	copyAndClose(local, remote)
}

func copyAndClose(dst net.Conn, src net.Conn) {
	_, _ = io.Copy(dst, src)
	_ = dst.Close()
	_ = src.Close()
}

// Close 关闭本地监听与 SSH 会话，所有转发连接随之断开。
func (t *Tunnel) Close() {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.closed = true
	t.mu.Unlock()
	_ = t.ln.Close()
	_ = t.client.Close()
}
