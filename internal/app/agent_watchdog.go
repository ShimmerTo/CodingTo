package app

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// interactiveUIWatchdogTimeout bounds how long an interactive extension UI
// request (select/confirm/input/editor) may wait for the frontend to confirm it
// rendered the dialog. When the frontend never acknowledges the request (for
// example the dialog failed to render, or the frontend is gone) Pi would block
// forever waiting for a response; the watchdog auto-answers with a cancellation
// so the agent is never wedged by a blocking extension.
const interactiveUIWatchdogTimeout = 90 * time.Second

func (s *AgentService) armFirstResponseWatchdogLocked(sessionID int64, nodeID string) {
	s.disarmFirstResponseWatchdogLocked()
	timeout := s.firstResponseTimeout
	if timeout <= 0 {
		timeout = defaultModelFirstResponseTimeout
	}
	s.firstResponseToken++
	token := s.firstResponseToken
	s.firstResponseNodeID = nodeID
	s.firstResponseTimer = time.AfterFunc(timeout, func() {
		s.fireFirstResponseWatchdog(token, sessionID, nodeID, timeout)
	})
}

func (s *AgentService) disarmFirstResponseWatchdogLocked() {
	if s.firstResponseTimer != nil {
		s.firstResponseTimer.Stop()
		s.firstResponseTimer = nil
	}
	s.firstResponseNodeID = ""
	s.firstResponseToken++
}

func (s *AgentService) fireFirstResponseWatchdog(token uint64, sessionID int64, nodeID string, timeout time.Duration) {
	s.mu.Lock()
	if token != s.firstResponseToken ||
		s.firstResponseNodeID != nodeID ||
		s.activeSessionID != sessionID ||
		s.activeChangeNode != nodeID ||
		s.execTurnStart.IsZero() {
		s.mu.Unlock()
		return
	}
	s.firstResponseTimer = nil
	s.firstResponseNodeID = ""
	s.firstResponseToken++
	action := s.firstResponseTimeoutAction
	s.mu.Unlock()

	if action != nil {
		action(sessionID, nodeID, timeout)
		return
	}
	s.handleFirstResponseTimeout(sessionID, nodeID, timeout)
}

// isInteractiveUIEvent reports whether an event is an interactive extension UI
// request that blocks Pi until the frontend answers. Only these need the
// watchdog; non-interactive requests (setWidget/setStatus/notify) never block.
func isInteractiveUIEvent(event map[string]any) bool {
	if stringValue(event["type"]) != "extension_ui_request" {
		return false
	}
	switch stringValue(event["method"]) {
	case "select", "confirm", "input", "editor":
		return stringValue(event["id"]) != ""
	default:
		return false
	}
}

// armUIWatchdogLocked (re)arms the interactive-UI watchdog for the given request
// id, cancelling any previously armed watchdog. Caller must hold s.mu.
func (s *AgentService) armUIWatchdogLocked(sessionID int64, sessionDir, requestID string) {
	s.disarmUIWatchdogLocked("")
	if requestID == "" {
		return
	}
	s.uiWatchdogID = requestID
	s.uiWatchdogTimer = time.AfterFunc(interactiveUIWatchdogTimeout, func() {
		s.fireUIWatchdog(sessionID, sessionDir, requestID)
	})
}

// disarmUIWatchdogLocked stops the interactive-UI watchdog. When requestID is
// non-empty it only disarms if that request is the one currently pending, so a
// stale ack/response for an old dialog cannot cancel a newer one. Caller must
// hold s.mu.
func (s *AgentService) disarmUIWatchdogLocked(requestID string) {
	if requestID != "" && s.uiWatchdogID != requestID {
		return
	}
	if s.uiWatchdogTimer != nil {
		s.uiWatchdogTimer.Stop()
		s.uiWatchdogTimer = nil
	}
	s.uiWatchdogID = ""
}

// fireUIWatchdog auto-cancels an interactive UI request that the frontend never
// acknowledged. It answers Pi with a cancellation so the agent is unblocked, and
// notifies the frontend so any stale dialog state is cleared.
func (s *AgentService) fireUIWatchdog(sessionID int64, sessionDir, requestID string) {
	s.mu.Lock()
	if s.uiWatchdogID != requestID {
		s.mu.Unlock()
		return
	}
	s.uiWatchdogTimer = nil
	s.uiWatchdogID = ""
	s.mu.Unlock()

	_ = s.adapter.SendCommand(mustJSON(map[string]any{
		"type": "extension_ui_response", "id": requestID, "cancelled": true,
	}))
	event := map[string]any{
		"type": "extension_ui_timeout", "id": requestID,
		"codingToSessionId": sessionID, "_recordedAt": time.Now().UnixMilli(),
	}
	if sessionDir != "" {
		if err := s.appendEvent(sessionDir, event); err != nil {
			log.Printf("[session %d] append ui-timeout event: %v", sessionID, err)
		}
	}
	application.Get().Event.Emit("agent:event", event)
	log.Printf("[session %d] interactive UI request %s timed out; auto-cancelled", sessionID, requestID)
}

// toolWatchdogToolNames 是启用执行超时看门狗的工具。目前仅 bash 工具可能
// 无限挂起（无界扫描、阻塞子进程）；read/write/edit 等 pi 内置工具耗时有限，
// 浏览器与会话类工具已有各自的服务层超时，无需看门狗干预。
var toolWatchdogToolNames = map[string]bool{"bash": true}

// armToolWatchdogLocked (re)arms the tool-execution watchdog for the given
// tool call. Only tools registered in toolWatchdogToolNames are bounded; other
// tools are left unmonitored. Caller must hold s.mu.
func (s *AgentService) armToolWatchdogLocked(sessionID int64, toolName, toolCallID string) {
	s.disarmToolWatchdogLocked()
	if !toolWatchdogToolNames[toolName] {
		return
	}
	timeout := s.toolExecutionTimeout
	if timeout <= 0 {
		timeout = defaultToolExecutionTimeout
	}
	s.toolWatchdogToken++
	token := s.toolWatchdogToken
	s.toolWatchdogName = toolName
	s.toolWatchdogToolID = toolCallID
	s.toolWatchdogTimer = time.AfterFunc(timeout, func() {
		s.fireToolWatchdog(token, sessionID, toolName, toolCallID, timeout)
	})
}

// disarmToolWatchdogLocked stops the tool-execution watchdog. Caller must hold
// s.mu.
func (s *AgentService) disarmToolWatchdogLocked() {
	if s.toolWatchdogTimer != nil {
		s.toolWatchdogTimer.Stop()
		s.toolWatchdogTimer = nil
	}
	s.toolWatchdogName = ""
	s.toolWatchdogToolID = ""
	s.toolWatchdogToken++
}

// fireToolWatchdog aborts a tool call that exceeded its execution budget. For
// the bash tool it sends Pi's abort_bash RPC, which cancels the running
// command (the tool result comes back cancelled and the agent can continue
// with an alternative approach) instead of killing the whole turn. A
// tool_execution_timeout event is recorded so the UI can surface the reason.
func (s *AgentService) fireToolWatchdog(token uint64, sessionID int64, toolName, toolCallID string, timeout time.Duration) {
	s.mu.Lock()
	if token != s.toolWatchdogToken ||
		s.toolWatchdogName != toolName ||
		s.activeSessionID != sessionID ||
		s.execTurnStart.IsZero() {
		s.mu.Unlock()
		return
	}
	s.toolWatchdogTimer = nil
	s.toolWatchdogToken++
	s.toolWatchdogName = ""
	s.toolWatchdogToolID = ""
	sessionDir := s.activeSessionDir
	s.mu.Unlock()

	// abort_bash cancels the currently running bash command in Pi. It is
	// idempotent and safe even when the command already finished between the
	// timer firing and the RPC arriving.
	if toolName == "bash" {
		if err := s.sendAdapterCommand(mustJSON(map[string]string{
			"id": "codingto-tool-timeout", "type": "abort_bash",
		})); err != nil {
			log.Printf("[session %d] tool timeout: send abort_bash: %v", sessionID, err)
		}
	}

	seconds := int(timeout.Round(time.Second) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	message := fmt.Sprintf(
		"Tool %s exceeded the %d second execution limit and was aborted automatically.",
		toolName, seconds,
	)
	event := map[string]any{
		"type": "tool_execution_timeout", "toolName": toolName, "toolCallId": toolCallID,
		"message": message, "timeoutSeconds": seconds,
		"codingToSessionId": sessionID, "_recordedAt": time.Now().UnixMilli(),
	}
	if sessionDir != "" {
		if err := s.appendEvent(sessionDir, event); err != nil {
			log.Printf("[session %d] append tool timeout event: %v", sessionID, err)
		}
	}
	s.emitEvent("agent:event", event)
	log.Printf("[session %d] tool %s exceeded %s execution limit; aborted bash command", sessionID, toolName, timeout)
}

func firstResponseObserved(event map[string]any) bool {
	switch stringValue(event["type"]) {
	case "message_update", "tool_execution_start", "tool_execution_update", "tool_execution_end",
		"extension_ui_request", "turn_end", "agent_end", "agent_settled", "error":
		return true
	case "message_end":
		return stringValue(mapValue(event["message"])["role"]) == "assistant"
	default:
		return false
	}
}

func (s *AgentService) handleFirstResponseTimeout(sessionID int64, nodeID string, timeout time.Duration) {
	seconds := int(timeout.Round(time.Second) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	message := fmt.Sprintf(
		"Model did not return a response within %d seconds. The stalled agent process was stopped; please retry.",
		seconds,
	)

	s.mu.Lock()
	if s.activeSessionID != sessionID || s.activeChangeNode != nodeID || s.execTurnStart.IsZero() {
		s.mu.Unlock()
		return
	}
	cancel := s.cancel
	s.cancel = nil
	s.pendingRestart = false
	s.activeChangeNode = ""
	sessionDir := s.activeSessionDir
	sessionPath := s.activeSession
	if sessionPath != "" {
		if _, err := os.Stat(sessionPath); errors.Is(err, os.ErrNotExist) {
			s.activeSession = ""
			sessionPath = ""
			if s.store != nil {
				_ = s.store.Store().UpdateSession(sessionID, map[string]any{"session_path": ""})
			}
		}
	}
	s.finishExecutionLocked("active")
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	_ = s.adapter.Stop()
	_ = os.Remove(filepath.Join(sessionDir, ".active-change-node"))

	recordedAt := time.Now().UnixMilli()
	_ = finishChangeNode(sessionDir, nodeID, "timeout", recordedAt)
	summary, err := readChangeSummary(sessionDir, nodeID)
	if err != nil {
		summary = ChangeSummary{
			NodeID: nodeID, Status: "timeout", Files: []FileChangeSummary{},
		}
	}
	events := []map[string]any{
		{
			"type": "agent_end", "messages": []any{}, "errorMessage": message,
			"changeSummary": summary, "changeNodeId": nodeID,
			"codingToSessionId": sessionID, "_recordedAt": recordedAt,
		},
		{
			"type": "error", "code": "model_first_response_timeout", "error": message,
			"changeNodeId": nodeID, "codingToSessionId": sessionID, "_recordedAt": recordedAt,
		},
		{
			"type": "agent_settled", "reason": "model_first_response_timeout",
			"codingToSessionId": sessionID, "_recordedAt": recordedAt,
		},
	}
	for _, event := range events {
		if err := s.appendEvent(sessionDir, event); err != nil {
			log.Printf("[session %d] append first-response timeout event: %v", sessionID, err)
		}
		application.Get().Event.Emit("agent:event", event)
	}
	application.Get().Event.Emit("agent:state", map[string]any{
		"running": false, "processRunning": false,
		"codingToSessionId": sessionID, "error": message,
	})
	log.Printf("[session %d] model first-response timeout after %s; stopped stalled Pi process", sessionID, timeout)
}
