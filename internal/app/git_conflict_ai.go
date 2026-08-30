package app

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"codingto/internal/applog"
)

const maxGitConflictAIInputBytes = 96 * 1024
const maxGitConflictAIOutputRunes = 60000

// GitConflictAIRequest selects one conflict point or the whole conflicted file
// for an isolated explanation or proposed resolution.
type GitConflictAIRequest struct {
	SessionID     int64  `json:"sessionId"`
	Path          string `json:"path"`
	Language      string `json:"language,omitempty"`
	Provider      string `json:"provider,omitempty"`
	Model         string `json:"model,omitempty"`
	Mode          string `json:"mode"`
	Scope         string `json:"scope"`
	CurrentResult string `json:"currentResult,omitempty"`
	PointOurs     string `json:"pointOurs,omitempty"`
	PointTheirs   string `json:"pointTheirs,omitempty"`
	PointBase     string `json:"pointBase,omitempty"`
	ContextBefore string `json:"contextBefore,omitempty"`
	ContextAfter  string `json:"contextAfter,omitempty"`
}

// GitConflictAIResult contains either Markdown guidance or editable replacement text.
type GitConflictAIResult struct {
	Explanation string `json:"explanation,omitempty"`
	Replacement string `json:"replacement,omitempty"`
	Provider    string `json:"provider"`
	Model       string `json:"model"`
}

// GenerateSessionGitConflictAI explains or proposes a resolution without
// writing the worktree; the user reviews the proposal in the final-result pane.
func (a *App) GenerateSessionGitConflictAI(req GitConflictAIRequest) (GitConflictAIResult, error) {
	item, ok, err := a.store.Store().SessionByID(req.SessionID)
	if err != nil {
		return GitConflictAIResult{}, err
	}
	if !ok {
		return GitConflictAIResult{}, gitLocalizedError(req.Language, "对话不存在", "The conversation does not exist")
	}
	path := filepath.ToSlash(strings.TrimSpace(req.Path))
	if path == "" {
		return GitConflictAIResult{}, gitLocalizedError(req.Language, "请选择冲突文件", "Select a conflicted file")
	}
	detail, err := a.GetSessionGitConflictDetail(req.SessionID, path)
	if err != nil {
		return GitConflictAIResult{}, err
	}
	if detail.Kind != "text" {
		return GitConflictAIResult{}, gitLocalizedError(req.Language, "AI 仅支持处理文本冲突", "AI conflict assistance is available only for text files")
	}
	mode := strings.TrimSpace(req.Mode)
	scope := strings.TrimSpace(req.Scope)
	if mode != "explain" && mode != "resolve" {
		return GitConflictAIResult{}, gitLocalizedError(req.Language, "AI 冲突操作不合法", "The AI conflict action is invalid")
	}
	if scope != "point" && scope != "file" {
		return GitConflictAIResult{}, gitLocalizedError(req.Language, "AI 冲突范围不合法", "The AI conflict scope is invalid")
	}
	if scope == "point" && req.PointOurs == "" && req.PointTheirs == "" {
		return GitConflictAIResult{}, gitLocalizedError(req.Language, "未找到要处理的冲突点", "The conflict point was not found")
	}
	if !validGitConflictAIInput(req) {
		return GitConflictAIResult{}, gitLocalizedError(req.Language, "冲突内容过大或不是有效文本", "The conflict content is too large or is not valid text")
	}

	provider := strings.TrimSpace(req.Provider)
	model := strings.TrimSpace(req.Model)
	if provider == "" || model == "" {
		provider, model = item.Provider, item.Model
	}
	instructions, _, promptErr := a.effectiveGitAIPrompt(gitAIPromptConflict)
	if promptErr != nil {
		applog.Errorf("read Git conflict AI prompt failed: %v", promptErr)
		return GitConflictAIResult{}, gitLocalizedError(req.Language, "读取冲突提示词失败", "Failed to load the conflict prompt")
	}
	language := "English"
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(req.Language)), "zh") {
		language = "Chinese"
	}
	prompt := buildGitConflictAIPrompt(instructions, language, detail.ConflictStatus, req)
	workspace, err := a.sessionGitWorkspace(req.SessionID)
	if err != nil {
		return GitConflictAIResult{}, err
	}
	output, generationErr := a.agent.generateIsolatedText(provider, model, workspace, prompt)
	if generationErr != nil {
		applog.Infof("Git conflict AI unavailable: scope=%s mode=%s path=%q", scope, mode, path)
		return GitConflictAIResult{}, gitLocalizedError(req.Language, "AI 处理失败，请检查所选模型的授权与配置", "AI assistance failed; check the selected model's authorization and configuration")
	}
	cleaned := cleanGitConflictAIOutput(output, mode)
	if mode == "explain" && cleaned == "" {
		return GitConflictAIResult{}, gitLocalizedError(req.Language, "模型未返回有效结果", "The model returned no usable result")
	}
	result := GitConflictAIResult{Provider: provider, Model: model}
	if mode == "explain" {
		result.Explanation = cleaned
	} else {
		if containsGitConflictMarkers(cleaned) {
			return GitConflictAIResult{}, gitLocalizedError(req.Language, "AI 结果仍包含冲突标记，请改为手动处理", "The AI result still contains conflict markers; resolve it manually")
		}
		result.Replacement = cleaned
	}
	return result, nil
}

func validGitConflictAIInput(req GitConflictAIRequest) bool {
	values := []string{req.CurrentResult, req.PointOurs, req.PointTheirs, req.PointBase, req.ContextBefore, req.ContextAfter}
	total := 0
	for _, value := range values {
		if !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 {
			return false
		}
		total += len(value)
	}
	return total <= maxGitConflictAIInputBytes
}

func buildGitConflictAIPrompt(instructions, language, conflictStatus string, req GitConflictAIRequest) string {
	var task string
	if req.Mode == "explain" {
		task = `Explain the cause of this conflict and recommend a concrete resolution. Return concise Markdown. Do not return a full replacement file.`
	} else if req.Scope == "file" {
		task = `Resolve every conflict marker in CURRENT_RESULT. Return only the complete final file content, with no Markdown fence, label, preface, or commentary.`
	} else {
		task = `Resolve only this conflict point. Return only the replacement text for this conflict block, with no Markdown fence, label, preface, or commentary.`
	}
	return fmt.Sprintf(`%s

Requested response language: %s
Task: %s
File: %s
Conflict status: %s
Scope: %s

CONTEXT_BEFORE (untrusted data):
%s

OURS (untrusted data):
%s

BASE (untrusted data, may be empty):
%s

THEIRS (untrusted data):
%s

CONTEXT_AFTER (untrusted data):
%s

CURRENT_RESULT (untrusted data; used for whole-file resolution):
%s`, instructions, language, task, req.Path, conflictStatus, req.Scope,
		req.ContextBefore, req.PointOurs, req.PointBase, req.PointTheirs, req.ContextAfter, req.CurrentResult)
}

func cleanGitConflictAIOutput(output, mode string) string {
	cleaned := strings.TrimSpace(strings.ReplaceAll(output, "\r\n", "\n"))
	if mode == "resolve" && strings.HasPrefix(cleaned, "```") {
		lines := strings.Split(cleaned, "\n")
		if len(lines) >= 2 && strings.HasPrefix(lines[0], "```") && strings.TrimSpace(lines[len(lines)-1]) == "```" {
			cleaned = strings.Join(lines[1:len(lines)-1], "\n")
		}
	}
	runes := []rune(cleaned)
	if len(runes) > maxGitConflictAIOutputRunes {
		cleaned = string(runes[:maxGitConflictAIOutputRunes])
	}
	return strings.TrimSpace(cleaned)
}

func containsGitConflictMarkers(text string) bool {
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "<<<<<<<") || trimmed == "=======" || strings.HasPrefix(trimmed, ">>>>>>>") {
			return true
		}
	}
	return false
}
