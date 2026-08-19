package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const maxUserMemoryBytes = 8 * 1024

// MemoryConfigSnapshot is the complete editable global state shown by the
// Memory built-in configuration dialog.
type MemoryConfigSnapshot struct {
	UserMemory          string `json:"userMemory"`
	UserMemoryPath      string `json:"userMemoryPath"`
	ProjectHistoryLimit int    `json:"projectHistoryLimit"`
	MaxUserMemoryBytes  int    `json:"maxUserMemoryBytes"`
	Prompt              string `json:"prompt"`
	DefaultPrompt       string `json:"defaultPrompt"`
	PromptIsDefault     bool   `json:"promptIsDefault"`
	MaxPromptBytes      int    `json:"maxPromptBytes"`
}

type SaveMemoryConfigRequest struct {
	UserMemory           string `json:"userMemory"`
	ProjectHistoryLimit  int    `json:"projectHistoryLimit"`
	Prompt               string `json:"prompt"`
	RestorePromptDefault bool   `json:"restorePromptDefault"`
}

func (a *App) memoryFilePath() string {
	return filepath.Join(a.store.Dir(), "memory", "user-memory.md")
}

// GetMemoryConfig reads the global user memory on demand. It is deliberately
// not part of GetBootstrap, so opening the app or the extensions page does not
// pull memory text into the frontend unless the user asks to configure it.
func (a *App) GetMemoryConfig() (MemoryConfigSnapshot, error) {
	path := a.memoryFilePath()
	content, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return MemoryConfigSnapshot{}, fmt.Errorf("read user memory: %w", err)
	}
	cfg := a.store.Get()
	prompt, err := a.getBuiltinPromptConfig("memory")
	if err != nil {
		return MemoryConfigSnapshot{}, err
	}
	return MemoryConfigSnapshot{
		UserMemory:          string(content),
		UserMemoryPath:      path,
		ProjectHistoryLimit: cfg.Memory.ProjectHistoryLimit,
		MaxUserMemoryBytes:  maxUserMemoryBytes,
		Prompt:              prompt.Prompt,
		DefaultPrompt:       prompt.DefaultPrompt,
		PromptIsDefault:     prompt.IsDefault,
		MaxPromptBytes:      prompt.MaxPromptBytes,
	}, nil
}

// SaveMemoryConfig updates the small global memory configuration. Project
// histories are pruned lazily by the extension on their next write, avoiding a
// synchronous scan of every configured workspace from this UI operation.
func (a *App) SaveMemoryConfig(req SaveMemoryConfigRequest) (MemoryConfigSnapshot, error) {
	previousConfig := a.store.Get()
	if len([]byte(req.UserMemory)) > maxUserMemoryBytes {
		return MemoryConfigSnapshot{}, fmt.Errorf("user memory exceeds %d bytes", maxUserMemoryBytes)
	}
	if req.ProjectHistoryLimit < 1 || req.ProjectHistoryLimit > 10000 {
		return MemoryConfigSnapshot{}, fmt.Errorf("project history limit must be between 1 and 10000")
	}
	if !req.RestorePromptDefault && len([]byte(req.Prompt)) > maxBuiltinPromptBytes {
		return MemoryConfigSnapshot{}, fmt.Errorf("memory prompt exceeds %d bytes", maxBuiltinPromptBytes)
	}
	content := strings.TrimSpace(req.UserMemory)
	if content != "" {
		content += "\n"
	}
	previous, previousErr := os.ReadFile(a.memoryFilePath())
	previousExists := previousErr == nil
	if previousErr != nil && !os.IsNotExist(previousErr) {
		return MemoryConfigSnapshot{}, fmt.Errorf("read previous user memory: %w", previousErr)
	}
	previousPrompt, previousPromptErr := os.ReadFile(a.builtinPromptPath("memory"))
	previousPromptExists := previousPromptErr == nil
	if previousPromptErr != nil && !os.IsNotExist(previousPromptErr) {
		return MemoryConfigSnapshot{}, fmt.Errorf("read previous memory prompt: %w", previousPromptErr)
	}
	dir := filepath.Dir(a.memoryFilePath())
	if err := ensurePrivateDir(dir); err != nil {
		return MemoryConfigSnapshot{}, fmt.Errorf("create memory directory: %w", err)
	}
	if err := writePrivateFileAtomic(a.memoryFilePath(), []byte(content)); err != nil {
		return MemoryConfigSnapshot{}, fmt.Errorf("write user memory: %w", err)
	}
	if err := a.store.SaveProjectHistoryLimit(req.ProjectHistoryLimit); err != nil {
		if previousExists {
			_ = writePrivateFileAtomic(a.memoryFilePath(), previous)
		} else {
			_ = os.Remove(a.memoryFilePath())
		}
		return MemoryConfigSnapshot{}, fmt.Errorf("save memory configuration: %w", err)
	}
	if _, err := a.saveBuiltinPromptConfig("memory", SaveBuiltinPromptConfigRequest{Prompt: req.Prompt, RestoreDefault: req.RestorePromptDefault}); err != nil {
		_ = a.store.SaveProjectHistoryLimit(previousConfig.Memory.ProjectHistoryLimit)
		if previousExists {
			_ = writePrivateFileAtomic(a.memoryFilePath(), previous)
		} else {
			_ = os.Remove(a.memoryFilePath())
		}
		return MemoryConfigSnapshot{}, fmt.Errorf("save memory prompt: %w", err)
	}
	next, err := a.GetMemoryConfig()
	if err == nil {
		return next, nil
	}
	_ = a.store.SaveProjectHistoryLimit(previousConfig.Memory.ProjectHistoryLimit)
	if previousExists {
		_ = writePrivateFileAtomic(a.memoryFilePath(), previous)
	} else {
		_ = os.Remove(a.memoryFilePath())
	}
	if previousPromptExists {
		_ = ensurePrivateDir(filepath.Dir(a.builtinPromptPath("memory")))
		_ = writePrivateFileAtomic(a.builtinPromptPath("memory"), previousPrompt)
	} else {
		_ = os.Remove(a.builtinPromptPath("memory"))
	}
	return MemoryConfigSnapshot{}, err
}

func writePrivateFileAtomic(target string, content []byte) error {
	dir := filepath.Dir(target)
	temp, err := os.CreateTemp(dir, ".memory-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(content); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	// Windows does not replace an existing destination with os.Rename. The
	// memory file is non-secret Markdown and this very short fallback window is
	// preferable to leaving a partial write behind.
	if err := os.Rename(tempPath, target); err == nil {
		return nil
	}
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Rename(tempPath, target)
}
