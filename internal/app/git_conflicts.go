package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"codingto/internal/applog"
)

// GitConflictDetail contains the three versions shown by the conflict resolver.
type GitConflictDetail struct {
	Path           string         `json:"path"`
	ConflictStatus string         `json:"conflictStatus"`
	Kind           string         `json:"kind"`
	MimeType       string         `json:"mimeType,omitempty"`
	Ours           GitFileVersion `json:"ours"`
	Theirs         GitFileVersion `json:"theirs"`
	Result         GitFileVersion `json:"result"`
	ResultHash     string         `json:"resultHash"`
}

// ResolveGitConflictRequest resolves one still-conflicted path with a bounded result.
type ResolveGitConflictRequest struct {
	SessionID          int64  `json:"sessionId"`
	Path               string `json:"path"`
	ExpectedResultHash string `json:"expectedResultHash"`
	Resolution         string `json:"resolution"`
	ResultText         string `json:"resultText,omitempty"`
	Language           string `json:"language,omitempty"`
}

// GetSessionGitConflictDetail returns the index stage-2, editable worktree, and
// index stage-3 versions of one unmerged file.
func (a *App) GetSessionGitConflictDetail(id int64, requestedPath string) (GitConflictDetail, error) {
	workspace, err := a.sessionGitWorkspace(id)
	if err != nil {
		return GitConflictDetail{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitSnapshotTimeout)
	defer cancel()
	rootText, err := runGit(ctx, workspace, "rev-parse", "--show-toplevel")
	if err != nil {
		return GitConflictDetail{}, errors.New("workspace is not a Git repository")
	}
	root := filepath.Clean(strings.TrimSpace(rootText))
	paths, err := normalizeGitBatchPaths(root, []string{requestedPath})
	if err != nil {
		return GitConflictDetail{}, err
	}
	path := paths[0]
	conflicts, err := currentGitConflictMap(ctx, root)
	if err != nil {
		return GitConflictDetail{}, err
	}
	conflict, exists := conflicts[path]
	if !exists {
		return GitConflictDetail{}, errors.New("the selected file is no longer conflicted")
	}

	oursData, ours := readGitConflictStage(ctx, root, 2, path)
	theirsData, theirs := readGitConflictStage(ctx, root, 3, path)
	resultData, result, resultErr := readGitConflictWorktree(root, path)
	if resultErr != nil {
		return GitConflictDetail{}, resultErr
	}
	resultHash, err := gitConflictResultHash(root, path)
	if err != nil {
		return GitConflictDetail{}, err
	}

	mimeType := detectGitMime(path, resultData, oursData)
	if len(resultData) == 0 && len(oursData) == 0 {
		mimeType = detectGitMime(path, theirsData, nil)
	}
	kind := gitConflictPreviewKind(mimeType, oursData, theirsData, resultData, ours, theirs, result)
	applyGitConflictPreview(kind, mimeType, oursData, &ours)
	applyGitConflictPreview(kind, mimeType, theirsData, &theirs)
	applyGitConflictPreview(kind, mimeType, resultData, &result)

	return GitConflictDetail{
		Path: path, ConflictStatus: conflict.ConflictStatus, Kind: kind, MimeType: mimeType,
		Ours: ours, Theirs: theirs, Result: result, ResultHash: resultHash,
	}, nil
}

// ResolveSessionGitConflict writes or selects the final result and stages that
// exact path after revalidating both its conflict status and worktree hash.
func (a *App) ResolveSessionGitConflict(req ResolveGitConflictRequest) (GitOperationResult, error) {
	a.gitWriteMu.Lock()
	defer a.gitWriteMu.Unlock()

	workspace, err := a.sessionGitWorkspace(req.SessionID)
	if err != nil {
		return GitOperationResult{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitSnapshotTimeout)
	defer cancel()
	rootText, err := runGit(ctx, workspace, "rev-parse", "--show-toplevel")
	if err != nil {
		return GitOperationResult{}, gitLocalizedError(req.Language, "当前工作区不是 Git 仓库", "The current workspace is not a Git repository")
	}
	root := filepath.Clean(strings.TrimSpace(rootText))
	paths, err := normalizeGitBatchPaths(root, []string{req.Path})
	if err != nil {
		return GitOperationResult{}, gitLocalizedError(req.Language, "冲突文件路径不合法", "The conflict file path is invalid")
	}
	path := paths[0]
	conflicts, err := currentGitConflictMap(ctx, root)
	if err != nil {
		applog.Errorf("read Git conflict status before resolution failed: root=%q", root)
		return GitOperationResult{}, gitLocalizedError(req.Language, "读取冲突状态失败，请刷新后重试", "Failed to read the conflict state; refresh and try again")
	}
	conflict, exists := conflicts[path]
	if !exists {
		return GitOperationResult{}, gitLocalizedError(req.Language, "该文件已不再处于冲突状态，请刷新列表", "The file is no longer conflicted; refresh the list")
	}
	currentHash, err := gitConflictResultHash(root, path)
	if err != nil {
		applog.Errorf("hash Git conflict result failed: root=%q path=%q", root, path)
		return GitOperationResult{}, gitLocalizedError(req.Language, "读取最终结果失败，请刷新后重试", "Failed to read the current result; refresh and try again")
	}
	if strings.TrimSpace(req.ExpectedResultHash) == "" || currentHash != req.ExpectedResultHash {
		return GitOperationResult{}, gitLocalizedError(req.Language, "最终结果已被其他操作修改，请刷新后重新处理", "The final result changed; refresh before resolving it")
	}

	resolution := strings.TrimSpace(req.Resolution)
	if conflict.ConflictStatus == "DD" && resolution != "delete" {
		return GitOperationResult{}, gitLocalizedError(req.Language, "双方删除冲突只能确认删除结果", "A both-deleted conflict can only be resolved as deleted")
	}
	switch resolution {
	case "content":
		data := []byte(req.ResultText)
		if len(data) > maxGitPreviewSize || !utf8.Valid(data) || strings.IndexByte(req.ResultText, 0) >= 0 {
			return GitOperationResult{}, gitLocalizedError(req.Language, "最终结果不是可保存的文本或文件过大", "The final result is not valid editable text or is too large")
		}
		beforeData, beforeVersion, readErr := readGitConflictWorktree(root, path)
		if readErr != nil || beforeVersion.Size > maxGitPreviewSize {
			return GitOperationResult{}, gitLocalizedError(req.Language, "当前最终结果无法安全编辑，请刷新后重试", "The current result cannot be edited safely; refresh and try again")
		}
		if err = writeGitConflictResult(root, path, data); err == nil {
			_, err = runGit(ctx, root, "add", "--", path)
			if err != nil {
				rollbackGitConflictResult(root, path, beforeData, beforeVersion.Exists)
			}
		}
	case "ours", "theirs":
		stage := 2
		checkoutSide := "--ours"
		if resolution == "theirs" {
			stage = 3
			checkoutSide = "--theirs"
		}
		if _, version := readGitConflictStage(ctx, root, stage, path); !version.Exists {
			return GitOperationResult{}, gitLocalizedError(req.Language, "所选一侧不存在该文件，可选择删除结果", "That side does not contain the file; choose the deleted result instead")
		}
		if _, err = runGit(ctx, root, "checkout", checkoutSide, "--", path); err == nil {
			_, err = runGit(ctx, root, "add", "--", path)
		}
	case "delete":
		_, err = runGit(ctx, root, "rm", "--", path)
	default:
		return GitOperationResult{}, gitLocalizedError(req.Language, "不支持的冲突解决方式", "Unsupported conflict resolution")
	}
	if err != nil {
		applog.Errorf("resolve Git conflict failed: root=%q path=%q resolution=%q", root, path, resolution)
		return GitOperationResult{}, gitLocalizedError(req.Language, "保存冲突解决结果失败，请检查仓库状态", "Failed to save the conflict resolution; check the repository state")
	}
	return GitOperationResult{Message: gitLocalizedText(req.Language, "冲突已解决并暂存", "Conflict resolved and staged")}, nil
}

func currentGitConflictMap(ctx context.Context, root string) (map[string]GitFileChange, error) {
	statusBytes, err := runGitBytes(ctx, root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	conflicts := make(map[string]GitFileChange)
	for _, file := range parseGitStatus(string(statusBytes)) {
		if file.Conflicted {
			conflicts[file.Path] = file
		}
	}
	return conflicts, nil
}

func readGitConflictStage(ctx context.Context, root string, stage int, path string) ([]byte, GitFileVersion) {
	version := GitFileVersion{}
	spec := fmt.Sprintf(":%d:%s", stage, filepath.ToSlash(path))
	sizeText, err := runGit(ctx, root, "cat-file", "-s", spec)
	if err != nil {
		return nil, version
	}
	version.Exists = true
	version.Size, _ = strconv.ParseInt(strings.TrimSpace(sizeText), 10, 64)
	if version.Size > maxGitPreviewSize {
		return nil, version
	}
	data, err := runGitBytes(ctx, root, "show", spec)
	if err != nil {
		return nil, version
	}
	return data, version
}

func readGitConflictWorktree(root, path string) ([]byte, GitFileVersion, error) {
	absolute, err := safeGitWorktreePath(root, path)
	if err != nil {
		return nil, GitFileVersion{}, err
	}
	info, err := os.Lstat(absolute)
	if os.IsNotExist(err) {
		return nil, GitFileVersion{}, nil
	}
	if err != nil {
		return nil, GitFileVersion{}, err
	}
	version := GitFileVersion{
		Exists: true, Size: info.Size(), Permissions: info.Mode().String(),
		CreatedAt: fileCreatedAt(info), ModifiedAt: info.ModTime().UnixMilli(),
	}
	if !info.Mode().IsRegular() || info.Size() > maxGitPreviewSize {
		return nil, version, nil
	}
	data, err := os.ReadFile(absolute)
	if err != nil {
		return nil, GitFileVersion{}, err
	}
	return data, version, nil
}

func gitConflictPreviewKind(mimeType string, oursData, theirsData, resultData []byte, ours, theirs, result GitFileVersion) string {
	versions := []struct {
		data    []byte
		version GitFileVersion
	}{{oursData, ours}, {theirsData, theirs}, {resultData, result}}
	if strings.HasPrefix(mimeType, "image/") {
		for _, item := range versions {
			if item.version.Exists && item.version.Size > maxGitPreviewSize {
				return "binary"
			}
		}
		return "image"
	}
	for _, item := range versions {
		if !item.version.Exists {
			continue
		}
		if item.version.Size > maxGitPreviewSize || len(item.data) == 0 && item.version.Size > 0 ||
			!utf8.Valid(item.data) || strings.IndexByte(string(item.data), 0) >= 0 {
			return "binary"
		}
	}
	return "text"
}

func applyGitConflictPreview(kind, mimeType string, data []byte, version *GitFileVersion) {
	version.MimeType = mimeType
	if !version.Exists {
		return
	}
	if kind == "text" {
		version.Text = string(data)
		version.LineCount = countGitLines(version.Text)
		return
	}
	if strings.HasPrefix(mimeType, "image/") && len(data) > 0 {
		version.ImageData = encodeGitImage(mimeType, data)
		version.Width, version.Height = gitImageDimensions(data)
	}
}

func gitConflictResultHash(root, path string) (string, error) {
	absolute, err := safeGitWorktreePath(root, path)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	info, err := os.Lstat(absolute)
	if os.IsNotExist(err) {
		_, _ = hash.Write([]byte("missing\x00"))
		return hex.EncodeToString(hash.Sum(nil)), nil
	}
	if err != nil {
		return "", err
	}
	_, _ = hash.Write([]byte(info.Mode().String()))
	_, _ = hash.Write([]byte{0})
	if info.Mode()&os.ModeSymlink != 0 {
		target, readErr := os.Readlink(absolute)
		if readErr != nil {
			return "", readErr
		}
		_, _ = hash.Write([]byte(target))
		return hex.EncodeToString(hash.Sum(nil)), nil
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("conflict result is not a regular file")
	}
	file, err := os.Open(absolute)
	if err != nil {
		return "", err
	}
	_, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func writeGitConflictResult(root, path string, data []byte) error {
	absolute, err := safeGitWorktreePath(root, path)
	if err != nil {
		return err
	}
	parent := filepath.Dir(absolute)
	if !withinPath(root, parent) {
		return errors.New("Git conflict parent is outside the worktree")
	}
	if err := ensureGitConflictParent(root, parent); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if info, statErr := os.Lstat(absolute); statErr == nil {
		if !info.Mode().IsRegular() {
			return errors.New("Git conflict result is not a regular file")
		}
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	return os.WriteFile(absolute, data, mode)
}

func ensureGitConflictParent(root, parent string) error {
	relative, err := filepath.Rel(root, parent)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return errors.New("Git conflict parent is outside the worktree")
	}
	current := filepath.Clean(root)
	for _, part := range strings.Split(relative, string(os.PathSeparator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if os.IsNotExist(statErr) {
			if makeErr := os.Mkdir(current, 0o755); makeErr != nil {
				return makeErr
			}
			continue
		}
		if statErr != nil {
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("Git conflict parent is not a regular directory")
		}
	}
	return nil
}

func rollbackGitConflictResult(root, path string, before []byte, existed bool) {
	absolute, err := safeGitWorktreePath(root, path)
	if err != nil {
		return
	}
	if existed {
		err = writeGitConflictResult(root, path, before)
	} else {
		err = os.Remove(absolute)
		if os.IsNotExist(err) {
			err = nil
		}
	}
	if err != nil {
		applog.Errorf("rollback Git conflict result failed: root=%q path=%q", root, path)
	}
}

func gitLocalizedText(language, zh, en string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(language)), "zh") {
		return zh
	}
	return en
}
