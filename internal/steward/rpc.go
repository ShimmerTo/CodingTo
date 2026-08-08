package steward

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// rpcRequest is the envelope the steward-tools extension posts to /rpc.
type rpcRequest struct {
	Tool string         `json:"tool"`
	Args map[string]any `json:"args"`
}

// rpcResponse is the envelope returned to the extension.
type rpcResponse struct {
	OK     bool   `json:"ok"`
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

// handleRPC authenticates and dispatches steward-tools tool calls. It is the
// only boundary the steward Pi uses to reach this service.
func (s *Service) handleRPC(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.mu.Lock()
	token := s.rpcToken
	s.mu.Unlock()
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if auth == "" || token == "" || auth != token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	s.logger.Printf("rpc request: tool=%q argNames=%s", req.Tool, stewardArgNames(req.Args))
	result, err := s.dispatchRPC(req.Tool, req.Args)
	if err != nil {
		s.logger.Printf("rpc failed: tool=%q error=%v", req.Tool, err)
	} else {
		s.logger.Printf("rpc completed: tool=%q", req.Tool)
	}
	resp := rpcResponse{OK: err == nil, Result: result}
	if err != nil {
		resp.Error = err.Error()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Service) dispatchRPC(tool string, args map[string]any) (any, error) {
	switch tool {
	case "steward_reply":
		if err := s.rpcReply(args); err != nil {
			return nil, err
		}
		// Return a non-empty result: an omitted `result` (omitempty) leaves the
		// extension's JSON.stringify(value) with `undefined`, which drops the
		// text field from `{type:"text"}` blocks in the tool result and later
		// crashes pi's context-token estimator (estimate.js reading .length).
		return map[string]any{"sent": true}, nil
	case "steward_list_environments":
		return s.app.ListEnvironments()
	case "steward_create_environment":
		return s.app.AddEnvironment(EnvironmentView{
			Name:        str(args["name"]),
			Path:        str(args["path"]),
			Description: str(args["description"]),
		})
	case "steward_remove_environment":
		if err := s.app.RemoveEnvironment(str(args["id"])); err != nil {
			return nil, err
		}
		return map[string]any{"removed": str(args["id"])}, nil
	case "steward_start_task":
		return s.rpcStartTask(args)
	case "steward_stop_task":
		id, err := int64Arg(args, "sessionId")
		if err != nil {
			return nil, err
		}
		if err := s.app.StopSession(id); err != nil {
			return nil, err
		}
		s.FinishTask(id, "aborted", fmt.Sprintf("⏹ 对话 #%d 已结束。", id))
		return map[string]any{"sessionId": id}, nil
	case "steward_list_running":
		return s.rpcListRunning()
	case "steward_list_sessions":
		return s.app.ListSessions()
	case "steward_delete_session":
		id, err := int64Arg(args, "sessionId")
		if err != nil {
			return nil, err
		}
		if err := s.app.DeleteSession(id); err != nil {
			return nil, err
		}
		s.finishTaskBySession(id, "aborted", "🗑 对话已删除。")
		return map[string]any{"sessionId": id}, nil
	case "steward_ask_confirm":
		return s.rpcAskConfirm(args)
	default:
		return nil, fmt.Errorf("steward: unknown tool %s", tool)
	}
}

// rpcReply sends a message to the current inbound origin (or an explicit
// channel/thread).
func (s *Service) rpcReply(args map[string]any) error {
	text := str(args["text"])
	if text == "" {
		return fmt.Errorf("steward_reply: text is required")
	}
	s.mu.Lock()
	cur := s.current
	var contextMsg InboundMessage
	if cur != nil {
		contextMsg = *cur
	}
	s.mu.Unlock()
	channelID := contextMsg.ChannelID
	threadID := contextMsg.ThreadID
	if raw := str(args["channelId"]); raw != "" {
		if id, err := strconv.ParseInt(raw, 10, 64); err == nil {
			channelID = id
		}
	}
	if raw := str(args["threadId"]); raw != "" {
		threadID = raw
	}
	if channelID == 0 {
		return fmt.Errorf("steward_reply: no channel context; pass channelId explicitly")
	}
	s.logger.Printf("rpc reply: channel=%d thread=%q text=%q", channelID, threadID, stewardLogPreview(text))
	if err := s.SendToChannel(channelID, OutboundMessage{
		ThreadID: threadID, ReceiveIDType: contextMsg.ReceiveIDType,
		ReplyToMessageID: contextMsg.ReplyToMessageID, Text: text, Markdown: true,
	}); err != nil {
		return err
	}
	s.markResidentReply(curSession(s))
	return nil
}

// rpcStartTask creates a conversation in the given environment, injects the
// task as the first prompt, and records a bot task.
func (s *Service) rpcStartTask(args map[string]any) (any, error) {
	task := str(args["task"])
	if task == "" {
		return nil, fmt.Errorf("steward_start_task: task is required")
	}
	s.mu.Lock()
	cur := s.current
	agentID := s.stewardAgent
	s.mu.Unlock()
	if cur == nil {
		return nil, fmt.Errorf("steward_start_task: no active inbound message")
	}
	envID := str(args["envId"])
	title := str(args["title"])
	if title == "" {
		title = truncateRunes(task, 30)
	}
	if agentID == "" {
		return nil, fmt.Errorf("steward_start_task: steward agent not configured")
	}
	s.logger.Printf("rpc start task: agent=%q env=%q title=%q task=%q", agentID, envID, title, stewardLogPreview(task))
	// Leave provider/model empty so the task resolves to the working-directory
	// default config (agent default, then global default) — exactly like creating
	// a new conversation in that directory. The steward profile's own model is NOT
	// imposed on dispatched tasks.
	provider, model := "", ""
	session, err := s.app.CreateSession(agentID, envID, title, provider, model)
	if err != nil {
		return nil, err
	}
	if _, err := s.RegisterTask(session.ID, cur.ChannelID, cur.SenderID, cur.ThreadID, title); err != nil {
		return nil, err
	}
	if err := s.app.StartPrompt(session.ID, task); err != nil {
		s.logger.Printf("rpc start task failed: session=%d error=%v", session.ID, err)
		s.FinishTask(session.ID, "failed", "任务启动失败："+err.Error())
		return nil, err
	}
	s.markResidentTask(curSession(s))
	s.logger.Printf("rpc start task accepted: session=%d thinking=model-default", session.ID)
	return map[string]any{
		"sessionId": session.ID, "title": title, "environmentId": envID, "status": "running",
	}, nil
}

func (s *Service) rpcListRunning() (any, error) {
	sessions, err := s.app.ListSessions()
	if err != nil {
		return nil, err
	}
	running := make([]SessionView, 0)
	for _, session := range sessions {
		// The resident steward runtime is busy while it answers this very query;
		// it is infrastructure, not one of the user's executing tasks.
		if session.Status == "running" && !s.IsStewardSession(session.ID) {
			running = append(running, session)
		}
	}
	return running, nil
}

// rpcAskConfirm asks the bot user a confirmation/selection and blocks until
// the answer (or timeout).
func (s *Service) rpcAskConfirm(args map[string]any) (any, error) {
	title := str(args["title"])
	body := str(args["body"])
	if title == "" {
		return nil, fmt.Errorf("steward_ask_confirm: title is required")
	}
	var options []CardOption
	if raw, ok := args["options"].([]any); ok {
		for _, item := range raw {
			if m, ok := item.(map[string]any); ok {
				options = append(options, CardOption{Label: str(m["label"]), Value: str(m["value"])})
			}
		}
	}
	s.mu.Lock()
	cur := s.current
	s.mu.Unlock()
	if cur == nil {
		return nil, fmt.Errorf("steward_ask_confirm: no active inbound message")
	}
	method := "confirm"
	if len(options) > 0 {
		method = "select"
	}
	req := &PermissionRequest{
		RequestID: fmt.Sprintf("steward-confirm-%d", unixMillisNow()),
		SessionID: curSession(s),
		Method:    method,
		Title:     title,
		Body:      body,
		Options:   options,
		ChannelID: cur.ChannelID,
		Sender:    cur.SenderID,
		ThreadID:  cur.ThreadID,
		answerCh:  make(chan *PermissionAnswer, 1),
	}
	if err := s.QueuePermission(*req); err != nil {
		return nil, err
	}
	select {
	case answer := <-req.answerCh:
		if answer.Cancelled {
			return map[string]any{"cancelled": true}, nil
		}
		return map[string]any{"confirmed": answer.Confirmed, "value": answer.Value}, nil
	case <-timeAfter(s.permissionTimeout):
		s.cancelPermission(req.RequestID, "timeout")
		return map[string]any{"cancelled": true, "reason": "timeout"}, nil
	}
}

// curSession returns the session id of the message currently being processed.
func curSession(s *Service) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stewardSession
}

func str(v any) string {
	if v == nil {
		return ""
	}
	if text, ok := v.(string); ok {
		return text
	}
	return fmt.Sprintf("%v", v)
}

func int64Arg(args map[string]any, key string) (int64, error) {
	raw := str(args[key])
	if raw == "" {
		return 0, fmt.Errorf("steward: %s is required", key)
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("steward: %s invalid: %s", key, raw)
	}
	return id, nil
}

func truncateRunes(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max]) + "…"
}
