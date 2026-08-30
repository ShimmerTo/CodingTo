package terminal

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	ptylib "github.com/aymanbagabas/go-pty"
)

// LocalProfile is one locally available interactive shell.
type LocalProfile struct {
	ID         string
	Title      string
	Executable string
	Args       []string
	Env        []string
}

// LocalSpec contains the trusted executable details resolved by the backend.
type LocalSpec struct {
	Executable string
	Args       []string
	Env        []string
}

type localProcess struct {
	pty       ptylib.Pty
	command   *ptylib.Cmd
	closeOnce sync.Once
	closeErr  error
}

// LocalProfiles detects supported shells installed on the current machine.
func LocalProfiles() []LocalProfile {
	if runtime.GOOS == "windows" {
		return windowsProfiles()
	}
	shell := strings.TrimSpace(os.Getenv("SHELL"))
	if shell == "" {
		shell, _ = exec.LookPath("bash")
	}
	if shell == "" {
		shell, _ = exec.LookPath("sh")
	}
	if shell == "" {
		return nil
	}
	return []LocalProfile{{ID: "shell", Title: filepath.Base(shell), Executable: shell, Args: []string{"-l"}}}
}

// ResolveLocalProfile returns a detected local profile by its stable id.
func ResolveLocalProfile(id string) (LocalProfile, bool) {
	for _, profile := range LocalProfiles() {
		if profile.ID == id {
			return profile, true
		}
	}
	return LocalProfile{}, false
}

func windowsProfiles() []LocalProfile {
	profiles := make([]LocalProfile, 0, 3)
	if executable, err := exec.LookPath("pwsh.exe"); err == nil {
		profiles = append(profiles, LocalProfile{ID: "powershell", Title: "PowerShell", Executable: executable, Args: []string{"-NoLogo"}})
	} else if executable, err := exec.LookPath("powershell.exe"); err == nil {
		profiles = append(profiles, LocalProfile{ID: "powershell", Title: "Windows PowerShell", Executable: executable, Args: []string{"-NoLogo"}})
	}
	cmdPath := strings.TrimSpace(os.Getenv("ComSpec"))
	if cmdPath == "" {
		cmdPath, _ = exec.LookPath("cmd.exe")
	}
	if cmdPath != "" {
		profiles = append(profiles, LocalProfile{ID: "cmd", Title: "Command Prompt", Executable: cmdPath, Args: []string{"/Q"}})
	}
	if bash := findGitBash(); bash != "" {
		profiles = append(profiles, LocalProfile{
			ID: "git-bash", Title: "Git Bash", Executable: bash, Args: []string{"--login", "-i"}, Env: []string{"CHERE_INVOKING=1"},
		})
	}
	return profiles
}

func findGitBash() string {
	candidates := make([]string, 0, 6)
	if gitPath, err := exec.LookPath("git.exe"); err == nil {
		gitDir := filepath.Dir(gitPath)
		candidates = append(candidates,
			filepath.Join(gitDir, "..", "bin", "bash.exe"),
			filepath.Join(gitDir, "..", "usr", "bin", "bash.exe"),
		)
	}
	for _, base := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("LocalAppData")} {
		if strings.TrimSpace(base) == "" {
			continue
		}
		candidates = append(candidates,
			filepath.Join(base, "Git", "bin", "bash.exe"),
			filepath.Join(base, "Programs", "Git", "bin", "bash.exe"),
		)
	}
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		candidate = filepath.Clean(candidate)
		key := strings.ToLower(candidate)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

func startLocalProcess(root string, spec LocalSpec, columns, rows int) (terminalProcess, error) {
	if strings.TrimSpace(spec.Executable) == "" {
		return nil, errors.New("terminal executable is empty")
	}
	instance, err := ptylib.New()
	if err != nil {
		return nil, err
	}
	if err := instance.Resize(columns, rows); err != nil {
		return nil, errors.Join(err, instance.Close())
	}
	command := instance.Command(spec.Executable, spec.Args...)
	command.Dir = root
	command.Env = append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor")
	command.Env = append(command.Env, spec.Env...)
	if err := command.Start(); err != nil {
		return nil, errors.Join(err, instance.Close())
	}
	return &localProcess{pty: instance, command: command}, nil
}

func (p *localProcess) Read(buffer []byte) (int, error)  { return p.pty.Read(buffer) }
func (p *localProcess) Write(buffer []byte) (int, error) { return p.pty.Write(buffer) }
func (p *localProcess) Resize(columns, rows int) error   { return p.pty.Resize(columns, rows) }

func (p *localProcess) Wait() (int, error) {
	err := p.command.Wait()
	if p.command.ProcessState != nil {
		return p.command.ProcessState.ExitCode(), err
	}
	return -1, err
}

func (p *localProcess) Close() error {
	p.closeOnce.Do(func() {
		if p.command.Process != nil {
			if err := p.command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				p.closeErr = err
			}
		}
		if err := p.pty.Close(); err != nil && !errors.Is(err, io.ErrClosedPipe) && p.closeErr == nil {
			p.closeErr = err
		}
	})
	return p.closeErr
}
