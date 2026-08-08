package piagent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"sync"

	"codingto/internal/applog"
)

type Event struct {
	Raw json.RawMessage `json:"raw"`
}

type StartConfig struct {
	WorkDir     string
	SessionDir  string
	SessionID   string
	SessionPath string
	Provider    string
	Model       string
	ExtraArgs   []string
	Env         map[string]string
}

type Adapter struct {
	mu       sync.Mutex
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	events   chan Event
	done     chan struct{}
	doneOnce *sync.Once
	running  bool
	exitErr  error
}

func NewAdapter() *Adapter { return &Adapter{events: make(chan Event, 256), doneOnce: &sync.Once{}} }

func FindExecutable() (string, bool) {
	for _, name := range []string{"pi", "pi.cmd", "pi.exe"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, true
		}
	}
	return "", false
}

func (a *Adapter) Start(ctx context.Context, cfg StartConfig) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.running {
		return fmt.Errorf("pi agent is already running")
	}
	bin, ok := FindExecutable()
	if !ok {
		return fmt.Errorf("Pi CLI is not installed; run: npm install -g --ignore-scripts @earendil-works/pi-coding-agent")
	}
	args := []string{"--mode", "rpc"}
	if cfg.Provider != "" {
		args = append(args, "--provider", cfg.Provider)
	}
	if cfg.Model != "" {
		args = append(args, "--model", cfg.Model)
	}
	if cfg.SessionDir != "" {
		args = append(args, "--session-dir", cfg.SessionDir)
	}
	if cfg.SessionID != "" {
		args = append(args, "--session-id", cfg.SessionID)
	}
	if cfg.SessionPath != "" {
		args = append(args, "--session", cfg.SessionPath)
	}
	args = append(args, cfg.ExtraArgs...)

	a.cmd = exec.CommandContext(ctx, bin, args...)
	configureBackgroundProcess(a.cmd)
	a.cmd.Dir = cfg.WorkDir
	a.cmd.Env = a.cmd.Environ()
	for key, value := range cfg.Env {
		a.cmd.Env = append(a.cmd.Env, key+"="+value)
	}
	var err error
	a.stdin, err = a.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("create Pi stdin: %w", err)
	}
	stdout, err := a.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("create Pi stdout: %w", err)
	}
	stderr, err := a.cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("create Pi stderr: %w", err)
	}
	if err := a.cmd.Start(); err != nil {
		return fmt.Errorf("start Pi: %w", err)
	}

	a.running = true
	a.exitErr = nil
	a.done = make(chan struct{})
	a.doneOnce = &sync.Once{}
	a.events = make(chan Event, 256)
	cmd, done, doneOnce, events := a.cmd, a.done, a.doneOnce, a.events
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			applog.Infof("[pi] %s", scanner.Text())
		}
	}()
	go readJSONL(stdout, events, done)
	go func() {
		err := cmd.Wait()
		a.mu.Lock()
		if a.cmd == cmd {
			a.running, a.exitErr = false, err
		}
		a.mu.Unlock()
		doneOnce.Do(func() { close(done) })
	}()
	return nil
}

func (a *Adapter) SendCommand(raw json.RawMessage) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.running || a.stdin == nil {
		return fmt.Errorf("Pi agent is not running")
	}
	data := append([]byte{}, raw...)
	if len(data) == 0 || data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	_, err := a.stdin.Write(data)
	return err
}

func (a *Adapter) Stop() error {
	a.mu.Lock()
	if !a.running {
		a.mu.Unlock()
		return nil
	}
	cmd, done, doneOnce := a.cmd, a.done, a.doneOnce
	a.running = false
	a.mu.Unlock()
	// Kill the whole process tree, not just the direct child. On Windows the
	// direct child of the bridge is the pi.cmd cmd.exe shim with the real node
	// process underneath; a plain Kill only kills the shim and leaves the node
	// orphaned (it keeps running and keeps CodingTo's resources locked) — the
	// exact exe that escapes on shutdown. killProcessTree walks the tree
	// (taskkill /T /F on Windows, plain Kill on Unix where the launcher execs
	// node directly), so every caller of Stop (session stop, restart, close,
	// shutdown) now reliably terminates the node too.
	if cmd != nil && cmd.Process != nil {
		killProcessTree(cmd.Process)
	}
	if done != nil && doneOnce != nil {
		doneOnce.Do(func() { close(done) })
	}
	return nil
}

func (a *Adapter) Events() <-chan Event { return a.events }
func (a *Adapter) IsRunning() bool      { a.mu.Lock(); defer a.mu.Unlock(); return a.running }

// KillTree terminates the child Pi process and all its descendants. On
// Windows the direct child is the pi.cmd cmd.exe shim with node underneath, so
// a plain Kill would leave the wedged node orphaned; killProcessTree handles
// the platform differences (taskkill /T on Windows, direct Kill elsewhere).
func (a *Adapter) KillTree() {
	a.mu.Lock()
	cmd := a.cmd
	a.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	killProcessTree(cmd.Process)
}
func (a *Adapter) ExitError() error { a.mu.Lock(); defer a.mu.Unlock(); return a.exitErr }
func InstallHint() string {
	if runtime.GOOS == "windows" {
		return "npm.cmd install -g --ignore-scripts @earendil-works/pi-coding-agent"
	}
	return "npm install -g --ignore-scripts @earendil-works/pi-coding-agent"
}

func readJSONL(reader io.Reader, events chan<- Event, done <-chan struct{}) {
	defer close(events)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		if len(line) == 0 {
			continue
		}
		select {
		case events <- Event{Raw: line}:
		case <-done:
			return
		}
	}
}
