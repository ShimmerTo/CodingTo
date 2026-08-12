package piagent

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

const PackageName = "@earendil-works/pi-coding-agent"

func piInstallArgs() ([]string, error) {
	args := []string{"install", "-g", "--ignore-scripts"}
	if prefix := managedNpmPrefix(); prefix != "" {
		if err := os.MkdirAll(prefix, 0o755); err != nil {
			return nil, fmt.Errorf("create managed npm directory: %w", err)
		}
		args = append(args, "--prefix", prefix)
	}
	return append(args, PackageName), nil
}

// Install performs the same global npm installation advertised by the app,
// without invoking a shell.
func Install() (string, error) {
	npm := "npm"
	if runtime.GOOS == "windows" {
		npm = "npm.cmd"
	}
	path, err := commandPath(npm)
	if err != nil {
		return "", fmt.Errorf("npm is not installed or is not available on PATH")
	}
	args, err := piInstallArgs()
	if err != nil {
		return "", err
	}
	cmd := exec.Command(path, args...)
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
	path, err := commandPath(npm)
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
	path, err := commandPath(npm)
	if err != nil {
		return "", fmt.Errorf("npm is not installed or is not available on PATH")
	}
	args, err := piInstallArgs()
	if err != nil {
		return "", err
	}
	cmd := exec.Command(path, args...)
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

// NpmInstalled reports whether the npm CLI is discoverable on PATH.
func NpmInstalled() bool {
	npm := "npm"
	if runtime.GOOS == "windows" {
		npm = "npm.cmd"
	}
	_, err := commandPath(npm)
	return err == nil
}

// NodeInstalled reports whether the node runtime is discoverable on PATH.
func NodeInstalled() bool {
	_, err := commandPath("node")
	return err == nil
}
