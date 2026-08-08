package steward

import (
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
	record, err := s.store.CreateStewardPermission(store.StewardPermission{
		RequestID: req.RequestID, SessionID: req.SessionID, RunID: req.RunID,
		ChannelID: req.ChannelID, Sender: req.Sender, Method: req.Method,
		Title: req.Title, OptionsJSON: optionsJSON, Scope: scope, Status: "pending",
	})
	if err != nil {
		return err
	}
	s.mu.Lock()
	req.CreatedAt = time.UnixMilli(record.CreatedAt)
	s.pending[req.RequestID] = &req
	s.mu.Unlock()

	// Ack immediately (async) so the 90s watchdog is disarmed; the steward
	// timeout below replaces it. For subagent requests this satisfies the
	// bridge's ack window.
	if req.RunID != "" {
		go func() { _ = s.app.AckSubagentUI(req.SessionID, req.RunID, req.RequestID) }()
	} else {
		go func() { _ = s.app.AckExtensionUI(req.SessionID, req.RequestID) }()
	}

	// Plain-text connectors do not render structured card options. Include the
	// choices in the body so a bot user can answer by number or label.
	cardBody := appendOptionsBody(appendPlanBody(req.Body, req.Plan), req.Options)
	card := &CardPayload{Title: req.Title, Body: cardBody, Options: req.Options}
	if req.Method == "confirm" {
		card.Confirm = true
	}
	// Plan confirmations: when the full plan body would exceed the platform's
	// single-message limit, send the plan steps first (split into multiple
	// messages), then a separate short confirm card. Otherwise a long plan is
	// truncated and the user never sees the steps they are asked to approve.
	platform := s.channelPlatform(req.ChannelID)
	if len(req.Plan) > 0 && textTooLong(cardBody, platform) {
		for _, part := range splitOutboundText(renderPlanSteps(req.Plan), platform) {
			_ = s.SendToChannel(req.ChannelID, OutboundMessage{ThreadID: req.ThreadID, Text: part, Markdown: true})
		}
		card.Body = appendOptionsBody(req.Body, req.Options)
	}
	_ = s.SendToChannel(req.ChannelID, OutboundMessage{ThreadID: req.ThreadID, Card: card, Markdown: true})

	if s.emit != nil {
		// The desktop permission panel keeps the full plan in its body (the
		// bot message above may have split it out); widget lines are relayed
		// to IM separately through the plan-then-confirm flow.
		viewReq := req
		viewReq.Body = appendPlanBody(req.Body, req.Plan)
		view := permissionView(record, viewReq)
		s.emit("steward:permission", view)
	}

	// Timeout handling: cancel the request if the user never answers.
	go func(requestID string) {
		select {
		case <-timeAfter(s.permissionTimeout):
			s.cancelPermission(requestID, "timeout")
		case <-s.permissionDone(requestID):
		}
	}(req.RequestID)

	// Steward-initiated confirmations are awaited by their caller. Queueing must
	// remain non-blocking; consuming answerCh here would discard the answer
	// before rpcAskConfirm/relayConfirm can inspect it.
	return nil
}

// permissionDone returns a channel closed when the request leaves the pending
// map (answered or cancelled).
func (s *Service) permissionDone(requestID string) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		for {
			s.mu.Lock()
			_, ok := s.pending[requestID]
			s.mu.Unlock()
			if !ok {
				close(done)
				return
			}
			time.Sleep(250 * time.Millisecond)
		}
	}()
	return done
}

// cancelPermission marks a pending request cancelled (timeout or app abort).
func (s *Service) cancelPermission(requestID, reason string) {
	s.mu.Lock()
	req, ok := s.pending[requestID]
	if ok {
		delete(s.pending, requestID)
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
		"status": "cancelled", "answer": "timeout", "answered_at": now,
	})
	if req.answerCh != nil {
		req.answerCh <- &PermissionAnswer{Cancelled: true, Raw: "timeout"}
		return
	}
	if req.RunID != "" {
		_ = s.app.RespondSubagentUI(req.SessionID, req.RunID, SubagentUIAnswer{ID: req.RequestID, Cancelled: true})
	} else {
		_ = s.app.SendExtensionUIResponse(req.SessionID, req.RequestID, boolPtr(false), nil)
	}
	s.emitPermissionUpdate(req, "cancelled", "timeout")
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
		s.mu.Unlock()
		return s.beginProfileCreation(req)
	}
	delete(s.pending, requestID)
	parent := req.ProfileParent
	if parent != nil {
		delete(s.profileParents, requestID)
	}
	s.mu.Unlock()
	if parent != nil {
		return s.finishProfileCreation(req, parent, rawText)
	}
	answer := parsePermissionAnswer(req, rawText)
	s.logger.Printf("permission answered: request=%q method=%s text=%q", requestID, req.Method, stewardLogPreview(rawText))
	s.markPermissionAnswered(req, rawText)
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
	s.profileParents[requestID] = parent
	s.mu.Unlock()
	if err := s.SendToChannel(parent.ChannelID, OutboundMessage{
		ThreadID: parent.ThreadID,
		Card:     &CardPayload{Title: follow.Title, Body: follow.Body},
	}); err != nil {
		s.mu.Lock()
		delete(s.pending, requestID)
		delete(s.profileParents, requestID)
		s.pending[parent.RequestID] = parent
		s.mu.Unlock()
		return nil, err
	}
	go func() {
		select {
		case <-timeAfter(s.permissionTimeout):
			s.cancelPermission(requestID, "timeout")
		case <-s.permissionDone(requestID):
		}
	}()
	return &PermissionAnswer{Value: requestID, Raw: "profile-create"}, nil
}

func (s *Service) finishProfileCreation(follow, parent *PermissionRequest, rawText string) (*PermissionAnswer, error) {
	key := strings.TrimSpace(rawText)
	profileID, err := s.app.SaveBrowserProfile(key, profileTargetURL(parent))
	if err != nil {
		s.mu.Lock()
		s.pending[follow.RequestID] = follow
		s.profileParents[follow.RequestID] = parent
		s.mu.Unlock()
		_ = s.SendToChannel(parent.ChannelID, OutboundMessage{ThreadID: parent.ThreadID, Text: "创建 Browser Profile 失败：" + err.Error()})
		return nil, err
	}
	answer := &PermissionAnswer{Value: profileID, Raw: key}
	s.logger.Printf("permission answered: request=%q method=select profile=%q", parent.RequestID, stewardLogPreview(profileID))
	s.markPermissionAnswered(parent, key)
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
			RequestID: r.RequestID, SessionID: r.SessionID, RunID: r.RunID,
			Method: r.Method, Title: r.Title,
		}))
	}
	return views
}

// parsePermissionAnswer maps bot text to a structured answer based on the
// request method.
func parsePermissionAnswer(req *PermissionRequest, text string) *PermissionAnswer {
	raw := strings.TrimSpace(text)
	lower := strings.ToLower(raw)
	if req.Method == "confirm" {
		confirmed := isAffirmative(lower)
		if !isNegative(lower) && !isAffirmative(lower) {
			// Treat anything non-answer-like as confirm to avoid deadlock on
			// ambiguous text; negative words cancel explicitly.
			confirmed = true
		}
		return &PermissionAnswer{Confirmed: &confirmed, Raw: raw}
	}
	if req.Method == "select" {
		if index, err := strconv.Atoi(raw); err == nil && index >= 1 && index <= len(req.Options) {
			return &PermissionAnswer{Value: req.Options[index-1].Value, Raw: raw}
		}
		value := any(raw)
		for _, opt := range req.Options {
			if strings.EqualFold(opt.Label, raw) || strings.EqualFold(opt.Value, raw) {
				value = opt.Value
				break
			}
		}
		return &PermissionAnswer{Value: value, Raw: raw}
	}
	// input / editor: pass the text through.
	return &PermissionAnswer{Value: raw, Raw: raw}
}

func isAffirmative(lower string) bool {
	for _, word := range []string{"确认", "同意", "允许", "是", "yes", "y", "ok", "allow", "approve", "confirm", "accept"} {
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
		"requestId": req.RequestID,
		"sessionId": req.SessionID,
		"runId":     req.RunID,
		"status":    status,
		"answer":    answer,
	})
}

func permissionView(record store.StewardPermission, req PermissionRequest) PublicPermissionView {
	var options []CardOption
	if record.OptionsJSON != "" {
		_ = json.Unmarshal([]byte(record.OptionsJSON), &options)
	}
	return PublicPermissionView{
		ID: record.ID, RequestID: record.RequestID, SessionID: record.SessionID,
		RunID: record.RunID, ChannelID: record.ChannelID, Method: record.Method,
		Title: record.Title, Body: req.Body, Options: options, Scope: record.Scope,
		Status: record.Status, Answer: record.Answer, CreatedAt: record.CreatedAt,
	}
}

func boolPtr(v bool) *bool { return &v }
