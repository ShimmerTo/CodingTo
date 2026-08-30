// Package terminal manages interactive terminal processes shared by workspace.
package terminal

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const maxReplayBytes = 2 * 1024 * 1024

// Emitter forwards terminal lifecycle events to the desktop frontend.
type Emitter func(name string, payload any)

// ErrorLogger records internal terminal errors without exposing credentials or output.
type ErrorLogger func(format string, args ...any)

// CreateSpec describes one local or SSH-backed interactive terminal.
type CreateSpec struct {
	ProfileID string
	Kind      string
	Title     string
	Local     *LocalSpec
	SSH       *SSHSpec
	Columns   int
	Rows      int
}

// SessionSnapshot is the frontend-safe state of one terminal process.
type SessionSnapshot struct {
	ID           string `json:"id"`
	WorkspaceKey string `json:"workspaceKey"`
	ProfileID    string `json:"profileId"`
	Kind         string `json:"kind"`
	Title        string `json:"title"`
	Running      bool   `json:"running"`
	ExitCode     int    `json:"exitCode"`
	Columns      int    `json:"columns"`
	Rows         int    `json:"rows"`
	ReplayBase64 string `json:"replayBase64,omitempty"`
}

// Manager owns all terminal groups for the lifetime of the application.
type Manager struct {
	mu       sync.Mutex
	groups   map[string]map[string]*managedSession
	emit     Emitter
	logError ErrorLogger
	closed   bool
}

type terminalProcess interface {
	io.Reader
	io.Writer
	Resize(columns, rows int) error
	Wait() (int, error)
	Close() error
}

type managedSession struct {
	mu           sync.Mutex
	id           string
	workspaceKey string
	profileID    string
	kind         string
	title        string
	process      terminalProcess
	running      bool
	exitCode     int
	columns      int
	rows         int
	replay       []byte
	removed      bool
	closeOnce    sync.Once
	closeErr     error
	writeMu      sync.Mutex
	readDone     chan struct{}
}

var fallbackTerminalID atomic.Uint64

// NewManager creates an empty workspace-scoped terminal manager.
func NewManager(emit Emitter, logError ErrorLogger) *Manager {
	return &Manager{
		groups:   make(map[string]map[string]*managedSession),
		emit:     emit,
		logError: logError,
	}
}

// CanonicalWorkspace resolves a stable key so conversations using the same
// directory share one terminal group. Windows keys are case-insensitive.
func CanonicalWorkspace(root string) (key string, resolved string, err error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", "", errors.New("workspace directory is empty")
	}
	resolved, err = filepath.Abs(root)
	if err != nil {
		return "", "", fmt.Errorf("resolve workspace directory: %w", err)
	}
	resolved = filepath.Clean(resolved)
	if realPath, evalErr := filepath.EvalSymlinks(resolved); evalErr == nil {
		resolved = filepath.Clean(realPath)
	}
	key = resolved
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	return key, resolved, nil
}

// Snapshot returns every terminal currently owned by a workspace.
func (m *Manager) Snapshot(root string) (string, []SessionSnapshot, error) {
	key, _, err := CanonicalWorkspace(root)
	if err != nil {
		return "", nil, err
	}
	m.mu.Lock()
	group := m.groups[key]
	sessions := make([]*managedSession, 0, len(group))
	for _, session := range group {
		sessions = append(sessions, session)
	}
	m.mu.Unlock()

	snapshots := make([]SessionSnapshot, 0, len(sessions))
	for _, session := range sessions {
		snapshots = append(snapshots, session.snapshot(true))
	}
	sort.Slice(snapshots, func(left, right int) bool { return snapshots[left].ID < snapshots[right].ID })
	return key, snapshots, nil
}

// Create starts and registers one interactive terminal in a workspace group.
func (m *Manager) Create(root string, spec CreateSpec) (SessionSnapshot, error) {
	key, resolved, err := CanonicalWorkspace(root)
	if err != nil {
		return SessionSnapshot{}, err
	}
	columns, rows := clampSize(spec.Columns, spec.Rows)
	var process terminalProcess
	switch spec.Kind {
	case "local":
		if spec.Local == nil {
			return SessionSnapshot{}, errors.New("local terminal profile is missing")
		}
		process, err = startLocalProcess(resolved, *spec.Local, columns, rows)
	case "ssh":
		if spec.SSH == nil {
			return SessionSnapshot{}, errors.New("SSH terminal profile is missing")
		}
		process, err = startSSHProcess(*spec.SSH, columns, rows)
	default:
		return SessionSnapshot{}, errors.New("unsupported terminal kind")
	}
	if err != nil {
		return SessionSnapshot{}, err
	}

	session := &managedSession{
		id:           newTerminalID(),
		workspaceKey: key,
		profileID:    spec.ProfileID,
		kind:         spec.Kind,
		title:        strings.TrimSpace(spec.Title),
		process:      process,
		running:      true,
		exitCode:     -1,
		columns:      columns,
		rows:         rows,
		replay:       make([]byte, 0, 4096),
		readDone:     make(chan struct{}),
	}
	if session.title == "" {
		session.title = "Terminal"
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		if closeErr := process.Close(); closeErr != nil && m.logError != nil {
			m.logError("close unregistered terminal: %v", closeErr)
		}
		return SessionSnapshot{}, errors.New("terminal manager is closed")
	}
	if m.groups[key] == nil {
		m.groups[key] = make(map[string]*managedSession)
	}
	m.groups[key][session.id] = session
	m.mu.Unlock()

	go m.readOutput(session)
	go m.waitForExit(session)
	return session.snapshot(true), nil
}

// Write sends user input to one terminal after verifying its workspace group.
func (m *Manager) Write(root, terminalID, data string) error {
	session, err := m.session(root, terminalID)
	if err != nil {
		return err
	}
	session.mu.Lock()
	if !session.running || session.removed {
		session.mu.Unlock()
		return errors.New("terminal is not running")
	}
	session.mu.Unlock()
	session.writeMu.Lock()
	defer session.writeMu.Unlock()
	_, err = io.WriteString(session.process, data)
	return err
}

// Resize updates one terminal's pseudo-console dimensions.
func (m *Manager) Resize(root, terminalID string, columns, rows int) error {
	session, err := m.session(root, terminalID)
	if err != nil {
		return err
	}
	columns, rows = clampSize(columns, rows)
	session.mu.Lock()
	if !session.running || session.removed {
		session.mu.Unlock()
		return nil
	}
	if columns == session.columns && rows == session.rows {
		session.mu.Unlock()
		return nil
	}
	session.mu.Unlock()
	if err := session.process.Resize(columns, rows); err != nil {
		return err
	}
	session.mu.Lock()
	session.columns = columns
	session.rows = rows
	session.mu.Unlock()
	return nil
}

// CloseTerminal stops and removes exactly one terminal from its workspace group.
func (m *Manager) CloseTerminal(root, terminalID string) error {
	session, err := m.session(root, terminalID)
	if err != nil {
		return err
	}
	session.mu.Lock()
	session.removed = true
	session.mu.Unlock()

	closeErr := session.closeProcess()
	m.mu.Lock()
	if group := m.groups[session.workspaceKey]; group != nil {
		delete(group, session.id)
		if len(group) == 0 {
			delete(m.groups, session.workspaceKey)
		}
	}
	m.mu.Unlock()
	return closeErr
}

// Close stops every managed terminal. It is safe to call more than once.
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	sessions := make([]*managedSession, 0)
	for _, group := range m.groups {
		for _, session := range group {
			sessions = append(sessions, session)
		}
	}
	m.groups = make(map[string]map[string]*managedSession)
	m.mu.Unlock()

	var firstErr error
	for _, session := range sessions {
		session.mu.Lock()
		session.removed = true
		session.mu.Unlock()
		if err := session.closeProcess(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (m *Manager) session(root, terminalID string) (*managedSession, error) {
	key, _, err := CanonicalWorkspace(root)
	if err != nil {
		return nil, err
	}
	terminalID = strings.TrimSpace(terminalID)
	if terminalID == "" {
		return nil, errors.New("terminal id is empty")
	}
	m.mu.Lock()
	session := m.groups[key][terminalID]
	m.mu.Unlock()
	if session == nil {
		return nil, errors.New("terminal not found in this workspace")
	}
	return session, nil
}

func (m *Manager) readOutput(session *managedSession) {
	defer close(session.readDone)
	buffer := make([]byte, 32*1024)
	for {
		n, err := session.process.Read(buffer)
		if n > 0 {
			chunk := append([]byte(nil), buffer[:n]...)
			session.appendReplay(chunk)
			session.mu.Lock()
			removed := session.removed
			session.mu.Unlock()
			if !removed && m.emit != nil {
				m.emit("terminal:data", map[string]any{
					"workspaceKey": session.workspaceKey,
					"terminalId":   session.id,
					"dataBase64":   base64.StdEncoding.EncodeToString(chunk),
				})
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && !isExpectedCloseError(err) && m.logError != nil {
				m.logError("read terminal %s output: %v", session.id, err)
			}
			return
		}
	}
}

func (m *Manager) waitForExit(session *managedSession) {
	exitCode, err := session.process.Wait()
	// A process can exit while its final bytes are still buffered in the PTY.
	// Give the reader a short bounded drain window, then close the PTY to make
	// any blocked read return. This preserves trailing output without allowing
	// shutdown to wait forever on a platform pipe.
	if !waitForTerminalRead(session.readDone, 120*time.Millisecond) {
		m.closeAfterExit(session)
		_ = waitForTerminalRead(session.readDone, time.Second)
	} else {
		m.closeAfterExit(session)
	}
	session.mu.Lock()
	session.running = false
	session.exitCode = exitCode
	removed := session.removed
	session.mu.Unlock()
	if err != nil && !removed && m.logError != nil {
		m.logError("terminal %s exited: %v", session.id, err)
	}
	if !removed && m.emit != nil {
		m.emit("terminal:exit", map[string]any{
			"workspaceKey": session.workspaceKey,
			"terminalId":   session.id,
			"exitCode":     exitCode,
		})
	}
}

func (m *Manager) closeAfterExit(session *managedSession) {
	if closeErr := session.closeProcess(); closeErr != nil && !isExpectedCloseError(closeErr) && m.logError != nil {
		m.logError("close terminal %s after exit: %v", session.id, closeErr)
	}
}

func waitForTerminalRead(done <-chan struct{}, timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

func (s *managedSession) closeProcess() error {
	s.closeOnce.Do(func() { s.closeErr = s.process.Close() })
	return s.closeErr
}

func (s *managedSession) appendReplay(chunk []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replay = append(s.replay, chunk...)
	if len(s.replay) > maxReplayBytes {
		overflow := len(s.replay) - maxReplayBytes
		copy(s.replay, s.replay[overflow:])
		s.replay = s.replay[:maxReplayBytes]
	}
}

func (s *managedSession) snapshot(includeReplay bool) SessionSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot := SessionSnapshot{
		ID:           s.id,
		WorkspaceKey: s.workspaceKey,
		ProfileID:    s.profileID,
		Kind:         s.kind,
		Title:        s.title,
		Running:      s.running,
		ExitCode:     s.exitCode,
		Columns:      s.columns,
		Rows:         s.rows,
	}
	if includeReplay && len(s.replay) > 0 {
		snapshot.ReplayBase64 = base64.StdEncoding.EncodeToString(s.replay)
	}
	return snapshot
}

func newTerminalID() string {
	buffer := make([]byte, 12)
	if _, err := rand.Read(buffer); err == nil {
		return "term-" + hex.EncodeToString(buffer)
	}
	return fmt.Sprintf("term-fallback-%d", fallbackTerminalID.Add(1))
}

func clampSize(columns, rows int) (int, int) {
	if columns < 2 {
		columns = 80
	} else if columns > 500 {
		columns = 500
	}
	if rows < 1 {
		rows = 24
	} else if rows > 200 {
		rows = 200
	}
	return columns, rows
}

func isExpectedCloseError(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "closed") || strings.Contains(message, "file already") || strings.Contains(message, "broken pipe")
}
