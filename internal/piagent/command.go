package piagent

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// commandEnv builds the environment for agent commands. Besides the agent data
// dir it also configures a Playwright browser-download mirror when the user has
// not overridden PLAYWRIGHT_DOWNLOAD_HOST, because the default Playwright CDN
// can be extremely slow (or blocked) on some networks, which makes installs
// appear to hang for minutes with no output.
func commandEnv(dataDir string) []string {
	env := make([]string, 0, len(os.Environ())+2)
	for _, item := range os.Environ() {
		key, _, found := strings.Cut(item, "=")
		if found && ((runtime.GOOS == "windows" && strings.EqualFold(key, "PI_CODING_AGENT_DIR")) || (runtime.GOOS != "windows" && key == "PI_CODING_AGENT_DIR")) {
			continue
		}
		env = append(env, item)
	}
	env = append(env, "PI_CODING_AGENT_DIR="+dataDir)
	if os.Getenv("PLAYWRIGHT_DOWNLOAD_HOST") == "" {
		env = append(env, "PLAYWRIGHT_DOWNLOAD_HOST=https://cdn.npmmirror.com/binaries/playwright")
	}
	return env
}

// RunAgentCommand executes a shell command with an agent-scoped Pi user/global
// directory. A normal `pi install` therefore materializes extensions into THAT
// agent only. Callers must not add `-l`: that flag targets <cwd>/.pi instead,
// which is project-local and is not loaded when the agent later runs elsewhere.
//
// The command is run through the platform shell so users can paste a complete
// install command (for example `pi install npm:pi-agent-browser-native`) and
// have it resolved via PATH exactly as it would be from a terminal.
func RunAgentCommand(dataDir, command string) (string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", fmt.Errorf("empty command")
	}

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/c", command)
	} else {
		cmd = exec.Command("sh", "-c", command)
	}
	configureBackgroundProcess(cmd)
	cmd.Dir = dataDir
	cmd.Env = commandEnv(dataDir)

	output, err := cmd.CombinedOutput()
	return normalizeOutput(output, err), err
}

// RunAgentCommandWithProgress runs a shell command inside an agent's isolated
// data directory, streaming each output line to onLine as it is produced (so
// long-running installs such as Playwright browser downloads can show live
// progress instead of looking frozen), and returns the full combined output
// (trimmed) at the end.
func RunAgentCommandWithProgress(dataDir, command string, onLine func(string)) (string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", fmt.Errorf("empty command")
	}

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/c", command)
	} else {
		cmd = exec.Command("sh", "-c", command)
	}
	configureBackgroundProcess(cmd)
	cmd.Dir = dataDir
	cmd.Env = commandEnv(dataDir)

	// A pipe lets us merge stderr into stdout and scan the combined stream
	// line by line as it is produced.
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw

	var buf bytes.Buffer
	scanner := bufio.NewScanner(pr)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for scanner.Scan() {
			line := scanner.Text()
			buf.WriteString(line)
			buf.WriteString("\n")
			if onLine != nil {
				onLine(line)
			}
		}
	}()

	if err := cmd.Start(); err != nil {
		pw.Close()
		<-done
		return "", err
	}

	waitErr := cmd.Wait()
	// Closing the write end makes the scanner reach EOF so the goroutine exits.
	pw.Close()
	<-done

	return normalizeOutput(buf.Bytes(), waitErr), waitErr
}

// ParsePiInstallCommand accepts only the skill install form used by CodingTo.
// Keeping source parsing here prevents the Skills page from turning arbitrary
// shell text into a command while still accepting npm:, git:, URL, and local
// package sources supported by Pi.
func ParsePiInstallCommand(command string) (string, error) {
	command = strings.TrimSpace(command)
	if command == "" || strings.ContainsAny(command, "\r\n\x00;&|<>`") {
		return "", fmt.Errorf("invalid pi install command")
	}
	parts := strings.Fields(command)
	if len(parts) != 3 || !strings.EqualFold(parts[0], "pi") || !strings.EqualFold(parts[1], "install") {
		return "", fmt.Errorf("请输入形如 pi install git:github.com/user/repo 的命令")
	}
	source := strings.TrimSpace(parts[2])
	if source == "" || strings.HasPrefix(source, "-") {
		return "", fmt.Errorf("invalid pi package source")
	}
	return source, nil
}

// InstallAgentPackage runs pi install with PI_CODING_AGENT_DIR forced to this
// agent. It is deliberately narrower than RunAgentCommand so Skills cannot
// accidentally install into the default/global Pi profile.
func InstallAgentPackage(dataDir, source string) (string, error) {
	if strings.TrimSpace(source) == "" || strings.ContainsAny(source, "\r\n\x00;&|<>`") {
		return "", fmt.Errorf("invalid pi package source")
	}
	if runtime.GOOS == "windows" && strings.ContainsAny(source, `%!"`) {
		return "", fmt.Errorf("invalid pi package source")
	}
	return RunAgentCommand(dataDir, "pi install "+quoteShellArgument(source))
}

// UpdateAgentPackage updates exactly one package in this agent's isolated Pi
// profile. PI_CODING_AGENT_DIR is applied by RunAgentCommand.
func UpdateAgentPackage(dataDir, source string) (string, error) {
	if strings.TrimSpace(source) == "" || strings.ContainsAny(source, "\r\n\x00;&|<>`") {
		return "", fmt.Errorf("invalid pi package source")
	}
	if runtime.GOOS == "windows" && strings.ContainsAny(source, `%!"`) {
		return "", fmt.Errorf("invalid pi package source")
	}
	return RunAgentCommand(dataDir, "pi update "+quoteShellArgument(source))
}

// UninstallAgentPackage removes one exact source already present in the
// agent's settings.json. Quoting is kept here, beside the platform shell
// invocation, so callers never concatenate untrusted package identifiers.
func UninstallAgentPackage(dataDir, source string) (string, error) {
	source = strings.TrimSpace(source)
	if source == "" || strings.ContainsAny(source, "\r\n\x00") {
		return "", fmt.Errorf("invalid package source")
	}
	if runtime.GOOS == "windows" && strings.ContainsAny(source, `%!"`) {
		return "", fmt.Errorf("invalid package source")
	}
	return RunAgentCommand(dataDir, "pi uninstall "+quoteShellArgument(source))
}

func quoteShellArgument(value string) string {
	if runtime.GOOS == "windows" {
		// cmd.exe expands %VAR% even inside double quotes. Pi package sources do
		// not require percent signs, so reject them instead of risking expansion.
		return `"` + value + `"`
	}
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

// normalizeOutput trims combined command output and guarantees a non-empty
// message so the UI always has something to display.
func normalizeOutput(output []byte, err error) string {
	text := strings.TrimSpace(string(output))
	if err != nil {
		if text == "" {
			text = err.Error()
		}
		return text
	}
	if text == "" {
		text = "ok"
	}
	return text
}
