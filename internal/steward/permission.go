package steward

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"codingto/internal/store"
)

// QueuePermission relays an interactive extension UI request (from a main
// agent or a subagent) to the bot user. The caller (app event dispatcher)
// must have already armed the watchdog; this method immediately acks so the
// steward owns the timeout, then sends a card and waits for the answer.
func (s *Service) QueuePermission(req PermissionRequest) error {
	if req.RequestID == "" {
		return fmt.Errorf("steward: permission request without id")
	}
	s.logger.Printf("permission queued: request=%q session=%d run=%q method=%s channel=%d title=%q", req.RequestID, req.SessionID, req.RunID, req.Method, req.ChannelID, stewardLogPreview(req.Title))
	scope := "once"
	optionsJSON := ""
	if len(req.Options) > 0 {
		raw, _ := json.Marshal(req.Options)
		optionsJSON = string(raw)
	}
	planJSON := ""
	if len(req.Plan) > 0 {
		raw, _ := json.Marshal(req.Plan)
		planJSON = string(raw)
	}
	record, err := s.store.CreateStewardPermission(store.StewardPermission{
		RequestID: req.RequestID, SessionID: req.SessionID, RunID: req.RunID,
		ChannelID: req.ChannelID, Sender: req.Sender, Thread: req.ThreadID, Method: req.Method,
		Title: req.Title, Body: req.Body, OptionsJSON: optionsJSON, PlanJSON: planJSON,
		ReceiveIDType: req.ReceiveIDType, ReplyToMessageID: req.ReplyToMessageID,
		Scope: scope, Status: "pending",
	})
	if err != nil {
		return err
	}
	s.mu.Lock()
	req.RecordID = record.ID
	req.CreatedAt = time.UnixMilli(record.CreatedAt)
	s.pending[req.RequestID] = &req
	s.permissionWaiters[req.RequestID] = make(chan struct{})
	s.mu.Unlock()

	// Ack immediately (async) so the 90s watchdog is disarmed; the steward
	// timeout below replaces it. For subagent requests this satisfies the
	// bridge's ack window.
	if req.RunID != "" {
		s.launchBackground(func() { _ = s.app.AckSubagentUI(req.SessionID, req.RunID, req.RequestID) })
	} else {
		s.launchBackground(func() { _ = s.app.AckExtensionUI(req.SessionID, req.RequestID) })
	}

	// The resident steward is now the primary business-message sender. Queue a
	// short resident turn and release it after the notification; the worker
	// permission remains pending independently in this map/table.
	notice := s.renderPermissionNotice(&req)
	var notifyErr error
	if req.answerCh != nil {
		// This tool call is running inside the resident turn. Queueing its own
		// question behind that turn would deadlock, so send synchronously on the
		// steward agent's behalf and keep waiting only for the user's answer.
		card := &CardPayload{
			Title:   fmt.Sprintf("[%s] %s", permissionCode(&req), req.Title),
			Body:    appendOptionsBody(appendPlanBody(req.Body, req.Plan), req.Options),
			Options: req.Options, Confirm: req.Method == "confirm",
		}
		notifyErr = s.SendToChannel(req.ChannelID, OutboundMessage{
			ThreadID: req.ThreadID, ReceiveIDType: req.ReceiveIDType,
			ReplyToMessageID: req.ReplyToMessageID, Card: card, Markdown: true,
		})
		if notifyErr == nil {
			s.markResidentReply(req.SessionID)
		}
	} else {
		_, notifyErr = s.enqueueStewardEvent(store.StewardEvent{
			Kind: "permission", SessionID: req.SessionID, RequestID: req.RequestID,
			ChannelID: req.ChannelID, Sender: req.Sender, Thread: req.ThreadID,
			ReceiveIDType: req.ReceiveIDType, ReplyToMessageID: req.ReplyToMessageID,
			PromptText:   fmt.Sprintf("有新的待审批工作项。请调用 codingto_steward_reply，并把 permissionRequestId 设置为 %q，以发送绑定了准确请求编号的审批卡；不要替用户做决定。工作项内容如下：\n\n%s", req.RequestID, notice),
			FallbackText: notice, Priority: stewardPriorityPermission,
		})
	}
	if notifyErr != nil {
		s.mu.Lock()
		delete(s.pending, req.RequestID)
		s.closePermissionWaiterLocked(req.RequestID)
		s.mu.Unlock()
		_ = s.store.UpdateStewardPermission(record.ID, map[string]any{"status": "cancelled", "answer": "notification queue failed"})
		return notifyErr
	}

	if s.emit != nil {
		// The desktop permission panel keeps the full plan in its body (the
		// bot message above may have split it out); widget lines are relayed
		// to IM separately through the plan-then-confirm flow.
		view := permissionView(record, req)
		s.emit("steward:permission", view)
	}

	// Timeout handling: cancel the request if the user never answers.
	s.startPermissionTimeout(req.RequestID, s.permissionTimeout)

	// Steward-initiated confirmations are awaited by their caller. Queueing must
	// remain non-blocking; consuming answerCh here would discard the answer
	// before rpcAskConfirm/relayConfirm can inspect it.
	return nil
}

// permissionDone returns a channel closed when the request leaves the pending
// map (answered or cancelled).
func (s *Service) permissionDone(requestID string) <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if done, ok := s.permissionWaiters[requestID]; ok {
		return done
	}
	done := make(chan struct{})
	close(done)
	return done
}

func (s *Service) closePermissionWaiterLocked(requestID string) {
	if done, ok := s.permissionWaiters[requestID]; ok {
		close(done)
		delete(s.permissionWaiters, requestID)
	}
}

// cancelPermission marks a pending request cancelled (timeout or app abort).
func (s *Service) cancelPermission(requestID, reason string) {
	if strings.TrimSpace(reason) == "" {
		reason = "cancelled"
	}
	s.mu.Lock()
	req, ok := s.pending[requestID]
	if ok {
		delete(s.pending, requestID)
		s.closePermissionWaiterLocked(requestID)
	}
	s.mu.Unlock()
	if !ok {
		return
	}
	if req.ProfileParent != nil {
		parent := req.ProfileParent
		s.mu.Lock()
		delete(s.profileParents, requestID)
		s.mu.Unlock()
		_ = s.store.UpdateStewardPermission(reqPermissionID(s, parent.RequestID), map[string]any{
			"status": "cancelled", "answer": reason, "answered_at": time.Now().UnixMilli(),
		})
		_ = s.deliverPermissionAnswer(parent, &PermissionAnswer{Cancelled: true, Raw: reason})
		s.emitPermissionUpdate(parent, "cancelled", reason)
		return
	}
	now := time.Now().UnixMilli()
	s.logger.Printf("permission cancelled: request=%q reason=%s", requestID, reason)
	_ = s.store.UpdateStewardPermission(reqPermissionID(s, requestID), map[string]any{
		"status": "cancelled", "answer": reason, "answered_at": now,
	})
	if req.answerCh != nil {
		req.answerCh <- &PermissionAnswer{Cancelled: true, Raw: reason}
		s.emitPermissionUpdate(req, "cancelled", reason)
		return
	}
	if req.RunID != "" {
		_ = s.app.RespondSubagentUI(req.SessionID, req.RunID, SubagentUIAnswer{ID: req.RequestID, Cancelled: true})
	} else {
		_ = s.app.SendExtensionUIResponse(req.SessionID, req.RequestID, boolPtr(false), nil)
	}
	s.emitPermissionUpdate(req, "cancelled", reason)
}

// AnswerPermission resolves a pending permission request from bot text (or the
// desktop UI). It returns the resolved answer.
func (s *Service) AnswerPermission(requestID, rawText string) (*PermissionAnswer, error) {
	s.mu.Lock()
	req, ok := s.pending[requestID]
	if !ok {
		s.mu.Unlock()
		return nil, fmt.Errorf("steward: permission request %s is not pending", requestID)
	}
	// Browser Profile's "+ 新建 Profile" is a two-step select + key flow.
	// Keep the original extension request blocked while asking the bot for the
	// key; the final response still carries the original request id.
	if req.Method == "select" && isCreateProfileOption(req, rawText) {
		delete(s.pending, requestID)
		s.closePermissionWaiterLocked(requestID)
		s.mu.Unlock()
		return s.beginProfileCreation(req)
	}
	parent := req.ProfileParent
	answer, recognized := recognizedPermissionAnswer(req, rawText)
	if !recognized {
		s.mu.Unlock()
		return nil, fmt.Errorf("steward: answer does not match permission %s", permissionCode(req))
	}
	delete(s.pending, requestID)
	s.closePermissionWaiterLocked(requestID)
	if parent != nil {
		delete(s.profileParents, requestID)
	}
	s.mu.Unlock()
	if parent != nil {
		return s.finishProfileCreation(req, parent, rawText)
	}
	s.logger.Printf("permission answered: request=%q method=%s text=%q", requestID, req.Method, stewardLogPreview(rawText))
	s.markPermissionAnswered(req, permissionAnswerAudit(req, answer))
	if err := s.deliverPermissionAnswer(req, answer); err != nil {
		return answer, err
	}
	s.emitPermissionUpdate(req, "answered", rawText)
	return answer, nil
}

func (s *Service) beginProfileCreation(parent *PermissionRequest) (*PermissionAnswer, error) {
	targetURL := profileTargetURL(parent)
	if targetURL == "" {
		s.mu.Lock()
		s.pending[parent.RequestID] = parent
		s.permissionWaiters[parent.RequestID] = make(chan struct{})
		s.mu.Unlock()
		return nil, fmt.Errorf("steward: browser profile target URL is missing")
	}
	requestID := parent.RequestID + ":profile-key"
	follow := &PermissionRequest{
		RequestID: requestID, SessionID: parent.SessionID, RunID: parent.RunID,
		Method: "input", Title: "创建 Browser Profile",
		Body:      fmt.Sprintf("请输入新的 Browser Profile Key（目标：%s）。", targetURL),
		ChannelID: parent.ChannelID, Sender: parent.Sender, ThreadID: parent.ThreadID,
		CreatedAt: time.Now(), ProfileParent: parent,
	}
	s.mu.Lock()
	s.pending[requestID] = follow
	s.permissionWaiters[requestID] = make(chan struct{})
	s.profileParents[requestID] = parent
	s.mu.Unlock()
	notice := fmt.Sprintf("[%s] %s\n%s", permissionCode(follow), follow.Title, follow.Body)
	if _, err := s.enqueueStewardEvent(store.StewardEvent{
		Kind: "permission_followup", SessionID: follow.SessionID, RequestID: follow.RequestID,
		ChannelID: follow.ChannelID, Sender: follow.Sender, Thread: follow.ThreadID,
		PromptText:   "请使用 codingto_steward_reply 向用户询问以下信息：\n\n" + notice,
		FallbackText: notice, Priority: stewardPriorityPermission,
	}); err != nil {
		s.mu.Lock()
		delete(s.pending, requestID)
		s.closePermissionWaiterLocked(requestID)
		delete(s.profileParents, requestID)
		s.pending[parent.RequestID] = parent
		s.permissionWaiters[parent.RequestID] = make(chan struct{})
		s.mu.Unlock()
		return nil, err
	}
	s.startPermissionTimeout(requestID, s.permissionTimeout)
	return &PermissionAnswer{Value: requestID, Raw: "profile-create"}, nil
}

func (s *Service) finishProfileCreation(follow, parent *PermissionRequest, rawText string) (*PermissionAnswer, error) {
	key := strings.TrimSpace(rawText)
	profileID, err := s.app.SaveBrowserProfile(key, profileTargetURL(parent))
	if err != nil {
		s.mu.Lock()
		s.pending[follow.RequestID] = follow
		s.permissionWaiters[follow.RequestID] = make(chan struct{})
		s.profileParents[follow.RequestID] = parent
		s.mu.Unlock()
		_ = s.SendToChannel(parent.ChannelID, OutboundMessage{ThreadID: parent.ThreadID, Text: "创建 Browser Profile 失败：" + err.Error()})
		return nil, err
	}
	answer := &PermissionAnswer{Value: profileID, Raw: key}
	s.logger.Printf("permission answered: request=%q method=select profile=%q", parent.RequestID, stewardLogPreview(profileID))
	s.markPermissionAnswered(parent, "[profile-created]")
	if err := s.deliverPermissionAnswer(parent, answer); err != nil {
		return answer, err
	}
	s.emitPermissionUpdate(parent, "answered", key)
	return answer, nil
}

func (s *Service) markPermissionAnswered(req *PermissionRequest, rawText string) {
	if id := reqPermissionID(s, req.RequestID); id != 0 {
		_ = s.store.UpdateStewardPermission(id, map[string]any{
			"status": "answered", "answer": rawText, "answered_at": time.Now().UnixMilli(),
		})
	}
}

func (s *Service) deliverPermissionAnswer(req *PermissionRequest, answer *PermissionAnswer) error {
	if req.answerCh != nil {
		req.answerCh <- answer
		return nil
	}
	if req.RunID != "" {
		return s.app.RespondSubagentUI(req.SessionID, req.RunID, SubagentUIAnswer{
			ID: req.RequestID, Value: answer.Value, Confirmed: answer.Confirmed, Cancelled: answer.Cancelled,
		})
	}
	return s.app.SendExtensionUIResponse(req.SessionID, req.RequestID, answer.Confirmed, answer.Value)
}

// ListPendingPermissions returns pending permission views for the frontend.
func (s *Service) ListPendingPermissions() []PublicPermissionView {
	records, err := s.store.ListStewardPermissions()
	if err != nil {
		return nil
	}
	views := make([]PublicPermissionView, 0, len(records))
	for _, r := range records {
		if r.Status != "pending" {
			continue
		}
		views = append(views, permissionView(r, PermissionRequest{
			RecordID: r.ID, RequestID: r.RequestID, SessionID: r.SessionID, RunID: r.RunID,
			Method: r.Method, Title: r.Title, Body: r.Body,
			Options: decodeCardOptions(r.OptionsJSON), Plan: decodePlanSteps(r.PlanJSON),
		}))
	}
	return views
}

func decodeCardOptions(raw string) []CardOption {
	var options []CardOption
	_ = json.Unmarshal([]byte(raw), &options)
	return options
}

func decodePlanSteps(raw string) []PlanStep {
	var plan []PlanStep
	_ = json.Unmarshal([]byte(raw), &plan)
	return plan
}

func (s *Service) reloadPendingPermissions() {
	records, err := s.store.ListStewardPermissions()
	if err != nil {
		s.logger.Printf("reload pending permissions failed: %v", err)
		return
	}
	type restoredTimeout struct {
		requestID string
		remaining time.Duration
	}
	timeouts := make([]restoredTimeout, 0)
	s.mu.Lock()
	for _, record := range records {
		if record.Status != "pending" {
			continue
		}
		req := &PermissionRequest{
			RecordID: record.ID, RequestID: record.RequestID, SessionID: record.SessionID, RunID: record.RunID,
			Method: record.Method, Title: record.Title, Body: record.Body,
			Options: decodeCardOptions(record.OptionsJSON), Plan: decodePlanSteps(record.PlanJSON),
			ChannelID: record.ChannelID, Sender: record.Sender, ThreadID: record.Thread,
			ReceiveIDType: record.ReceiveIDType, ReplyToMessageID: record.ReplyToMessageID,
			CreatedAt: time.UnixMilli(record.CreatedAt),
		}
		s.pending[req.RequestID] = req
		s.permissionWaiters[req.RequestID] = make(chan struct{})
		remaining := s.permissionTimeout - time.Since(req.CreatedAt)
		timeouts = append(timeouts, restoredTimeout{requestID: req.RequestID, remaining: remaining})
	}
	s.mu.Unlock()
	for _, restored := range timeouts {
		if restored.remaining <= 0 {
			s.cancelPermission(restored.requestID, "timeout")
			continue
		}
		s.startPermissionTimeout(restored.requestID, restored.remaining)
	}
}

// recognizedPermissionAnswer maps valid bot text to a structured answer and
// explicitly rejects values that do not match the request contract.
func recognizedPermissionAnswer(req *PermissionRequest, text string) (*PermissionAnswer, bool) {
	raw := strings.TrimSpace(text)
	lower := strings.ToLower(raw)
	if req.Method == "confirm" {
		if isNegative(lower) {
			confirmed := false
			return &PermissionAnswer{Confirmed: &confirmed, Raw: raw}, true
		}
		if isAffirmative(lower) {
			confirmed := true
			return &PermissionAnswer{Confirmed: &confirmed, Raw: raw}, true
		}
		return nil, false
	}
	if req.Method == "select" {
		if index, err := strconv.Atoi(raw); err == nil && index >= 1 && index <= len(req.Options) {
			return &PermissionAnswer{Value: req.Options[index-1].Value, Raw: raw}, true
		}
		for _, opt := range req.Options {
			if strings.EqualFold(opt.Label, raw) || strings.EqualFold(opt.Value, raw) {
				return &PermissionAnswer{Value: opt.Value, Raw: raw}, true
			}
		}
		if len(req.Options) == 0 && raw != "" {
			return &PermissionAnswer{Value: raw, Raw: raw}, true
		}
		return nil, false
	}
	// input / editor: pass the text through.
	if raw == "" {
		return nil, false
	}
	return &PermissionAnswer{Value: raw, Raw: raw}, true
}

func isAffirmative(lower string) bool {
	for _, word := range []string{"确认", "批准", "通过", "同意", "允许", "是", "yes", "y", "ok", "allow", "approve", "confirm", "accept"} {
		if strings.HasPrefix(lower, word) {
			return true
		}
	}
	return false
}

func isNegative(lower string) bool {
	for _, word := range []string{"拒绝", "取消", "否", "no", "n", "deny", "cancel", "reject", "不同意", "不允许"} {
		if strings.HasPrefix(lower, word) {
			return true
		}
	}
	return false
}

// reqPermissionID resolves the stored record id for a request id.
func reqPermissionID(s *Service, requestID string) int64 {
	record, ok, err := s.store.StewardPermissionByRequestID(requestID)
	if err != nil || !ok {
		return 0
	}
	return record.ID
}

func (s *Service) emitPermissionUpdate(req *PermissionRequest, status, answer string) {
	if s.emit == nil || req == nil {
		return
	}
	// Include the owning session/run so the desktop can dismiss exactly the
	// dialog that was answered remotely, including a persisted background-task
	// dialog, without touching a newer request from another conversation.
	s.emit("steward:permission", map[string]any{
		"requestId":      req.RequestID,
		"sessionId":      req.SessionID,
		"runId":          req.RunID,
		"status":         status,
		"answerProvided": strings.TrimSpace(answer) != "",
	})
}

func (s *Service) startPermissionTimeout(requestID string, timeout time.Duration) {
	s.launchBackground(func() {
		s.mu.Lock()
		ctx := s.runCtx
		s.mu.Unlock()
		if ctx == nil {
			ctx = context.Background()
		}
		select {
		case <-timeAfter(timeout):
			s.cancelPermission(requestID, "timeout")
		case <-s.permissionDone(requestID):
		case <-ctx.Done():
		}
	})
}

func permissionAnswerAudit(req *PermissionRequest, answer *PermissionAnswer) string {
	if answer == nil {
		return "[answered]"
	}
	if answer.Confirmed != nil {
		if *answer.Confirmed {
			return "approved"
		}
		return "denied"
	}
	if answer.Cancelled {
		return "cancelled"
	}
	if req != nil && (req.Method == "input" || req.Method == "editor") {
		return "[provided]"
	}
	return fmt.Sprintf("%v", answer.Value)
}

func permissionView(record store.StewardPermission, req PermissionRequest) PublicPermissionView {
	var options []CardOption
	if record.OptionsJSON != "" {
		_ = json.Unmarshal([]byte(record.OptionsJSON), &options)
	}
	return PublicPermissionView{
		ID: record.ID, Code: permissionCode(&req), RequestID: record.RequestID, SessionID: record.SessionID,
		RunID: record.RunID, ChannelID: record.ChannelID, Method: record.Method,
		Title: record.Title, Body: appendPlanBody(req.Body, req.Plan), Options: options, Scope: record.Scope,
		Status: record.Status, Answer: record.Answer, CreatedAt: record.CreatedAt,
	}
}

func boolPtr(v bool) *bool { return &v }
