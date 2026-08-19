package steward

import (
	"strings"
)

// OnTaskSettled is called by the app when a bot-managed session's operation
// truly ends (agent_settled without pending subagents, or a terminal error /
// abort). It composes the result summary and delivers it to the originating
// channel.
func (s *Service) OnTaskSettled(sessionID int64, event map[string]any) {
	title := ""
	if task, ok := s.tasks[sessionID]; ok {
		title = task.TaskBrief
	}
	status, text := composeResult(event, title)
	s.logger.Printf("task settled: session=%d event=%s status=%s result=%q", sessionID, str(event["type"]), status, stewardLogPreview(text))
	s.FinishTask(sessionID, status, text)
}

// composeResult derives a task status and human-readable summary from the
// terminal event. It never relies on agent_end's changeSummary (absent while
// subagents are still running); it extracts the assistant's final text. The
// optional task title (truncated to 100 runes) is prefixed so the notification
// carries both what was asked and its result — an error counts as a result.
func composeResult(event map[string]any, title string) (string, string) {
	eventType := str(event["type"])
	status := "finished"
	var lines []string

	if sid := strings.TrimSpace(str(event["sessionId"])); sid != "" && sid != "0" {
		lines = append(lines, "💬 对话ID："+sid)
	}

	if title = strings.TrimSpace(title); title != "" {
		runes := []rune(title)
		if len(runes) > 100 {
			title = string(runes[:100]) + "…"
		}
		lines = append(lines, "📋 任务："+title)
	}

	if question := strings.TrimSpace(str(event["firstQuestion"])); question != "" {
		lines = append(lines, "❓ 问题："+question)
	}

	// 会话最终失败（error 事件，或管家标记的 failed）：明确上报失败并附带真实
	// 错误信息，绝不把最后一条可用文本当作成功结果回传。
	failed := eventType == "error" || str(event["status"]) == "failed" || str(event["errorMessage"]) != ""
	if failed {
		status = "failed"
		lines = append(lines, "❌ 任务失败")
		if msg := str(event["errorMessage"]); msg != "" {
			lines = append(lines, msg)
		} else if msg := str(event["message"]); msg != "" {
			lines = append(lines, msg)
		}
	} else {
		lines = append(lines, "✅ 任务完成")
		if text := extractFinalMessage(event); text != "" {
			lines = append(lines, text)
		}
	}

	summary := strings.TrimSpace(strings.Join(lines, "\n"))
	if summary == "" {
		summary = "✅ 任务完成"
	}
	// 不在此截断：完整结果交给 SendToChannel，由平台阈值（飞书/钉钉 2000 runes）
	// 按标题/段落结构分段逐条发送；同时 result_text 存库也是完整内容。
	return status, summary
}

// extractFinalMessage pulls the last assistant text from the common event
// shapes (agent_end messages array or a direct message field).
func extractFinalMessage(event map[string]any) string {
	if raw, ok := event["messages"].([]any); ok {
		for i := len(raw) - 1; i >= 0; i-- {
			if m, ok := raw[i].(map[string]any); ok {
				if role := str(m["role"]); role != "assistant" {
					continue
				}
				if content := str(m["content"]); content != "" {
					return content
				}
				if text := str(m["text"]); text != "" {
					return text
				}
			}
		}
	}
	if msg := str(event["message"]); msg != "" {
		return msg
	}
	if text := str(event["text"]); text != "" {
		return text
	}
	return ""
}
