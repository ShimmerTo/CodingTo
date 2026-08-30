package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"codingto/internal/applog"
)

const maxGitCommitPromptDiff = 32000

// GitCommitMessageRequest asks for a commit message based only on staged changes.
type GitCommitMessageRequest struct {
	SessionID int64  `json:"sessionId"`
	Language  string `json:"language,omitempty"`
	Provider  string `json:"provider,omitempty"`
	Model     string `json:"model,omitempty"`
}

// GitCommitMessageResult contains an editable generated commit message.
type GitCommitMessageResult struct {
	Message  string `json:"message"`
	AI       bool   `json:"ai"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Notice   string `json:"notice,omitempty"`
}

// GenerateSessionGitCommitMessage generates a repository-style message for staged changes.
func (a *App) GenerateSessionGitCommitMessage(req GitCommitMessageRequest) (GitCommitMessageResult, error) {
	workspace, err := a.sessionGitWorkspace(req.SessionID)
	if err != nil {
		return GitCommitMessageResult{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitSnapshotTimeout)
	defer cancel()
	rootText, err := runGit(ctx, workspace, "rev-parse", "--show-toplevel")
	if err != nil {
		return GitCommitMessageResult{}, gitLocalizedError(req.Language, "当前工作区不是 Git 仓库", "The current workspace is not a Git repository")
	}
	root := filepath.Clean(strings.TrimSpace(rootText))
	nameStatus, err := runGit(ctx, root, "diff", "--cached", "--name-status", "--")
	if err != nil || strings.TrimSpace(nameStatus) == "" {
		return GitCommitMessageResult{}, gitLocalizedError(req.Language, "暂存区为空，请先暂存要提交的文件", "The index is empty; stage the files you want to commit first")
	}
	fallback := fallbackGitCommitMessage(nameStatus, req.Language)
	stat, statErr := runGit(ctx, root, "diff", "--cached", "--stat", "--")
	if statErr != nil {
		return GitCommitMessageResult{Message: fallback, AI: false}, nil
	}
	diff, diffErr := runGit(ctx, root, "diff", "--cached", "--no-ext-diff", "--unified=2", "--")
	if diffErr != nil {
		return GitCommitMessageResult{Message: fallback, AI: false}, nil
	}
	diff = truncateGitCommitPromptDiff(diff)
	history, historyErr := runGit(ctx, root, "log", "-12", "--pretty=format:%s")
	if historyErr != nil {
		history = ""
	}
	language := "English"
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(req.Language)), "zh") {
		language = "Chinese"
	}
	instructions, _, promptErr := a.effectiveGitAIPrompt(gitAIPromptCommit)
	if promptErr != nil {
		applog.Errorf("read Git commit prompt failed: %v", promptErr)
		return GitCommitMessageResult{}, gitLocalizedError(req.Language, "读取提交信息提示词失败", "Failed to load the commit message prompt")
	}
	prompt := fmt.Sprintf(`%s

Requested response language: %s

Recent subjects:
%s

Staged name/status:
%s

Staged stat:
%s

Staged diff:
%s`, instructions, language, history, nameStatus, stat, diff)
	provider := strings.TrimSpace(req.Provider)
	model := strings.TrimSpace(req.Model)
	if provider == "" || model == "" {
		cfg := a.store.Get()
		provider = strings.TrimSpace(cfg.DefaultProvider)
		model = strings.TrimSpace(cfg.DefaultModel)
	}
	output, generationErr := a.agent.generateIsolatedText(provider, model, root, prompt)
	if generationErr != nil {
		// The raw provider error can contain endpoint or credential-adjacent data;
		// keep the log intentionally categorical and fall back deterministically.
		applog.Infof("Git commit message model unavailable; using deterministic fallback")
		notice := "AI 模型暂不可用，已生成基础提交信息；请检查所选模型的授权与配置"
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(req.Language)), "en") {
			notice = "The AI model is unavailable. A basic commit message was generated; check the selected model's authorization and configuration"
		}
		return GitCommitMessageResult{Message: fallback, AI: false, Provider: provider, Model: model, Notice: notice}, nil
	}
	message := cleanGeneratedGitMessage(output)
	if message == "" {
		return GitCommitMessageResult{Message: fallback, AI: false, Provider: provider, Model: model}, nil
	}
	return GitCommitMessageResult{Message: message, AI: true, Provider: provider, Model: model}, nil
}

func truncateGitCommitPromptDiff(diff string) string {
	if len(diff) <= maxGitCommitPromptDiff {
		return diff
	}
	end := maxGitCommitPromptDiff
	for end > 0 && !utf8.ValidString(diff[:end]) {
		end--
	}
	return diff[:end] + "\n[diff truncated]"
}

func fallbackGitCommitMessage(nameStatus, language string) string {
	lines := strings.Split(strings.ReplaceAll(nameStatus, "\r\n", "\n"), "\n")
	paths := make([]string, 0, len(lines))
	statuses := make([]string, 0, len(lines))
	for _, line := range lines {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		statuses = append(statuses, parts[0])
		paths = append(paths, filepath.ToSlash(parts[1]))
	}
	chinese := strings.HasPrefix(strings.ToLower(strings.TrimSpace(language)), "zh")
	verb := "Update"
	if chinese {
		verb = "更新"
	}
	allAdded, allDeleted := len(statuses) > 0, len(statuses) > 0
	for _, status := range statuses {
		allAdded = allAdded && strings.HasPrefix(status, "A")
		allDeleted = allDeleted && strings.HasPrefix(status, "D")
	}
	if allAdded {
		if chinese {
			verb = "新增"
		} else {
			verb = "Add"
		}
	} else if allDeleted {
		if chinese {
			verb = "删除"
		} else {
			verb = "Remove"
		}
	}
	if len(paths) == 1 {
		return strings.TrimSpace(verb + " " + paths[0])
	}
	if chinese {
		return fmt.Sprintf("%s %d 个文件", verb, len(paths))
	}
	return fmt.Sprintf("%s %d files", verb, len(paths))
}

func cleanGeneratedGitMessage(output string) string {
	message := strings.TrimSpace(strings.ReplaceAll(output, "\r\n", "\n"))
	message = strings.TrimPrefix(message, "```text\n")
	message = strings.TrimPrefix(message, "```\n")
	message = strings.TrimSuffix(message, "```")
	message = strings.TrimSpace(message)
	message = strings.Trim(message, "\"")
	if len([]rune(message)) > 8000 {
		message = string([]rune(message)[:8000])
	}
	return strings.TrimSpace(message)
}
