package browsersession

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

var allowedCommands = map[string]bool{
	"snapshot": true, "click": true, "fill": true, "type": true, "press": true,
	"hover": true, "scroll": true, "select": true, "check": true, "uncheck": true,
	"get": true, "eval": true, "screenshot": true, "wait": true, "find": true,
	"back": true, "forward": true, "reload": true,
}

func validateExecuteArgs(args []string) error {
	if len(args) == 0 || !allowedCommands[strings.ToLower(strings.TrimSpace(args[0]))] {
		return errors.New("browser command is not in the allowlist")
	}
	for _, arg := range args {
		lower := strings.ToLower(strings.TrimSpace(arg))
		if strings.ContainsAny(arg, "\r\n\x00") {
			return errors.New("browser arguments contain invalid control characters")
		}
		for _, forbidden := range []string{"--profile", "--cdp", "--session", "--headed"} {
			if lower == forbidden || strings.HasPrefix(lower, forbidden+"=") {
				return errors.New("browser connection arguments are managed by the service")
			}
		}
	}
	return nil
}

func (s *Service) runAdapter(parent context.Context, leaseID string, port int, args []string, timeout time.Duration) (string, error) {
	binary := strings.TrimSpace(s.options.AgentBrowserBinary)
	if binary == "" {
		binary = strings.TrimSpace(os.Getenv("AGENT_BROWSER_BIN"))
	}
	if binary == "" {
		names := []string{"agent-browser"}
		if runtime.GOOS == "windows" {
			names = append(names, "agent-browser.cmd")
		}
		for _, name := range names {
			if path, err := exec.LookPath(name); err == nil {
				binary = path
				break
			}
		}
	}
	if binary == "" {
		return "", errors.New("agent-browser is not installed")
	}
	binary, prefix, err := resolveAdapterBinary(binary)
	if err != nil {
		return "", err
	}
	commandArgs := adapterCommandArgs(prefix, leaseID, port, args)
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, commandArgs...)
	configureAdapterProcess(cmd)
	cmd.Env = append(cmd.Environ(),
		// The adapter daemon closes its attached browser when it expires. Keep
		// this crash-recovery timeout beyond the Go lease reaper so the adapter
		// can never terminate a lease that the service still considers active.
		"AGENT_BROWSER_IDLE_TIMEOUT_MS="+strconv.FormatInt((2*s.options.IdleTimeout).Milliseconds(), 10),
	)
	// agent-browser starts a persistent daemon on the first command. On
	// Windows that daemon inherits stdout/stderr, so CombinedOutput waits for
	// pipe EOF until the daemon exits even though the CLI client has already
	// completed. A real file lets Wait track only the direct child process.
	outputFile, err := os.CreateTemp("", "codingto-browser-adapter-*.log")
	if err != nil {
		return "", errors.New("could not create browser adapter output")
	}
	outputPath := outputFile.Name()
	defer os.Remove(outputPath)
	defer outputFile.Close()
	cmd.Stdout = outputFile
	cmd.Stderr = outputFile
	runErr := cmd.Run()
	if _, err := outputFile.Seek(0, io.SeekStart); err != nil {
		return "", errors.New("could not read browser adapter output")
	}
	output, readErr := io.ReadAll(outputFile)
	if readErr != nil {
		return "", errors.New("could not read browser adapter output")
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if runErr != nil {
		return "", fmt.Errorf("agent-browser execution failed")
	}
	return string(output), nil
}

func adapterSessionName(leaseID string) string {
	return "codingto-browser-" + strings.TrimPrefix(leaseID, "bl_")
}

func adapterCommandArgs(prefix []string, leaseID string, port int, args []string) []string {
	commandArgs := append([]string{}, prefix...)
	commandArgs = append(commandArgs,
		"--session", adapterSessionName(leaseID),
		"--cdp", strconv.Itoa(port),
	)
	return append(commandArgs, args...)
}

var windowsShimTarget = regexp.MustCompile(`(?i)%~dp0([^"%\r\n]+?\.(?:exe|js))`)

// resolveAdapterBinary avoids cmd.exe entirely. Besides preventing shell
// injection through fill/type values, this lets Go keep exact argument
// boundaries for passwords and other user input.
func resolveAdapterBinary(binary string) (string, []string, error) {
	extension := strings.ToLower(filepath.Ext(binary))
	if runtime.GOOS != "windows" || (extension != ".cmd" && extension != ".bat") {
		return binary, nil, nil
	}
	raw, err := os.ReadFile(binary)
	if err != nil {
		return "", nil, errors.New("agent-browser launcher is unavailable")
	}
	match := windowsShimTarget.FindStringSubmatch(string(raw))
	if len(match) != 2 {
		return "", nil, errors.New("agent-browser launcher format is unsupported")
	}
	target := filepath.Clean(filepath.Join(filepath.Dir(binary), filepath.FromSlash(strings.ReplaceAll(match[1], `\`, `/`))))
	if strings.EqualFold(filepath.Ext(target), ".exe") {
		return target, nil, nil
	}
	node, err := exec.LookPath("node.exe")
	if err != nil {
		node, err = exec.LookPath("node")
	}
	if err != nil {
		return "", nil, errors.New("Node.js is unavailable for the agent-browser launcher")
	}
	return node, []string{target}, nil
}
