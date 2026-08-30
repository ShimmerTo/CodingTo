package terminal

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// SSHSpec contains one stored SSH profile and its workspace remote directory.
type SSHSpec struct {
	Address              string
	Port                 int
	Username             string
	AuthMode             string
	Password             string
	PrivateKey           string
	PrivateKeyPassphrase string
	RemotePath           string
}

type sshProcess struct {
	client      *ssh.Client
	session     *ssh.Session
	stdin       io.WriteCloser
	outputRead  *io.PipeReader
	outputWrite *io.PipeWriter
	closeOnce   sync.Once
	closeErr    error
}

func startSSHProcess(spec SSHSpec, columns, rows int) (terminalProcess, error) {
	if strings.TrimSpace(spec.Address) == "" || strings.TrimSpace(spec.Username) == "" {
		return nil, errors.New("SSH address and username are required")
	}
	if spec.Port < 1 || spec.Port > 65535 {
		spec.Port = 22
	}
	auth, err := sshAuthMethod(spec)
	if err != nil {
		return nil, err
	}
	config := &ssh.ClientConfig{
		User:            spec.Username,
		Auth:            []ssh.AuthMethod{auth},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // Matches the existing stored-profile tunnel behavior.
		Timeout:         15 * time.Second,
	}
	address := net.JoinHostPort(spec.Address, strconv.Itoa(spec.Port))
	client, err := ssh.Dial("tcp", address, config)
	if err != nil {
		return nil, fmt.Errorf("connect SSH terminal: %w", err)
	}
	session, err := client.NewSession()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("create SSH terminal: %w", err), client.Close())
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("open SSH input: %w", err), session.Close(), client.Close())
	}
	outputRead, outputWrite := io.Pipe()
	session.Stdout = outputWrite
	session.Stderr = outputWrite
	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	if err := session.RequestPty("xterm-256color", rows, columns, modes); err != nil {
		return nil, errors.Join(
			fmt.Errorf("request SSH terminal: %w", err),
			outputRead.Close(), outputWrite.Close(), session.Close(), client.Close(),
		)
	}
	if err := session.Shell(); err != nil {
		return nil, errors.Join(
			fmt.Errorf("start SSH shell: %w", err),
			outputRead.Close(), outputWrite.Close(), session.Close(), client.Close(),
		)
	}
	process := &sshProcess{
		client: client, session: session, stdin: stdin,
		outputRead: outputRead, outputWrite: outputWrite,
	}
	if command := remoteChangeDirectoryCommand(spec.RemotePath); command != "" {
		if _, err := io.WriteString(stdin, command+"\r"); err != nil {
			return nil, errors.Join(fmt.Errorf("set SSH working directory: %w", err), process.Close())
		}
	}
	return process, nil
}

func sshAuthMethod(spec SSHSpec) (ssh.AuthMethod, error) {
	if spec.AuthMode != "key" {
		if spec.Password == "" {
			return nil, errors.New("SSH password is empty")
		}
		return ssh.Password(spec.Password), nil
	}
	key := []byte(spec.PrivateKey)
	if len(key) == 0 {
		return nil, errors.New("SSH private key is empty")
	}
	var signer ssh.Signer
	var err error
	if spec.PrivateKeyPassphrase != "" {
		signer, err = ssh.ParsePrivateKeyWithPassphrase(key, []byte(spec.PrivateKeyPassphrase))
	} else {
		signer, err = ssh.ParsePrivateKey(key)
	}
	if err != nil {
		return nil, errors.New("SSH private key could not be parsed")
	}
	return ssh.PublicKeys(signer), nil
}

func remoteChangeDirectoryCommand(remotePath string) string {
	path := strings.TrimSpace(remotePath)
	if path == "" || path == "~" {
		return ""
	}
	if strings.HasPrefix(path, "~/") {
		remainder := strings.TrimPrefix(path, "~/")
		return "cd -- \"$HOME\"/" + quotePOSIX(remainder)
	}
	return "cd -- " + quotePOSIX(path)
}

func quotePOSIX(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func (p *sshProcess) Read(buffer []byte) (int, error)  { return p.outputRead.Read(buffer) }
func (p *sshProcess) Write(buffer []byte) (int, error) { return p.stdin.Write(buffer) }
func (p *sshProcess) Resize(columns, rows int) error   { return p.session.WindowChange(rows, columns) }

func (p *sshProcess) Wait() (int, error) {
	err := p.session.Wait()
	// No more remote writes can arrive after Wait. Closing our pipe writer lets
	// the manager drain buffered stdout/stderr before it closes the SSH client.
	closeErr := normalizeSSHCloseError(p.outputWrite.Close())
	if err == nil {
		return 0, closeErr
	}
	var exitError *ssh.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitStatus(), errors.Join(err, closeErr)
	}
	return -1, errors.Join(err, closeErr)
}

func (p *sshProcess) Close() error {
	p.closeOnce.Do(func() {
		p.closeErr = errors.Join(
			normalizeSSHCloseError(p.session.Close()),
			normalizeSSHCloseError(p.client.Close()),
			normalizeSSHCloseError(p.stdin.Close()),
			normalizeSSHCloseError(p.outputWrite.Close()),
			normalizeSSHCloseError(p.outputRead.Close()),
		)
	})
	return p.closeErr
}

func normalizeSSHCloseError(err error) error {
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}
