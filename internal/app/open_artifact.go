package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// OpenSessionArtifact opens one archived input/browser artifact or one
// canonical parsed-document artifact with the operating system's default
// application. The frontend supplies the path returned by GetSessionChanges,
// but the backend remains authoritative.
func (a *App) OpenSessionArtifact(path string) error {
	sessions, err := a.store.Store().ListSessions()
	if err != nil {
		return fmt.Errorf("list conversations: %w", err)
	}
	sessionDirs := make([]string, 0, len(sessions))
	for _, session := range sessions {
		if session.SessionDir != "" {
			sessionDirs = append(sessionDirs, session.SessionDir)
		}
	}
	resolved, err := resolveSessionArtifact(path, sessionDirs)
	if err != nil {
		return err
	}
	if err := openLocalPath(resolved); err != nil {
		return fmt.Errorf("open session artifact: %w", err)
	}
	return nil
}

// OpenSessionWorkspaceFile opens a regular file that belongs to the
// conversation workspace with the operating system's default application.
// Tool-call arguments are model-controlled, so the backend resolves symlinks
// and enforces workspace containment instead of trusting the frontend path.
func (a *App) OpenSessionWorkspaceFile(sessionID int64, path string) error {
	workspace, err := a.sessionGitWorkspace(sessionID)
	if err != nil {
		return err
	}
	resolved, err := resolveWorkspaceFile(workspace, path)
	if err != nil {
		return err
	}
	if err := openLocalPath(resolved); err != nil {
		return fmt.Errorf("open workspace file: %w", err)
	}
	return nil
}

func resolveWorkspaceFile(workspace, path string) (string, error) {
	root := filepath.Clean(strings.TrimSpace(workspace))
	if root == "." || !filepath.IsAbs(root) {
		return "", errors.New("conversation workspace must be absolute")
	}
	rootResolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve conversation workspace: %w", err)
	}

	candidate := filepath.Clean(strings.TrimSpace(path))
	if candidate == "." || candidate == "" {
		return "", errors.New("workspace file path is required")
	}
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(rootResolved, candidate)
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve workspace file: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat workspace file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("workspace path is not a regular file")
	}
	relative, err := filepath.Rel(rootResolved, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return "", errors.New("file does not belong to the conversation workspace")
	}
	return resolved, nil
}

func resolveSessionArtifact(path string, sessionDirs []string) (string, error) {
	candidate := filepath.Clean(strings.TrimSpace(path))
	if candidate == "." || !filepath.IsAbs(candidate) {
		return "", errors.New("artifact path must be absolute")
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve artifact: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat artifact: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("artifact is not a regular file")
	}

	for _, sessionDir := range sessionDirs {
		allowedRoots := sessionArtifactRoots(sessionDir)
		for _, root := range allowedRoots {
			rootResolved, rootErr := filepath.EvalSymlinks(root)
			if rootErr != nil {
				continue
			}
			relative, relErr := filepath.Rel(rootResolved, resolved)
			if relErr == nil && relative != ".." &&
				!strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
				return resolved, nil
			}
		}
	}
	return "", errors.New("artifact does not belong to a known conversation")
}
