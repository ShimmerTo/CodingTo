package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"codingto/internal/applog"
)

const gitSnapshotTimeout = 8 * time.Second
const maxGitPreviewSize = 4 * 1024 * 1024

// errGitWorkspaceNotFound signals that no Git working directory could be
// resolved for a conversation. It is returned when a new conversation has no
// session yet and no active workspace is configured; callers that render a
// snapshot treat it as a non-repository state instead of a hard failure.
var errGitWorkspaceNotFound = errors.New("conversation workspace not found")

// GitSnapshot keeps repository state separate from the per-prompt artifacts in
// SessionChanges. Worktree is the live checkout versus HEAD; Branch is the
// committed current branch versus the automatically detected base branch.
type GitSnapshot struct {
	IsRepository  bool         `json:"isRepository"`
	Root          string       `json:"root,omitempty"`
	WorktreePath  string       `json:"worktreePath,omitempty"`
	CurrentBranch string       `json:"currentBranch,omitempty"`
	BaseBranch    string       `json:"baseBranch,omitempty"`
	BaseBranches  []string     `json:"baseBranches"`
	Ahead         int          `json:"ahead"`
	Behind        int          `json:"behind"`
	Worktree      GitChangeSet `json:"worktree"`
	Branch        GitChangeSet `json:"branch"`
	Error         string       `json:"error,omitempty"`
	// CompareLeft and CompareRight are the two refs used by the "compare"
	// scope; they are never serialized to the frontend.
	CompareLeft  string `json:"-"`
	CompareRight string `json:"-"`
}

// GitBranchCompareRequest selects two refs to compare. Empty refs fall back to
// the current branch (left) and the automatically detected base branch (right).
type GitBranchCompareRequest struct {
	SessionID int64  `json:"sessionId"`
	Left      string `json:"left"`
	Right     string `json:"right"`
	Language  string `json:"language,omitempty"`
}

// GitBranchCompareResult is the two-sided diff between two branches.
type GitBranchCompareResult struct {
	IsRepository bool            `json:"isRepository"`
	Root         string          `json:"root,omitempty"`
	Left         string          `json:"left"`
	Right        string          `json:"right"`
	Ahead        int             `json:"ahead"`
	Behind       int             `json:"behind"`
	Files        []GitFileChange `json:"files"`
	Added        int             `json:"added"`
	Deleted      int             `json:"deleted"`
}

// GitCompareFileDetailRequest selects one file between two refs.
type GitCompareFileDetailRequest struct {
	SessionID int64  `json:"sessionId"`
	Left      string `json:"left"`
	Right     string `json:"right"`
	Path      string `json:"path"`
	Language  string `json:"language,omitempty"`
}

type GitChangeSet struct {
	Files   []GitFileChange `json:"files"`
	Added   int             `json:"added"`
	Deleted int             `json:"deleted"`
}

type GitFileChange struct {
	Path    string `json:"path"`
	OldPath string `json:"oldPath,omitempty"`
	Status  string `json:"status"`
	// ConflictStatus is the two-character porcelain v1 unmerged status.
	ConflictStatus string `json:"conflictStatus,omitempty"`
	Staged         bool   `json:"staged,omitempty"`
	Unstaged       bool   `json:"unstaged,omitempty"`
	Untracked      bool   `json:"untracked,omitempty"`
	Conflicted     bool   `json:"conflicted,omitempty"`
	Ignored        bool   `json:"ignored,omitempty"`
	Added          int    `json:"added"`
	Deleted        int    `json:"deleted"`
	// StagedAdded and StagedDeleted count only HEAD-to-index line changes.
	StagedAdded   int `json:"stagedAdded"`
	StagedDeleted int `json:"stagedDeleted"`
	// UnstagedAdded and UnstagedDeleted count only index-to-worktree line changes.
	UnstagedAdded   int  `json:"unstagedAdded"`
	UnstagedDeleted int  `json:"unstagedDeleted"`
	Binary          bool `json:"binary"`
}

type GitFileDetail struct {
	Path     string         `json:"path"`
	OldPath  string         `json:"oldPath,omitempty"`
	Scope    string         `json:"scope"`
	Status   string         `json:"status"`
	Kind     string         `json:"kind"`
	MimeType string         `json:"mimeType,omitempty"`
	Before   GitFileVersion `json:"before"`
	After    GitFileVersion `json:"after"`
	Hunks    []DiffHunk     `json:"hunks"`
	Added    int            `json:"added"`
	Deleted  int            `json:"deleted"`
}

// GitCommitFileDetailRequest selects one file from one immutable commit for comparison with its first parent.
type GitCommitFileDetailRequest struct {
	SessionID int64  `json:"sessionId"`
	Commit    string `json:"commit"`
	Path      string `json:"path"`
	Language  string `json:"language,omitempty"`
}

type GitFileVersion struct {
	Exists      bool   `json:"exists"`
	Size        int64  `json:"size"`
	Permissions string `json:"permissions,omitempty"`
	CreatedAt   int64  `json:"createdAt,omitempty"`
	ModifiedAt  int64  `json:"modifiedAt,omitempty"`
	LineCount   int    `json:"lineCount,omitempty"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
	ImageData   string `json:"imageData,omitempty"`
	Text        string `json:"text,omitempty"`
}

func readGitSnapshot(workspace, preferredBase string) GitSnapshot {
	ctx, cancel := context.WithTimeout(context.Background(), gitSnapshotTimeout)
	defer cancel()

	snapshot := GitSnapshot{
		BaseBranches: []string{},
		Worktree:     GitChangeSet{Files: []GitFileChange{}},
		Branch:       GitChangeSet{Files: []GitFileChange{}},
	}
	root, err := runGit(ctx, workspace, "rev-parse", "--show-toplevel")
	if err != nil {
		snapshot.Error = strings.TrimSpace(err.Error())
		return snapshot
	}
	snapshot.IsRepository = true
	snapshot.Root = filepath.Clean(strings.TrimSpace(root))
	snapshot.WorktreePath = snapshot.Root

	branch, _ := runGit(ctx, snapshot.Root, "branch", "--show-current")
	snapshot.CurrentBranch = strings.TrimSpace(branch)
	if snapshot.CurrentBranch == "" {
		shortSHA, _ := runGit(ctx, snapshot.Root, "rev-parse", "--short", "HEAD")
		snapshot.CurrentBranch = strings.TrimSpace(shortSHA)
	}

	statusBytes, statusErr := runGitBytes(ctx, snapshot.Root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if statusErr == nil {
		snapshot.Worktree.Files = parseGitStatus(string(statusBytes))
		markGitIgnoredFiles(ctx, snapshot.Root, snapshot.Worktree.Files)
		if numstat, diffErr := runGitBytes(ctx, snapshot.Root, "diff", "--numstat", "-z", "--no-renames", "HEAD", "--"); diffErr == nil {
			applyGitNumstat(&snapshot.Worktree, string(numstat))
		}
		if stagedNumstat, diffErr := runGitBytes(ctx, snapshot.Root, "diff", "--cached", "--numstat", "-z", "--no-renames", "--"); diffErr == nil {
			applyGitScopedNumstat(&snapshot.Worktree, string(stagedNumstat), true)
		}
		if unstagedNumstat, diffErr := runGitBytes(ctx, snapshot.Root, "diff", "--numstat", "-z", "--no-renames", "--"); diffErr == nil {
			applyGitScopedNumstat(&snapshot.Worktree, string(unstagedNumstat), false)
		}
	}

	if _, headErr := runGit(ctx, snapshot.Root, "rev-parse", "--verify", "HEAD"); headErr != nil {
		return snapshot
	}
	snapshot.BaseBranches = listGitBaseBranches(ctx, snapshot.Root)
	snapshot.BaseBranch = detectGitBaseBranch(ctx, snapshot.Root, snapshot.CurrentBranch, preferredBase, snapshot.BaseBranches)
	if snapshot.BaseBranch == "" {
		return snapshot
	}

	mergeBase, mergeErr := runGit(ctx, snapshot.Root, "merge-base", snapshot.BaseBranch, "HEAD")
	if mergeErr != nil || strings.TrimSpace(mergeBase) == "" {
		return snapshot
	}
	rangeSpec := strings.TrimSpace(mergeBase) + "..HEAD"
	names, namesErr := runGitBytes(ctx, snapshot.Root, "diff", "--name-status", "-z", "--no-renames", rangeSpec, "--")
	if namesErr == nil {
		snapshot.Branch.Files = parseGitNameStatus(string(names))
	}
	if numstat, diffErr := runGitBytes(ctx, snapshot.Root, "diff", "--numstat", "-z", "--no-renames", rangeSpec, "--"); diffErr == nil {
		applyGitNumstat(&snapshot.Branch, string(numstat))
	}
	if counts, countErr := runGit(ctx, snapshot.Root, "rev-list", "--left-right", "--count", snapshot.BaseBranch+"...HEAD"); countErr == nil {
		fields := strings.Fields(counts)
		if len(fields) == 2 {
			snapshot.Behind, _ = strconv.Atoi(fields[0])
			snapshot.Ahead, _ = strconv.Atoi(fields[1])
		}
	}
	return snapshot
}

// readGitWorktreeSnapshot reads only the live checkout state: repository
// location, current branch and the status/numstat of the worktree versus HEAD.
// It never queries refs or history, so ordinary file events can refresh the
// worktree view without re-reading branches, remotes and commits.
func readGitWorktreeSnapshot(workspace string) (GitSnapshot, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), gitSnapshotTimeout)
	defer cancel()

	snapshot := GitSnapshot{
		BaseBranches: []string{},
		Worktree:     GitChangeSet{Files: []GitFileChange{}},
		Branch:       GitChangeSet{Files: []GitFileChange{}},
	}
	root, err := runGit(ctx, workspace, "rev-parse", "--show-toplevel")
	if err != nil {
		snapshot.Error = strings.TrimSpace(err.Error())
		return snapshot, false
	}
	snapshot.IsRepository = true
	snapshot.Root = filepath.Clean(strings.TrimSpace(root))
	snapshot.WorktreePath = snapshot.Root

	branch, _ := runGit(ctx, snapshot.Root, "branch", "--show-current")
	snapshot.CurrentBranch = strings.TrimSpace(branch)
	if snapshot.CurrentBranch == "" {
		shortSHA, _ := runGit(ctx, snapshot.Root, "rev-parse", "--short", "HEAD")
		snapshot.CurrentBranch = strings.TrimSpace(shortSHA)
	}

	statusBytes, statusErr := runGitBytes(ctx, snapshot.Root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if statusErr != nil {
		return snapshot, false
	}
	snapshot.Worktree.Files = parseGitStatus(string(statusBytes))
	markGitIgnoredFiles(ctx, snapshot.Root, snapshot.Worktree.Files)
	if numstat, diffErr := runGitBytes(ctx, snapshot.Root, "diff", "--numstat", "-z", "--no-renames", "HEAD", "--"); diffErr == nil {
		applyGitNumstat(&snapshot.Worktree, string(numstat))
	}
	if stagedNumstat, diffErr := runGitBytes(ctx, snapshot.Root, "diff", "--cached", "--numstat", "-z", "--no-renames", "--"); diffErr == nil {
		applyGitScopedNumstat(&snapshot.Worktree, string(stagedNumstat), true)
	}
	if unstagedNumstat, diffErr := runGitBytes(ctx, snapshot.Root, "diff", "--numstat", "-z", "--no-renames", "--"); diffErr == nil {
		applyGitScopedNumstat(&snapshot.Worktree, string(unstagedNumstat), false)
	}
	return snapshot, true
}

// markGitIgnoredFiles annotates worktree changes that match an ignore rule,
// including staged index deletions created by git rm --cached.
func markGitIgnoredFiles(ctx context.Context, root string, files []GitFileChange) {
	if len(files) == 0 {
		return
	}
	paths := make([]string, 0, len(files))
	for _, file := range files {
		if file.Path != "" {
			paths = append(paths, file.Path)
		}
	}
	if len(paths) == 0 {
		return
	}
	output, err := runGitInput(ctx, root, strings.Join(paths, "\x00")+"\x00", "check-ignore", "--no-index", "-z", "--stdin")
	if err != nil {
		return
	}
	ignored := make(map[string]struct{})
	for _, path := range strings.Split(output, "\x00") {
		path = filepath.ToSlash(path)
		if path != "" {
			ignored[path] = struct{}{}
		}
	}
	for index := range files {
		_, files[index].Ignored = ignored[files[index].Path]
	}
}

func (a *App) GetSessionGitSnapshot(id int64, baseBranch string) (GitSnapshot, error) {
	workspace, err := a.sessionGitWorkspace(id)
	if err != nil {
		if errors.Is(err, errGitWorkspaceNotFound) {
			applog.Infof("git snapshot workspace unavailable for session %d: %v", id, err)
			return GitSnapshot{BaseBranches: []string{}, Worktree: GitChangeSet{Files: []GitFileChange{}}, Branch: GitChangeSet{Files: []GitFileChange{}}}, nil
		}
		return GitSnapshot{}, err
	}
	return readGitSnapshot(workspace, strings.TrimSpace(baseBranch)), nil
}

// GitFileOperationRequest describes a file-level Git action applied to the
// session worktree. Op is one of:
//   - track/stage: git add the file (track an untracked file or stage worktree changes)
//   - unstage:     git restore --staged the file (drop the staged entry)
//   - delete_untracked: delete the file only while it is still untracked
//   - discard_tracked: restore the file only while it is still tracked
//   - restore:     git restore the file (recover a deleted file from HEAD)
//   - ignore:      add the path to the root .gitignore and stop tracking it
type GitFileOperationRequest struct {
	SessionID   int64  `json:"sessionId"`
	Op          string `json:"op"`
	Path        string `json:"path"`
	IsDirectory bool   `json:"isDirectory,omitempty"`
}

// GitFileOperationsRequest describes one file-level Git action applied to a
// bounded set of paths in the session worktree.
type GitFileOperationsRequest struct {
	SessionID int64    `json:"sessionId"`
	Op        string   `json:"op"`
	Paths     []string `json:"paths"`
}

// ApplyGitFileOperation applies one validated, serialized file or directory operation.
func (a *App) ApplyGitFileOperation(req GitFileOperationRequest) error {
	a.gitWriteMu.Lock()
	defer a.gitWriteMu.Unlock()

	workspace, err := a.sessionGitWorkspace(req.SessionID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitSnapshotTimeout)
	defer cancel()

	rootText, err := runGit(ctx, workspace, "rev-parse", "--show-toplevel")
	if err != nil {
		return errors.New("workspace is not a Git repository")
	}
	root := filepath.Clean(strings.TrimSpace(rootText))
	if strings.TrimSpace(req.Op) == "ignore" {
		if err := addPathToGitIgnore(ctx, root, req.Path, req.IsDirectory); err != nil {
			applog.Errorf("add Git ignore path failed: root=%q path=%q directory=%t: %v", root, req.Path, req.IsDirectory, err)
			return errors.New("failed to add the selected item to the Git ignore list")
		}
	} else if err := applyGitFileOperation(ctx, root, req.Op, req.Path); err != nil {
		return err
	}
	if a.gitMonitor != nil {
		a.gitMonitor.RefreshNow(workspace, false)
	}
	return nil
}

// ApplyGitFileOperations applies one validated Git action to multiple paths.
func (a *App) ApplyGitFileOperations(req GitFileOperationsRequest) error {
	a.gitWriteMu.Lock()
	defer a.gitWriteMu.Unlock()

	workspace, err := a.sessionGitWorkspace(req.SessionID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitSnapshotTimeout)
	defer cancel()

	rootText, err := runGit(ctx, workspace, "rev-parse", "--show-toplevel")
	if err != nil {
		return errors.New("workspace is not a Git repository")
	}
	root := filepath.Clean(strings.TrimSpace(rootText))
	if err := applyGitFileOperations(ctx, root, req.Op, req.Paths); err != nil {
		return err
	}
	if a.gitMonitor != nil {
		a.gitMonitor.RefreshNow(workspace, false)
	}
	return nil
}

// addPathToGitIgnore adds one repository-relative path to the root ignore file.
// If the path is already in the index, git rm --cached removes only its tracked
// state and deliberately leaves every worktree file untouched.
func addPathToGitIgnore(ctx context.Context, root, requestedPath string, isDirectory bool) error {
	if strings.ContainsAny(requestedPath, "\x00\r\n") {
		return errors.New("invalid Git ignore path")
	}
	paths, err := normalizeGitBatchPaths(root, []string{requestedPath})
	if err != nil {
		return err
	}
	path := paths[0]
	absolute, err := safeGitWorktreePath(root, path)
	if err != nil {
		return err
	}
	if info, statErr := os.Stat(absolute); statErr == nil {
		isDirectory = info.IsDir()
	} else if !os.IsNotExist(statErr) {
		return statErr
	}

	trackedPaths, err := gitTrackedPathSet(ctx, root, []string{path})
	if err != nil {
		return err
	}

	ignorePath := filepath.Join(root, ".gitignore")
	before, mode, existed, err := readWritableGitIgnore(ignorePath)
	if err != nil {
		return err
	}
	pattern := rootGitIgnorePattern(path, isDirectory)
	after, changed := appendGitIgnorePattern(before, pattern)
	if changed {
		if err := writeAppendedGitIgnore(ignorePath, before, after, mode, existed); err != nil {
			return err
		}
	}

	if len(trackedPaths) == 0 {
		return nil
	}
	if _, err := runGit(ctx, root, "rm", "-r", "--cached", "--force", "--ignore-unmatch", "--", path); err != nil {
		if changed {
			rollbackGitIgnore(ignorePath, before, after, mode, existed)
		}
		return err
	}
	return nil
}

func readWritableGitIgnore(path string) ([]byte, os.FileMode, bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return []byte{}, 0o644, false, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, 0, false, errors.New("repository .gitignore is not a regular file")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, false, err
	}
	return contents, info.Mode().Perm(), true, nil
}

func writeAppendedGitIgnore(path string, before, after []byte, mode os.FileMode, existed bool) error {
	current, err := os.ReadFile(path)
	if !existed && os.IsNotExist(err) {
		current, err = []byte{}, nil
	}
	if err != nil {
		return err
	}
	if !bytes.Equal(current, before) || len(after) < len(before) {
		return errors.New("repository .gitignore changed during the operation")
	}
	flags := os.O_WRONLY | os.O_APPEND
	if !existed {
		flags |= os.O_CREATE | os.O_EXCL
	}
	file, err := os.OpenFile(path, flags, mode)
	if err != nil {
		return err
	}
	suffix := after[len(before):]
	written, writeErr := file.Write(suffix)
	closeErr := file.Close()
	if writeErr == nil && written != len(suffix) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		rollbackPartialGitIgnore(path, before, after, mode, existed)
		return writeErr
	}
	return nil
}

func rootGitIgnorePattern(path string, isDirectory bool) string {
	var pattern strings.Builder
	pattern.WriteByte('/')
	for _, char := range filepath.ToSlash(path) {
		switch char {
		case '\\', '!', '#', '*', '?', '[', ']', ' ':
			pattern.WriteByte('\\')
		}
		pattern.WriteRune(char)
	}
	if isDirectory {
		pattern.WriteByte('/')
	}
	return pattern.String()
}

func appendGitIgnorePattern(contents []byte, pattern string) ([]byte, bool) {
	normalized := strings.ReplaceAll(string(contents), "\r\n", "\n")
	for _, line := range strings.Split(normalized, "\n") {
		if line == pattern {
			return contents, false
		}
	}
	lineEnding := "\n"
	if bytes.Contains(contents, []byte("\r\n")) {
		lineEnding = "\r\n"
	}
	result := append([]byte(nil), contents...)
	if len(result) > 0 && !bytes.HasSuffix(result, []byte("\n")) && !bytes.HasSuffix(result, []byte("\r")) {
		result = append(result, lineEnding...)
	}
	result = append(result, pattern...)
	result = append(result, lineEnding...)
	return result, true
}

func rollbackGitIgnore(path string, before, expected []byte, mode os.FileMode, existed bool) {
	current, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(current, expected) {
		applog.Errorf("rollback Git ignore skipped because the file changed: path=%q", path)
		return
	}
	if existed {
		err = os.WriteFile(path, before, mode)
	} else {
		err = os.Remove(path)
	}
	if err != nil {
		applog.Errorf("rollback Git ignore failed: path=%q: %v", path, err)
	}
}

func rollbackPartialGitIgnore(path string, before, expected []byte, mode os.FileMode, existed bool) {
	current, err := os.ReadFile(path)
	if err != nil || len(current) < len(before) || len(current) > len(expected) || !bytes.Equal(current, expected[:len(current)]) {
		applog.Errorf("rollback partial Git ignore append skipped because the file changed: path=%q", path)
		return
	}
	if existed {
		err = os.WriteFile(path, before, mode)
	} else {
		err = os.Remove(path)
	}
	if err != nil {
		applog.Errorf("rollback partial Git ignore append failed: path=%q: %v", path, err)
	}
}

// applyGitFileOperation applies one validated file-level operation inside root.
func applyGitFileOperation(ctx context.Context, root, op, requestedPath string) error {
	switch strings.TrimSpace(op) {
	case "track":
		return applyGitFileOperations(ctx, root, "stage", []string{requestedPath})
	case "stage", "unstage", "delete_untracked", "discard_tracked", "resolve_both_deleted":
		return applyGitFileOperations(ctx, root, op, []string{requestedPath})
	case "restore":
		paths, err := normalizeGitBatchPaths(root, []string{requestedPath})
		if err != nil {
			return err
		}
		path := paths[0]
		if _, err := runGit(ctx, root, "restore", "--", path); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported Git file operation: %s", op)
	}
	return nil
}

const maxGitBatchFiles = 200
const maxGitBatchPathBytes = 24 * 1024

// applyGitFileOperations validates every path and expected tracking state
// before performing a bounded batch operation.
func applyGitFileOperations(ctx context.Context, root, op string, requestedPaths []string) error {
	paths, err := normalizeGitBatchPaths(root, requestedPaths)
	if err != nil {
		return err
	}

	switch strings.TrimSpace(op) {
	case "stage":
		args := append([]string{"add", "--"}, paths...)
		_, err = runGit(ctx, root, args...)
		return err
	case "unstage":
		if _, headErr := runGit(ctx, root, "rev-parse", "--verify", "HEAD"); headErr == nil {
			args := append([]string{"reset", "-q", "HEAD", "--"}, paths...)
			_, err = runGit(ctx, root, args...)
			return err
		}
		args := append([]string{"rm", "--cached", "--ignore-unmatch", "--"}, paths...)
		_, err = runGit(ctx, root, args...)
		return err
	case "discard_tracked":
		untrackedPaths, statusErr := gitUntrackedPathSet(ctx, root)
		if statusErr != nil {
			return statusErr
		}
		trackedPaths, trackedErr := gitTrackedPathSet(ctx, root, paths)
		if trackedErr != nil {
			return trackedErr
		}
		for _, path := range paths {
			_, untracked := untrackedPaths[path]
			_, tracked := trackedPaths[path]
			if untracked || !tracked {
				return fmt.Errorf("file is no longer tracked: %s", path)
			}
		}
		args := append([]string{"restore", "--"}, paths...)
		_, err = runGit(ctx, root, args...)
		return err
	case "delete_untracked":
		untrackedPaths, statusErr := gitUntrackedPathSet(ctx, root)
		if statusErr != nil {
			return statusErr
		}
		absolutePaths := make([]string, 0, len(paths))
		for _, path := range paths {
			_, untracked := untrackedPaths[path]
			if !untracked {
				return fmt.Errorf("file is no longer untracked: %s", path)
			}
			absolute, pathErr := safeGitWorktreePath(root, path)
			if pathErr != nil {
				return pathErr
			}
			info, statErr := os.Lstat(absolute)
			if statErr != nil {
				return statErr
			}
			if info.IsDir() {
				return fmt.Errorf("refusing to delete an untracked directory: %s", path)
			}
			absolutePaths = append(absolutePaths, absolute)
		}
		for _, absolute := range absolutePaths {
			if err := os.Remove(absolute); err != nil {
				return err
			}
		}
		return nil
	case "resolve_both_deleted":
		conflicts, statusErr := currentGitConflictMap(ctx, root)
		if statusErr != nil {
			return statusErr
		}
		for _, path := range paths {
			if conflict, exists := conflicts[path]; !exists || conflict.ConflictStatus != "DD" {
				return fmt.Errorf("selected file is no longer a both-deleted conflict: %s", path)
			}
		}
		args := append([]string{"rm", "--"}, paths...)
		_, err = runGit(ctx, root, args...)
		return err
	default:
		return fmt.Errorf("unsupported Git batch operation: %s", op)
	}
}

func normalizeGitBatchPaths(root string, requestedPaths []string) ([]string, error) {
	if len(requestedPaths) == 0 || len(requestedPaths) > maxGitBatchFiles {
		return nil, fmt.Errorf("Git batch operation requires 1 to %d files", maxGitBatchFiles)
	}
	paths := make([]string, 0, len(requestedPaths))
	seen := make(map[string]struct{}, len(requestedPaths))
	totalBytes := 0
	for _, requestedPath := range requestedPaths {
		path := filepath.ToSlash(strings.TrimSpace(requestedPath))
		if path == "" || path == "." || path == ".." {
			return nil, errors.New("invalid Git file path")
		}
		if _, pathErr := safeGitWorktreePath(root, path); pathErr != nil {
			return nil, pathErr
		}
		if _, exists := seen[path]; exists {
			continue
		}
		totalBytes += len(path)
		if totalBytes > maxGitBatchPathBytes {
			return nil, errors.New("Git batch file paths are too long")
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	if len(paths) == 0 {
		return nil, errors.New("Git batch operation has no unique files")
	}
	return paths, nil
}

// isGitUntracked reports whether the porcelain status marks path as untracked,
// which lets destructive operations enforce their expected tracking state.
func isGitUntracked(ctx context.Context, root, path string) (bool, error) {
	paths, err := gitUntrackedPathSet(ctx, root)
	if err != nil {
		return false, err
	}
	_, ok := paths[path]
	return ok, nil
}

func gitUntrackedPathSet(ctx context.Context, root string) (map[string]struct{}, error) {
	statusBytes, err := runGitBytes(ctx, root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	paths := make(map[string]struct{})
	for _, file := range parseGitStatus(string(statusBytes)) {
		if file.Untracked {
			paths[file.Path] = struct{}{}
		}
	}
	return paths, nil
}

func gitTrackedPathSet(ctx context.Context, root string, paths []string) (map[string]struct{}, error) {
	args := append([]string{"ls-files", "-z", "--"}, paths...)
	output, err := runGitBytes(ctx, root, args...)
	if err != nil {
		return nil, err
	}
	tracked := make(map[string]struct{}, len(paths))
	for _, path := range strings.Split(string(output), "\x00") {
		path = filepath.ToSlash(strings.TrimSpace(path))
		if path != "" {
			tracked[path] = struct{}{}
		}
	}
	return tracked, nil
}

func (a *App) GetSessionGitFileDetail(id int64, scope, path, baseBranch string) (GitFileDetail, error) {
	workspace, err := a.sessionGitWorkspace(id)
	if err != nil {
		return GitFileDetail{}, err
	}
	scope = strings.TrimSpace(scope)
	baseBranch = strings.TrimSpace(baseBranch)
	if a.gitMonitor != nil && scope != "branch" {
		if detail, detailErr, ready := a.gitMonitor.FileDetail(workspace, scope, path, baseBranch); ready {
			return detail, detailErr
		}
		return GitFileDetail{}, errors.New("Git repository cache is still warming")
	}
	snapshot := readGitSnapshot(workspace, strings.TrimSpace(baseBranch))
	if !snapshot.IsRepository {
		return GitFileDetail{}, errors.New("workspace is not a Git repository")
	}
	var set GitChangeSet
	switch scope {
	case "worktree", "staged", "unstaged", "untracked":
		set = snapshot.Worktree
	case "branch":
		set = snapshot.Branch
	default:
		return GitFileDetail{}, fmt.Errorf("invalid Git comparison scope: %s", scope)
	}
	var change *GitFileChange
	for index := range set.Files {
		if set.Files[index].Path == filepath.ToSlash(path) {
			change = &set.Files[index]
			break
		}
	}
	if change == nil {
		return GitFileDetail{}, fmt.Errorf("Git change not found: %s", path)
	}
	if scope == "staged" && (!change.Staged || change.Conflicted) {
		return GitFileDetail{}, fmt.Errorf("staged Git change not found: %s", path)
	}
	if scope == "unstaged" && (!change.Unstaged || change.Untracked || change.Conflicted) {
		return GitFileDetail{}, fmt.Errorf("unstaged Git change not found: %s", path)
	}
	if scope == "untracked" && (!change.Untracked || change.Conflicted) {
		return GitFileDetail{}, fmt.Errorf("untracked Git change not found: %s", path)
	}
	return readGitFileDetail(snapshot, scope, *change)
}

func readGitFileDetailFromRepository(view GitRepositoryView, scope, path, baseBranch string) (GitFileDetail, error) {
	if !view.IsRepository || strings.TrimSpace(view.Root) == "" {
		return GitFileDetail{}, errors.New("workspace is not a Git repository")
	}
	scope = strings.TrimSpace(scope)
	if scope != "worktree" && scope != "staged" && scope != "unstaged" && scope != "untracked" {
		return GitFileDetail{}, fmt.Errorf("invalid Git comparison scope: %s", scope)
	}
	path = filepath.ToSlash(path)
	var change *GitFileChange
	for index := range view.Worktree.Files {
		if view.Worktree.Files[index].Path == path {
			change = &view.Worktree.Files[index]
			break
		}
	}
	if change == nil {
		return GitFileDetail{}, fmt.Errorf("Git change not found: %s", path)
	}
	if scope == "staged" && (!change.Staged || change.Conflicted) {
		return GitFileDetail{}, fmt.Errorf("staged Git change not found: %s", path)
	}
	if scope == "unstaged" && (!change.Unstaged || change.Untracked || change.Conflicted) {
		return GitFileDetail{}, fmt.Errorf("unstaged Git change not found: %s", path)
	}
	if scope == "untracked" && (!change.Untracked || change.Conflicted) {
		return GitFileDetail{}, fmt.Errorf("untracked Git change not found: %s", path)
	}
	snapshot := GitSnapshot{
		IsRepository:  true,
		Root:          view.Root,
		WorktreePath:  view.WorktreePath,
		CurrentBranch: view.CurrentBranch,
		BaseBranch:    strings.TrimSpace(baseBranch),
		BaseBranches:  []string{},
		Worktree:      view.Worktree,
		Branch:        GitChangeSet{Files: []GitFileChange{}},
	}
	return readGitFileDetail(snapshot, scope, *change)
}

// GetSessionGitCommitFileDetail returns the selected commit's file content compared with its first parent.
func (a *App) GetSessionGitCommitFileDetail(req GitCommitFileDetailRequest) (GitFileDetail, error) {
	workspace, err := a.sessionGitWorkspace(req.SessionID)
	if err != nil {
		return GitFileDetail{}, err
	}
	commit := strings.TrimSpace(req.Commit)
	if !validGitCommitHash(commit) {
		return GitFileDetail{}, gitLocalizedError(req.Language, "提交标识不合法", "The Git commit is invalid")
	}
	if a.gitMonitor != nil {
		detail, detailErr, ready := a.gitMonitor.CommitFileDetail(workspace, commit, req.Path, func(root, path string) (GitFileDetail, error) {
			return readGitCommitFileDetailRequest(root, commit, path, req.Language)
		})
		if ready {
			return detail, detailErr
		}
		return GitFileDetail{}, gitLocalizedError(req.Language, "Git 仓库缓存仍在预热", "The Git repository cache is still warming")
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitSnapshotTimeout)
	defer cancel()
	rootText, err := runGit(ctx, workspace, "rev-parse", "--show-toplevel")
	if err != nil {
		return GitFileDetail{}, gitLocalizedError(req.Language, "当前工作区不是 Git 仓库", "The current workspace is not a Git repository")
	}
	return readGitCommitFileDetailRequest(filepath.Clean(strings.TrimSpace(rootText)), commit, req.Path, req.Language)
}

func readGitCommitFileDetailRequest(root, commit, requestedPath, language string) (GitFileDetail, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitSnapshotTimeout)
	defer cancel()
	resolved := strings.TrimSpace(gitTrimmed(ctx, root, "rev-parse", "--verify", commit+"^{commit}"))
	if resolved == "" || !strings.EqualFold(resolved, commit) {
		return GitFileDetail{}, gitLocalizedError(language, "找不到该提交", "The Git commit was not found")
	}
	statusRaw, err := runGit(ctx, root, "diff-tree", "--root", "--no-commit-id", "-r", "--name-status", "-z", "--no-renames", commit, "--")
	if err != nil {
		return GitFileDetail{}, gitLocalizedError(language, "读取该提交的文件变更失败", "Failed to read the Git commit changes")
	}
	path := filepath.ToSlash(requestedPath)
	var change *GitFileChange
	for _, item := range parseGitNameStatus(statusRaw) {
		if item.Path == path {
			copy := item
			change = &copy
			break
		}
	}
	if change == nil {
		return GitFileDetail{}, gitLocalizedError(language, "该文件不在此提交的变更中", "The file is not part of this Git commit")
	}
	return readGitCommitFileDetail(ctx, root, commit, *change)
}

// CompareSessionGitBranches returns the two-sided diff between two refs.
// Empty refs default to the current branch (left) and the automatically
// detected base branch (right), so a freshly opened compare tab works without
// any selection. The diff direction is left → right: status added means the
// file exists only on the right side, deleted means only on the left.
func (a *App) CompareSessionGitBranches(req GitBranchCompareRequest) (GitBranchCompareResult, error) {
	workspace, err := a.sessionGitWorkspace(req.SessionID)
	if err != nil {
		return GitBranchCompareResult{}, gitLocalizedError(req.Language, "当前工作区不是 Git 仓库", "The current workspace is not a Git repository")
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitSnapshotTimeout)
	defer cancel()
	rootText, err := runGit(ctx, workspace, "rev-parse", "--show-toplevel")
	if err != nil {
		return GitBranchCompareResult{}, gitLocalizedError(req.Language, "当前工作区不是 Git 仓库", "The current workspace is not a Git repository")
	}
	root := filepath.Clean(strings.TrimSpace(rootText))
	left := strings.TrimSpace(req.Left)
	right := strings.TrimSpace(req.Right)
	if left == "" {
		left = gitCurrentBranchRef(ctx, root)
	}
	if right == "" {
		right = detectGitBaseBranch(ctx, root, left, "", listGitBaseBranches(ctx, root))
	}
	if left == "" || right == "" {
		return GitBranchCompareResult{}, gitLocalizedError(req.Language, "未找到可对比的分支", "No branch is available to compare")
	}
	if err := verifyGitCommitRefs(ctx, root, left, right); err != nil {
		return GitBranchCompareResult{}, gitLocalizedError(req.Language, "对比的分支不存在", "The branch to compare does not exist")
	}
	result := GitBranchCompareResult{
		IsRepository: true,
		Root:         root,
		Left:         left,
		Right:        right,
		Files:        []GitFileChange{},
	}
	if names, namesErr := runGitBytes(ctx, root, "diff", "--name-status", "-z", "--no-renames", left, right, "--"); namesErr == nil {
		result.Files = parseGitNameStatus(string(names))
	}
	set := GitChangeSet{Files: result.Files}
	if numstat, numstatErr := runGitBytes(ctx, root, "diff", "--numstat", "-z", "--no-renames", left, right, "--"); numstatErr == nil {
		applyGitNumstat(&set, string(numstat))
	}
	result.Files, result.Added, result.Deleted = set.Files, set.Added, set.Deleted
	if counts, countErr := runGit(ctx, root, "rev-list", "--left-right", "--count", left+"..."+right); countErr == nil {
		fields := strings.Fields(counts)
		if len(fields) == 2 {
			result.Ahead, _ = strconv.Atoi(fields[0])
			result.Behind, _ = strconv.Atoi(fields[1])
		}
	}
	return result, nil
}

// GetSessionGitCompareFileDetail returns one file's before/after versions and
// diff between two refs, reusing the standard text/image/binary rendering.
func (a *App) GetSessionGitCompareFileDetail(req GitCompareFileDetailRequest) (GitFileDetail, error) {
	workspace, err := a.sessionGitWorkspace(req.SessionID)
	if err != nil {
		return GitFileDetail{}, gitLocalizedError(req.Language, "当前工作区不是 Git 仓库", "The current workspace is not a Git repository")
	}
	left, right := strings.TrimSpace(req.Left), strings.TrimSpace(req.Right)
	if left == "" || right == "" {
		return GitFileDetail{}, gitLocalizedError(req.Language, "对比的分支不完整", "The branches to compare are incomplete")
	}
	path := filepath.ToSlash(strings.TrimSpace(req.Path))
	if path == "" {
		return GitFileDetail{}, gitLocalizedError(req.Language, "请选择要对比的文件", "Select a file to compare")
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitSnapshotTimeout)
	defer cancel()
	if err := verifyGitCommitRefs(ctx, workspace, left, right); err != nil {
		return GitFileDetail{}, gitLocalizedError(req.Language, "对比的分支不存在", "The branch to compare does not exist")
	}
	rootText, err := runGit(ctx, workspace, "rev-parse", "--show-toplevel")
	if err != nil {
		return GitFileDetail{}, gitLocalizedError(req.Language, "当前工作区不是 Git 仓库", "The current workspace is not a Git repository")
	}
	root := filepath.Clean(strings.TrimSpace(rootText))
	statusRaw, err := runGitBytes(ctx, root, "diff", "--name-status", "-z", "--no-renames", left, right, "--")
	if err != nil {
		return GitFileDetail{}, gitLocalizedError(req.Language, "读取分支对比失败", "Failed to read the branch comparison")
	}
	var change *GitFileChange
	for _, item := range parseGitNameStatus(string(statusRaw)) {
		if item.Path == path {
			copy := item
			change = &copy
			break
		}
	}
	if change == nil {
		return GitFileDetail{}, gitLocalizedError(req.Language, "该文件不在分支对比的变更中", "The file is not part of the branch comparison")
	}
	snapshot := GitSnapshot{
		IsRepository: true,
		Root:         root,
		WorktreePath: root,
		CompareLeft:  left,
		CompareRight: right,
	}
	return readGitFileDetail(snapshot, "compare", *change)
}

// gitCurrentBranchRef returns the checked out branch name, falling back to the
// short HEAD SHA on a detached checkout.
func gitCurrentBranchRef(ctx context.Context, root string) string {
	branch, _ := runGit(ctx, root, "branch", "--show-current")
	if value := strings.TrimSpace(branch); value != "" {
		return value
	}
	shortSHA, _ := runGit(ctx, root, "rev-parse", "--short", "HEAD")
	return strings.TrimSpace(shortSHA)
}

// verifyGitCommitRefs resolves both refs as commits, returning an error when
// either cannot be resolved. Refs are passed as separate command arguments so
// branch names can never be interpreted as flags or shell commands.
func verifyGitCommitRefs(ctx context.Context, root, left, right string) error {
	for _, ref := range []string{left, right} {
		if strings.TrimSpace(gitTrimmed(ctx, root, "rev-parse", "--verify", ref+"^{commit}")) == "" {
			return errors.New("Git ref not found: " + ref)
		}
	}
	return nil
}

// sessionGitWorkspace resolves the Git working directory for a conversation.
// An existing conversation uses its bound environment (or recorded session
// root). A brand-new conversation with no session yet resolves the app's
// currently active workspace, because Git is a working-directory concern rather
// than a session concern. When no workspace can be resolved it returns
// errGitWorkspaceNotFound. The active-workspace fallback is deliberately limited
// to sessions that do not exist (id < 1) so a missing or broken session never
// reroutes destructive Git operations to the wrong repository.
func (a *App) sessionGitWorkspace(id int64) (string, error) {
	if id < 1 {
		if workspace := a.activeEnvironmentWorkspace(); workspace != "" {
			return workspace, nil
		}
		return "", errGitWorkspaceNotFound
	}
	item, ok, err := a.store.Store().SessionByID(id)
	if err != nil {
		return "", err
	}
	if ok {
		workspace := a.sessionWorkspace(item.EnvironmentID, "")
		if workspace == "" {
			if changes, changeErr := readSessionChanges(item.SessionDir); changeErr == nil {
				workspace = changes.Root
			}
		}
		if workspace != "" {
			return workspace, nil
		}
	}
	return "", errGitWorkspaceNotFound
}

// activeEnvironmentWorkspace returns the process-local working directory for
// a new conversation. The selection is never persisted as a default.
func (a *App) activeEnvironmentWorkspace() string {
	return strings.TrimSpace(a.runtimeWorkspace().Root)
}

func (a *App) sessionWorkspace(environmentID, fallback string) string {
	if fallback != "" {
		return fallback
	}
	if environmentID == "" {
		return fallback
	}
	environments, err := a.store.Store().ListEnvironments()
	if err != nil {
		return fallback
	}
	for _, environment := range environments {
		if environment.ID == environmentID && environment.Path != "" {
			return environment.Path
		}
	}
	return fallback
}

func runGit(ctx context.Context, root string, args ...string) (string, error) {
	commandArgs := append([]string{"-C", root}, args...)
	command := exec.CommandContext(ctx, "git", commandArgs...)
	configureGitProcess(command)
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	output, err := command.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return "", &gitCommandError{message: message}
	}
	return string(output), nil
}

type gitCommandError struct {
	message string
}

func (e *gitCommandError) Error() string { return e.message }

func listGitBaseBranches(ctx context.Context, root string) []string {
	output, err := runGit(ctx, root, "for-each-ref", "--format=%(refname:short)", "refs/heads", "refs/remotes")
	if err != nil {
		return []string{}
	}
	seen := map[string]bool{}
	branches := make([]string, 0)
	for _, line := range strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n") {
		branch := strings.TrimSpace(line)
		if branch == "" || strings.HasSuffix(branch, "/HEAD") || seen[branch] {
			continue
		}
		seen[branch] = true
		branches = append(branches, branch)
	}
	sort.SliceStable(branches, func(i, j int) bool {
		iRemote := strings.Contains(branches[i], "/")
		jRemote := strings.Contains(branches[j], "/")
		if iRemote != jRemote {
			return !iRemote
		}
		return branches[i] < branches[j]
	})
	return branches
}

func detectGitBaseBranch(ctx context.Context, root, current, preferred string, branches []string) string {
	if preferred != "" {
		for _, branch := range branches {
			if branch == preferred {
				return branch
			}
		}
	}
	if remoteHead, err := runGit(ctx, root, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if value := strings.TrimSpace(remoteHead); value != "" {
			return value
		}
	}
	candidates := []string{"origin/main", "main", "origin/master", "master", "origin/develop", "develop"}
	for _, candidate := range candidates {
		if _, err := runGit(ctx, root, "rev-parse", "--verify", "--quiet", candidate); err == nil {
			return candidate
		}
	}
	// On a repository with only one local branch, comparing it to itself is
	// still useful and accurately reports no committed branch-only changes.
	if current != "" {
		if _, err := runGit(ctx, root, "rev-parse", "--verify", "--quiet", current); err == nil {
			return current
		}
	}
	return ""
}

func parseGitStatus(raw string) []GitFileChange {
	records := strings.Split(raw, "\x00")
	files := make([]GitFileChange, 0, len(records))
	for index := 0; index < len(records); index++ {
		record := records[index]
		if len(record) < 4 {
			continue
		}
		x, y := record[0], record[1]
		path := filepath.ToSlash(record[3:])
		conflicted := isGitConflictStatus(x, y)
		change := GitFileChange{
			Path:       path,
			Status:     gitStatusName(x, y),
			Staged:     x != ' ' && x != '?',
			Unstaged:   y != ' ' && y != '?',
			Untracked:  x == '?' && y == '?',
			Conflicted: conflicted,
		}
		if conflicted {
			change.ConflictStatus = string([]byte{x, y})
		}
		if x == 'R' || x == 'C' || y == 'R' || y == 'C' {
			if index+1 < len(records) {
				change.OldPath = filepath.ToSlash(records[index+1])
			}
			index++
		}
		files = append(files, change)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files
}

func readGitFileDetail(snapshot GitSnapshot, scope string, change GitFileChange) (GitFileDetail, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitSnapshotTimeout)
	defer cancel()

	detail := GitFileDetail{
		Path: change.Path, OldPath: change.OldPath, Scope: scope, Status: change.Status,
		Hunks: []DiffHunk{},
	}
	beforePath := change.Path
	if change.OldPath != "" {
		beforePath = change.OldPath
	}
	var beforeData, afterData []byte
	var err error
	switch scope {
	case "worktree":
		beforeData, detail.Before = readGitBlobVersion(ctx, snapshot.Root, "HEAD", beforePath)
		afterData, detail.After, err = readWorktreeVersion(snapshot.Root, change.Path)
	case "staged":
		beforeData, detail.Before = readGitBlobVersion(ctx, snapshot.Root, "HEAD", beforePath)
		afterData, detail.After = readGitIndexVersion(ctx, snapshot.Root, change.Path)
	case "unstaged":
		beforeData, detail.Before = readGitIndexVersion(ctx, snapshot.Root, change.Path)
		afterData, detail.After, err = readWorktreeVersion(snapshot.Root, change.Path)
	case "untracked":
		// An untracked file has no previous revision at all: keep the before
		// version empty and explicitly non-existent so the diff reports the
		// worktree content as added lines instead of silently falling back to
		// a HEAD blob (which does not exist and yields an empty before side).
		beforeData, detail.Before = nil, GitFileVersion{Exists: false}
		afterData, detail.After, err = readWorktreeVersion(snapshot.Root, change.Path)
	case "branch":
		if snapshot.BaseBranch == "" {
			return GitFileDetail{}, errors.New("base branch not found")
		}
		mergeBase, mergeErr := runGit(ctx, snapshot.Root, "merge-base", snapshot.BaseBranch, "HEAD")
		if mergeErr != nil {
			return GitFileDetail{}, mergeErr
		}
		beforeData, detail.Before = readGitBlobVersion(ctx, snapshot.Root, strings.TrimSpace(mergeBase), beforePath)
		afterData, detail.After = readGitBlobVersion(ctx, snapshot.Root, "HEAD", change.Path)
	case "compare":
		if snapshot.CompareLeft == "" || snapshot.CompareRight == "" {
			return GitFileDetail{}, errors.New("compare refs not found")
		}
		beforeData, detail.Before = readGitBlobVersion(ctx, snapshot.Root, snapshot.CompareLeft, beforePath)
		afterData, detail.After = readGitBlobVersion(ctx, snapshot.Root, snapshot.CompareRight, change.Path)
	}
	if err != nil && !os.IsNotExist(err) {
		return GitFileDetail{}, err
	}

	detail.MimeType = detectGitMime(change.Path, afterData, beforeData)
	detail.Kind = gitPreviewKind(detail.MimeType, beforeData, afterData, detail.Before, detail.After)
	detail.Before.MimeType = detail.MimeType
	detail.After.MimeType = detail.MimeType
	switch detail.Kind {
	case "image":
		detail.Before.ImageData = encodeGitImage(detail.MimeType, beforeData)
		detail.After.ImageData = encodeGitImage(detail.MimeType, afterData)
		detail.Before.Width, detail.Before.Height = gitImageDimensions(beforeData)
		detail.After.Width, detail.After.Height = gitImageDimensions(afterData)
	case "text":
		beforeText, afterText := string(beforeData), string(afterData)
		detail.Before.Text = beforeText
		detail.After.Text = afterText
		detail.Before.LineCount = countGitLines(beforeText)
		detail.After.LineCount = countGitLines(afterText)
		detail.Hunks, detail.Added, detail.Deleted = makeDiff(beforeText, afterText)
	}
	return detail, nil
}

func readGitCommitFileDetail(ctx context.Context, root, commit string, change GitFileChange) (GitFileDetail, error) {
	detail := GitFileDetail{
		Path: change.Path, OldPath: change.OldPath, Scope: "commit", Status: change.Status,
		Hunks: []DiffHunk{},
	}
	beforePath := change.Path
	if change.OldPath != "" {
		beforePath = change.OldPath
	}
	beforeData, before := readGitBlobVersion(ctx, root, commit+"^", beforePath)
	afterData, after := readGitBlobVersion(ctx, root, commit, change.Path)
	detail.Before, detail.After = before, after
	detail.MimeType = detectGitMime(change.Path, afterData, beforeData)
	detail.Kind = gitPreviewKind(detail.MimeType, beforeData, afterData, detail.Before, detail.After)
	detail.Before.MimeType = detail.MimeType
	detail.After.MimeType = detail.MimeType
	switch detail.Kind {
	case "image":
		detail.Before.ImageData = encodeGitImage(detail.MimeType, beforeData)
		detail.After.ImageData = encodeGitImage(detail.MimeType, afterData)
		detail.Before.Width, detail.Before.Height = gitImageDimensions(beforeData)
		detail.After.Width, detail.After.Height = gitImageDimensions(afterData)
	case "text":
		beforeText, afterText := string(beforeData), string(afterData)
		detail.Before.Text = beforeText
		detail.After.Text = afterText
		detail.Before.LineCount = countGitLines(beforeText)
		detail.After.LineCount = countGitLines(afterText)
		detail.Hunks, detail.Added, detail.Deleted = makeDiff(beforeText, afterText)
	}
	return detail, nil
}

func readGitBlobVersion(ctx context.Context, root, ref, path string) ([]byte, GitFileVersion) {
	version := GitFileVersion{}
	if ref == "" || path == "" {
		return nil, version
	}
	spec := ref + ":" + filepath.ToSlash(path)
	sizeText, err := runGit(ctx, root, "cat-file", "-s", spec)
	if err != nil {
		return nil, version
	}
	version.Exists = true
	version.Size, _ = strconv.ParseInt(strings.TrimSpace(sizeText), 10, 64)
	if tree, treeErr := runGit(ctx, root, "ls-tree", ref, "--", filepath.ToSlash(path)); treeErr == nil {
		fields := strings.Fields(tree)
		if len(fields) > 0 {
			version.Permissions = fields[0]
		}
	}
	version.ModifiedAt = gitPathTimestamp(ctx, root, ref, path, false)
	version.CreatedAt = gitPathTimestamp(ctx, root, ref, path, true)
	if version.Size > maxGitPreviewSize {
		return nil, version
	}
	data, err := runGitBytes(ctx, root, "show", spec)
	if err != nil {
		return nil, version
	}
	return data, version
}

func readGitIndexVersion(ctx context.Context, root, path string) ([]byte, GitFileVersion) {
	version := GitFileVersion{}
	path = filepath.ToSlash(path)
	if path == "" {
		return nil, version
	}
	spec := ":" + path
	sizeText, err := runGit(ctx, root, "cat-file", "-s", spec)
	if err != nil {
		return nil, version
	}
	version.Exists = true
	version.Size, _ = strconv.ParseInt(strings.TrimSpace(sizeText), 10, 64)
	if entry, entryErr := runGitBytes(ctx, root, "ls-files", "--stage", "-z", "--", path); entryErr == nil {
		fields := strings.Fields(strings.TrimSuffix(string(entry), "\x00"))
		if len(fields) > 0 {
			version.Permissions = fields[0]
		}
	}
	if version.Size > maxGitPreviewSize {
		return nil, version
	}
	data, err := runGitBytes(ctx, root, "show", spec)
	if err != nil {
		return nil, version
	}
	return data, version
}

func readWorktreeVersion(root, relativePath string) ([]byte, GitFileVersion, error) {
	absolute, err := safeGitWorktreePath(root, relativePath)
	if err != nil {
		return nil, GitFileVersion{}, err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return nil, GitFileVersion{}, err
	}
	version := GitFileVersion{
		Exists: true, Size: info.Size(), Permissions: info.Mode().String(),
		CreatedAt: fileCreatedAt(info), ModifiedAt: info.ModTime().UnixMilli(),
	}
	if info.Size() > maxGitPreviewSize {
		return nil, version, nil
	}
	data, err := os.ReadFile(absolute)
	if err != nil {
		return nil, GitFileVersion{}, err
	}
	return data, version, nil
}

func safeGitWorktreePath(root, relativePath string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(relativePath))
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", errors.New("invalid Git file path")
	}
	absolute := filepath.Join(root, clean)
	if !withinPath(root, absolute) {
		return "", errors.New("Git file path is outside the worktree")
	}
	return absolute, nil
}

func runGitBytes(ctx context.Context, root string, args ...string) ([]byte, error) {
	commandArgs := append([]string{"-C", root}, args...)
	command := exec.CommandContext(ctx, "git", commandArgs...)
	configureGitProcess(command)
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	output, err := command.Output()
	if err != nil {
		return nil, err
	}
	return output, nil
}

func gitPathTimestamp(ctx context.Context, root, ref, path string, first bool) int64 {
	args := []string{"log", "--format=%ct"}
	if !first {
		args = append(args, "-1")
	}
	args = append(args, ref, "--", filepath.ToSlash(path))
	output, err := runGit(ctx, root, args...)
	if err != nil {
		return 0
	}
	lines := strings.Fields(output)
	if len(lines) == 0 {
		return 0
	}
	value := lines[0]
	if first {
		value = lines[len(lines)-1]
	}
	seconds, _ := strconv.ParseInt(value, 10, 64)
	return seconds * 1000
}

func detectGitMime(path string, preferred, fallback []byte) string {
	if extension := strings.ToLower(filepath.Ext(path)); extension != "" {
		if value := mime.TypeByExtension(extension); value != "" {
			return strings.Split(value, ";")[0]
		}
	}
	data := preferred
	if len(data) == 0 {
		data = fallback
	}
	if len(data) == 0 {
		return "application/octet-stream"
	}
	return strings.Split(http.DetectContentType(data), ";")[0]
}

func gitPreviewKind(mimeType string, before, after []byte, beforeVersion, afterVersion GitFileVersion) string {
	if strings.HasPrefix(mimeType, "image/") {
		return "image"
	}
	if (beforeVersion.Exists && beforeVersion.Size > 0 && len(before) == 0) ||
		(afterVersion.Exists && afterVersion.Size > 0 && len(after) == 0) {
		return "binary"
	}
	if len(before) <= maxDiffFileSize && len(after) <= maxDiffFileSize &&
		utf8.Valid(before) && utf8.Valid(after) &&
		bytes.IndexByte(before, 0) < 0 && bytes.IndexByte(after, 0) < 0 {
		return "text"
	}
	return "binary"
}

func encodeGitImage(mimeType string, data []byte) string {
	if len(data) == 0 || len(data) > maxGitPreviewSize {
		return ""
	}
	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data)
}

func gitImageDimensions(data []byte) (int, int) {
	if len(data) == 0 {
		return 0, 0
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0
	}
	return config.Width, config.Height
}

func countGitLines(text string) int {
	if text == "" {
		return 0
	}
	count := strings.Count(text, "\n")
	if !strings.HasSuffix(text, "\n") {
		count++
	}
	return count
}

func parseGitNameStatus(raw string) []GitFileChange {
	files := make([]GitFileChange, 0)
	if strings.Contains(raw, "\x00") {
		records := strings.Split(raw, "\x00")
		files = make([]GitFileChange, 0, len(records)/2)
		for index := 0; index+1 < len(records); index += 2 {
			status, path := records[index], records[index+1]
			if status == "" || path == "" {
				continue
			}
			files = append(files, GitFileChange{
				Path:   filepath.ToSlash(path),
				Status: gitStatusName(status[0], ' '),
			})
		}
	} else {
		lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
		files = make([]GitFileChange, 0, len(lines))
		for _, line := range lines {
			parts := strings.SplitN(line, "\t", 2)
			if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
				continue
			}
			files = append(files, GitFileChange{
				Path:   filepath.ToSlash(parts[1]),
				Status: gitStatusName(parts[0][0], ' '),
			})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files
}

func gitStatusName(x, y byte) string {
	switch {
	case isGitConflictStatus(x, y):
		return "conflicted"
	case x == '?' && y == '?':
		return "untracked"
	case x == 'D' || y == 'D':
		return "deleted"
	case x == 'A' || y == 'A':
		return "added"
	case x == 'R' || y == 'R':
		return "renamed"
	default:
		return "modified"
	}
}

func isGitConflictStatus(x, y byte) bool {
	status := string([]byte{x, y})
	switch status {
	case "DD", "AU", "UD", "UA", "DU", "AA", "UU":
		return true
	default:
		return false
	}
}

func applyGitNumstat(set *GitChangeSet, raw string) {
	byPath := make(map[string]int, len(set.Files))
	for index := range set.Files {
		byPath[set.Files[index].Path] = index
	}
	records := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	if strings.Contains(raw, "\x00") {
		records = strings.Split(raw, "\x00")
	}
	for _, record := range records {
		parts := strings.SplitN(record, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		path := filepath.ToSlash(parts[2])
		index, ok := byPath[path]
		binary := parts[0] == "-" || parts[1] == "-"
		if !ok {
			if !binary {
				added, _ := strconv.Atoi(parts[0])
				deleted, _ := strconv.Atoi(parts[1])
				set.Added += added
				set.Deleted += deleted
			}
			continue
		}
		file := &set.Files[index]
		if binary {
			file.Binary = true
			continue
		}
		file.Added, _ = strconv.Atoi(parts[0])
		file.Deleted, _ = strconv.Atoi(parts[1])
		set.Added += file.Added
		set.Deleted += file.Deleted
	}
	sort.Slice(set.Files, func(i, j int) bool { return set.Files[i].Path < set.Files[j].Path })
}

func applyGitScopedNumstat(set *GitChangeSet, raw string, staged bool) {
	byPath := make(map[string]int, len(set.Files))
	for index := range set.Files {
		byPath[set.Files[index].Path] = index
	}
	records := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	if strings.Contains(raw, "\x00") {
		records = strings.Split(raw, "\x00")
	}
	for _, record := range records {
		parts := strings.SplitN(record, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		index, ok := byPath[filepath.ToSlash(parts[2])]
		if !ok {
			continue
		}
		file := &set.Files[index]
		if parts[0] == "-" || parts[1] == "-" {
			file.Binary = true
			continue
		}
		added, _ := strconv.Atoi(parts[0])
		deleted, _ := strconv.Atoi(parts[1])
		if staged {
			file.StagedAdded = added
			file.StagedDeleted = deleted
		} else {
			file.UnstagedAdded = added
			file.UnstagedDeleted = deleted
		}
	}
}
