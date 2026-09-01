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

// GitStash describes one named stash entry in the repository reflog.
type GitStash struct {
	Hash      string `json:"hash"`
	Ref       string `json:"ref"`
	Name      string `json:"name"`
	Branch    string `json:"branch,omitempty"`
	Subject   string `json:"subject,omitempty"`
	Timestamp int64  `json:"timestamp,omitempty"`
}

// GitRepositoryView contains the data required by the Git management dialog.
type GitRepositoryView struct {
	IsRepository  bool            `json:"isRepository"`
	Warming       bool            `json:"warming,omitempty"`
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
	Stashes       []GitStash      `json:"stashes"`
}

// GitRepositoryOperationRequest describes one bounded repository operation.
type GitRepositoryOperationRequest struct {
	SessionID  int64    `json:"sessionId"`
	Op         string   `json:"op"`
	Language   string   `json:"language,omitempty"`
	Message    string   `json:"message,omitempty"`
	Branch     string   `json:"branch,omitempty"`
	StartPoint string   `json:"startPoint,omitempty"`
	Remote     string   `json:"remote,omitempty"`
	Commit     string   `json:"commit,omitempty"`
	StashHash  string   `json:"stashHash,omitempty"`
	Paths      []string `json:"paths,omitempty"`
}

// GitOperationResult is returned after a successful repository operation.
type GitOperationResult struct {
	Message      string          `json:"message"`
	Output       string          `json:"output,omitempty"`
	HasConflicts bool            `json:"hasConflicts,omitempty"`
	StashKept    bool            `json:"stashKept,omitempty"`
	Conflicts    []GitFileChange `json:"conflicts,omitempty"`
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
	if a.gitMonitor != nil {
		// The monitor preloads workspaces in the background. A cache miss returns
		// immediately so startup and workspace switches never run Git commands on
		// the caller's path; git:workspace delivers the fresh availability.
		if cached, ok := a.gitMonitor.Availability(workspace); ok {
			return cached, nil
		}
		return GitAvailability{}, nil
	}
	return readGitAvailability(workspace), nil
}

func readGitAvailability(workspace string) GitAvailability {
	ctx, cancel := context.WithTimeout(context.Background(), gitSnapshotTimeout)
	defer cancel()
	rootText, err := runGit(ctx, workspace, "rev-parse", "--show-toplevel")
	if err != nil {
		return GitAvailability{}
	}
	root := filepath.Clean(strings.TrimSpace(rootText))
	branch := gitTrimmed(ctx, root, "branch", "--show-current")
	outgoing := resolveGitOutgoingRevision(ctx, root)
	ahead := outgoing.Ahead
	statusBytes, statusErr := runGitBytes(ctx, root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if statusErr != nil {
		applog.Errorf("read Git sidebar status failed: root=%q", root)
		return GitAvailability{IsRepository: true, Root: root, CurrentBranch: branch, Ahead: ahead}
	}
	files := parseGitStatus(string(statusBytes))
	availability := GitAvailability{IsRepository: true, Root: root, CurrentBranch: branch, ChangeCount: len(files), Ahead: ahead}
	for _, file := range files {
		availability.HasConflicts = availability.HasConflicts || file.Conflicted
	}
	return availability
}

// GetSessionGitRepository returns the complete lightweight Git dialog model.
func (a *App) GetSessionGitRepository(id int64) (GitRepositoryView, error) {
	workspace, err := a.sessionGitWorkspace(id)
	if err != nil {
		return GitRepositoryView{}, err
	}
	if a.gitMonitor != nil {
		if cached, ok := a.gitMonitor.Repository(workspace); ok {
			return cached, nil
		}
		a.gitMonitor.Ensure(workspace)
		return GitRepositoryView{
			Warming:   true,
			Conflicts: []GitFileChange{},
			Worktree:  GitChangeSet{Files: []GitFileChange{}},
			Remotes:   []GitRemote{},
			Branches:  []GitBranch{},
			Commits:   []GitCommit{},
			Stashes:   []GitStash{},
		}, nil
	}
	return readGitRepositoryView(workspace), nil
}

// RefreshSessionGitRepository rebuilds the cached Git model for a conversation
// workspace and notifies the UI through the git:workspace event. It exists so an
// open dialog can revalidate a cache that file events may have missed, instead of
// overlaying a partial read on top of cached data.
func (a *App) RefreshSessionGitRepository(id int64) {
	if a.gitMonitor == nil {
		return
	}
	workspace, err := a.sessionGitWorkspace(id)
	if err != nil {
		return
	}
	a.gitMonitor.RefreshNow(workspace, true)
}

func readGitRepositoryView(workspace string) GitRepositoryView {
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
		Stashes:       []GitStash{},
	}
	if !snapshot.IsRepository {
		return view
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitSnapshotTimeout)
	defer cancel()
	rawBranch := gitTrimmed(ctx, view.Root, "branch", "--show-current")
	view.Detached = rawBranch == ""
	view.CurrentBranch = rawBranch
	view.Head = gitTrimmed(ctx, view.Root, "rev-parse", "--short", "HEAD")
	view.Upstream = gitTrimmed(ctx, view.Root, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	outgoing := resolveGitOutgoingRevision(ctx, view.Root)
	view.Behind, view.Ahead = outgoing.Behind, outgoing.Ahead
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
	view.Stashes = listGitStashes(ctx, view.Root)
	return view
}

// GetSessionGitOutgoingCommits returns every commit that has not reached the
// current branch's push destination, including per-commit file statistics.
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
	outgoing := resolveGitOutgoingRevision(ctx, root)
	if len(outgoing.Args) == 0 {
		return []GitCommit{}, nil
	}
	commits, err := listGitOutgoingCommits(ctx, root, outgoing.Args)
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
	var resultMessage string
	var hasConflicts bool
	var stashKept bool
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
		output, hasConflicts, stashKept, err = runGitPullWithAutoStash(ctx, root, req.Language)
		if hasConflicts {
			if err != nil {
				resultMessage = gitLocalizedText(req.Language, "Pull 未完成，恢复本地改动时产生了冲突；原搁置已保留，请先解决冲突", "Pull did not complete, and restoring local changes caused conflicts. The original stash was kept; resolve the conflicts first")
				err = nil
			} else {
				resultMessage = gitLocalizedText(req.Language, "Pull 已完成，但恢复本地改动时产生了冲突；原搁置已保留，请先解决冲突", "Pull completed, but restoring local changes caused conflicts. The original stash was kept; resolve the conflicts first")
			}
		} else if stashKept && err == nil {
			resultMessage = gitLocalizedText(req.Language, "Pull 已完成并恢复本地改动；自动搁置仍保留，可稍后删除", "Pull completed and local changes were restored. The automatic stash was kept and can be deleted later")
		}
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
	case "stash_create":
		if detectGitRepositoryState(ctx, root) != "" {
			return GitOperationResult{}, gitLocalizedError(req.Language, "当前 Git 操作尚未完成，请先解决冲突或中止操作", "The current Git operation is incomplete; resolve its conflicts or abort it first")
		}
		message, messageErr := validateGitStashMessage(req.Message, req.Language)
		if messageErr != nil {
			return GitOperationResult{}, messageErr
		}
		paths, pathErr := normalizeGitStashPaths(root, req.Paths)
		if pathErr != nil {
			return GitOperationResult{}, gitLocalizedError(req.Language, "请选择要搁置的有效文件", "Select valid files to stash")
		}
		output, _, err = createGitStash(ctx, root, message, paths)
	case "stash_apply":
		if detectGitRepositoryState(ctx, root) != "" {
			return GitOperationResult{}, gitLocalizedError(req.Language, "当前 Git 操作尚未完成，请先解决冲突或中止操作", "The current Git operation is incomplete; resolve its conflicts or abort it first")
		}
		output, hasConflicts, stashKept, err = applyAndDropGitStash(ctx, root, req.StashHash)
		if hasConflicts {
			resultMessage = gitLocalizedText(req.Language, "搁置已恢复，但产生了冲突；原搁置已保留，请先解决冲突", "The stash was restored with conflicts. The original stash was kept; resolve the conflicts first")
		} else if stashKept && err == nil {
			resultMessage = gitLocalizedText(req.Language, "改动已恢复，但搁置未能自动删除，可稍后手动删除", "Changes were restored, but the stash could not be deleted automatically; you can delete it later")
		}
	case "stash_drop":
		output, err = dropGitStash(ctx, root, req.StashHash)
	case "checkout":
		branch := strings.TrimSpace(req.Branch)
		if !validLocalGitBranch(ctx, root, branch) {
			return GitOperationResult{}, gitLocalizedError(req.Language, "本地分支不存在", "The local branch does not exist")
		}
		output, hasConflicts, stashKept, err = runGitBranchOperationWithAutoStash(ctx, root, op, branch, req.Language, func() (string, error) {
			return runGit(ctx, root, "checkout", branch)
		})
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
		output, hasConflicts, stashKept, err = runGitBranchOperationWithAutoStash(ctx, root, op, branch, req.Language, func() (string, error) {
			if validLocalGitBranch(ctx, root, localName) && gitTrimmed(ctx, root, "rev-parse", "--abbrev-ref", localName+"@{upstream}") == branch {
				return runGit(ctx, root, "checkout", localName)
			}
			return runGit(ctx, root, "checkout", "--track", branch)
		})
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
		output, hasConflicts, stashKept, err = runGitBranchOperationWithAutoStash(ctx, root, op, branch, req.Language, func() (string, error) {
			return runGit(ctx, root, "checkout", "-b", branch, startPoint)
		})
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
	if hasConflicts && resultMessage == "" && (op == "checkout" || op == "checkout_remote" || op == "create_branch") {
		resultMessage = gitLocalizedText(req.Language, "分支已切换，当前改动已恢复但存在冲突；原搁置已保留", "The branch was switched and the changes were restored with conflicts. The original stash was kept")
	} else if stashKept && resultMessage == "" && (op == "checkout" || op == "checkout_remote" || op == "create_branch") {
		resultMessage = gitLocalizedText(req.Language, "分支已切换，当前改动已恢复；自动搁置仍保留，可稍后删除", "The branch was switched and changes were restored. The automatic stash was kept and can be deleted later")
	}
	if err != nil {
		applog.Errorf("Git repository operation failed: op=%q root=%q category=%q", op, root, gitOperationErrorCategory(err))
		if a.gitMonitor != nil {
			a.gitMonitor.RefreshNow(workspace, true)
		}
		return GitOperationResult{}, friendlyGitOperationError(op, req.Language, err)
	}
	if op == "fetch" || op == "pull" || op == "push" {
		output = ""
	}
	if a.gitMonitor != nil {
		a.gitMonitor.RefreshNow(workspace, true)
	}
	if resultMessage == "" {
		resultMessage = gitOperationSuccessMessage(op, req.Language)
	}
	conflicts := []GitFileChange{}
	if hasConflicts {
		conflicts = gitCurrentConflictFiles(ctx, root)
	}
	return GitOperationResult{Message: resultMessage, Output: strings.TrimSpace(output), HasConflicts: hasConflicts, StashKept: stashKept, Conflicts: conflicts}, nil
}

func runGitInput(ctx context.Context, root, input string, args ...string) (string, error) {
	commandArgs := append([]string{"-C", root}, args...)
	command := exec.CommandContext(ctx, "git", commandArgs...)
	configureGitProcess(command)
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
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

type gitOutgoingRevision struct {
	Args   []string
	Ahead  int
	Behind int
}

// resolveGitOutgoingRevision resolves the bounded revision set used by every
// "commits to push" view. A new branch without a remote counterpart contains
// commits reachable from HEAD but absent from every ref of the push remote.
func resolveGitOutgoingRevision(ctx context.Context, root string) gitOutgoingRevision {
	upstream := gitTrimmed(ctx, root, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	if upstream != "" {
		behind, ahead := gitAheadBehind(ctx, root, upstream, "HEAD")
		return gitOutgoingRevision{Args: []string{"--end-of-options", upstream + "..HEAD"}, Ahead: ahead, Behind: behind}
	}
	branch := gitTrimmed(ctx, root, "branch", "--show-current")
	if branch == "" {
		return gitOutgoingRevision{}
	}
	remote := preferredGitRemote(ctx, root)
	if remote == "" {
		return gitOutgoingRevision{}
	}
	remoteBranch := remote + "/" + branch
	if _, err := runGit(ctx, root, "show-ref", "--verify", "--quiet", "refs/remotes/"+remoteBranch); err == nil {
		behind, ahead := gitAheadBehind(ctx, root, remoteBranch, "HEAD")
		return gitOutgoingRevision{Args: []string{"--end-of-options", remoteBranch + "..HEAD"}, Ahead: ahead, Behind: behind}
	}

	args := []string{"HEAD", "--not", "--remotes=" + remote}
	return gitOutgoingRevision{Args: args, Ahead: gitRevisionCount(ctx, root, args)}
}

func gitRevisionCount(ctx context.Context, root string, revisions []string) int {
	args := append([]string{"rev-list", "--count"}, revisions...)
	count, err := strconv.Atoi(gitTrimmed(ctx, root, args...))
	if err != nil || count < 0 {
		return 0
	}
	return count
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

func listGitOutgoingCommits(ctx context.Context, root string, revisions []string) ([]GitCommit, error) {
	format := "%x1e%H%x00%h%x00%P%x00%an%x00%ae%x00%at%x00%s%x00%B%x00"
	statusArgs := []string{"log", "--date-order", "--pretty=format:" + format, "--name-status", "-z", "--no-renames"}
	statusArgs = append(statusArgs, revisions...)
	statusArgs = append(statusArgs, "--")
	statusOutput, err := runGit(ctx, root, statusArgs...)
	if err != nil {
		return nil, err
	}
	commits := parseGitOutgoingCommits(statusOutput)
	if len(commits) == 0 {
		return []GitCommit{}, nil
	}

	statFormat := "%x1e%H%x00"
	statArgs := []string{"log", "--date-order", "--pretty=format:" + statFormat, "--numstat", "-z", "--no-renames"}
	statArgs = append(statArgs, revisions...)
	statArgs = append(statArgs, "--")
	statOutput, err := runGit(ctx, root, statArgs...)
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
	case strings.Contains(message, "invalid stash hash") || strings.Contains(message, "stash entry not found"):
		return gitLocalizedError(language, "该搁置已不存在，请刷新后重试", "The stash no longer exists; refresh and try again")
	case strings.Contains(message, "no selected changes were stashed"):
		return gitLocalizedError(language, "所选文件没有可搁置的改动", "The selected files have no changes to stash")
	case strings.Contains(message, "automatic stash restore failed"):
		return gitLocalizedError(language, "分支已切换，但自动恢复改动失败；改动仍安全保存在搁置中", "The branch was switched, but changes could not be restored automatically. They remain safely stored in the stash")
	case strings.Contains(message, "automatic pull stash restore failed after pull completed"):
		return gitLocalizedError(language, "Pull 已完成，但自动恢复本地改动失败；改动仍安全保存在搁置中", "Pull completed, but local changes could not be restored automatically. They remain safely stored in the stash")
	case strings.Contains(message, "automatic pull stash restore failed after pull failure"):
		return gitLocalizedError(language, "Pull 未完成，自动恢复本地改动时发生冲突或失败；原改动仍安全保存在搁置中", "Pull did not complete, and restoring local changes caused conflicts or failed. The original changes remain safely stored in the stash")
	case strings.Contains(message, "automatic pull stash kept after pull failure"):
		return gitLocalizedError(language, "Pull 未完成，本地改动已恢复；自动搁置仍保留，可稍后删除", "Pull did not complete. Local changes were restored, and the automatic stash was kept and can be deleted later")
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
		"stash_create": "改动已搁置", "stash_apply": "搁置已恢复到当前分支", "stash_drop": "搁置已删除",
		"reset_mixed": "已回退到所选提交，后续改动已保留在工作区", "reset_hard": "已回退到所选提交并清空已跟踪改动",
	}
	enMessages := map[string]string{
		"stage_all": "All changes staged", "unstage_all": "All changes unstaged", "commit": "Commit created",
		"fetch": "Remote refs updated", "pull": "Latest commits pulled", "push": "Commits pushed",
		"checkout": "Branch switched", "checkout_remote": "Tracking branch created and checked out", "create_branch": "Branch created and checked out", "abort": "Git operation aborted",
		"stash_create": "Changes stashed", "stash_apply": "Stash restored to the current branch", "stash_drop": "Stash deleted",
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
