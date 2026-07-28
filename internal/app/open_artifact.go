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
