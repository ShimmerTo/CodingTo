package app

import (
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
)

func (a *App) ChooseImages() ([]ImageInput, error) {
	paths, err := application.Get().Dialog.OpenFile().
		SetTitle("Choose images").
		CanChooseFiles(true).
		CanChooseDirectories(false).
		AddFilter("Images", "*.png;*.jpg;*.jpeg;*.webp;*.gif").
		AttachToWindow(a.window).
		PromptForMultipleSelection()
	if err != nil {
		return nil, err
	}
	if len(paths) > 10 {
		return nil, errors.New("select at most 10 images")
	}
	images := make([]ImageInput, 0, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if len(data) > 20*1024*1024 {
			return nil, fmt.Errorf("image is larger than 20 MB: %s", filepath.Base(path))
		}
		mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
		images = append(images, ImageInput{
			Name: filepath.Base(path), Type: "image",
			Data: base64.StdEncoding.EncodeToString(data), MimeType: mimeType,
		})
	}
	return images, nil
}

func (a *App) ChooseWorkspace() (string, error) {
	return application.Get().Dialog.OpenFile().
		SetTitle("Choose workspace").
		CanChooseFiles(false).
		CanChooseDirectories(true).
		CanCreateDirectories(true).
		AttachToWindow(a.window).
		PromptForSingleSelection()
}

func (a *App) ChooseSessionDir() (string, error) {
	return application.Get().Dialog.OpenFile().
		SetTitle("Choose session storage directory").
		CanChooseFiles(false).
		CanChooseDirectories(true).
		CanCreateDirectories(true).
		AttachToWindow(a.window).
		PromptForSingleSelection()
}

// agentFileWhitelist lists the only files the UI may read or write inside an
// agent's data directory. This keeps the surface narrow (no arbitrary file
// access) while supporting the per-agent prompt file (AGENTS.md) that Pi loads
// by default.
var agentFileWhitelist = map[string]bool{
	"AGENTS.md":          true,
	"PROMPT_FORCE.md":    true,
	"PROMPT_COMPRESS.md": true,
}

// agentFilePath resolves the absolute path for filename inside the data
// directory of the agent identified by agentId. It returns an error when the
// agent is unknown or the filename is not whitelisted.
func (a *App) agentFilePath(agentId, filename string) (string, error) {
	if !agentFileWhitelist[filename] {
		return "", fmt.Errorf("file not editable: %s", filename)
	}
	cfg := a.store.Get()
	profile, found := cfg.Agent(agentId)
	if !found {
		return "", fmt.Errorf("agent not found: %s", agentId)
	}
	// Make sure an empty dataDir resolves to the managed default and the
	// directory exists before we touch files inside it.
	a.store.EnsureAgentDataDirs(&cfg)
	profile, found = cfg.Agent(agentId)
	if !found || profile.DataDir == "" {
		return "", fmt.Errorf("agent data directory unavailable: %s", agentId)
	}
	return filepath.Join(profile.DataDir, filename), nil
}

// ReadAgentFile returns the contents of a whitelisted file inside an agent's
// data directory. A missing file yields an empty string rather than an error,
// so the UI can edit and create the file from scratch.
func (a *App) ReadAgentFile(agentId, filename string) (string, error) {
	path, err := a.agentFilePath(agentId, filename)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read %s: %w", filename, err)
	}
	return string(data), nil
}

// WriteAgentFile writes content to a whitelisted file inside an agent's data
// directory, creating the directory if needed.
func (a *App) WriteAgentFile(agentId, filename, content string) error {
	path, err := a.agentFilePath(agentId, filename)
	if err != nil {
		return err
	}
	if err := ensurePrivateDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("create agent directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", filename, err)
	}
	return os.Chmod(path, 0o600)
}
