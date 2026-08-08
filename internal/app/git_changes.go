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
)

const gitSnapshotTimeout = 8 * time.Second
const maxGitPreviewSize = 4 * 1024 * 1024

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
}

type GitChangeSet struct {
	Files   []GitFileChange `json:"files"`
	Added   int             `json:"added"`
	Deleted int             `json:"deleted"`
}

type GitFileChange struct {
	Path      string `json:"path"`
	OldPath   string `json:"oldPath,omitempty"`
	Status    string `json:"status"`
	Staged    bool   `json:"staged,omitempty"`
	Unstaged  bool   `json:"unstaged,omitempty"`
	Untracked bool   `json:"untracked,omitempty"`
	Added     int    `json:"added"`
	Deleted   int    `json:"deleted"`
	Binary    bool   `json:"binary"`
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
}

func readGitSnapshot(workspace, preferredBase string) GitSnapshot {
	snapshot := GitSnapshot{
		BaseBranches: []string{},
		Worktree:     GitChangeSet{Files: []GitFileChange{}},
		Branch:       GitChangeSet{Files: []GitFileChange{}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitSnapshotTimeout)
	defer cancel()

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

	status, statusErr := runGit(ctx, snapshot.Root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if statusErr == nil {
		snapshot.Worktree.Files = parseGitStatus(status)
		if numstat, diffErr := runGit(ctx, snapshot.Root, "diff", "--numstat", "-z", "--no-renames", "HEAD", "--"); diffErr == nil {
			applyGitNumstat(&snapshot.Worktree, numstat)
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
	names, namesErr := runGit(ctx, snapshot.Root, "diff", "--name-status", "-z", "--no-renames", rangeSpec, "--")
	if namesErr == nil {
		snapshot.Branch.Files = parseGitNameStatus(names)
	}
	if numstat, diffErr := runGit(ctx, snapshot.Root, "diff", "--numstat", "-z", "--no-renames", rangeSpec, "--"); diffErr == nil {
		applyGitNumstat(&snapshot.Branch, numstat)
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

func (a *App) GetSessionGitSnapshot(id int64, baseBranch string) (GitSnapshot, error) {
	workspace, err := a.sessionGitWorkspace(id)
	if err != nil {
		return GitSnapshot{}, err
	}
	return readGitSnapshot(workspace, strings.TrimSpace(baseBranch)), nil
}

// GitFileOperationRequest describes a file-level Git action applied to the
// session worktree. Op is one of:
//   - track/stage: git add the file (track an untracked file or stage worktree changes)
//   - unstage:     git restore --staged the file (drop the staged entry)
//   - discard:     delete an untracked file from disk, or restore a tracked
//     file to HEAD (drop worktree modifications)
//   - restore:     git restore the file (recover a deleted file from HEAD)
type GitFileOperationRequest struct {
	SessionID int64  `json:"sessionId"`
	Op        string `json:"op"`
	Path      string `json:"path"`
}

func (a *App) ApplyGitFileOperation(req GitFileOperationRequest) error {
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
	path := filepath.ToSlash(strings.TrimSpace(req.Path))
	if path == "" || path == "." || path == ".." {
		return errors.New("invalid Git file path")
	}

	switch strings.TrimSpace(req.Op) {
	case "track", "stage":
		// git add stages everything for the path, including deletions and new files.
		if _, err := runGit(ctx, root, "add", "--", path); err != nil {
			return err
		}
	case "unstage":
		// git reset 在旧版本 Git 上同样可用（git restore --staged 需要 2.23+）。
		if _, err := runGit(ctx, root, "reset", "-q", "HEAD", "--", path); err != nil {
			return err
		}
	case "discard":
		untracked, statusErr := isGitUntracked(ctx, root, path)
		if statusErr != nil {
			return statusErr
		}
		if untracked {
			absolute, pathErr := safeGitWorktreePath(root, path)
			if pathErr != nil {
				return pathErr
			}
			if err := os.Remove(absolute); err != nil && !os.IsNotExist(err) {
				return err
			}
			return nil
		}
		if _, err := runGit(ctx, root, "restore", "--", path); err != nil {
			return err
		}
	case "restore":
		if _, err := runGit(ctx, root, "restore", "--", path); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported Git file operation: %s", req.Op)
	}
	return nil
}

// isGitUntracked reports whether the porcelain status marks path as untracked,
// which decides whether discard should remove the file from disk instead of
// restoring it from Git.
func isGitUntracked(ctx context.Context, root, path string) (bool, error) {
	status, err := runGit(ctx, root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return false, err
	}
	for _, file := range parseGitStatus(status) {
		if file.Path == path {
			return file.Untracked, nil
		}
	}
	return false, nil
}

func (a *App) GetSessionGitFileDetail(id int64, scope, path, baseBranch string) (GitFileDetail, error) {
	workspace, err := a.sessionGitWorkspace(id)
	if err != nil {
		return GitFileDetail{}, err
	}
	snapshot := readGitSnapshot(workspace, strings.TrimSpace(baseBranch))
	if !snapshot.IsRepository {
		return GitFileDetail{}, errors.New("workspace is not a Git repository")
	}
	scope = strings.TrimSpace(scope)
	var set GitChangeSet
	switch scope {
	case "worktree":
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
	return readGitFileDetail(snapshot, scope, *change)
}

func (a *App) sessionGitWorkspace(id int64) (string, error) {
	item, ok, err := a.store.Store().SessionByID(id)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("conversation not found: %d", id)
	}
	workspace := a.sessionWorkspace(item.EnvironmentID, "")
	if workspace == "" {
		if changes, changeErr := readSessionChanges(item.SessionDir); changeErr == nil {
			workspace = changes.Root
		}
	}
	if workspace == "" {
		return "", errors.New("conversation workspace not found")
	}
	return workspace, nil
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
		change := GitFileChange{
			Path:      path,
			Status:    gitStatusName(x, y),
			Staged:    x != ' ' && x != '?',
			Unstaged:  y != ' ' && y != '?',
			Untracked: x == '?' && y == '?',
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
