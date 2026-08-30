package app

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"codingto/internal/applog"
)

const maxGitFileAnalysisPromptDiff = 32000

// GitFileAnalysisRequest selects one Git file change and the model used to review it.
type GitFileAnalysisRequest struct {
	SessionID  int64  `json:"sessionId"`
	Scope      string `json:"scope"`
	Path       string `json:"path"`
	BaseBranch string `json:"baseBranch,omitempty"`
	Commit     string `json:"commit,omitempty"`
	Language   string `json:"language,omitempty"`
	Provider   string `json:"provider,omitempty"`
	Model      string `json:"model,omitempty"`
}

// GitFileAnalysisResult contains an isolated model review of one file change.
type GitFileAnalysisResult struct {
	Analysis string `json:"analysis"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// GenerateSessionGitFileAnalysis reviews one validated Git file change without mutating the conversation.
func (a *App) GenerateSessionGitFileAnalysis(req GitFileAnalysisRequest) (GitFileAnalysisResult, error) {
	item, ok, err := a.store.Store().SessionByID(req.SessionID)
	if err != nil {
		return GitFileAnalysisResult{}, err
	}
	if !ok {
		return GitFileAnalysisResult{}, gitLocalizedError(req.Language, "对话不存在", "The conversation does not exist")
	}

	scope := strings.TrimSpace(req.Scope)
	path := filepath.ToSlash(strings.TrimSpace(req.Path))
	if path == "" {
		return GitFileAnalysisResult{}, gitLocalizedError(req.Language, "请选择要分析的文件", "Select a file to analyze")
	}

	var detail GitFileDetail
	switch scope {
	case "worktree", "staged", "unstaged", "untracked", "branch":
		detail, err = a.GetSessionGitFileDetail(req.SessionID, scope, path, strings.TrimSpace(req.BaseBranch))
	case "commit":
		detail, err = a.GetSessionGitCommitFileDetail(GitCommitFileDetailRequest{
			SessionID: req.SessionID,
			Commit:    strings.TrimSpace(req.Commit),
			Path:      path,
			Language:  req.Language,
		})
	default:
		return GitFileAnalysisResult{}, gitLocalizedError(req.Language, "文件对比范围不合法", "The file comparison scope is invalid")
	}
	if err != nil {
		return GitFileAnalysisResult{}, err
	}
	if detail.Kind != "text" || len(detail.Hunks) == 0 {
		return GitFileAnalysisResult{}, gitLocalizedError(req.Language, "当前文件没有可供模型分析的文本差异", "This file has no text diff for the model to analyze")
	}

	workspace, err := a.sessionGitWorkspace(req.SessionID)
	if err != nil {
		return GitFileAnalysisResult{}, err
	}
	provider := strings.TrimSpace(req.Provider)
	model := strings.TrimSpace(req.Model)
	if provider == "" || model == "" {
		provider, model = item.Provider, item.Model
	}
	diff := truncateGitFileAnalysisDiff(formatGitFileAnalysisDiff(detail.Hunks))
	language := "English"
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(req.Language)), "zh") {
		language = "Chinese"
	}
	instructions, _, promptErr := a.effectiveGitAIPrompt(gitAIPromptFileAnalysis)
	if promptErr != nil {
		applog.Errorf("read Git file analysis prompt failed: %v", promptErr)
		return GitFileAnalysisResult{}, gitLocalizedError(req.Language, "读取文件分析提示词失败", "Failed to load the file analysis prompt")
	}
	prompt := fmt.Sprintf(`%s

Requested response language: %s

File: %s
Scope: %s
Status: %s
Added lines: %d
Deleted lines: %d

Diff:
%s`, instructions, language, detail.Path, scope, detail.Status, detail.Added, detail.Deleted, diff)

	output, generationErr := a.agent.generateIsolatedText(provider, model, workspace, prompt)
	if generationErr != nil {
		applog.Infof("Git file analysis model unavailable: scope=%s path=%q", scope, detail.Path)
		return GitFileAnalysisResult{}, gitLocalizedError(req.Language,
			"模型分析失败，请检查所选模型的授权与配置",
			"Model analysis failed; check the selected model's authorization and configuration")
	}
	analysis := cleanGitFileAnalysis(output)
	if analysis == "" {
		return GitFileAnalysisResult{}, gitLocalizedError(req.Language, "模型未返回分析结果", "The model returned no analysis")
	}
	return GitFileAnalysisResult{Analysis: analysis, Provider: provider, Model: model}, nil
}

func formatGitFileAnalysisDiff(hunks []DiffHunk) string {
	var builder strings.Builder
	for _, hunk := range hunks {
		builder.WriteString(hunk.Header)
		builder.WriteByte('\n')
		for _, line := range hunk.Lines {
			prefix := " "
			switch line.Kind {
			case "added":
				prefix = "+"
			case "deleted":
				prefix = "-"
			}
			oldNumber, newNumber := "", ""
			if line.OldNumber > 0 {
				oldNumber = fmt.Sprintf("%d", line.OldNumber)
			}
			if line.NewNumber > 0 {
				newNumber = fmt.Sprintf("%d", line.NewNumber)
			}
			fmt.Fprintf(&builder, "%s %6s %6s | %s\n", prefix, oldNumber, newNumber, line.Text)
		}
	}
	return builder.String()
}

func truncateGitFileAnalysisDiff(diff string) string {
	if len(diff) <= maxGitFileAnalysisPromptDiff {
		return diff
	}
	end := maxGitFileAnalysisPromptDiff
	for end > 0 && !utf8.ValidString(diff[:end]) {
		end--
	}
	return diff[:end] + "\n[diff truncated]"
}

func cleanGitFileAnalysis(output string) string {
	analysis := strings.TrimSpace(strings.ReplaceAll(output, "\r\n", "\n"))
	if len([]rune(analysis)) > 20000 {
		analysis = string([]rune(analysis)[:20000])
	}
	return strings.TrimSpace(analysis)
}
