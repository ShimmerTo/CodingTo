package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"codingto/internal/sshsecurity"
	"golang.org/x/crypto/ssh"
)

const (
	dialTimeout    = 10 * time.Second
	maxOutputBytes = 256 * 1024
)

var safeShellTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_./:@%+=,-]+$`)

// Result is the bounded output of one remote capability execution.
type Result struct {
	Output     string `json:"output"`
	ExitCode   int    `json:"exitCode"`
	DurationMs int64  `json:"durationMs"`
	Truncated  bool   `json:"truncated"`
}

// Prepared is a validated command ready for SSH execution.
type Prepared struct {
	Command string
	Summary map[string]string
}

// Prepare validates every supplied parameter and renders a shell-safe argv command.
// Standard SSH exec transports a command string; strict token quoting keeps Agent
// values data-only even though the remote SSH daemon invokes the account shell.
func Prepare(resource sshsecurity.Resource, capability sshsecurity.Capability, values map[string]any) (Prepared, error) {
	if capability.Name == "shell.raw" {
		spec := capability.Params["command"]
		command, err := spec.ValidateValue(values["command"])
		if err != nil {
			return Prepared{}, fmt.Errorf("command：%w", err)
		}
		if len(values) != 1 {
			return Prepared{}, fmt.Errorf("shell.raw 只接受 command 参数")
		}
		return Prepared{Command: command, Summary: map[string]string{"command": command}}, nil
	}
	if err := capability.Validate(strings.HasPrefix(capability.Name, "custom.")); err != nil {
		return Prepared{}, err
	}
	for name := range values {
		if _, ok := capability.Params[name]; !ok {
			return Prepared{}, fmt.Errorf("未声明参数：%s", name)
		}
	}
	validated := make(map[string]string, len(capability.Params))
	for name, spec := range capability.Params {
		value, err := spec.ValidateValue(values[name])
		if err != nil {
			return Prepared{}, fmt.Errorf("%s：%w", name, err)
		}
		validated[name] = value
	}
	args := make([]string, 0, len(capability.Args))
	for _, template := range capability.Args {
		if template == "{resourceWorkDir}" {
			if strings.TrimSpace(resource.WorkDir) == "" {
				return Prepared{}, fmt.Errorf("该 Git 能力需要工作区配置远程目录")
			}
			args = append(args, resource.WorkDir)
			continue
		}
		if strings.HasPrefix(template, "{") && strings.HasSuffix(template, "}") {
			name := strings.TrimSuffix(strings.TrimPrefix(template, "{"), "}")
			args = append(args, validated[name])
			continue
		}
		args = append(args, template)
	}
	tokens := make([]string, 0, len(args)+1)
	tokens = append(tokens, quoteToken(capability.Executable))
	for _, arg := range args {
		tokens = append(tokens, quoteToken(arg))
	}
	return Prepared{Command: strings.Join(tokens, " "), Summary: validated}, nil
}

// Run establishes one SSH connection, executes one prepared command and closes it.
func Run(ctx context.Context, resource sshsecurity.Resource, capability sshsecurity.Capability, prepared Prepared, known *sshsecurity.KnownHosts) (Result, error) {
	timeout := time.Duration(capability.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if timeout > 5*time.Minute {
		timeout = 5 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client, err := dial(runCtx, resource, known)
	if err != nil {
		return Result{}, err
	}
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		return Result{}, fmt.Errorf("创建 SSH 会话失败：%w", err)
	}
	defer session.Close()
	output := &limitedBuffer{limit: maxOutputBytes}
	session.Stdout = output
	session.Stderr = output
	started := time.Now()
	if err := session.Start(prepared.Command); err != nil {
		return Result{}, fmt.Errorf("启动远程命令失败：%w", err)
	}
	wait := make(chan error, 1)
	go func() { wait <- session.Wait() }()
	select {
	case err := <-wait:
		result := Result{Output: output.String(), ExitCode: exitCode(err), DurationMs: time.Since(started).Milliseconds(), Truncated: output.Truncated()}
		if err != nil {
			return result, fmt.Errorf("远程命令退出码 %d", result.ExitCode)
		}
		return result, nil
	case <-runCtx.Done():
		_ = session.Close()
		_ = client.Close()
		return Result{Output: output.String(), ExitCode: -1, DurationMs: time.Since(started).Milliseconds(), Truncated: output.Truncated()}, runCtx.Err()
	}
}

func dial(ctx context.Context, resource sshsecurity.Resource, known *sshsecurity.KnownHosts) (*ssh.Client, error) {
	auth, err := authMethod(resource)
	if err != nil {
		return nil, err
	}
	hostKeyCallback, err := sshsecurity.HostKeyCallback(resource.HostKeyFingerprint, known)
	if err != nil {
		return nil, err
	}
	address := net.JoinHostPort(resource.Address, strconv.Itoa(resource.Port))
	dialer := net.Dialer{Timeout: dialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("SSH TCP 连接失败：%w", err)
	}
	_ = conn.SetDeadline(time.Now().Add(dialTimeout))
	clientConfig := &ssh.ClientConfig{
		User: resource.Username, Auth: []ssh.AuthMethod{auth},
		HostKeyCallback: hostKeyCallback, Timeout: dialTimeout,
	}
	connection, channels, requests, err := ssh.NewClientConn(conn, address, clientConfig)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("SSH 握手失败：%w", err)
	}
	_ = conn.SetDeadline(time.Time{})
	return ssh.NewClient(connection, channels, requests), nil
}

func authMethod(resource sshsecurity.Resource) (ssh.AuthMethod, error) {
	if resource.AuthMode != "key" {
		return ssh.Password(resource.Password), nil
	}
	key := []byte(strings.TrimSpace(resource.PrivateKey))
	if len(key) == 0 {
		return nil, fmt.Errorf("SSH 私钥为空")
	}
	var signer ssh.Signer
	var err error
	if resource.PrivateKeyPassphrase == "" {
		signer, err = ssh.ParsePrivateKey(key)
	} else {
		signer, err = ssh.ParsePrivateKeyWithPassphrase(key, []byte(resource.PrivateKeyPassphrase))
	}
	if err != nil {
		return nil, fmt.Errorf("解析 SSH 私钥失败：%w", err)
	}
	return ssh.PublicKeys(signer), nil
}

func quoteToken(value string) string {
	if value != "" && safeShellTokenPattern.MatchString(value) {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitError *ssh.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitStatus()
	}
	return -1
}

type limitedBuffer struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	original := len(p)
	remaining := b.limit - b.buffer.Len()
	if remaining <= 0 {
		b.truncated = true
		return original, nil
	}
	if len(p) > remaining {
		p = p[:remaining]
		b.truncated = true
	}
	_, _ = b.buffer.Write(p)
	return original, nil
}

func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func (b *limitedBuffer) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated
}

// SummaryText formats validated parameter names deterministically for confirmation UI.
func SummaryText(values map[string]string) string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+"="+values[name])
	}
	return strings.Join(parts, ", ")
}
