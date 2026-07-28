package piagent

import (
	"bufio"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
)

const PackageName = "@earendil-works/pi-coding-agent"

// Install performs the same global npm installation advertised by the app,
// without invoking a shell.
func Install() (string, error) {
	npm := "npm"
	if runtime.GOOS == "windows" {
		npm = "npm.cmd"
	}
	path, err := exec.LookPath(npm)
	if err != nil {
		return "", fmt.Errorf("npm is not installed or is not available on PATH")
	}
	cmd := exec.Command(path, "install", "-g", "--ignore-scripts", PackageName)
	configureBackgroundProcess(cmd)
	output, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(output))
	if err != nil {
		return text, fmt.Errorf("%w", err)
	}
	return text, nil
}

// Version returns the installed Pi Agent version by running `pi --version`.
func Version() (string, error) {
	path, installed := FindExecutable()
	if !installed {
		return "", fmt.Errorf("Pi Agent is not installed")
	}
	cmd := exec.Command(path, "--version")
	configureBackgroundProcess(cmd)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(out))
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	return line, nil
}

// LatestVersion returns the latest published version of the Pi Agent npm package.
func LatestVersion() (string, error) {
	npm := "npm"
	if runtime.GOOS == "windows" {
		npm = "npm.cmd"
	}
	path, err := exec.LookPath(npm)
	if err != nil {
		return "", fmt.Errorf("npm is not installed or is not available on PATH")
	}
	cmd := exec.Command(path, "view", PackageName, "version")
	configureBackgroundProcess(cmd)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// InstallWithProgress installs the Pi Agent globally and streams each output
// line to onLine. It mirrors Install but reports progress for UI consumption.
func InstallWithProgress(onLine func(string)) (string, error) {
	npm := "npm"
	if runtime.GOOS == "windows" {
		npm = "npm.cmd"
	}
	path, err := exec.LookPath(npm)
	if err != nil {
		return "", fmt.Errorf("npm is not installed or is not available on PATH")
	}
	cmd := exec.Command(path, "install", "-g", "--ignore-scripts", PackageName)
	configureBackgroundProcess(cmd)
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	go func() {
		scanner := bufio.NewScanner(pr)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			onLine(scanner.Text())
		}
	}()
	runErr := cmd.Run()
	pw.Close()
	return "", runErr
}
