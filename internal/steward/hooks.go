package steward

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ResolveStewardAgent reloads the persona profile and resolves the configured
// (or default) agent for the steward conversation.
func (s *Service) ResolveStewardAgent() {
	if profile, ok, err := s.store.GetStewardProfile(); err == nil && ok {
		s.mu.Lock()
		s.profile = stewardProfileWithDefaults(profile)
		s.mu.Unlock()
	}
	s.resolveStewardAgent()
}

// ResolveAgentView returns the currently resolved steward agent.
func (s *Service) ResolveAgentView() (AgentView, error) {
	s.mu.Lock()
	agentID := s.stewardAgent
	s.mu.Unlock()
	if agentID == "" {
		return AgentView{}, fmt.Errorf("steward agent not resolved")
	}
	return s.app.ResolveAgent(agentID)
}

// IsStewardSession reports whether the session is the resident steward
// conversation (used to arm idle reclaim only for it).
func (s *Service) IsStewardSession(sessionID int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stewardSession != 0 && s.stewardSession == sessionID
}

// RelayPermission is the AgentService hook: when a bot-managed session emits
// an interactive extension UI request, it is relayed to the bot user. It
// returns true when the request was relayed (the watchdog is then owned by the
// steward timeout). Non-interactive or non-managed requests return false and
// keep the desktop dialog path.
func (s *Service) RelayPermission(sessionID int64, event map[string]any) bool {
	managed := s.IsBotManaged(sessionID)
	resident := s.IsStewardSession(sessionID)
	if !managed && !resident {
		return false
	}
	plan := s.takePlan(sessionID, "")
	s.mu.Lock()
	task, hasTask := s.tasks[sessionID]
	cur := s.current
	s.mu.Unlock()
	req := PermissionRequest{
		RequestID: str(event["id"]),
		SessionID: sessionID,
		Method:    str(event["method"]),
		Title:     cleanPermissionTitle(permissionTitle(event)),
		Body:      permissionBody(event),
		Options:   permissionOptions(event),
		Plan:      plan,
	}
	if hasTask {
		req.ChannelID = task.ChannelID
		req.Sender = task.Sender
		req.ThreadID = task.Thread
	} else if resident && cur != nil {
		req.ChannelID = cur.ChannelID
		req.Sender = cur.SenderID
		req.ThreadID = cur.ThreadID
		req.ReceiveIDType = cur.ReceiveIDType
		req.ReplyToMessageID = cur.ReplyToMessageID
	}
	if req.ChannelID == 0 && s.takeoverScope() == "all" {
		if cid, sender, thread := s.reportTarget(); cid != 0 {
			req.ChannelID, req.Sender, req.ThreadID = cid, sender, thread
		}
	}
	// 常驻管家自主运行：当其计划确认弹窗没有外部渠道可转发时，直接自动确认，
	// 避免在前端“管家设置-消息”中弹出需要用户手动确认的对话框（消息 Tab 仅展示会话详情）。
	if resident && !managed && req.ChannelID == 0 && strings.HasPrefix(str(event["title"]), planConfirmDialogPrefix) {
		if req.RequestID != "" {
			confirmed := true
			s.launchBackground(func() {
				if err := s.app.SendExtensionUIResponse(sessionID, req.RequestID, &confirmed, nil); err != nil {
					s.logger.Printf("auto-confirm resident plan failed: request=%q error=%v", req.RequestID, err)
				}
			})
		}
		return true
	}
	if req.RequestID == "" || req.ChannelID == 0 {
		return false
	}
	if err := s.QueuePermission(req); err != nil {
		s.logger.Printf("relay permission failed: request=%q error=%v", req.RequestID, err)
		return false
	}
	return true
}

// subagentEventMap normalizes the nested subagent event from a map or JSON
// string into a map.
func subagentEventMap(raw any) (map[string]any, bool) {
	if event, ok := raw.(map[string]any); ok {
		return event, true
	}
	if text, ok := raw.(string); ok && text != "" {
		var event map[string]any
		if json.Unmarshal([]byte(text), &event) == nil {
			return event, true
		}
	}
	return nil, false
}

func isInteractiveMethod(method string) bool {
	switch method {
	case "select", "confirm", "input", "editor":
		return true
	default:
		return false
	}
}

// RelaySubagentPermission is the AgentService hook for nested subagent UI
// requests carried by subagent:event payloads. It extracts the interactive
// request plus run id and relays it to the bot user.
func (s *Service) RelaySubagentPermission(sessionID int64, subagent map[string]any) bool {
	runID := str(subagent["runId"])
	if runID == "" {
		return false
	}
	// The nested event may arrive as a map or as a JSON string (slimmed by
	// slimSubagentStreamEvent for message_update events; interactive UI
	// requests stay maps, but be defensive for both shapes).
	event, ok := subagentEventMap(subagent["event"])
	if !ok {
		return false
	}
	s.capturePlan(sessionID, runID, event)
	if !isInteractiveMethod(str(event["method"])) || str(event["id"]) == "" {
		return false
	}
	s.mu.Lock()
	task, hasTask := s.tasks[sessionID]
	cur := s.current
	s.mu.Unlock()
	plan := s.takePlan(sessionID, runID)
	req := PermissionRequest{
		RequestID: str(event["id"]),
		SessionID: sessionID,
		RunID:     runID,
		Method:    str(event["method"]),
		Title:     cleanPermissionTitle(permissionTitle(event)),
		Body:      permissionBody(event),
		Options:   permissionOptions(event),
		Plan:      plan,
	}
	if hasTask {
		req.ChannelID = task.ChannelID
		req.Sender = task.Sender
		req.ThreadID = task.Thread
	} else if s.IsStewardSession(sessionID) && cur != nil {
		req.ChannelID = cur.ChannelID
		req.Sender = cur.SenderID
		req.ThreadID = cur.ThreadID
		req.ReceiveIDType = cur.ReceiveIDType
		req.ReplyToMessageID = cur.ReplyToMessageID
	}
	if req.ChannelID == 0 && s.takeoverScope() == "all" {
		if cid, sender, thread := s.reportTarget(); cid != 0 {
			req.ChannelID, req.Sender, req.ThreadID = cid, sender, thread
		}
	}
	if req.ChannelID == 0 {
		return false
	}
	if err := s.QueuePermission(req); err != nil {
		s.logger.Printf("relay subagent permission failed: request=%q error=%v", req.RequestID, err)
		return false
	}
	return true
}

func permissionTitle(event map[string]any) string {
	for _, key := range []string{"title", "question", "name"} {
		if value := str(event[key]); value != "" {
			return value
		}
	}
	return "需要您的授权"
}

func permissionBody(event map[string]any) string {
	// Pi's ui.confirm(title, message) protocol emits the second argument as
	// `message`. Keep the older aliases for other extensions, but prefer the
	// canonical field so bot-relayed approvals include the exact operation the
	// user is being asked to authorize (notably the DCG command preview).
	for _, key := range []string{"message", "body", "description", "detail", "reason"} {
		if value := str(event[key]); value != "" {
			return value
		}
	}
	return ""
}

func permissionOptions(event map[string]any) []CardOption {
	raw := event["options"]
	if raw == nil {
		return nil
	}
	options := make([]CardOption, 0)
	switch values := raw.(type) {
	case []string:
		for _, text := range values {
			if text != "" {
				options = append(options, CardOption{Label: text, Value: text})
			}
		}
	case []any:
		for _, item := range values {
			if text, ok := item.(string); ok {
				options = append(options, CardOption{Label: text, Value: text})
				continue
			}
			if m, ok := item.(map[string]any); ok {
				label := str(m["label"])
				value := str(m["value"])
				if label == "" {
					label = value
				}
				if label != "" {
					options = append(options, CardOption{
						Label: label, Value: value, Description: str(m["description"]),
						CreateProfile: boolValue(m["createProfile"]), TargetURL: str(m["targetUrl"]),
					})
				}
			}
		}
	}
	return options
}
