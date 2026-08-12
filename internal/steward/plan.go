package steward

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"codingto/internal/store"
)

const planConfirmDialogPrefix = "__CODINGTO_PLAN_CONFIRM__:"
const dcgConfirmDialogPrefix = "__CODINGTO_DCG_CONFIRM__:"
const pendingApprovalReminderLimit = 10

// answerInboundPermission is intentionally limited to confirmations initiated
// by the resident steward itself. The resident is blocked inside ask_confirm
// while these requests are pending, so their answer must wake the waiter
// directly. Worker/session permissions are left to the next resident AI turn,
// which can inspect and answer them through steward RPC tools.
func (s *Service) answerInboundPermission(msg InboundMessage) bool {
	all := s.pendingPermissionsFor(msg)
	candidates := make([]*PermissionRequest, 0, len(all))
	for _, candidate := range all {
		if candidate.answerCh != nil {
			candidates = append(candidates, candidate)
		}
	}
	if len(candidates) == 0 {
		return false
	}

	if target, answerText := s.explicitPermissionTarget(candidates, msg.Text); target != nil {
		if _, ok := recognizedPermissionAnswer(target, answerText); !ok {
			return s.queuePermissionClarification(msg, "", []*PermissionRequest{target})
		}
		return s.answerPermissionFromInbound(msg, target, answerText)
	}

	matching := make([]*PermissionRequest, 0, len(candidates))
	for _, candidate := range candidates {
		if len(candidates) > 1 && (candidate.Method == "input" || candidate.Method == "editor") {
			continue
		}
		if _, ok := recognizedPermissionAnswer(candidate, msg.Text); ok {
			matching = append(matching, candidate)
		}
	}
	if len(matching) == 0 {
		return false
	}
	if len(matching) == 1 {
		return s.answerPermissionFromInbound(msg, matching[0], msg.Text)
	}
	return s.queuePermissionClarification(msg, msg.Text, matching)
}

func (s *Service) answerPermissionFromInbound(msg InboundMessage, request *PermissionRequest, answer string) bool {
	residentOwned := request.answerCh != nil
	resolved, err := s.AnswerPermission(request.RequestID, answer)
	if err != nil {
		s.logger.Printf("permission answer failed: request=%q error=%v", request.RequestID, err)
		_ = s.SendToChannel(msg.ChannelID, OutboundMessage{ThreadID: msg.ThreadID, Text: "处理授权回复失败：" + err.Error()})
		return true
	}
	if !residentOwned {
		decision := permissionAnswerSummary(resolved)
		text := fmt.Sprintf("工作项 [%s]（对话 #%d：%s）已由用户明确选择：%s。请使用 codingto_steward_reply 告知用户该操作已经应用到对应对话，不要声称处理了其他待办。", permissionCode(request), request.SessionID, request.Title, decision)
		fallback := fmt.Sprintf("已处理 [%s]：%s。", permissionCode(request), decision)
		if _, queueErr := s.enqueueStewardEvent(store.StewardEvent{
			Kind: "permission_resolved", SessionID: request.SessionID, RequestID: request.RequestID,
			ChannelID: msg.ChannelID, Sender: msg.SenderID, Thread: msg.ThreadID,
			ReceiveIDType: msg.ReceiveIDType, ReplyToMessageID: msg.ReplyToMessageID,
			PromptText: text, FallbackText: fallback, Priority: stewardPriorityPermission,
		}); queueErr != nil {
			_ = s.SendToChannel(msg.ChannelID, OutboundMessage{ThreadID: msg.ThreadID, Text: fallback})
		}
	}
	return true
}

func permissionAnswerSummary(answer *PermissionAnswer) string {
	if answer == nil {
		return "已处理"
	}
	if answer.Confirmed != nil {
		if *answer.Confirmed {
			return "批准"
		}
		return "拒绝"
	}
	if answer.Cancelled {
		return "取消"
	}
	return fmt.Sprintf("选择 %v", answer.Value)
}

func (s *Service) pendingPermissionsFor(msg InboundMessage) []*PermissionRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	candidates := make([]*PermissionRequest, 0)
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
	sort.SliceStable(candidates, func(i, j int) bool {
		if !candidates[i].CreatedAt.Equal(candidates[j].CreatedAt) {
			return candidates[i].CreatedAt.Before(candidates[j].CreatedAt)
		}
		return candidates[i].RecordID < candidates[j].RecordID
	})
	return candidates
}

// renderPendingApprovalReminder takes a live snapshot immediately before a
// task result is dispatched. Channel and sender are the primary isolation
// boundary; thread further narrows the result when it is available. A bounded
// list keeps the resident prompt and IM notification small under heavy load.
func (s *Service) renderPendingApprovalReminder(channelID int64, sender, thread string) string {
	if channelID <= 0 || (sender == "" && thread == "") {
		return ""
	}
	s.mu.Lock()
	candidates := make([]*PermissionRequest, 0)
	for _, request := range s.pending {
		if request.ChannelID != channelID {
			continue
		}
		if sender != "" && request.Sender != sender {
			continue
		}
		if sender == "" && thread != "" && request.ThreadID != thread {
			continue
		}
		if sender != "" && thread != "" && request.ThreadID != "" && request.ThreadID != thread {
			continue
		}
		candidates = append(candidates, request)
	}
	s.mu.Unlock()
	if len(candidates) == 0 {
		return ""
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if !candidates[i].CreatedAt.Equal(candidates[j].CreatedAt) {
			return candidates[i].CreatedAt.Before(candidates[j].CreatedAt)
		}
		return candidates[i].RecordID < candidates[j].RecordID
	})

	sessionTitles := make(map[int64]string)
	if sessions, err := s.app.ListSessions(); err == nil {
		for _, session := range sessions {
			sessionTitles[session.ID] = strings.TrimSpace(session.Title)
		}
	}

	visible := len(candidates)
	if visible > pendingApprovalReminderLimit {
		visible = pendingApprovalReminderLimit
	}
	var b strings.Builder
	fmt.Fprintf(&b, "另外，目前还有 %d 个待批准事项：\n", len(candidates))
	for i, candidate := range candidates[:visible] {
		source := sessionTitles[candidate.SessionID]
		if source == "" {
			source = "对话 #" + strconv.FormatInt(candidate.SessionID, 10)
		} else {
			source = fmt.Sprintf("%s（#%d）", source, candidate.SessionID)
		}
		kind := cleanPermissionTitle(candidate.Title)
		if kind == "" {
			kind = "审批/授权"
		}
		fmt.Fprintf(&b, "%d. [%s] %s：%s\n", i+1, permissionCode(candidate), source, kind)
	}
	if remaining := len(candidates) - visible; remaining > 0 {
		fmt.Fprintf(&b, "另有 %d 项未展开。\n", remaining)
	}
	b.WriteString("你可以直接用自然语言说明如何处理，例如“都批准”“第二项拒绝”或“按建议选项”；我会结合上下文执行，意图不清楚时再确认。")
	return b.String()
}

func permissionCode(request *PermissionRequest) string {
	prefix := "A"
	if len(request.Plan) > 0 || strings.Contains(strings.ToLower(request.Title), "计划") {
		prefix = "P"
	}
	if strings.Contains(request.Title, "危险命令") || strings.HasPrefix(request.Title, dcgConfirmDialogPrefix) {
		prefix = "D"
	}
	id := request.RecordID
	if id <= 0 {
		id = request.SessionID
	}
	return fmt.Sprintf("%s-%d", prefix, id)
}

func (s *Service) renderPermissionNotice(request *PermissionRequest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[%s] %s\n", permissionCode(request), request.Title)
	sessionTitle := s.sessionTitle(request.SessionID)
	if sessionTitle != "" {
		fmt.Fprintf(&b, "来源对话：%s（#%d）", sessionTitle, request.SessionID)
	} else {
		fmt.Fprintf(&b, "来源对话：#%d", request.SessionID)
	}
	if request.RunID != "" {
		fmt.Fprintf(&b, "（子 Agent %s）", request.RunID)
	}
	b.WriteString("\n")
	if body := strings.TrimSpace(request.Body); body != "" {
		b.WriteString(body)
		b.WriteString("\n")
	}
	if plan := renderPlanSteps(request.Plan); plan != "" {
		b.WriteString(plan)
		b.WriteString("\n")
	}
	if len(request.Options) > 0 {
		b.WriteString(appendOptionsBody("", request.Options))
		b.WriteString("\n")
	}
	if request.Method == "confirm" {
		fmt.Fprintf(&b, "请直接告诉我批准还是拒绝；有多个待办时也可以自然描述范围。工作项编号：%s。", permissionCode(request))
	} else {
		fmt.Fprintf(&b, "请直接告诉我你的选择或输入；工作项编号：%s。", permissionCode(request))
	}
	return strings.TrimSpace(b.String())
}

func (s *Service) explicitPermissionTarget(candidates []*PermissionRequest, text string) (*PermissionRequest, string) {
	lower := strings.ToLower(strings.TrimSpace(text))
	var matched *PermissionRequest
	matchedToken := ""
	for _, request := range candidates {
		tokens := []string{strings.ToLower(permissionCode(request)), strings.ToLower(request.RequestID)}
		if request.SessionID > 0 {
			tokens = append(tokens, "#"+strconv.FormatInt(request.SessionID, 10))
		}
		for _, token := range tokens {
			if token != "" && strings.Contains(lower, token) {
				if matched != nil && matched.RequestID != request.RequestID {
					return nil, text
				}
				matched, matchedToken = request, token
			}
		}
	}
	if matched == nil {
		return nil, text
	}
	answer := strings.TrimSpace(strings.ReplaceAll(lower, matchedToken, ""))
	return matched, answer
}

func stewardDialogContextKey(msg InboundMessage) string {
	return fmt.Sprintf("%d:%s:%s", msg.ChannelID, msg.SenderID, msg.ThreadID)
}

func (s *Service) queuePermissionClarification(msg InboundMessage, intent string, candidates []*PermissionRequest) bool {
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, candidate.RequestID)
	}
	raw, _ := json.Marshal(ids)
	_ = s.store.SaveStewardDialogState(store.StewardDialogState{
		ContextKey: stewardDialogContextKey(msg), ChannelID: msg.ChannelID, Sender: msg.SenderID,
		Thread: msg.ThreadID, Intent: intent, CandidatesJSON: string(raw),
	})
	text := s.renderClarificationQuestion(candidates)
	if intent == "" && len(candidates) == 1 {
		text = fmt.Sprintf("已选中 [%s]（对话 #%d：%s）。请明确回复批准或拒绝。", permissionCode(candidates[0]), candidates[0].SessionID, candidates[0].Title)
	}
	_, err := s.enqueueStewardEvent(store.StewardEvent{
		Kind: "permission_clarification", ChannelID: msg.ChannelID, Sender: msg.SenderID, Thread: msg.ThreadID,
		ReceiveIDType: msg.ReceiveIDType, ReplyToMessageID: msg.ReplyToMessageID,
		PromptText:   "用户的批准/拒绝目标不明确。请使用 codingto_steward_reply 询问用户具体选择哪一项，禁止替用户猜测：\n\n" + text,
		FallbackText: text, Priority: stewardPriorityPermission,
	})
	if err != nil {
		_ = s.SendToChannel(msg.ChannelID, OutboundMessage{ThreadID: msg.ThreadID, Text: text, Markdown: true})
	}
	return true
}

func (s *Service) renderClarificationQuestion(candidates []*PermissionRequest) string {
	var b strings.Builder
	b.WriteString("目前有多个待处理事项，请指定本次操作对应哪一项：\n")
	for i, candidate := range candidates {
		sessionTitle := s.sessionTitle(candidate.SessionID)
		if sessionTitle == "" {
			sessionTitle = "对话 #" + strconv.FormatInt(candidate.SessionID, 10)
		}
		fmt.Fprintf(&b, "%d. [%s] %s：%s\n", i+1, permissionCode(candidate), sessionTitle, candidate.Title)
	}
	b.WriteString("请回复序号或工作项编号；本次批准/拒绝意图会在选定后应用。")
	return b.String()
}

func (s *Service) answerPendingClarification(msg InboundMessage) bool {
	key := stewardDialogContextKey(msg)
	state, ok, err := s.store.StewardDialogStateByKey(key)
	if err != nil || !ok {
		return false
	}
	if time.Since(time.UnixMilli(state.CreatedAt)) > s.permissionTimeout {
		_ = s.store.DeleteStewardDialogState(key)
		return false
	}
	var ids []string
	if json.Unmarshal([]byte(state.CandidatesJSON), &ids) != nil {
		_ = s.store.DeleteStewardDialogState(key)
		return false
	}
	s.mu.Lock()
	candidates := make([]*PermissionRequest, 0, len(ids))
	for _, id := range ids {
		if request := s.pending[id]; request != nil {
			candidates = append(candidates, request)
		}
	}
	s.mu.Unlock()
	if len(candidates) == 0 {
		_ = s.store.DeleteStewardDialogState(key)
		return false
	}
	if state.Intent == "" && len(candidates) == 1 {
		if _, recognized := recognizedPermissionAnswer(candidates[0], msg.Text); recognized {
			_ = s.store.DeleteStewardDialogState(key)
			return s.answerPermissionFromInbound(msg, candidates[0], msg.Text)
		}
		// An unrelated natural-language message must not be swallowed for the
		// lifetime of a clarification window.
		return false
	}
	selected := s.selectClarificationCandidate(candidates, msg.Text)
	if selected == nil {
		if looksLikeClarificationSelection(candidates, msg.Text) {
			return s.queuePermissionClarification(msg, state.Intent, candidates)
		}
		return false
	}
	_ = s.store.DeleteStewardDialogState(key)
	return s.answerPermissionFromInbound(msg, selected, state.Intent)
}

func looksLikeClarificationSelection(candidates []*PermissionRequest, text string) bool {
	raw := strings.TrimSpace(text)
	if _, err := strconv.Atoi(raw); err == nil {
		return true
	}
	lower := strings.ToLower(raw)
	for _, candidate := range candidates {
		if strings.Contains(lower, strings.ToLower(permissionCode(candidate))) ||
			strings.Contains(lower, strings.ToLower(candidate.RequestID)) {
			return true
		}
	}
	return false
}

func (s *Service) selectClarificationCandidate(candidates []*PermissionRequest, text string) *PermissionRequest {
	raw := strings.TrimSpace(text)
	if index, err := strconv.Atoi(raw); err == nil && index >= 1 && index <= len(candidates) {
		return candidates[index-1]
	}
	lower := strings.ToLower(raw)
	var selected *PermissionRequest
	for _, candidate := range candidates {
		title := strings.ToLower(strings.TrimSpace(s.sessionTitle(candidate.SessionID)))
		if strings.Contains(lower, strings.ToLower(permissionCode(candidate))) ||
			strings.Contains(lower, strings.ToLower(candidate.RequestID)) ||
			(title != "" && lower == title) {
			if selected != nil && selected.RequestID != candidate.RequestID {
				return nil
			}
			selected = candidate
		}
	}
	return selected
}

func (s *Service) sessionTitle(sessionID int64) string {
	sessions, err := s.app.ListSessions()
	if err != nil {
		return ""
	}
	for _, session := range sessions {
		if session.ID == sessionID {
			return strings.TrimSpace(session.Title)
		}
	}
	return ""
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
	return fmt.Sprintf("%d:%s", sessionID, runID)
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
		for _, prefix := range []string{"☑", "✓", "✔", "[x]", "[X]"} {
			if strings.HasPrefix(text, prefix) {
				completed = true
				text = strings.TrimSpace(strings.TrimPrefix(text, prefix))
				break
			}
		}
		text = strings.TrimSpace(strings.TrimPrefix(text, "[ ]"))
		text = strings.TrimSpace(strings.TrimLeft(text, "☐○◯-"))
		if text == "" {
			continue
		}
		steps = append(steps, PlanStep{Index: index + 1, Text: text, Completed: completed})
	}
	return steps
}

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
	return strings.TrimRight(b.String(), "\n")
}

func appendPlanBody(body string, plan []PlanStep) string {
	steps := renderPlanSteps(plan)
	if steps == "" {
		return body
	}
	if strings.TrimSpace(body) == "" {
		return steps
	}
	return strings.TrimSpace(body) + "\n\n" + steps
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
	b.WriteString("可选项：\n")
	for i, option := range options {
		fmt.Fprintf(&b, "%d. %s", i+1, option.Label)
		if option.Description != "" {
			fmt.Fprintf(&b, " — %s", option.Description)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func cleanPermissionTitle(title string) string {
	if strings.HasPrefix(title, planConfirmDialogPrefix) {
		title = strings.TrimPrefix(title, planConfirmDialogPrefix)
		if title == "" {
			return "计划审批"
		}
	}
	if strings.HasPrefix(title, dcgConfirmDialogPrefix) {
		title = strings.TrimPrefix(title, dcgConfirmDialogPrefix)
		if title == "" {
			return "危险命令授权"
		}
	}
	return strings.TrimSpace(title)
}

func isCreateProfileOption(req *PermissionRequest, raw string) bool {
	raw = strings.TrimSpace(raw)
	if index, err := strconv.Atoi(raw); err == nil && index >= 1 && index <= len(req.Options) {
		return req.Options[index-1].CreateProfile
	}
	for _, option := range req.Options {
		if option.CreateProfile && (strings.EqualFold(raw, strings.TrimSpace(option.Label)) || strings.EqualFold(raw, strings.TrimSpace(option.Value))) {
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
