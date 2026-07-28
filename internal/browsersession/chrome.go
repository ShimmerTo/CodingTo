package browsersession

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
)

var errCDPStartup = errors.New("chrome CDP startup failed")
var errProfileReclaimed = errors.New("previous profile browser was reclaimed")

type lockRecord struct {
	InstanceID string    `json:"instanceId"`
	LeaseID    string    `json:"leaseId"`
	OwnerPID   int       `json:"ownerPid"`
	ChromePID  int       `json:"chromePid,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
}

type cdpTarget struct {
	ID                   string `json:"id"`
	Type                 string `json:"type"`
	URL                  string `json:"url"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

var cdpHTTPClient = &http.Client{Timeout: 3 * time.Second}

func (s *Service) acquireProfileLock(l *lease) error {
	if err := os.MkdirAll(l.ProfileDir, 0o700); err != nil {
		return err
	}
	record := lockRecord{InstanceID: s.instanceID, LeaseID: l.ID, OwnerPID: os.Getpid(), CreatedAt: s.options.Now()}
	content, _ := json.Marshal(record)
	for attempt := 0; attempt < 2; attempt++ {
		file, err := os.OpenFile(l.LockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			_, writeErr := file.Write(append(content, '\n'))
			closeErr := file.Close()
			if writeErr != nil {
				_ = os.Remove(l.LockPath)
				return writeErr
			}
			return closeErr
		}
		if !os.IsExist(err) {
			return err
		}
		raw, readErr := os.ReadFile(l.LockPath)
		var previous lockRecord
		if readErr == nil && json.Unmarshal(raw, &previous) == nil {
			starting := previous.ChromePID == 0 && s.options.Now().Sub(previous.CreatedAt) < 30*time.Second
			if starting {
				return errors.New("profile lock is active")
			}
			reclaimProfileBrowser(l.ProfileDir)
		}
		if removeErr := os.Remove(l.LockPath); removeErr != nil {
			return errors.New("profile lock is unavailable")
		}
		return errProfileReclaimed
	}
	return errors.New("profile lock is unavailable")
}

func reclaimProfileBrowser(profileDir string) {
	port, err := readDevToolsPort(filepath.Join(profileDir, "DevToolsActivePort"))
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := getTargets(ctx, port); err != nil {
		return
	}
	_ = closeBrowser(ctx, port)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := getTargets(ctx, port); err != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) updateProfileLock(l *lease) {
	record := lockRecord{
		InstanceID: s.instanceID, LeaseID: l.ID, OwnerPID: os.Getpid(), ChromePID: l.ChromePID, CreatedAt: l.CreatedAt,
	}
	content, _ := json.MarshalIndent(record, "", "  ")
	temp := l.LockPath + "." + l.ID + ".tmp"
	if os.WriteFile(temp, append(content, '\n'), 0o600) == nil {
		if replaceFile(temp, l.LockPath) != nil {
			_ = os.Remove(temp)
		}
	}
}

func (s *Service) launchChrome(ctx context.Context, l *lease) error {
	chrome := findChrome(s.options.ChromeExecutable)
	if chrome == "" {
		return errors.New("Google Chrome was not found")
	}
	activePortPath := filepath.Join(l.ProfileDir, "DevToolsActivePort")
	_ = os.Remove(activePortPath)
	cmd := exec.Command(chrome,
		"--user-data-dir="+l.ProfileDir,
		"--remote-debugging-port=0",
		"--remote-allow-origins=*",
		"--no-first-run",
		"--no-default-browser-check",
		l.TargetURL,
	)
	configureChromeProcess(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	l.Chrome = cmd
	l.ChromePID = cmd.Process.Pid
	l.exit = make(chan error, 1)
	s.updateProfileLock(l)
	go func() {
		l.exit <- cmd.Wait()
		close(l.exit)
	}()

	waitCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	port, err := waitForDevToolsPort(waitCtx, activePortPath, l.exit)
	if err != nil {
		return fmt.Errorf("%w: %v", errCDPStartup, err)
	}
	l.CDPPort = port
	if err := waitForCDP(waitCtx, port); err != nil {
		return fmt.Errorf("%w: %v", errCDPStartup, err)
	}
	return nil
}

func waitForDevToolsPort(ctx context.Context, path string, exited <-chan error) (int, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case err := <-exited:
			if err == nil {
				err = errors.New("Chrome exited before CDP became ready")
			}
			return 0, err
		case <-ticker.C:
			if port, err := readDevToolsPort(path); err == nil {
				return port, nil
			}
		}
	}
}

func readDevToolsPort(path string) (int, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		return 0, errors.New("DevToolsActivePort is empty")
	}
	port, err := strconv.Atoi(strings.TrimSpace(scanner.Text()))
	if err != nil || port <= 0 || port > 65535 {
		return 0, errors.New("DevToolsActivePort is invalid")
	}
	return port, nil
}

func waitForCDP(ctx context.Context, port int) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := getTargets(ctx, port); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func getTargets(ctx context.Context, port int) ([]cdpTarget, error) {
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/json/list", port), nil)
	response, err := cdpHTTPClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("CDP returned %s", response.Status)
	}
	var targets []cdpTarget
	if err := json.NewDecoder(response.Body).Decode(&targets); err != nil {
		return nil, err
	}
	return targets, nil
}

func selectPageTarget(ctx context.Context, port int, targetOrigin string) (cdpTarget, error) {
	targets, err := getTargets(ctx, port)
	if err != nil {
		return cdpTarget{}, err
	}
	var fallback cdpTarget
	for _, target := range targets {
		if target.Type != "page" || target.WebSocketDebuggerURL == "" {
			continue
		}
		if fallback.ID == "" || (fallback.URL == "about:blank" && target.URL != "about:blank") {
			fallback = target
		}
		if parsed, _, parseErr := validateTarget(target.URL); parseErr == nil {
			_, origin, _ := validateTarget(parsed)
			if strings.EqualFold(origin, targetOrigin) {
				return target, nil
			}
		}
	}
	if fallback.ID == "" {
		return cdpTarget{}, errors.New("CDP has no page target")
	}
	return fallback, nil
}

func cdpCall(ctx context.Context, websocketURL, method string, params any) (json.RawMessage, error) {
	parsed, err := url.Parse(websocketURL)
	if err != nil || parsed.Scheme != "ws" || (parsed.Hostname() != "127.0.0.1" && !strings.EqualFold(parsed.Hostname(), "localhost")) {
		return nil, errors.New("CDP websocket is not loopback")
	}
	connection, _, err := websocket.Dial(ctx, websocketURL, nil)
	if err != nil {
		return nil, err
	}
	defer connection.Close(websocket.StatusNormalClosure, "")
	request := map[string]any{"id": 1, "method": method}
	if params != nil {
		request["params"] = params
	}
	encoded, _ := json.Marshal(request)
	if err := connection.Write(ctx, websocket.MessageText, encoded); err != nil {
		return nil, err
	}
	for {
		_, message, err := connection.Read(ctx)
		if err != nil {
			return nil, err
		}
		var response struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal(message, &response) != nil || response.ID != 1 {
			continue
		}
		if len(response.Error) > 0 {
			return nil, fmt.Errorf("CDP %s failed", method)
		}
		return response.Result, nil
	}
}

const assessmentScript = `(() => {
  const visible = (e) => !!e && (e.offsetWidth || e.offsetHeight || e.getClientRects().length);
  const text = ((document.title || '') + '\n' + (document.body?.innerText || '')).trim();
  const htmlLength = document.documentElement?.outerHTML?.length || 0;
  const password = [...document.querySelectorAll('input[type=password]')].some(visible);
  const loginAction = [...document.querySelectorAll('a,button,[role=button],input[type=submit]')]
    .filter(visible).some((e) => /^(sign[ -]?in|log[ -]?in|login)(\b|$)|^(登录|登陆|去登录|立即登录)(\s*[/·|]\s*注册)?$/i
      .test((e.innerText || e.value || e.getAttribute('aria-label') || '').trim()));
  const challenge = /(captcha|verify you are human|checking your browser|just a moment|access denied|security check|cloudflare|验证码|人机验证|安全检查|访问受限)/i.test(text);
  const appRoot = !!document.querySelector('#app:not(:empty),#root:not(:empty),main:not(:empty),[data-reactroot]');
  const navigation = performance.getEntriesByType('navigation')[0];
  const responseStatus = Number(navigation?.responseStatus || 0);
  return { url: location.href, readyState: document.readyState, textLength: text.length,
    htmlLength, password, loginAction, challenge, appRoot, responseStatus };
})()`

func assessPage(ctx context.Context, port int, targetOrigin string) (pageAssessment, error) {
	target, err := selectPageTarget(ctx, port, targetOrigin)
	if err != nil {
		return pageAssessment{}, err
	}
	if target.URL == "" || target.URL == "about:blank" || strings.HasPrefix(target.URL, "chrome-error://") {
		return pageAssessment{status: "not_ready", reason: "blank or error page"}, nil
	}
	result, err := cdpCall(ctx, target.WebSocketDebuggerURL, "Runtime.evaluate", map[string]any{
		"expression": assessmentScript, "returnByValue": true, "awaitPromise": true,
	})
	if err != nil {
		return pageAssessment{}, err
	}
	var envelope struct {
		Result struct {
			Value struct {
				URL            string `json:"url"`
				ReadyState     string `json:"readyState"`
				TextLength     int    `json:"textLength"`
				HTMLLength     int    `json:"htmlLength"`
				Password       bool   `json:"password"`
				LoginAction    bool   `json:"loginAction"`
				Challenge      bool   `json:"challenge"`
				AppRoot        bool   `json:"appRoot"`
				ResponseStatus int    `json:"responseStatus"`
			} `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(result, &envelope); err != nil {
		return pageAssessment{}, err
	}
	page := envelope.Result.Value
	if page.URL == "" || page.URL == "about:blank" || strings.HasPrefix(page.URL, "chrome-error://") {
		return pageAssessment{status: "not_ready", reason: "blank or error page"}, nil
	}
	_, pageOrigin, err := validateTarget(page.URL)
	if err != nil {
		return pageAssessment{status: "not_ready", reason: "non-http page"}, nil
	}
	parsedPage, _ := url.Parse(page.URL)
	pathLower := strings.ToLower(parsedPage.EscapedPath())
	loginPath := false
	for _, marker := range []string{"/login", "/signin", "/sign-in", "/sign_in", "/sso", "/oauth", "/auth"} {
		if strings.HasPrefix(pathLower, marker) || strings.Contains(pathLower, marker+"/") || strings.Contains(pathLower, marker+"?") {
			loginPath = true
			break
		}
	}
	if page.Challenge || page.Password || page.LoginAction || loginPath || !strings.EqualFold(pageOrigin, targetOrigin) {
		return pageAssessment{status: "login_required", reason: "login or security challenge"}, nil
	}
	if page.ResponseStatus >= 400 {
		return pageAssessment{status: "not_ready", reason: "navigation returned an HTTP error"}, nil
	}
	if page.ReadyState != "complete" || page.HTMLLength < 100 || (page.TextLength < 20 && !page.AppRoot) {
		return pageAssessment{status: "not_ready", reason: "page has insufficient positive evidence"}, nil
	}
	return pageAssessment{status: "ready", reason: "positive page evidence"}, nil
}

func navigate(ctx context.Context, port int, targetURL string) error {
	_, origin, err := validateTarget(targetURL)
	if err != nil {
		return err
	}
	target, err := selectPageTarget(ctx, port, origin)
	if err != nil {
		return err
	}
	_, err = cdpCall(ctx, target.WebSocketDebuggerURL, "Page.navigate", map[string]any{"url": targetURL})
	return err
}

func ensureTarget(ctx context.Context, port int, targetURL, targetOrigin string) error {
	target, err := selectPageTarget(ctx, port, targetOrigin)
	if err != nil {
		return err
	}
	current, _, currentErr := validateTarget(target.URL)
	wanted, _, wantedErr := validateTarget(targetURL)
	if currentErr == nil && wantedErr == nil && strings.EqualFold(current, wanted) {
		return nil
	}
	_, err = cdpCall(ctx, target.WebSocketDebuggerURL, "Page.navigate", map[string]any{"url": targetURL})
	return err
}

func closeBrowser(ctx context.Context, port int) error {
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/json/version", port), nil)
	response, err := cdpHTTPClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	var version struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(response.Body).Decode(&version); err != nil {
		return err
	}
	if version.WebSocketDebuggerURL == "" {
		return errors.New("browser CDP websocket unavailable")
	}
	_, err = cdpCall(ctx, version.WebSocketDebuggerURL, "Browser.close", nil)
	return err
}
