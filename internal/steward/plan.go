package steward

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const planConfirmDialogPrefix = "__CODINGTO_PLAN_CONFIRM__:"

// answerInboundPermission routes the next message from the originating bot
// channel to the oldest matching pending UI request. Without this bridge,
// plan confirmations and browser profile choices were sent to the resident
// steward as ordinary prompts and the worker agent stayed blocked forever.
func (s *Service) answerInboundPermission(msg InboundMessage) bool {
	request := s.pendingPermissionFor(msg)
	if request == nil {
		return false
	}
	if _, err := s.AnswerPermission(request.RequestID, msg.Text); err != nil {
		s.logger.Printf("permission answer failed: request=%q error=%v", request.RequestID, err)
		_ = s.SendToChannel(msg.ChannelID, OutboundMessage{
			ThreadID: msg.ThreadID,
			Text:     "处理授权回复失败：" + err.Error(),
		})
	}
	return true
}

func (s *Service) pendingPermissionFor(msg InboundMessage) *PermissionRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	var candidates []*PermissionRequest
	for _, request := range s.pending {
		if request.ChannelID != msg.ChannelID {
			continue
		}
		if request.Sender != "" && request.Sender != msg.SenderID {
			continue
		}
		if request.ThreadID != "" && request.ThreadID != msg.ThreadID {
			continue
		}
		candidates = append(candidates, request)
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].CreatedAt.Before(candidates[j].CreatedAt)
	})
	return candidates[0]
}

// capturePlan remembers the latest plan widget for the main agent or a
// subagent. Widget events are non-blocking and arrive immediately before the
// corresponding confirm request.
func (s *Service) capturePlan(sessionID int64, runID string, event map[string]any) {
	if str(event["type"]) != "extension_ui_request" || str(event["method"]) != "setWidget" {
		return
	}
	key := planKey(sessionID, runID)
	widgetKey := str(event["widgetKey"])
	if widgetKey != "plan-todos" && widgetKey != "plan-execution" {
		return
	}
	plan := parsePlanLines(event["widgetLines"])
	s.mu.Lock()
	if len(plan) == 0 {
		delete(s.plans, key)
	} else {
		s.plans[key] = plan
	}
	s.mu.Unlock()
}

func planKey(sessionID int64, runID string) string {
	if runID == "" {
		return strconv.FormatInt(sessionID, 10)
	}
	return strconv.FormatInt(sessionID, 10) + ":" + runID
}

func (s *Service) takePlan(sessionID int64, runID string) []PlanStep {
	key := planKey(sessionID, runID)
	s.mu.Lock()
	defer s.mu.Unlock()
	plan := append([]PlanStep(nil), s.plans[key]...)
	delete(s.plans, key)
	return plan
}

func parsePlanLines(raw any) []PlanStep {
	var lines []string
	switch values := raw.(type) {
	case []string:
		lines = append(lines, values...)
	case []any:
		for _, value := range values {
			if text, ok := value.(string); ok {
				lines = append(lines, text)
			}
		}
	}
	steps := make([]PlanStep, 0, len(lines))
	for index, line := range lines {
		text := strings.TrimSpace(line)
		if text == "" {
			continue
		}
		completed := false
		for _, prefix := range []string{"☑", "✓", "✔"} {
			if strings.HasPrefix(text, prefix) {
				completed = true
				text = strings.TrimSpace(strings.TrimPrefix(text, prefix))
				break
			}
		}
		text = strings.TrimSpace(strings.TrimLeft(text, "☐○◯-"))
		if text == "" {
			continue
		}
		steps = append(steps, PlanStep{Index: index + 1, Text: text, Completed: completed})
	}
	return steps
}

// renderPlanSteps renders the ordered plan list (without the confirmation
// prompt), so a long plan can be sent separately from the confirm card.
func renderPlanSteps(plan []PlanStep) string {
	if len(plan) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("执行计划：\n")
	for _, step := range plan {
		mark := "☐"
		if step.Completed {
			mark = "☑"
		}
		fmt.Fprintf(&b, "%d. %s %s\n", step.Index, mark, step.Text)
	}
	return strings.TrimSpace(b.String())
}

func appendPlanBody(body string, plan []PlanStep) string {
	steps := renderPlanSteps(plan)
	if steps == "" {
		return body
	}
	var b strings.Builder
	if strings.TrimSpace(body) != "" {
		b.WriteString(strings.TrimSpace(body))
		b.WriteString("\n\n")
	}
	b.WriteString(steps)
	b.WriteString("\n\n请回复“确认”开始执行，或“拒绝”取消。")
	return b.String()
}

func appendOptionsBody(body string, options []CardOption) string {
	if len(options) == 0 {
		return body
	}
	var b strings.Builder
	b.WriteString(strings.TrimSpace(body))
	if b.Len() > 0 {
		b.WriteString("\n\n")
	}
	b.WriteString("可选项（回复序号或完整名称）：\n")
	for index, option := range options {
		fmt.Fprintf(&b, "%d. %s\n", index+1, option.Label)
	}
	return b.String()
}

func cleanPermissionTitle(title string) string {
	if strings.HasPrefix(title, planConfirmDialogPrefix) {
		title = strings.TrimPrefix(title, planConfirmDialogPrefix)
		if title == "" {
			return "计划审批"
		}
	}
	return title
}

func isCreateProfileOption(req *PermissionRequest, raw string) bool {
	raw = strings.TrimSpace(raw)
	if index, err := strconv.Atoi(raw); err == nil && index >= 1 && index <= len(req.Options) {
		return req.Options[index-1].CreateProfile
	}
	for _, option := range req.Options {
		if !option.CreateProfile {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(raw), strings.TrimSpace(option.Label)) ||
			strings.EqualFold(strings.TrimSpace(raw), strings.TrimSpace(option.Value)) {
			return true
		}
	}
	return false
}

func profileTargetURL(req *PermissionRequest) string {
	for _, option := range req.Options {
		if option.CreateProfile && option.TargetURL != "" {
			return option.TargetURL
		}
	}
	return ""
}
