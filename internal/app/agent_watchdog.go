package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"codingto/internal/applog"

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
			applog.Infof("[session %d] append ui-timeout event: %v", sessionID, err)
		}
	}
	application.Get().Event.Emit("agent:event", event)
	applog.Infof("[session %d] interactive UI request %s timed out; auto-cancelled", sessionID, requestID)
}

// toolWatchdogToolNames 是启用执行超时看门狗的工具。目前仅 bash 工具可能
// 无限挂起（无界扫描、阻塞子进程）；read/write/edit 等 pi 内置工具耗时有限，
// 浏览器与会话类工具已有各自的服务层超时，无需看门狗干预。
var toolWatchdogToolNames = map[string]bool{"bash": true}

// toolWatchdogState belongs to one concrete tool call. Pi can execute several
// tool calls from the same assistant message in parallel, so a runtime-wide
// singleton timer cannot safely represent the execution deadline.
type toolWatchdogState struct {
	timer    *time.Timer
	token    uint64
	toolName string
	timedOut bool
}

// toolWatchdogAbortGrace gives Pi a brief opportunity to finish a normal
// abort_bash before the stronger process-tree fallback is used. The fallback
// is needed on Windows when a shell/descendant keeps stdio handles open after
// taskkill, which otherwise delays tool_execution_end indefinitely.
const toolWatchdogAbortGrace = 5 * time.Second

// armToolWatchdogLocked arms an independent timer for the given tool call.
// Only tools registered in toolWatchdogToolNames are bounded; starting another
// (possibly parallel) tool never cancels an existing deadline. Caller must
// hold s.mu.
func (s *AgentService) armToolWatchdogLocked(sessionID int64, toolName, toolCallID string) {
	toolName = strings.ToLower(strings.TrimSpace(toolName))
	if !toolWatchdogToolNames[toolName] {
		return
	}
	key := toolWatchdogKey(toolName, toolCallID)
	s.disarmToolWatchdogLocked(toolName, toolCallID)
	timeout := s.toolExecutionTimeout
	if timeout <= 0 {
		timeout = defaultToolExecutionTimeout
	}
	s.toolWatchdogToken++
	token := s.toolWatchdogToken
	if s.toolWatchdogs == nil {
		s.toolWatchdogs = make(map[string]*toolWatchdogState)
	}
	state := &toolWatchdogState{token: token, toolName: toolName}
	state.timer = time.AfterFunc(timeout, func() {
		s.fireToolWatchdog(token, sessionID, toolName, toolCallID, timeout)
	})
	s.toolWatchdogs[key] = state
}

func toolWatchdogKey(toolName, toolCallID string) string {
	if toolCallID != "" {
		return toolCallID
	}
	// The Pi protocol normally supplies a call id. Keep a deterministic fallback
	// for malformed/legacy events so their start and end can still pair up.
	return "tool:" + strings.ToLower(strings.TrimSpace(toolName))
}

// disarmToolWatchdogLocked stops only the matching tool call. An end event for
// one parallel call must not remove another call's deadline. Caller must hold
// s.mu.
func (s *AgentService) disarmToolWatchdogLocked(toolName, toolCallID string) {
	key := toolWatchdogKey(toolName, toolCallID)
	state := s.toolWatchdogs[key]
	if state == nil {
		return
	}
	if state.timer != nil {
		state.timer.Stop()
	}
	delete(s.toolWatchdogs, key)
}

// disarmAllToolWatchdogsLocked clears every outstanding tool deadline when a
// turn/runtime finishes. Caller must hold s.mu.
func (s *AgentService) disarmAllToolWatchdogsLocked() {
	for key, state := range s.toolWatchdogs {
		if state != nil && state.timer != nil {
			state.timer.Stop()
		}
		delete(s.toolWatchdogs, key)
	}
}

// fireToolWatchdog aborts a tool call that exceeded its execution budget. For
// the bash tool it sends Pi's abort_bash RPC, which cancels the running
// command (the tool result comes back cancelled and the agent can continue
// with an alternative approach) instead of killing the whole turn. A
// tool_execution_timeout event is recorded so the UI can surface the reason.
func (s *AgentService) fireToolWatchdog(token uint64, sessionID int64, toolName, toolCallID string, timeout time.Duration) {
	s.mu.Lock()
	key := toolWatchdogKey(toolName, toolCallID)
	state := s.toolWatchdogs[key]
	if state == nil || state.token != token || state.toolName != toolName ||
		s.activeSessionID != sessionID ||
		s.execTurnStart.IsZero() {
		s.mu.Unlock()
		return
	}
	state.timer = nil
	state.timedOut = true
	sessionDir := s.activeSessionDir
	s.mu.Unlock()

	// abort_bash cancels the currently running bash command in Pi. It is
	// idempotent and safe even when the command already finished between the
	// timer firing and the RPC arriving.
	if toolName == "bash" {
		if err := s.sendAdapterCommand(mustJSON(map[string]string{
			"id": "codingto-tool-timeout", "type": "abort_bash",
		})); err != nil {
			applog.Infof("[session %d] tool timeout: send abort_bash: %v", sessionID, err)
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
			applog.Infof("[session %d] append tool timeout event: %v", sessionID, err)
		}
	}
	s.emitEvent("agent:event", event)
	go s.escalateToolWatchdog(token, sessionID, toolName, toolCallID)
	applog.Infof("[session %d] tool %s exceeded %s execution limit; aborted bash command", sessionID, toolName, timeout)
}

// escalateToolWatchdog kills the Pi process tree only when abort_bash did not
// produce a tool end within the grace period. A later matching tool end or turn
// end removes the state and makes this stale callback harmless.
//
// Killing the Pi process tree also destroys the only channel through which a
// tool_execution_end / agent_settled could ever arrive, so after the kill we
// synthesize the terminal events ourselves (finalizeToolTimeoutSession);
// otherwise the frontend would keep the timed-out tool call and the whole
// session in a permanent "running" state with no visible way to recover.
func (s *AgentService) escalateToolWatchdog(token uint64, sessionID int64, toolName, toolCallID string) {
	grace := s.toolWatchdogAbortGrace
	if grace <= 0 {
		grace = toolWatchdogAbortGrace
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	<-timer.C

	s.mu.Lock()
	state := s.toolWatchdogs[toolWatchdogKey(toolName, toolCallID)]
	if s.activeSessionID != sessionID || s.execTurnStart.IsZero() || state == nil ||
		state.token != token || !state.timedOut {
		s.mu.Unlock()
		return
	}
	timeout := s.toolExecutionTimeout
	if timeout <= 0 {
		timeout = defaultToolExecutionTimeout
	}
	sessionDir := s.activeSessionDir
	nodeID := s.activeChangeNode
	stewardHookSet := s.stewardHooks
	stewardToken := s.activeStewardToken
	if s.stewardPromptPending {
		stewardToken = s.pendingStewardToken
		s.activeStewardToken = stewardToken
		s.pendingStewardToken = ""
		s.stewardPromptPending = false
	}
	// End the turn and clear every watchdog timer (this one included) so a
	// parallel deadline can never trigger a duplicate process-tree kill while
	// the settle events below are still in flight.
	s.pendingRestart = false
	s.activeChangeNode = ""
	s.finishExecutionLocked("active")
	s.mu.Unlock()

	killTree := s.killTreeOverride
	if killTree == nil {
		killTree = s.adapter.KillTree
	}
	killTree()
	// Put the adapter into the stopped state right away so a follow-up prompt
	// cannot write into the dead process before the Wait goroutine resets
	// running. Stop is idempotent here: the tree is already gone, taskkill just
	// fails silently and the flag + done channel are reset for the next turn.
	_ = s.adapter.Stop()
	applog.Infof("[session %d] tool %s (%s) did not finish after abort; killed Pi process tree", sessionID, toolName, toolCallID)

	s.finalizeToolTimeoutSession(sessionID, sessionDir, nodeID, toolName, toolCallID, timeout, stewardHookSet, stewardToken)
}

// finalizeToolTimeoutSession emits the terminal events that the killed Pi
// process can no longer produce after a tool-execution-timeout escalation. It
// mirrors the first-response-timeout teardown (handleFirstResponseTimeout) so
// the frontend and the steward both observe a settled, failed session instead
// of an endless running tool call. Caller must not hold s.mu.
func (s *AgentService) finalizeToolTimeoutSession(sessionID int64, sessionDir, nodeID, toolName, toolCallID string, timeout time.Duration, stewardHookSet *stewardHooks, stewardToken string) {
	recordedAt := time.Now().UnixMilli()
	seconds := int(timeout.Round(time.Second) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	message := fmt.Sprintf(
		"Tool %s exceeded the %d second execution limit and the stalled command was force-stopped.",
		toolName, seconds,
	)

	// The Pi process is gone, so synthesize the tool end the frontend relies on
	// to mark the tool call as finished (its detail.status is only reset by
	// tool_execution_end).
	toolEnd := map[string]any{
		"type": "tool_execution_end", "toolName": toolName, "toolCallId": toolCallID,
		"cancelled": true, "errorMessage": message, "output": "",
		"codingToSessionId": sessionID, "_recordedAt": recordedAt,
	}
	if err := s.appendEvent(sessionDir, toolEnd); err != nil {
		applog.Infof("[session %d] append tool-timeout end event: %v", sessionID, err)
	}
	s.emitEvent("agent:event", toolEnd)

	_ = os.Remove(filepath.Join(sessionDir, ".active-change-node"))
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
			"type": "error", "code": "tool_execution_timeout", "error": message,
			"changeNodeId": nodeID, "codingToSessionId": sessionID, "_recordedAt": recordedAt,
		},
		{
			"type": "agent_settled", "reason": "tool_execution_timeout",
			"status": "failed", "errorMessage": message,
			"codingToSessionId": sessionID, "_recordedAt": recordedAt,
		},
	}
	for _, event := range events {
		if stewardToken != "" {
			event["_stewardDispatchToken"] = stewardToken
		}
		if err := s.appendEvent(sessionDir, event); err != nil {
			applog.Infof("[session %d] append tool-timeout settle event: %v", sessionID, err)
		}
		s.emitEvent("agent:event", event)
		if stewardHookSet != nil && stewardHookSet.onAgentEvent != nil {
			stewardHookSet.onAgentEvent(sessionID, event)
		}
	}
	s.notifyStewardTaskSettled(stewardHookSet, sessionID, sessionDir, events[len(events)-1])
	s.emitEvent("agent:state", map[string]any{
		"running": false, "processRunning": false,
		"codingToSessionId": sessionID, "error": message,
	})
	applog.Infof("[session %d] tool %s (%s) timed out after %s; session settled as failed", sessionID, toolName, toolCallID, timeout)
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
	stewardToken := s.activeStewardToken
	if s.stewardPromptPending {
		stewardToken = s.pendingStewardToken
		s.activeStewardToken = stewardToken
		s.pendingStewardToken = ""
		s.stewardPromptPending = false
	}
	stewardHookSet := s.stewardHooks
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
			"status": "failed", "errorMessage": message,
			"codingToSessionId": sessionID, "_recordedAt": recordedAt,
		},
	}
	for _, event := range events {
		if stewardToken != "" {
			event["_stewardDispatchToken"] = stewardToken
		}
		if err := s.appendEvent(sessionDir, event); err != nil {
			applog.Infof("[session %d] append first-response timeout event: %v", sessionID, err)
		}
		s.emitEvent("agent:event", event)
		if stewardHookSet != nil && stewardHookSet.onAgentEvent != nil {
			stewardHookSet.onAgentEvent(sessionID, event)
		}
	}
	s.notifyStewardTaskSettled(stewardHookSet, sessionID, sessionDir, events[len(events)-1])
	s.emitEvent("agent:state", map[string]any{
		"running": false, "processRunning": false,
		"codingToSessionId": sessionID, "error": message,
	})
	applog.Infof("[session %d] model first-response timeout after %s; stopped stalled Pi process", sessionID, timeout)
}
