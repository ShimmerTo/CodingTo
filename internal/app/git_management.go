package app

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"codingto/internal/applog"
)

const gitNetworkTimeout = 2 * time.Minute
const gitHistoryLimit = 80

// GitAvailability is the lightweight repository state used by the sidebar.
type GitAvailability struct {
	IsRepository  bool   `json:"isRepository"`
	Root          string `json:"root,omitempty"`
	CurrentBranch string `json:"currentBranch,omitempty"`
	ChangeCount   int    `json:"changeCount"`
	Ahead         int    `json:"ahead"`
	HasConflicts  bool   `json:"hasConflicts"`
}

// GitRemote describes one configured fetch and push destination.
type GitRemote struct {
	Name     string `json:"name"`
	FetchURL string `json:"fetchUrl,omitempty"`
	PushURL  string `json:"pushUrl,omitempty"`
}

// GitBranch describes a local or remote branch and its tracking state.
type GitBranch struct {
	Name         string `json:"name"`
	FullName     string `json:"fullName"`
	Remote       bool   `json:"remote"`
	Current      bool   `json:"current"`
	SHA          string `json:"sha,omitempty"`
	Upstream     string `json:"upstream,omitempty"`
	Ahead        int    `json:"ahead"`
	Behind       int    `json:"behind"`
	Subject      string `json:"subject,omitempty"`
	Timestamp    int64  `json:"timestamp,omitempty"`
	WorktreePath string `json:"worktreePath,omitempty"`
}

// GitCommit describes a repository history entry and optional outgoing details.
type GitCommit struct {
	Hash        string          `json:"hash"`
	ShortHash   string          `json:"shortHash"`
	Parents     []string        `json:"parents"`
	Author      string          `json:"author"`
	AuthorEmail string          `json:"authorEmail,omitempty"`
	Timestamp   int64           `json:"timestamp"`
	Subject     string          `json:"subject"`
	Message     string          `json:"message,omitempty"`
	Decorations string          `json:"decorations,omitempty"`
	Files       []GitFileChange `json:"files,omitempty"`
	Added       int             `json:"added,omitempty"`
	Deleted     int             `json:"deleted,omitempty"`
}

// GitRepositoryView contains the data required by the Git management dialog.
type GitRepositoryView struct {
	IsRepository  bool            `json:"isRepository"`
	Root          string          `json:"root,omitempty"`
	WorktreePath  string          `json:"worktreePath,omitempty"`
	CurrentBranch string          `json:"currentBranch,omitempty"`
	Detached      bool            `json:"detached"`
	Head          string          `json:"head,omitempty"`
	Upstream      string          `json:"upstream,omitempty"`
	Ahead         int             `json:"ahead"`
	Behind        int             `json:"behind"`
	State         string          `json:"state,omitempty"`
	HasConflicts  bool            `json:"hasConflicts"`
	Conflicts     []GitFileChange `json:"conflicts"`
	Worktree      GitChangeSet    `json:"worktree"`
	Remotes       []GitRemote     `json:"remotes"`
	Branches      []GitBranch     `json:"branches"`
	Commits       []GitCommit     `json:"commits"`
}

// GitRepositoryOperationRequest describes one bounded repository operation.
type GitRepositoryOperationRequest struct {
	SessionID  int64  `json:"sessionId"`
	Op         string `json:"op"`
	Language   string `json:"language,omitempty"`
	Message    string `json:"message,omitempty"`
	Branch     string `json:"branch,omitempty"`
	StartPoint string `json:"startPoint,omitempty"`
	Remote     string `json:"remote,omitempty"`
	Commit     string `json:"commit,omitempty"`
}

// GitOperationResult is returned after a successful repository operation.
type GitOperationResult struct {
	Message string `json:"message"`
	Output  string `json:"output,omitempty"`
}

// GetSessionGitAvailability checks whether a conversation workspace is a Git repository.
func (a *App) GetSessionGitAvailability(id int64) (GitAvailability, error) {
	workspace, err := a.sessionGitWorkspace(id)
	if err != nil {
		if errors.Is(err, errGitWorkspaceNotFound) {
			return GitAvailability{}, nil
		}
		return GitAvailability{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitSnapshotTimeout)
	defer cancel()
	rootText, err := runGit(ctx, workspace, "rev-parse", "--show-toplevel")
	if err != nil {
		return GitAvailability{}, nil
	}
	root := filepath.Clean(strings.TrimSpace(rootText))
	branch := gitTrimmed(ctx, root, "branch", "--show-current")
	upstream := gitTrimmed(ctx, root, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	ahead := 0
	if upstream != "" {
		_, ahead = gitAheadBehind(ctx, root, upstream, "HEAD")
	}
	statusBytes, statusErr := runGitBytes(ctx, root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if statusErr != nil {
		applog.Errorf("read Git sidebar status failed: root=%q", root)
		return GitAvailability{IsRepository: true, Root: root, CurrentBranch: branch, Ahead: ahead}, nil
	}
	files := parseGitStatus(string(statusBytes))
	availability := GitAvailability{
		IsRepository:  true,
		Root:          root,
		CurrentBranch: branch,
		ChangeCount:   len(files),
		Ahead:         ahead,
	}
	for _, file := range files {
		availability.HasConflicts = availability.HasConflicts || file.Conflicted
	}
	return availability, nil
}

// GetSessionGitRepository returns the complete lightweight Git dialog model.
func (a *App) GetSessionGitRepository(id int64) (GitRepositoryView, error) {
	workspace, err := a.sessionGitWorkspace(id)
	if err != nil {
		return GitRepositoryView{}, err
	}
	snapshot := readGitSnapshot(workspace, "")
	view := GitRepositoryView{
		IsRepository:  snapshot.IsRepository,
		Root:          snapshot.Root,
		WorktreePath:  snapshot.WorktreePath,
		CurrentBranch: snapshot.CurrentBranch,
		Worktree:      snapshot.Worktree,
		Conflicts:     []GitFileChange{},
		Remotes:       []GitRemote{},
		Branches:      []GitBranch{},
		Commits:       []GitCommit{},
	}
	if !snapshot.IsRepository {
		return view, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitSnapshotTimeout)
	defer cancel()
	rawBranch := gitTrimmed(ctx, view.Root, "branch", "--show-current")
	view.Detached = rawBranch == ""
	view.CurrentBranch = rawBranch
	view.Head = gitTrimmed(ctx, view.Root, "rev-parse", "--short", "HEAD")
	view.Upstream = gitTrimmed(ctx, view.Root, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	if view.Upstream != "" {
		view.Behind, view.Ahead = gitAheadBehind(ctx, view.Root, view.Upstream, "HEAD")
	}
	view.State = detectGitRepositoryState(ctx, view.Root)
	for _, file := range view.Worktree.Files {
		if file.Conflicted {
			view.HasConflicts = true
			view.Conflicts = append(view.Conflicts, file)
		}
	}
	view.Remotes = listGitRemotes(ctx, view.Root)
	view.Branches = listGitBranches(ctx, view.Root)
	view.Commits = listGitCommits(ctx, view.Root)
	return view, nil
}

// GetSessionGitOutgoingCommits returns every commit that is ahead of the
// current branch's configured upstream, including per-commit file statistics.
func (a *App) GetSessionGitOutgoingCommits(id int64) ([]GitCommit, error) {
	workspace, err := a.sessionGitWorkspace(id)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitSnapshotTimeout)
	defer cancel()
	rootText, err := runGit(ctx, workspace, "rev-parse", "--show-toplevel")
	if err != nil {
		applog.Errorf("locate Git repository for outgoing commits failed: session=%d", id)
		return nil, errors.New("failed to locate the Git repository")
	}
	root := filepath.Clean(strings.TrimSpace(rootText))
	upstream := gitTrimmed(ctx, root, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	if upstream == "" {
		return []GitCommit{}, nil
	}
	commits, err := listGitOutgoingCommits(ctx, root, upstream)
	if err != nil {
		applog.Errorf("read outgoing Git commits failed: session=%d root=%q", id, root)
		return nil, errors.New("failed to read outgoing Git commits")
	}
	return commits, nil
}

// RunSessionGitOperation executes one allow-listed Git action in the conversation worktree.
func (a *App) RunSessionGitOperation(req GitRepositoryOperationRequest) (GitOperationResult, error) {
	a.gitWriteMu.Lock()
	defer a.gitWriteMu.Unlock()

	workspace, err := a.sessionGitWorkspace(req.SessionID)
	if err != nil {
		return GitOperationResult{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitNetworkTimeout)
	defer cancel()
	rootText, err := runGit(ctx, workspace, "rev-parse", "--show-toplevel")
	if err != nil {
		return GitOperationResult{}, gitLocalizedError(req.Language, "当前工作区不是 Git 仓库", "The current workspace is not a Git repository")
	}
	root := filepath.Clean(strings.TrimSpace(rootText))
	op := strings.TrimSpace(req.Op)
	var output string
	switch op {
	case "stage_all":
		output, err = runGit(ctx, root, "add", "-A", "--", ".")
	case "unstage_all":
		if gitTrimmed(ctx, root, "rev-parse", "--verify", "HEAD") != "" {
			output, err = runGit(ctx, root, "reset", "-q", "HEAD", "--")
		} else {
			output, err = runGit(ctx, root, "rm", "--cached", "-r", "--ignore-unmatch", "--", ".")
		}
	case "commit":
		message := strings.TrimSpace(strings.ReplaceAll(req.Message, "\x00", ""))
		if message == "" {
			return GitOperationResult{}, gitLocalizedError(req.Language, "提交信息不能为空", "The commit message cannot be empty")
		}
		if len([]rune(message)) > 8000 {
			return GitOperationResult{}, gitLocalizedError(req.Language, "提交信息不能超过 8000 个字符", "The commit message cannot exceed 8,000 characters")
		}
		staged, stagedErr := runGit(ctx, root, "diff", "--cached", "--name-only", "-z", "--")
		if stagedErr != nil || strings.Trim(staged, "\x00") == "" {
			return GitOperationResult{}, gitLocalizedError(req.Language, "暂存区为空，请先暂存要提交的文件", "The index is empty; stage the files you want to commit first")
		}
		output, err = runGitInput(ctx, root, message+"\n", "commit", "--file=-")
	case "fetch":
		remote := strings.TrimSpace(req.Remote)
		if remote == "" {
			output, err = runGit(ctx, root, "fetch", "--all", "--prune")
		} else if validGitRemote(ctx, root, remote) {
			output, err = runGit(ctx, root, "fetch", "--prune", remote)
		} else {
			return GitOperationResult{}, gitLocalizedError(req.Language, "远端仓库不存在", "The remote does not exist")
		}
	case "pull":
		if detectGitRepositoryState(ctx, root) != "" {
			return GitOperationResult{}, gitLocalizedError(req.Language, "当前 Git 操作尚未完成，请先解决冲突或中止操作", "The current Git operation is incomplete; resolve its conflicts or abort it first")
		}
		if gitTrimmed(ctx, root, "rev-parse", "--abbrev-ref", "@{upstream}") == "" {
			return GitOperationResult{}, gitLocalizedError(req.Language, "当前分支尚未设置上游分支，请先 Push 发布分支", "The current branch has no upstream; push it to publish the branch first")
		}
		output, err = runGit(ctx, root, "pull", "--ff-only")
	case "push":
		branch := gitTrimmed(ctx, root, "branch", "--show-current")
		if branch == "" {
			return GitOperationResult{}, gitLocalizedError(req.Language, "游离 HEAD 状态下不能直接推送", "Cannot push directly from a detached HEAD")
		}
		upstream := gitTrimmed(ctx, root, "rev-parse", "--abbrev-ref", "@{upstream}")
		if upstream != "" {
			output, err = runGit(ctx, root, "push")
		} else {
			remote := strings.TrimSpace(req.Remote)
			if remote == "" {
				remote = preferredGitRemote(ctx, root)
			}
			if remote == "" || !validGitRemote(ctx, root, remote) {
				return GitOperationResult{}, gitLocalizedError(req.Language, "未配置可用的远端仓库", "No usable remote is configured")
			}
			output, err = runGit(ctx, root, "push", "--set-upstream", remote, branch)
		}
	case "checkout":
		branch := strings.TrimSpace(req.Branch)
		if !validLocalGitBranch(ctx, root, branch) {
			return GitOperationResult{}, gitLocalizedError(req.Language, "本地分支不存在", "The local branch does not exist")
		}
		output, err = runGit(ctx, root, "checkout", branch)
	case "checkout_remote":
		branch := strings.TrimSpace(req.Branch)
		if branch == "" || strings.HasPrefix(branch, "-") || strings.ContainsRune(branch, '\x00') || strings.HasSuffix(branch, "/HEAD") {
			return GitOperationResult{}, gitLocalizedError(req.Language, "远端分支不存在", "The remote branch does not exist")
		}
		if _, verifyErr := runGit(ctx, root, "show-ref", "--verify", "--quiet", "refs/remotes/"+branch); verifyErr != nil {
			return GitOperationResult{}, gitLocalizedError(req.Language, "远端分支不存在", "The remote branch does not exist")
		}
		localName := ""
		if slash := strings.Index(branch, "/"); slash >= 0 && slash+1 < len(branch) {
			localName = branch[slash+1:]
		}
		if validLocalGitBranch(ctx, root, localName) && gitTrimmed(ctx, root, "rev-parse", "--abbrev-ref", localName+"@{upstream}") == branch {
			output, err = runGit(ctx, root, "checkout", localName)
		} else {
			output, err = runGit(ctx, root, "checkout", "--track", branch)
		}
	case "create_branch":
		branch := strings.TrimSpace(req.Branch)
		if _, checkErr := runGit(ctx, root, "check-ref-format", "--branch", branch); checkErr != nil {
			return GitOperationResult{}, gitLocalizedError(req.Language, "分支名称不合法", "The branch name is invalid")
		}
		startPoint := strings.TrimSpace(req.StartPoint)
		if startPoint == "" {
			startPoint = "HEAD"
		}
		if len(startPoint) > 512 || strings.HasPrefix(startPoint, "-") || strings.ContainsRune(startPoint, '\x00') {
			return GitOperationResult{}, gitLocalizedError(req.Language, "新分支的起点不合法", "The new branch start point is invalid")
		}
		if _, verifyErr := runGit(ctx, root, "rev-parse", "--verify", startPoint+"^{commit}"); verifyErr != nil {
			return GitOperationResult{}, gitLocalizedError(req.Language, "新分支的起点不存在", "The new branch start point does not exist")
		}
		output, err = runGit(ctx, root, "checkout", "-b", branch, startPoint)
	case "reset_mixed", "reset_hard":
		if detectGitRepositoryState(ctx, root) != "" {
			return GitOperationResult{}, gitLocalizedError(req.Language, "当前 Git 操作尚未完成，请先解决冲突或中止操作", "The current Git operation is incomplete; resolve its conflicts or abort it first")
		}
		if gitTrimmed(ctx, root, "branch", "--show-current") == "" {
			return GitOperationResult{}, gitLocalizedError(req.Language, "游离 HEAD 状态下不能回退当前分支", "Cannot reset the current branch from a detached HEAD")
		}
		commit := strings.TrimSpace(req.Commit)
		if !validGitCommitHash(commit) {
			return GitOperationResult{}, gitLocalizedError(req.Language, "要回退到的提交不合法", "The reset target commit is invalid")
		}
		resolved := gitTrimmed(ctx, root, "rev-parse", "--verify", commit+"^{commit}")
		if resolved == "" || !strings.EqualFold(resolved, commit) {
			return GitOperationResult{}, gitLocalizedError(req.Language, "要回退到的提交不存在", "The reset target commit does not exist")
		}
		mode := "--mixed"
		if op == "reset_hard" {
			mode = "--hard"
		}
		output, err = runGit(ctx, root, "reset", mode, commit)
	case "abort":
		state := detectGitRepositoryState(ctx, root)
		switch state {
		case "merge":
			output, err = runGit(ctx, root, "merge", "--abort")
		case "rebase":
			output, err = runGit(ctx, root, "rebase", "--abort")
		case "cherry-pick":
			output, err = runGit(ctx, root, "cherry-pick", "--abort")
		case "revert":
			output, err = runGit(ctx, root, "revert", "--abort")
		default:
			return GitOperationResult{}, gitLocalizedError(req.Language, "当前没有可中止的 Git 操作", "There is no Git operation to abort")
		}
	default:
		return GitOperationResult{}, gitLocalizedError(req.Language, "不支持的 Git 操作", "Unsupported Git operation")
	}
	if err != nil {
		applog.Errorf("Git repository operation failed: op=%q root=%q category=%q", op, root, gitOperationErrorCategory(err))
		return GitOperationResult{}, friendlyGitOperationError(op, req.Language, err)
	}
	if op == "fetch" || op == "pull" || op == "push" {
		output = ""
	}
	return GitOperationResult{Message: gitOperationSuccessMessage(op, req.Language), Output: strings.TrimSpace(output)}, nil
}

func runGitInput(ctx context.Context, root, input string, args ...string) (string, error) {
	commandArgs := append([]string{"-C", root}, args...)
	command := exec.CommandContext(ctx, "git", commandArgs...)
	configureGitProcess(command)
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	command.Stdin = strings.NewReader(input)
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

func gitTrimmed(ctx context.Context, root string, args ...string) string {
	output, err := runGit(ctx, root, args...)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(output)
}

func gitAheadBehind(ctx context.Context, root, left, right string) (int, int) {
	counts := gitTrimmed(ctx, root, "rev-list", "--left-right", "--count", left+"..."+right)
	fields := strings.Fields(counts)
	if len(fields) != 2 {
		return 0, 0
	}
	behind, behindErr := strconv.Atoi(fields[0])
	if behindErr != nil {
		behind = 0
	}
	ahead, aheadErr := strconv.Atoi(fields[1])
	if aheadErr != nil {
		ahead = 0
	}
	return behind, ahead
}

func listGitRemotes(ctx context.Context, root string) []GitRemote {
	names := strings.Fields(gitTrimmed(ctx, root, "remote"))
	remotes := make([]GitRemote, 0, len(names))
	for _, name := range names {
		remotes = append(remotes, GitRemote{
			Name:     name,
			FetchURL: sanitizeGitRemoteURL(gitTrimmed(ctx, root, "remote", "get-url", name)),
			PushURL:  sanitizeGitRemoteURL(gitTrimmed(ctx, root, "remote", "get-url", "--push", name)),
		})
	}
	return remotes
}

func listGitBranches(ctx context.Context, root string) []GitBranch {
	format := "%(refname)%09%(refname:short)%09%(HEAD)%09%(objectname:short)%09%(upstream:short)%09%(upstream:track,nobracket)%09%(authordate:unix)%09%(subject)"
	output := gitTrimmed(ctx, root, "for-each-ref", "--sort=-authordate", "--format="+format, "refs/heads", "refs/remotes")
	worktrees := gitWorktreeBranches(ctx, root)
	branches := make([]GitBranch, 0)
	for _, line := range strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n") {
		parts := strings.SplitN(line, "\t", 8)
		if len(parts) < 8 || parts[0] == "" || strings.HasSuffix(parts[0], "/HEAD") {
			continue
		}
		branch := GitBranch{
			FullName: parts[0], Name: parts[1], Remote: strings.HasPrefix(parts[0], "refs/remotes/"),
			Current: strings.TrimSpace(parts[2]) == "*", SHA: parts[3], Upstream: parts[4],
			Subject: parts[7], WorktreePath: worktrees[parts[0]],
		}
		branch.Ahead, branch.Behind = parseGitTrack(parts[5])
		if seconds, parseErr := strconv.ParseInt(parts[6], 10, 64); parseErr == nil {
			branch.Timestamp = seconds * 1000
		}
		branches = append(branches, branch)
	}
	return branches
}

func parseGitTrack(track string) (int, int) {
	track = strings.NewReplacer(",", " ", ":", " ").Replace(track)
	fields := strings.Fields(track)
	ahead, behind := 0, 0
	for index := 0; index+1 < len(fields); index++ {
		value, err := strconv.Atoi(fields[index+1])
		if err != nil {
			continue
		}
		switch fields[index] {
		case "ahead":
			ahead = value
		case "behind":
			behind = value
		}
	}
	return ahead, behind
}

func gitWorktreeBranches(ctx context.Context, root string) map[string]string {
	output := gitTrimmed(ctx, root, "worktree", "list", "--porcelain")
	result := map[string]string{}
	path := ""
	for _, line := range strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			path = strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
		case strings.HasPrefix(line, "branch ") && path != "":
			result[strings.TrimSpace(strings.TrimPrefix(line, "branch "))] = path
		}
	}
	return result
}

func listGitCommits(ctx context.Context, root string) []GitCommit {
	format := "%H%x1f%h%x1f%P%x1f%an%x1f%ae%x1f%at%x1f%s%x1f%D%x1e"
	output, err := runGit(ctx, root, "log", "--date-order", "--max-count="+strconv.Itoa(gitHistoryLimit), "--pretty=format:"+format, "--all")
	if err != nil {
		return []GitCommit{}
	}
	commits := make([]GitCommit, 0)
	for _, record := range strings.Split(output, "\x1e") {
		parts := strings.Split(strings.Trim(record, "\r\n"), "\x1f")
		if len(parts) < 8 || parts[0] == "" {
			continue
		}
		commit := GitCommit{
			Hash: parts[0], ShortHash: parts[1], Author: parts[3], AuthorEmail: parts[4],
			Subject: parts[6], Decorations: parts[7], Parents: []string{},
		}
		if seconds, parseErr := strconv.ParseInt(parts[5], 10, 64); parseErr == nil {
			commit.Timestamp = seconds * 1000
		}
		if parts[2] != "" {
			commit.Parents = strings.Fields(parts[2])
		}
		commits = append(commits, commit)
	}
	return commits
}

func listGitOutgoingCommits(ctx context.Context, root, upstream string) ([]GitCommit, error) {
	rangeSpec := upstream + "..HEAD"
	format := "%x1e%H%x00%h%x00%P%x00%an%x00%ae%x00%at%x00%s%x00%B%x00"
	statusOutput, err := runGit(ctx, root, "log", "--date-order", "--pretty=format:"+format, "--name-status", "-z", "--no-renames", "--end-of-options", rangeSpec, "--")
	if err != nil {
		return nil, err
	}
	commits := parseGitOutgoingCommits(statusOutput)
	if len(commits) == 0 {
		return []GitCommit{}, nil
	}

	statFormat := "%x1e%H%x00"
	statOutput, err := runGit(ctx, root, "log", "--date-order", "--pretty=format:"+statFormat, "--numstat", "-z", "--no-renames", "--end-of-options", rangeSpec, "--")
	if err != nil {
		return nil, err
	}
	applyGitOutgoingNumstat(commits, statOutput)
	return commits, nil
}

func parseGitOutgoingCommits(raw string) []GitCommit {
	commits := make([]GitCommit, 0)
	for _, record := range strings.Split(raw, "\x1e") {
		fields := strings.Split(record, "\x00")
		if len(fields) < 8 {
			continue
		}
		hash := strings.TrimSpace(fields[0])
		if hash == "" {
			continue
		}
		commit := GitCommit{
			Hash: hash, ShortHash: fields[1], Author: fields[3], AuthorEmail: fields[4],
			Subject: fields[6], Message: strings.TrimSpace(fields[7]), Parents: []string{},
			Files: []GitFileChange{},
		}
		if seconds, parseErr := strconv.ParseInt(fields[5], 10, 64); parseErr == nil {
			commit.Timestamp = seconds * 1000
		}
		if fields[2] != "" {
			commit.Parents = strings.Fields(fields[2])
		}
		var statusRaw strings.Builder
		for index := 8; index+1 < len(fields); index += 2 {
			status := strings.TrimSpace(strings.TrimLeft(fields[index], "\r\n"))
			path := fields[index+1]
			if status == "" || path == "" {
				continue
			}
			statusRaw.WriteString(status)
			statusRaw.WriteByte(0)
			statusRaw.WriteString(path)
			statusRaw.WriteByte(0)
		}
		commit.Files = parseGitNameStatus(statusRaw.String())
		commits = append(commits, commit)
	}
	return commits
}

func applyGitOutgoingNumstat(commits []GitCommit, raw string) {
	byHash := make(map[string]int, len(commits))
	for index := range commits {
		byHash[commits[index].Hash] = index
	}
	for _, record := range strings.Split(raw, "\x1e") {
		fields := strings.Split(record, "\x00")
		if len(fields) < 2 {
			continue
		}
		commitIndex, ok := byHash[strings.TrimSpace(fields[0])]
		if !ok {
			continue
		}
		var statRaw strings.Builder
		for index := 1; index < len(fields); index++ {
			stat := strings.TrimLeft(fields[index], "\r\n")
			if stat == "" {
				continue
			}
			statRaw.WriteString(stat)
			statRaw.WriteByte(0)
		}
		set := GitChangeSet{Files: commits[commitIndex].Files}
		applyGitNumstat(&set, statRaw.String())
		commits[commitIndex].Files = set.Files
		commits[commitIndex].Added = set.Added
		commits[commitIndex].Deleted = set.Deleted
	}
}

func detectGitRepositoryState(ctx context.Context, root string) string {
	checks := []struct{ path, state string }{
		{"MERGE_HEAD", "merge"}, {"rebase-merge", "rebase"}, {"rebase-apply", "rebase"},
		{"CHERRY_PICK_HEAD", "cherry-pick"}, {"REVERT_HEAD", "revert"}, {"BISECT_LOG", "bisect"},
	}
	for _, check := range checks {
		path := gitTrimmed(ctx, root, "rev-parse", "--git-path", check.path)
		if path == "" {
			continue
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		if _, err := os.Stat(path); err == nil {
			return check.state
		}
	}
	return ""
}

func validGitRemote(ctx context.Context, root, remote string) bool {
	if remote == "" || strings.HasPrefix(remote, "-") || strings.ContainsRune(remote, '\x00') {
		return false
	}
	for _, name := range strings.Fields(gitTrimmed(ctx, root, "remote")) {
		if name == remote {
			return true
		}
	}
	return false
}

func preferredGitRemote(ctx context.Context, root string) string {
	names := strings.Fields(gitTrimmed(ctx, root, "remote"))
	for _, name := range names {
		if name == "origin" {
			return name
		}
	}
	if len(names) > 0 {
		return names[0]
	}
	return ""
}

func sanitizeGitRemoteURL(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" {
		if at := strings.LastIndex(value, "@"); at > 0 && strings.Contains(value[:at], ":") {
			return value[at+1:]
		}
		return value
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func validLocalGitBranch(ctx context.Context, root, branch string) bool {
	if branch == "" || strings.HasPrefix(branch, "-") {
		return false
	}
	_, err := runGit(ctx, root, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

func friendlyGitOperationError(op, language string, err error) error {
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "non-fast-forward") || strings.Contains(message, "not possible to fast-forward") || strings.Contains(message, "divergent"):
		return gitLocalizedError(language, "本地与远端分支已经分叉，已停止操作；请让 Agent 检查后选择 merge 或 rebase", "The local and remote branches have diverged. Ask the Agent to inspect them and choose merge or rebase")
	case strings.Contains(message, "conflict") || strings.Contains(message, "unmerged"):
		return gitLocalizedError(language, "Git 检测到冲突，请先处理冲突文件再继续", "Git detected conflicts; resolve the conflicted files before continuing")
	case strings.Contains(message, "local changes") || strings.Contains(message, "would be overwritten"):
		return gitLocalizedError(language, "本地未提交修改会被覆盖，Git 已停止操作", "Git stopped because local uncommitted changes would be overwritten")
	case strings.Contains(message, "authentication") || strings.Contains(message, "permission denied") || strings.Contains(message, "could not read username"):
		return gitLocalizedError(language, "远端身份验证失败，请检查 Git 或 SSH 凭据", "Remote authentication failed; check the Git or SSH credentials")
	case strings.Contains(message, "no upstream") || strings.Contains(message, "no tracking information"):
		return gitLocalizedError(language, "当前分支尚未设置上游分支", "The current branch has no upstream")
	case strings.Contains(message, "already checked out") || strings.Contains(message, "used by worktree"):
		return gitLocalizedError(language, "该分支已被其他 Git worktree 使用", "The branch is already used by another Git worktree")
	case strings.Contains(message, "nothing to commit"):
		return gitLocalizedError(language, "暂存区没有可提交的变更", "The index has no changes to commit")
	default:
		return gitLocalizedError(language, fmt.Sprintf("Git %s 操作失败，请检查仓库状态和远端配置", op), fmt.Sprintf("Git %s failed; check the repository state and remote configuration", op))
	}
}

func gitOperationErrorCategory(err error) string {
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "non-fast-forward") || strings.Contains(message, "not possible to fast-forward") || strings.Contains(message, "divergent"):
		return "diverged"
	case strings.Contains(message, "conflict") || strings.Contains(message, "unmerged"):
		return "conflict"
	case strings.Contains(message, "local changes") || strings.Contains(message, "would be overwritten"):
		return "local_changes"
	case strings.Contains(message, "authentication") || strings.Contains(message, "permission denied") || strings.Contains(message, "could not read username"):
		return "authentication"
	case strings.Contains(message, "no upstream") || strings.Contains(message, "no tracking information"):
		return "no_upstream"
	case strings.Contains(message, "already checked out") || strings.Contains(message, "used by worktree"):
		return "worktree_in_use"
	case strings.Contains(message, "nothing to commit"):
		return "nothing_to_commit"
	default:
		return "unknown"
	}
}

func gitOperationSuccessMessage(op, language string) string {
	zhMessages := map[string]string{
		"stage_all": "已暂存全部变更", "unstage_all": "已取消全部暂存", "commit": "提交已创建",
		"fetch": "远端信息已更新", "pull": "已拉取最新提交", "push": "提交已推送",
		"checkout": "已切换分支", "checkout_remote": "已创建跟踪分支并切换", "create_branch": "分支已创建并切换", "abort": "已中止当前 Git 操作",
		"reset_mixed": "已回退到所选提交，后续改动已保留在工作区", "reset_hard": "已回退到所选提交并清空已跟踪改动",
	}
	enMessages := map[string]string{
		"stage_all": "All changes staged", "unstage_all": "All changes unstaged", "commit": "Commit created",
		"fetch": "Remote refs updated", "pull": "Latest commits pulled", "push": "Commits pushed",
		"checkout": "Branch switched", "checkout_remote": "Tracking branch created and checked out", "create_branch": "Branch created and checked out", "abort": "Git operation aborted",
		"reset_mixed": "Reset to the selected commit; later changes were kept in the worktree", "reset_hard": "Reset to the selected commit and discarded later changes",
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(language)), "en") {
		return enMessages[op]
	}
	return zhMessages[op]
}

func validGitCommitHash(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, char := range value {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return false
		}
	}
	return true
}

func gitLocalizedError(language, chinese, english string) error {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(language)), "en") {
		return errors.New(english)
	}
	return errors.New(chinese)
}
