// Package browsersession owns persistent-profile Chrome processes and exposes
// a loopback-only, token-authenticated lease API. It intentionally has no
// dependency on the desktop application package so it can be registered as an
// optional runtime module.
package browsersession

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"codingto/internal/browserworkflow"
)

const (
	defaultIdleTimeout = 15 * time.Minute
	maxRequestBody     = 1 << 20
)

type LeaseState string

const (
	StateWaiting LeaseState = "waiting_for_login"
	StateReady   LeaseState = "ready"
	StateFailed  LeaseState = "failed"
	StateClosed  LeaseState = "closed"
)

// AgentDataDirResolver resolves an opaque agent ID to its private data root.
// The service never needs to know how the desktop application stores agents.
type AgentDataDirResolver func(agentID string) (string, bool)

type Options struct {
	ResolveAgentDataDir AgentDataDirResolver
	ChromeExecutable    string
	AgentBrowserBinary  string
	IdleTimeout         time.Duration
	Now                 func() time.Time
}

// Service is a self-contained runtime module. Start and Shutdown satisfy the
// app.RuntimeModule contract without importing internal/app.
type Service struct {
	options    Options
	instanceID string
	token      string

	mu        sync.Mutex
	listener  net.Listener
	server    *http.Server
	baseURL   string
	leases    map[string]*lease
	byProfile map[string]string
	stop      chan struct{}
	done      chan struct{}
}

type lease struct {
	ID         string
	AgentID    string
	SessionID  int64
	ProfileID  string
	TargetURL  string
	Origin     string
	ProfileDir string
	LockPath   string
	Chrome     *exec.Cmd
	ChromePID  int
	CDPPort    int
	Headed     bool
	State      LeaseState
	CreatedAt  time.Time
	LastUsedAt time.Time
	exit       chan error
	opMu       sync.Mutex
}

type PrepareRequest struct {
	AgentID           string `json:"agentId"`
	CodingToSessionID int64  `json:"codingToSessionId"`
	ProfileID         string `json:"profileId"`
	TargetURL         string `json:"targetUrl"`
	Headed            *bool  `json:"headed,omitempty"`
}

type ExecuteRequest struct {
	Args      []string `json:"args"`
	TimeoutMS int      `json:"timeoutMs"`
	Headed    *bool    `json:"headed,omitempty"`
}

type VerifyRequest struct {
	Headed *bool `json:"headed,omitempty"`
}

type Response struct {
	Status  string `json:"status"`
	LeaseID string `json:"leaseId,omitempty"`
	Headed  *bool  `json:"headed,omitempty"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	Output  string `json:"output,omitempty"`
}

type pageAssessment struct {
	status string
	reason string
}

func New(options Options) (*Service, error) {
	if options.IdleTimeout <= 0 {
		options.IdleTimeout = defaultIdleTimeout
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	instanceID, err := randomID("bi_", 12)
	if err != nil {
		return nil, err
	}
	token, err := randomID("", 32)
	if err != nil {
		return nil, err
	}
	return &Service{
		options:    options,
		instanceID: instanceID,
		token:      token,
		leases:     make(map[string]*lease),
		byProfile:  make(map[string]string),
	}, nil
}

// SetAgentDataDirResolver is called by the generic runtime-module registrar.
// It keeps the application callback out of cmd/codingto and out of Wails'
// exported service method surface.
func (s *Service) SetAgentDataDirResolver(resolve func(agentID string) (string, bool)) {
	s.mu.Lock()
	s.options.ResolveAgentDataDir = resolve
	s.mu.Unlock()
}

func (s *Service) Start(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener != nil {
		return nil
	}
	if s.options.ResolveAgentDataDir == nil {
		return errors.New("browser session agent data resolver is required")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("start browser session service: %w", err)
	}
	s.listener = listener
	s.baseURL = "http://" + listener.Addr().String()
	s.stop = make(chan struct{})
	s.done = make(chan struct{})
	s.server = &http.Server{
		Handler:           s,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	server := s.server
	go func() { _ = server.Serve(listener) }()
	go s.reapIdleLeases(s.stop, s.done)
	return nil
}

func (s *Service) Shutdown() error {
	s.mu.Lock()
	if s.listener == nil {
		s.mu.Unlock()
		return nil
	}
	server, stop, done := s.server, s.stop, s.done
	ids := make([]string, 0, len(s.leases))
	for id := range s.leases {
		ids = append(ids, id)
	}
	s.listener = nil
	s.server = nil
	s.baseURL = ""
	close(stop)
	s.mu.Unlock()

	for _, id := range ids {
		_ = s.closeLease(id)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := server.Shutdown(ctx)
	// The shutdown chain runs on the application's main thread, so an unbounded
	// wait must be avoided. reapIdleLeases closes done via defer, but if it ever
	// panics before defer runs, an unbuffered wait would hang the whole process
	// (CodingTo.exe stuck in the task list). Bound it to match server.Shutdown.
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
	return err
}

// AgentEnvironment returns only connection metadata and lease ownership
// identity. Chrome paths, ports, PIDs and profile IDs never enter this map.
func (s *Service) AgentEnvironment(agentID string, sessionID int64) map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.baseURL == "" {
		return nil
	}
	return map[string]string{
		"CODINGTO_BROWSER_SERVICE_URL":   s.baseURL,
		"CODINGTO_BROWSER_SERVICE_TOKEN": s.token,
		"CODINGTO_BROWSER_AGENT_ID":      agentID,
		"CODINGTO_BROWSER_SESSION_ID":    strconv.FormatInt(sessionID, 10),
	}
}

// ReleaseAgentSession closes leases when the desktop runtime explicitly stops
// or switches away from their owning CodingTo session.
func (s *Service) ReleaseAgentSession(agentID string, sessionID int64) {
	s.mu.Lock()
	var ids []string
	for id, l := range s.leases {
		if l.AgentID == agentID && l.SessionID == sessionID {
			ids = append(ids, id)
		}
	}
	s.mu.Unlock()
	for _, id := range ids {
		_ = s.closeLease(id)
	}
}

func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if !s.authorized(r) {
		writeResponse(w, http.StatusUnauthorized, Response{Status: "error", Code: "UNAUTHORIZED", Message: "unauthorized"})
		return
	}
	if r.URL.Path == "/v1/browser/prepare" {
		if r.Method != http.MethodPost {
			writeResponse(w, http.StatusMethodNotAllowed, Response{Status: "error", Code: "METHOD_NOT_ALLOWED"})
			return
		}
		s.handlePrepare(w, r)
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 || parts[0] != "v1" || parts[1] != "browser" {
		writeResponse(w, http.StatusNotFound, Response{Status: "error", Code: "NOT_FOUND"})
		return
	}
	leaseID := parts[2]
	switch {
	case len(parts) == 4 && parts[3] == "verify" && r.Method == http.MethodPost:
		s.handleVerify(w, r, leaseID)
	case len(parts) == 4 && parts[3] == "execute" && r.Method == http.MethodPost:
		s.handleExecute(w, r, leaseID)
	case len(parts) == 3 && r.Method == http.MethodDelete:
		s.handleClose(w, r, leaseID)
	default:
		writeResponse(w, http.StatusNotFound, Response{Status: "error", Code: "NOT_FOUND"})
	}
}

func (s *Service) authorized(r *http.Request) bool {
	const prefix = "Bearer "
	header := r.Header.Get("Authorization")
	return strings.HasPrefix(header, prefix) && secureEqual(strings.TrimPrefix(header, prefix), s.token)
}

func (s *Service) handlePrepare(w http.ResponseWriter, r *http.Request) {
	var req PrepareRequest
	if err := decodeJSON(r, &req); err != nil {
		writeResponse(w, http.StatusBadRequest, Response{Status: "error", Code: "INVALID_REQUEST", Message: err.Error()})
		return
	}
	ownerSessionID, ownerErr := strconv.ParseInt(r.Header.Get("X-CodingTo-Session-ID"), 10, 64)
	if ownerErr != nil || r.Header.Get("X-CodingTo-Agent-ID") != req.AgentID || ownerSessionID != req.CodingToSessionID {
		writeResponse(w, http.StatusForbidden, Response{Status: "error", Code: "OWNER_MISMATCH", Message: "browser lease owner does not match the runtime session"})
		return
	}
	targetURL, origin, err := validateTarget(req.TargetURL)
	if err != nil || strings.TrimSpace(req.AgentID) == "" || req.CodingToSessionID <= 0 || strings.TrimSpace(req.ProfileID) == "" {
		writeResponse(w, http.StatusBadRequest, Response{Status: "error", Code: "INVALID_REQUEST", Message: "agent, session, profile and valid target URL are required"})
		return
	}
	base, err := browserworkflow.ProfileBaseDir()
	if err != nil {
		writeResponse(w, http.StatusInternalServerError, Response{Status: "error", Code: "PROFILE_UNAVAILABLE", Message: "browser profile directory is unavailable"})
		return
	}
	profile, err := browserworkflow.Load(base, req.ProfileID)
	if err != nil {
		writeResponse(w, http.StatusNotFound, Response{Status: "error", Code: "PROFILE_UNAVAILABLE", Message: "browser profile is unavailable"})
		return
	}
	if !originAllowed(profile.Origins, origin) {
		writeResponse(w, http.StatusForbidden, Response{Status: "error", Code: "ORIGIN_NOT_ALLOWED", Message: "target origin is not allowed by this profile"})
		return
	}
	profileDir, err := browserworkflow.PersistentProfilePath(base, profile)
	if err != nil {
		writeResponse(w, http.StatusInternalServerError, Response{Status: "error", Code: "PROFILE_UNAVAILABLE", Message: "browser profile is unavailable"})
		return
	}
	response, status := s.prepare(r.Context(), req, targetURL, origin, profileDir)
	writeResponse(w, status, response)
}

func (s *Service) prepare(ctx context.Context, req PrepareRequest, targetURL, origin, profileDir string) (Response, int) {
	profileKey := strings.ToLower(req.AgentID + "\x00" + req.ProfileID)
	s.mu.Lock()
	if existingID := s.byProfile[profileKey]; existingID != "" {
		existing := s.leases[existingID]
		if existing != nil && existing.AgentID == req.AgentID && existing.SessionID == req.CodingToSessionID {
			s.mu.Unlock()
			existing.opMu.Lock()
			if req.Headed != nil && existing.Headed != *req.Headed {
				if err := s.restartChrome(ctx, existing, *req.Headed); err != nil {
					existing.opMu.Unlock()
					_ = s.closeLease(existing.ID)
					return Response{Status: "error", Code: "CHROME_LAUNCH_FAILED", Message: "could not switch browser visibility"}, http.StatusBadGateway
				}
			}
			if existing.TargetURL != targetURL {
				if err := navigate(ctx, existing.CDPPort, targetURL); err != nil {
					existing.opMu.Unlock()
					_ = s.closeLease(existing.ID)
					return Response{Status: "error", Code: "NAVIGATION_FAILED", Message: "target navigation failed"}, http.StatusBadGateway
				}
				existing.TargetURL, existing.Origin = targetURL, origin
			}
			response := s.assessResponseUntil(ctx, existing, 5*time.Second)
			response = s.ensureInteractiveVisible(ctx, existing, response)
			existing.opMu.Unlock()
			if shouldCloseAfterResponse(response) {
				_ = s.closeLease(existing.ID)
			}
			status := http.StatusOK
			if response.Code == "CHROME_LAUNCH_FAILED" {
				status = http.StatusBadGateway
			}
			return response, status
		}
		s.mu.Unlock()
		_ = s.closeLease(existingID)
		return Response{Status: "error", Code: "PROFILE_RECLAIMED", Message: "previous browser profile window was closed; choose a profile again"}, http.StatusConflict
	}
	leaseID, err := randomID("bl_", 12)
	if err != nil {
		s.mu.Unlock()
		return Response{Status: "error", Code: "INTERNAL_ERROR", Message: "could not create browser lease"}, http.StatusInternalServerError
	}
	now := s.options.Now()
	l := &lease{
		ID: leaseID, AgentID: req.AgentID, SessionID: req.CodingToSessionID,
		ProfileID: req.ProfileID, TargetURL: targetURL, Origin: origin,
		ProfileDir: profileDir, LockPath: filepath.Join(filepath.Dir(profileDir), ".codingto.lock"),
		Headed: requestedHeaded(req.Headed, true), State: StateWaiting, CreatedAt: now, LastUsedAt: now,
	}
	if err := s.acquireProfileLock(l); err != nil {
		s.mu.Unlock()
		if errors.Is(err, errProfileReclaimed) {
			return Response{Status: "error", Code: "PROFILE_RECLAIMED", Message: "previous browser profile window was closed; choose a profile again"}, http.StatusConflict
		}
		return Response{Status: "error", Code: "PROFILE_BUSY", Message: "browser profile is already in use"}, http.StatusConflict
	}
	s.leases[leaseID] = l
	s.byProfile[profileKey] = leaseID
	s.mu.Unlock()

	l.opMu.Lock()
	err = s.launchChrome(ctx, l)
	if err == nil {
		response := s.assessResponseUntil(ctx, l, 8*time.Second)
		response = s.ensureInteractiveVisible(ctx, l, response)
		l.opMu.Unlock()
		if response.Code == "CHROME_LAUNCH_FAILED" {
			_ = s.closeLease(leaseID)
			return response, http.StatusBadGateway
		}
		return response, http.StatusOK
	}
	l.opMu.Unlock()
	_ = s.closeLease(leaseID)
	code := "CHROME_LAUNCH_FAILED"
	if errors.Is(err, errCDPStartup) {
		code = "CDP_START_FAILED"
	}
	return Response{Status: "error", Code: code, Message: "could not start the browser profile window"}, http.StatusBadGateway
}

func (s *Service) handleVerify(w http.ResponseWriter, r *http.Request, leaseID string) {
	l, ok := s.ownedLease(r, leaseID)
	if !ok {
		writeResponse(w, http.StatusNotFound, Response{Status: "error", Code: "LEASE_NOT_FOUND", Message: "browser lease not found"})
		return
	}
	var req VerifyRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		writeResponse(w, http.StatusBadRequest, Response{Status: "error", Code: "INVALID_REQUEST", Message: err.Error()})
		return
	}
	l.opMu.Lock()
	if req.Headed != nil && l.Headed != *req.Headed {
		if err := s.restartChrome(r.Context(), l, *req.Headed); err != nil {
			l.opMu.Unlock()
			_ = s.closeLease(l.ID)
			writeResponse(w, http.StatusBadGateway, Response{Status: "error", Code: "CHROME_LAUNCH_FAILED", Message: "could not switch browser visibility"})
			return
		}
	}
	if l.Chrome == nil || l.Chrome.Process == nil || !processAlive(l.ChromePID) {
		response := s.assessResponse(r.Context(), l)
		l.opMu.Unlock()
		if response.Code == "CHROME_EXITED" {
			_ = s.closeLease(l.ID)
		}
		writeResponse(w, http.StatusOK, response)
		return
	}
	if err := ensureTarget(r.Context(), l.CDPPort, l.TargetURL, l.Origin); err != nil {
		response := responseForLease(l, "not_ready", "")
		response.Message = "目标页面尚未完成导航，请稍后重试。"
		l.opMu.Unlock()
		writeResponse(w, http.StatusOK, response)
		return
	}
	response := s.assessResponseUntil(r.Context(), l, 5*time.Second)
	response = s.ensureInteractiveVisible(r.Context(), l, response)
	l.opMu.Unlock()
	if shouldCloseAfterResponse(response) {
		_ = s.closeLease(l.ID)
	}
	status := http.StatusOK
	if response.Code == "CHROME_LAUNCH_FAILED" {
		status = http.StatusBadGateway
	}
	writeResponse(w, status, response)
}

func (s *Service) handleExecute(w http.ResponseWriter, r *http.Request, leaseID string) {
	l, ok := s.ownedLease(r, leaseID)
	if !ok {
		writeResponse(w, http.StatusNotFound, Response{Status: "error", Code: "LEASE_NOT_FOUND", Message: "browser lease not found"})
		return
	}
	var req ExecuteRequest
	if err := decodeJSON(r, &req); err != nil {
		writeResponse(w, http.StatusBadRequest, Response{Status: "error", Code: "INVALID_REQUEST", Message: err.Error()})
		return
	}
	if err := validateExecuteArgs(req.Args); err != nil {
		writeResponse(w, http.StatusBadRequest, Response{Status: "error", Code: "COMMAND_NOT_ALLOWED", Message: err.Error()})
		return
	}
	l.opMu.Lock()
	if req.Headed != nil && l.Headed != *req.Headed {
		if err := s.restartChrome(r.Context(), l, *req.Headed); err != nil {
			l.opMu.Unlock()
			_ = s.closeLease(l.ID)
			writeResponse(w, http.StatusBadGateway, Response{Status: "error", Code: "CHROME_LAUNCH_FAILED", Message: "could not switch browser visibility"})
			return
		}
	}
	assessment := s.assessResponseUntil(r.Context(), l, 5*time.Second)
	if assessment = s.ensureInteractiveVisible(r.Context(), l, assessment); assessment.Status != "ready" {
		l.opMu.Unlock()
		if shouldCloseAfterResponse(assessment) {
			_ = s.closeLease(l.ID)
		}
		status := http.StatusConflict
		if assessment.Code == "CHROME_LAUNCH_FAILED" {
			status = http.StatusBadGateway
		}
		writeResponse(w, status, assessment)
		return
	}
	timeout := time.Duration(req.TimeoutMS) * time.Millisecond
	if timeout < time.Second {
		timeout = 30 * time.Second
	}
	if timeout > 2*time.Minute {
		timeout = 2 * time.Minute
	}
	output, err := s.runAdapter(r.Context(), l.ID, l.CDPPort, req.Args, timeout)
	if err != nil {
		l.opMu.Unlock()
		writeResponse(w, http.StatusBadGateway, Response{Status: "error", LeaseID: l.ID, Code: "EXECUTE_FAILED", Message: "browser command failed"})
		return
	}
	s.touch(l)
	response := responseForLease(l, "ok", output)
	l.opMu.Unlock()
	writeResponse(w, http.StatusOK, response)
}

func (s *Service) handleClose(w http.ResponseWriter, r *http.Request, leaseID string) {
	if _, ok := s.ownedLease(r, leaseID); !ok {
		writeResponse(w, http.StatusNotFound, Response{Status: "error", Code: "LEASE_NOT_FOUND", Message: "browser lease not found"})
		return
	}
	if err := s.closeLease(leaseID); err != nil {
		writeResponse(w, http.StatusInternalServerError, Response{Status: "error", Code: "CLOSE_FAILED", Message: "browser lease cleanup failed"})
		return
	}
	writeResponse(w, http.StatusOK, Response{Status: "closed", LeaseID: leaseID})
}

func (s *Service) ownedLease(r *http.Request, leaseID string) (*lease, bool) {
	agentID := r.Header.Get("X-CodingTo-Agent-ID")
	sessionID, err := strconv.ParseInt(r.Header.Get("X-CodingTo-Session-ID"), 10, 64)
	if err != nil || agentID == "" || sessionID <= 0 {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.leases[leaseID]
	return l, l != nil && l.AgentID == agentID && l.SessionID == sessionID
}

func (s *Service) assessResponse(ctx context.Context, l *lease) Response {
	if l.Chrome == nil || l.Chrome.Process == nil {
		response := responseForLease(l, "not_ready", "")
		response.Message = "浏览器正在启动，请稍后重试。"
		return response
	}
	if !processAlive(l.ChromePID) {
		l.State = StateFailed
		response := responseForLease(l, "error", "")
		response.Code, response.Message = "CHROME_EXITED", "browser window was closed"
		return response
	}
	assessment, err := assessPage(ctx, l.CDPPort, l.Origin)
	s.touch(l)
	if err != nil {
		l.State = StateWaiting
		response := responseForLease(l, "not_ready", "")
		response.Message = "浏览器页面尚未就绪，请稍后重试。"
		return response
	}
	if assessment.status == "ready" {
		l.State = StateReady
		return responseForLease(l, "ready", "")
	}
	l.State = StateWaiting
	if assessment.status == "visibility_required" {
		response := responseForLease(l, "not_ready", "")
		if l.Headed {
			response.Code = "HTTP_ACCESS_BLOCKED"
			response.Message = "目标页面返回访问限制（HTTP 403/429），请在已打开的窗口中完成站点安全检查或确认访问权限后继续。"
		} else {
			response.Code = "HEADLESS_ACCESS_BLOCKED"
			response.Message = "目标页面拒绝了无头浏览器访问，正在切换到可见浏览器重试。"
		}
		return response
	}
	message := "浏览器页面尚未就绪，请在已打开的浏览器中完成登录后继续。"
	if assessment.status == "not_ready" {
		message = "浏览器页面尚未加载完成，请稍后重试。"
		if l.Headed {
			message = "浏览器页面尚未加载完成，请保留窗口并稍后重试。"
		}
	}
	response := responseForLease(l, assessment.status, "")
	response.Message = message
	return response
}

// ensureInteractiveVisible keeps login, security checks and headless access
// blocks out of an invisible browser. The same persistent profile and lease
// are retained while Chrome is restarted once in headed mode.
func (s *Service) ensureInteractiveVisible(ctx context.Context, l *lease, response Response) Response {
	if !shouldOpenVisibleBrowser(l, response) {
		return response
	}
	if err := s.restartChrome(ctx, l, true); err != nil {
		return responseErrorForLease(l, "CHROME_LAUNCH_FAILED", "could not open a visible browser for login")
	}
	return s.assessResponseUntil(ctx, l, 5*time.Second)
}

func shouldOpenVisibleBrowser(l *lease, response Response) bool {
	return !l.Headed && (response.Status == "login_required" || response.Code == "HEADLESS_ACCESS_BLOCKED")
}

func requestedHeaded(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

func responseForLease(l *lease, status, output string) Response {
	headed := l.Headed
	return Response{Status: status, LeaseID: l.ID, Headed: &headed, Output: output}
}

func responseErrorForLease(l *lease, code, message string) Response {
	response := responseForLease(l, "error", "")
	response.Code, response.Message = code, message
	return response
}

func shouldCloseAfterResponse(response Response) bool {
	return response.Code == "CHROME_EXITED" || response.Code == "CHROME_LAUNCH_FAILED"
}

func (s *Service) assessResponseUntil(ctx context.Context, l *lease, wait time.Duration) Response {
	deadline := time.Now().Add(wait)
	for {
		response := s.assessResponse(ctx, l)
		if response.Status != "not_ready" || !time.Now().Before(deadline) {
			return response
		}
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return response
		case <-timer.C:
		}
	}
}

func (s *Service) touch(l *lease) {
	s.mu.Lock()
	if current := s.leases[l.ID]; current == l {
		l.LastUsedAt = s.options.Now()
	}
	s.mu.Unlock()
}

func (s *Service) closeLease(id string) error {
	s.mu.Lock()
	l := s.leases[id]
	if l == nil {
		s.mu.Unlock()
		return nil
	}
	delete(s.leases, id)
	delete(s.byProfile, strings.ToLower(l.AgentID+"\x00"+l.ProfileID))
	l.State = StateClosed
	s.mu.Unlock()

	l.opMu.Lock()
	defer l.opMu.Unlock()
	closeErr := s.stopChrome(l)
	raw, readErr := os.ReadFile(l.LockPath)
	var record lockRecord
	if readErr == nil && json.Unmarshal(raw, &record) == nil && record.InstanceID == s.instanceID && record.LeaseID == l.ID {
		if err := os.Remove(l.LockPath); err != nil && !os.IsNotExist(err) && closeErr == nil {
			closeErr = err
		}
	}
	return closeErr
}

func (s *Service) reapIdleLeases(stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	interval := time.Minute
	if s.options.IdleTimeout < interval {
		interval = s.options.IdleTimeout
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			cutoff := s.options.Now().Add(-s.options.IdleTimeout)
			s.mu.Lock()
			var expired []string
			for id, l := range s.leases {
				if l.LastUsedAt.Before(cutoff) {
					expired = append(expired, id)
				}
			}
			s.mu.Unlock()
			for _, id := range expired {
				_ = s.closeLease(id)
			}
		}
	}
}

func decodeJSON(r *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxRequestBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid JSON request")
	}
	return nil
}

func decodeOptionalJSON(r *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxRequestBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("invalid JSON request")
	}
	return nil
}

func writeResponse(w http.ResponseWriter, status int, response Response) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response)
}

func validateTarget(raw string) (string, string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", "", errors.New("only http(s) URLs without embedded credentials are allowed")
	}
	parsed.Fragment = ""
	host := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if (parsed.Scheme == "https" && port == "443") || (parsed.Scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	origin := strings.ToLower(parsed.Scheme + "://" + host)
	return parsed.String(), origin, nil
}

func originAllowed(origins []string, wanted string) bool {
	for _, origin := range origins {
		if strings.EqualFold(strings.TrimRight(origin, "/"), strings.TrimRight(wanted, "/")) {
			return true
		}
	}
	return false
}

func randomID(prefix string, bytes int) (string, error) {
	value := make([]byte, bytes)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate browser session identifier: %w", err)
	}
	return prefix + hex.EncodeToString(value), nil
}

func secureEqual(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	var difference byte
	for i := range left {
		difference |= left[i] ^ right[i]
	}
	return difference == 0
}

func findChrome(configured string) string {
	if configured = strings.TrimSpace(configured); configured != "" {
		return configured
	}
	if configured = strings.TrimSpace(os.Getenv("AGENT_BROWSER_EXECUTABLE_PATH")); configured != "" {
		return configured
	}
	var candidates []string
	switch runtime.GOOS {
	case "windows":
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			candidates = append(candidates, filepath.Join(local, "Google", "Chrome", "Application", "chrome.exe"))
		}
		candidates = append(candidates,
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
		)
	case "darwin":
		candidates = append(candidates, "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome")
	default:
		candidates = append(candidates, "/usr/bin/google-chrome", "/usr/bin/google-chrome-stable", "/opt/google/chrome/chrome")
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}
