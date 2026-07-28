package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// SaveSessionArtifact copies the given file into the operating system's
// Downloads folder and opens it with the default application. The backend
// derives trusted roots from sessionID; callers cannot expand the allow-list by
// supplying a filesystem root. Symlinks are resolved before authorization.
func (a *App) SaveSessionArtifact(path string, sessionID int64) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("path 不能为空")
	}
	if !filepath.IsAbs(path) || sessionID <= 0 {
		return "", errors.New("path 必须为绝对路径且 sessionID 必须有效")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("无法访问文件: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("不是普通文件: %s", path)
	}
	abs, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("无法解析路径: %w", err)
	}
	if err := a.authorizeDownloadPath(abs, sessionID); err != nil {
		return "", fmt.Errorf("文件不在允许范围内: %s", path)
	}
	downloads := downloadsDir()
	if err := os.MkdirAll(downloads, 0o755); err != nil {
		return "", fmt.Errorf("无法创建下载目录: %w", err)
	}
	src, err := os.Open(abs)
	if err != nil {
		return "", err
	}
	defer src.Close()
	dest, dst, err := createUniqueDownload(downloads, filepath.Base(abs))
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		_ = os.Remove(dest)
		return "", fmt.Errorf("复制到下载目录失败: %w", err)
	}
	if err := dst.Close(); err != nil {
		_ = os.Remove(dest)
		return "", err
	}
	if err := openLocalPath(dest); err != nil {
		return dest, fmt.Errorf("已保存到 %s，但打开失败: %w", dest, err)
	}
	return dest, nil
}

// sessionArtifactRoots returns the directories inside a conversation session that
// may hold user-exposed artifacts. It is the single source of truth shared by
// OpenSessionArtifact and SaveSessionArtifact so the allow-list cannot drift.
func sessionArtifactRoots(sessionDir string) []string {
	return []string{
		filepath.Join(sessionDir, "artifacts"),
		filepath.Join(sessionDir, ".document-bridge", "objects"),
	}
}

func (a *App) authorizeDownloadPath(abs string, sessionID int64) error {
	session, ok, err := a.store.Store().SessionByID(sessionID)
	if err != nil {
		return err
	}
	if !ok || session.SessionDir == "" {
		return errors.New("conversation not found")
	}
	allowedRoots := sessionArtifactRoots(session.SessionDir)
	if changes, changeErr := readSessionChanges(session.SessionDir); changeErr == nil && changes.Root != "" {
		allowedRoots = append(allowedRoots, changes.Root)
	}
	if session.EnvironmentID != "" {
		if environments, environmentErr := a.store.Store().ListEnvironments(); environmentErr == nil {
			for _, environment := range environments {
				if environment.ID == session.EnvironmentID && environment.Path != "" {
					allowedRoots = append(allowedRoots, environment.Path)
					break
				}
			}
		}
	}
	for _, root := range allowedRoots {
		rootResolved, resolveErr := filepath.EvalSymlinks(root)
		if resolveErr == nil && withinPath(rootResolved, abs) {
			return nil
		}
	}
	return errors.New("path denied")
}

func withinPath(base, target string) bool {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}

func downloadsDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		if home, err = os.Getwd(); err != nil {
			return "."
		}
	}
	return filepath.Join(home, "Downloads")
}

func createUniqueDownload(dir, name string) (string, *os.File, error) {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 0; ; i++ {
		candidate := filepath.Join(dir, name)
		if i > 0 {
			candidate = filepath.Join(dir, fmt.Sprintf("%s(%d)%s", base, i, ext))
		}
		file, err := os.OpenFile(candidate, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			return candidate, file, nil
		}
		if !os.IsExist(err) {
			return "", nil, err
		}
	}
}
