package piagent

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

//go:embed auth_bridge.mjs
var authBridgeSource []byte

// DefaultAgentDir returns Pi's default global agent directory
// (~/.pi/agent), which CodingTo uses as the canonical credential source.
// Every CodingTo SDK runtime points authPath at this directory while keeping
// models and resources in its isolated Agent data directory.
func DefaultAgentDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", errors.New("cannot determine user home directory")
	}
	return filepath.Join(home, ".pi", "agent"), nil
}

// CodexAuthEvent is a non-secret progress event emitted by Pi's OAuth flow.
type CodexAuthEvent struct {
	Type      string      `json:"type"`
	URL       string      `json:"url,omitempty"`
	AccountID string      `json:"accountId,omitempty"`
	Expires   int64       `json:"expires,omitempty"`
	Code      string      `json:"code,omitempty"`
	Usage     *CodexUsage `json:"usage,omitempty"`
}

// CodexUsageWindow is one non-secret ChatGPT subscription quota window.
type CodexUsageWindow struct {
	Percent      float64 `json:"percent"`
	ResetSeconds int64   `json:"resetSeconds"`
}

// CodexUsage contains the rolling and weekly ChatGPT subscription quotas.
type CodexUsage struct {
	Rolling CodexUsageWindow `json:"rolling"`
	Weekly  CodexUsageWindow `json:"weekly"`
}

// RunOpenAICodexLogin delegates the complete OpenAI Codex OAuth flow and
// credential persistence to the installed Pi SDK. Login and usage operations
// target the same default auth.json used by CodingTo Agent runtimes.
func RunOpenAICodexLogin(ctx context.Context, dataDir string, onEvent func(CodexAuthEvent)) error {
	return runCodexAuthBridge(ctx, "login", dataDir, onEvent)
}

// RunOpenAICodexLogout asks Pi to remove only the openai-codex credential
// from the shared auth.json using Pi's serialized credential store.
func RunOpenAICodexLogout(ctx context.Context, dataDir string) error {
	return runCodexAuthBridge(ctx, "logout", dataDir, nil)
}

// RunOpenAICodexUsage refreshes the stored OAuth credential through Pi when
// needed, then returns only non-secret ChatGPT quota data from the bridge.
func RunOpenAICodexUsage(ctx context.Context, dataDir string) (CodexUsage, error) {
	var usage *CodexUsage
	err := runCodexAuthBridge(ctx, "usage", dataDir, func(event CodexAuthEvent) {
		if event.Type == "completed" && event.Usage != nil {
			value := *event.Usage
			usage = &value
		}
	})
	if err != nil {
		return CodexUsage{}, err
	}
	if usage == nil {
		return CodexUsage{}, errors.New("Pi OAuth bridge returned no usage data")
	}
	return *usage, nil
}

func runCodexAuthBridge(ctx context.Context, operation, dataDir string, onEvent func(CodexAuthEvent)) error {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		return errors.New("agent data directory is empty")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("prepare agent data directory: %w", err)
	}
	nodePath, err := commandPath("node")
	if err != nil {
		return fmt.Errorf("Node.js is unavailable: %w", err)
	}
	sdkEntry, err := codingAgentSDKEntry()
	if err != nil {
		return err
	}
	bridgePath, err := materializeAuthBridge()
	if err != nil {
		return fmt.Errorf("prepare Pi OAuth bridge: %w", err)
	}
	defer func() { _ = os.Remove(bridgePath) }()

	cmd := exec.CommandContext(ctx, nodePath, bridgePath, sdkEntry, operation, dataDir)
	configureBackgroundProcess(cmd)
	cmd.Dir = dataDir
	cmd.Env = commandEnv(dataDir)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("read Pi OAuth output: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start Pi OAuth: %w", err)
	}

	completed := false
	bridgeCode := ""
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		var event CodexAuthEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		if event.Type == "completed" {
			completed = true
		}
		if event.Type == "error" {
			bridgeCode = event.Code
		}
		if onEvent != nil {
			onEvent(event)
		}
	}
	scanErr := scanner.Err()
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if scanErr != nil {
		return fmt.Errorf("read Pi OAuth output: %w", scanErr)
	}
	if waitErr != nil || !completed {
		if bridgeCode == "" {
			bridgeCode = "oauth_failed"
		}
		return fmt.Errorf("Pi OAuth failed: %s", bridgeCode)
	}
	return nil
}

func materializeAuthBridge() (string, error) {
	file, err := os.CreateTemp("", "codingto-pi-auth-*.mjs")
	if err != nil {
		return "", err
	}
	path := file.Name()
	keep := false
	defer func() {
		_ = file.Close()
		if !keep {
			_ = os.Remove(path)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return "", err
	}
	if _, err := file.Write(authBridgeSource); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	keep = true
	return path, nil
}

func codingAgentSDKEntry() (string, error) {
	piPath, ok := FindExecutable()
	if !ok {
		return "", errors.New("Pi CLI is not installed")
	}
	candidates := []string{
		filepath.Join(filepath.Dir(piPath), "node_modules", "@earendil-works", "pi-coding-agent", "dist", "index.js"),
	}
	if resolved, err := filepath.EvalSymlinks(piPath); err == nil {
		if strings.EqualFold(filepath.Base(resolved), "cli.js") {
			candidates = append(candidates, filepath.Join(filepath.Dir(resolved), "index.js"))
		}
		candidates = append(candidates,
			filepath.Join(filepath.Dir(resolved), "node_modules", "@earendil-works", "pi-coding-agent", "dist", "index.js"),
		)
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			absolute, absErr := filepath.Abs(candidate)
			if absErr != nil {
				return "", absErr
			}
			return absolute, nil
		}
	}
	return "", fmt.Errorf("Pi SDK entry was not found beside %s; reinstall @earendil-works/pi-coding-agent", piPath)
}
