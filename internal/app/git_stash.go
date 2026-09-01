package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"codingto/internal/applog"
)

const maxGitStashMessageRunes = 200
const maxGitStashFiles = 10000
const maxGitStashPathBytes = 1024 * 1024

func listGitStashes(ctx context.Context, root string) []GitStash {
	output, err := runGit(ctx, root, "stash", "list", "--format=%H%x1f%gd%x1f%gs%x1f%ct%x1e")
	if err != nil {
		applog.Errorf("read Git stash list failed: root=%q category=%q", root, gitOperationErrorCategory(err))
		return []GitStash{}
	}
	stashes := make([]GitStash, 0)
	for _, record := range strings.Split(output, "\x1e") {
		record = strings.Trim(record, "\r\n")
		if record == "" {
			continue
		}
		fields := strings.Split(record, "\x1f")
		if len(fields) != 4 || !validGitCommitHash(strings.TrimSpace(fields[0])) {
			continue
		}
		hash := strings.TrimSpace(fields[0])
		ref := strings.TrimSpace(fields[1])
		subject := strings.TrimSpace(fields[2])
		timestamp, _ := parseInt64(strings.TrimSpace(fields[3]))
		branch, name := parseGitStashSubject(subject)
		stashes = append(stashes, GitStash{
			Hash: hash, Ref: ref, Name: name, Branch: branch, Subject: subject,
			Timestamp: timestamp * 1000,
		})
	}
	return stashes
}

func parseInt64(value string) (int64, error) {
	var parsed int64
	_, err := fmt.Sscan(value, &parsed)
	return parsed, err
}

func parseGitStashSubject(subject string) (string, string) {
	for _, prefix := range []string{"On ", "WIP on "} {
		if !strings.HasPrefix(subject, prefix) {
			continue
		}
		remainder := strings.TrimPrefix(subject, prefix)
		if separator := strings.Index(remainder, ": "); separator >= 0 {
			branch := strings.TrimSpace(remainder[:separator])
			name := strings.TrimSpace(remainder[separator+2:])
			if name != "" {
				return branch, name
			}
		}
	}
	if strings.TrimSpace(subject) == "" {
		return "", "Stash"
	}
	return "", strings.TrimSpace(subject)
}

func validateGitStashMessage(value, language string) (string, error) {
	message := strings.TrimSpace(strings.ReplaceAll(value, "\x00", ""))
	if message == "" {
		return "", gitLocalizedError(language, "搁置名称不能为空", "The stash name cannot be empty")
	}
	if len([]rune(message)) > maxGitStashMessageRunes {
		return "", gitLocalizedError(language, "搁置名称不能超过 200 个字符", "The stash name cannot exceed 200 characters")
	}
	for _, char := range message {
		if char < 0x20 || char == 0x7f {
			return "", gitLocalizedError(language, "搁置名称不能包含控制字符", "The stash name cannot contain control characters")
		}
	}
	return message, nil
}

func createGitStash(ctx context.Context, root, message string, paths []string) (string, string, error) {
	before := gitTrimmed(ctx, root, "rev-parse", "--verify", "refs/stash")
	args := []string{"stash", "push", "--include-untracked", "--message", message}
	if len(paths) > 0 {
		args = append(args, "--")
		args = append(args, paths...)
	}
	output, err := runGit(ctx, root, args...)
	if err != nil {
		return "", "", err
	}
	hash := gitTrimmed(ctx, root, "rev-parse", "--verify", "refs/stash")
	if hash == "" || strings.EqualFold(hash, before) {
		return "", "", errors.New("no selected changes were stashed")
	}
	return hash, output, nil
}

func createAutomaticGitStash(ctx context.Context, root, source, target, language string) (string, error) {
	status, err := runGitBytes(ctx, root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return "", err
	}
	files := parseGitStatus(string(status))
	if len(files) == 0 {
		return "", nil
	}
	name := fmt.Sprintf("CodingTo：%s → %s（%s）", source, target, time.Now().Format("2006-01-02 15:04:05"))
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(language)), "en") {
		name = fmt.Sprintf("CodingTo: %s -> %s (%s)", source, target, time.Now().Format("2006-01-02 15:04:05"))
	}
	hash, _, err := createGitStash(ctx, root, name, nil)
	return hash, err
}

func normalizeGitStashPaths(root string, requestedPaths []string) ([]string, error) {
	if len(requestedPaths) == 0 || len(requestedPaths) > maxGitStashFiles {
		return nil, fmt.Errorf("Git stash requires 1 to %d files", maxGitStashFiles)
	}
	paths := make([]string, 0, len(requestedPaths))
	seen := make(map[string]struct{}, len(requestedPaths))
	totalBytes := 0
	for _, requestedPath := range requestedPaths {
		path := strings.ReplaceAll(requestedPath, "\\", "/")
		if path == "" || path == "." || path == ".." || strings.ContainsRune(path, '\x00') {
			return nil, errors.New("invalid Git stash path")
		}
		if _, err := safeGitWorktreePath(root, path); err != nil {
			return nil, err
		}
		if _, exists := seen[path]; exists {
			continue
		}
		totalBytes += len(path)
		if totalBytes > maxGitStashPathBytes {
			return nil, errors.New("Git stash paths are too long")
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	if len(paths) == 0 {
		return nil, errors.New("Git stash has no unique files")
	}
	return paths, nil
}

func resolveGitStashRef(ctx context.Context, root, requestedHash string) (string, error) {
	hash := strings.TrimSpace(requestedHash)
	if !validGitCommitHash(hash) {
		return "", errors.New("invalid stash hash")
	}
	for _, stash := range listGitStashes(ctx, root) {
		if strings.EqualFold(stash.Hash, hash) {
			return stash.Ref, nil
		}
	}
	return "", errors.New("stash entry not found")
}

func applyAndDropGitStash(ctx context.Context, root, hash string) (string, bool, bool, error) {
	ref, err := resolveGitStashRef(ctx, root, hash)
	if err != nil {
		return "", false, false, err
	}
	output, applyErr := runGit(ctx, root, "stash", "apply", "--index", ref)
	if applyErr != nil {
		if gitRepositoryHasConflicts(ctx, root) {
			return output, true, true, nil
		}
		return "", false, true, applyErr
	}
	ref, err = resolveGitStashRef(ctx, root, hash)
	if err != nil {
		return output, false, true, nil
	}
	if _, dropErr := runGit(ctx, root, "stash", "drop", ref); dropErr != nil {
		applog.Errorf("drop applied Git stash failed: root=%q category=%q", root, gitOperationErrorCategory(dropErr))
		return output, false, true, nil
	}
	return output, false, false, nil
}

func dropGitStash(ctx context.Context, root, hash string) (string, error) {
	ref, err := resolveGitStashRef(ctx, root, hash)
	if err != nil {
		return "", err
	}
	return runGit(ctx, root, "stash", "drop", ref)
}

func gitRepositoryHasConflicts(ctx context.Context, root string) bool {
	return len(gitCurrentConflictFiles(ctx, root)) > 0
}

func gitCurrentConflictFiles(ctx context.Context, root string) []GitFileChange {
	status, err := runGitBytes(ctx, root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return []GitFileChange{}
	}
	conflicts := make([]GitFileChange, 0)
	for _, file := range parseGitStatus(string(status)) {
		if file.Conflicted {
			conflicts = append(conflicts, file)
		}
	}
	return conflicts
}

func runGitPullWithAutoStash(
	ctx context.Context,
	root, language string,
) (string, bool, bool, error) {
	if detectGitRepositoryState(ctx, root) != "" || gitRepositoryHasConflicts(ctx, root) {
		return "", false, false, gitLocalizedError(language, "当前 Git 操作尚未完成，请先解决冲突或中止操作", "The current Git operation is incomplete; resolve its conflicts or abort it first")
	}
	fetchOutput, err := runGit(ctx, root, "fetch")
	if err != nil {
		return "", false, false, err
	}
	head := gitTrimmed(ctx, root, "rev-parse", "--verify", "HEAD")
	upstreamHash := gitTrimmed(ctx, root, "rev-parse", "--verify", "@{upstream}")
	if head == "" || upstreamHash == "" {
		return "", false, false, errors.New("no upstream after fetch")
	}
	if strings.EqualFold(head, upstreamHash) {
		return fetchOutput, false, false, nil
	}
	if _, ancestorErr := runGit(ctx, root, "merge-base", "--is-ancestor", "HEAD", "@{upstream}"); ancestorErr != nil {
		if _, aheadErr := runGit(ctx, root, "merge-base", "--is-ancestor", "@{upstream}", "HEAD"); aheadErr == nil {
			return fetchOutput, false, false, nil
		}
		return "", false, false, errors.New("not possible to fast-forward")
	}
	source := gitTrimmed(ctx, root, "branch", "--show-current")
	if source == "" {
		source = gitTrimmed(ctx, root, "rev-parse", "--short", "HEAD")
	}
	upstream := gitTrimmed(ctx, root, "rev-parse", "--abbrev-ref", "@{upstream}")
	target := "Pull"
	if upstream != "" {
		target += " " + upstream
	}
	stashHash, err := createAutomaticGitStash(ctx, root, source, target, language)
	if err != nil {
		return "", false, false, err
	}

	output, pullErr := runGit(ctx, root, "merge", "--ff-only", "@{upstream}")
	if fetchOutput != "" {
		output = strings.TrimSpace(fetchOutput + "\n" + output)
	}
	if pullErr != nil {
		if stashHash == "" {
			return "", false, false, pullErr
		}
		_, conflicted, kept, restoreErr := applyAndDropGitStash(ctx, root, stashHash)
		if restoreErr != nil || conflicted {
			category := "conflict"
			if restoreErr != nil {
				category = gitOperationErrorCategory(restoreErr)
			}
			applog.Errorf("restore automatic Git stash after failed pull: root=%q category=%q", root, category)
			return "", conflicted, true, errors.New("automatic pull stash restore failed after pull failure")
		}
		if kept {
			return "", false, true, errors.New("automatic pull stash kept after pull failure")
		}
		return "", false, false, pullErr
	}
	if stashHash == "" {
		return output, false, false, nil
	}

	restoreOutput, conflicted, kept, restoreErr := applyAndDropGitStash(ctx, root, stashHash)
	if restoreErr != nil {
		applog.Errorf("restore automatic Git stash after pull: root=%q category=%q", root, gitOperationErrorCategory(restoreErr))
		return "", false, true, errors.New("automatic pull stash restore failed after pull completed")
	}
	if restoreOutput != "" {
		output = strings.TrimSpace(output + "\n" + restoreOutput)
	}
	return output, conflicted, kept, nil
}

func runGitBranchOperationWithAutoStash(
	ctx context.Context,
	root, operation, target, language string,
	runBranchOperation func() (string, error),
) (string, bool, bool, error) {
	if detectGitRepositoryState(ctx, root) != "" || gitRepositoryHasConflicts(ctx, root) {
		return "", false, false, gitLocalizedError(language, "当前 Git 操作尚未完成，请先解决冲突或中止操作", "The current Git operation is incomplete; resolve its conflicts or abort it first")
	}
	source := gitTrimmed(ctx, root, "branch", "--show-current")
	if source == "" {
		source = gitTrimmed(ctx, root, "rev-parse", "--short", "HEAD")
	}
	stashHash, err := createAutomaticGitStash(ctx, root, source, target, language)
	if err != nil {
		return "", false, false, err
	}
	output, switchErr := runBranchOperation()
	if switchErr != nil {
		if stashHash != "" {
			_, _, kept, restoreErr := applyAndDropGitStash(ctx, root, stashHash)
			if restoreErr != nil || kept {
				applog.Errorf("restore automatic Git stash after failed branch operation: op=%q root=%q category=%q", operation, root, gitOperationErrorCategory(firstGitError(restoreErr, switchErr)))
			}
		}
		return "", false, stashHash != "", switchErr
	}
	if stashHash == "" {
		return output, false, false, nil
	}
	restoreOutput, conflicted, kept, restoreErr := applyAndDropGitStash(ctx, root, stashHash)
	if restoreErr != nil {
		return "", false, true, errors.New("automatic stash restore failed")
	}
	if restoreOutput != "" {
		output = strings.TrimSpace(output + "\n" + restoreOutput)
	}
	return output, conflicted, kept, nil
}

func firstGitError(primary, fallback error) error {
	if primary != nil {
		return primary
	}
	return fallback
}
