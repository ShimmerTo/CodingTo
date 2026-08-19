package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"codingto/internal/applog"
	"codingto/internal/subagentbridge"

	"github.com/wailsapp/wails/v3/pkg/application"
)

func (s *AgentService) AbortPrompt(sessionIDs ...int64) error {
	if s.runtimes != nil {
		if len(sessionIDs) == 0 {
			s.mu.Lock()
			runtimes := make([]*AgentService, 0, len(s.runtimes))
			for _, runtime := range s.runtimes {
				runtimes = append(runtimes, runtime)
			}
			s.mu.Unlock()
			if len(runtimes) == 0 {
				return s.abortPromptSingle()
			}
			var result error
			for _, runtime := range runtimes {
				if err := runtime.abortPromptSingle(); err != nil {
					result = errors.Join(result, err)
				}
			}
			return result
		}
		s.mu.Lock()
		runtime := s.runtimes[sessionIDs[0]]
		s.mu.Unlock()
		// A missing runtime means the conversation is already idle. Treat abort as
		// idempotent and explicitly repair a frontend that missed the terminal
		// event instead of returning an error that leaves its stop button visible.
		if runtime == nil {
			s.emitEvent("agent:state", map[string]any{
				"running": false, "processRunning": false,
				"codingToSessionId": sessionIDs[0], "aborted": true, "fallback": true,
			})
			return nil
		}
		return runtime.abortPromptSingle(sessionIDs[0])
	}
	return s.abortPromptSingle()
}

func (s *AgentService) abortPromptSingle(requestedSessionIDs ...int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sessionID := s.activeSessionID
	if len(requestedSessionIDs) > 0 && requestedSessionIDs[0] > 0 {
		sessionID = requestedSessionIDs[0]
	}
	if s.preparing {
		s.prepareCanceled = true
		return nil
	}
	// Always fan the cancellation out to every child run before inspecting the
	// parent. The parent may already be idle while detached subagents are still
	// winding down, and one click must terminate the whole conversation tree.
	s.handleAbortCommandLocked()
	if s.execTurnStart.IsZero() {
		s.emitEvent("agent:state", map[string]any{
			"running": false, "processRunning": s.adapter.IsRunning(),
			"codingToSessionId": sessionID, "aborted": true, "fallback": true,
		})
		return nil
	}
	if !s.adapter.IsRunning() {
		s.finishExecutionLocked("active")
		s.emitEvent("agent:state", map[string]any{
			"running": false, "processRunning": false,
			"codingToSessionId": sessionID, "aborted": true, "fallback": true,
		})
		return nil
	}
	// Pi can remain busy after a low-level agent_end event while it retries,
	// compacts, or processes a queued continuation. The UI correctly keeps the
	// stop button visible in those phases, so never discard its abort merely
	// because the execution timer has already been settled by an event.
	raw, _ := json.Marshal(map[string]string{"id": "codingto-abort", "type": "abort"})
	return s.adapter.SendCommand(raw)
}

// handleAbortCommandLocked 执行终止命令的后端部分：给所有运行中的子 agent 写
// .abort 标记（bridge 轮询后杀进程并写终态 run.json），并强制收尾"等待子
// agent"状态的会话。调用者必须持有 s.mu。
func (s *AgentService) handleAbortCommandLocked() {
	s.abortRunningSubagentsLocked()
	s.forceSettleWaitingLocked()
}

// forceSettleWaitingLocked 在用户终止时强制收尾"等待子 agent"状态的会话。
// 等待期间主 agent 回合早已结束（agent_settled 保留 execTurnStart 维持忙碌），
// abort 命令只会被 Pi 应答，不会再有 agent_settled 触发收尾；若不强制收尾，
// 会话会永久卡在运行中且无法开启新回合（isBusy() 恒真）。调用者必须持有 s.mu。
func (s *AgentService) forceSettleWaitingLocked() {
	if !s.waitingSubagents {
		return
	}
	s.waitingSubagents = false
	s.finishExecutionLocked("active")
	s.emitEvent("agent:state", map[string]any{
		"running": false, "processRunning": true, "codingToSessionId": s.activeSessionID,
		"aborted": true, "fallback": true,
	})
}

// abortRunningSubagentsLocked scans the active conversation's subagents
// directory and drops an abort marker for every run still marked running. The
// caller must hold s.mu.
func (s *AgentService) abortRunningSubagentsLocked() {
	abortRunningSubagents(s.activeSessionDir, s.activeSessionID)
}

// abortRunningSubagents is also used by the App boundary before runtime lookup,
// so even a stale/missing parent runtime cannot strand detached child processes.
func abortRunningSubagents(sessionDir string, sessionID int64) int {
	if sessionDir == "" {
		return 0
	}
	root := filepath.Join(sessionDir, "subagents")
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0
	}
	requested := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		runDir := filepath.Join(root, entry.Name())
		record, err := subagentbridge.ReadRunRecord(filepath.Join(runDir, "run.json"))
		// A failed read (run.json not yet written while the bridge initialises, or
		// a transient rename gap) also drops the marker: the parent agent stopping
		// must stop every subagent that is starting or running under it. The
		// marker is idempotent and run IDs are unique, so a stale one is harmless.
		if err == nil && record.Status != "running" {
			continue
		}
		if err := os.WriteFile(filepath.Join(runDir, ".abort"), []byte("1"), 0o600); err != nil {
			applog.Infof("[session %d] mark subagent %s aborted: %v", sessionID, entry.Name(), err)
			continue
		}
		requested++
	}
	return requested
}

// agentEndErrorMessage 从 agent_end 事件中提取模型/provider 返回的真实错误。
// Pi 在模型调用失败时会把错误放在 assistant 消息的 errorMessage 字段（或顶层），
// 优先展示它，而不是笼统的 "model completed without a text response"。
func agentEndErrorMessage(raw json.RawMessage) string {
	var p struct {
		ErrorMessage string `json:"errorMessage"`
		Messages     []struct {
			Role         string `json:"role"`
			StopReason   string `json:"stopReason"`
			ErrorMessage string `json:"errorMessage"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return ""
	}
	if p.ErrorMessage != "" {
		return p.ErrorMessage
	}
	for _, m := range p.Messages {
		if m.Role == "assistant" && m.ErrorMessage != "" {
			return m.ErrorMessage
		}
	}
	return ""
}

// StopSession stops and removes exactly one conversation runtime.
func (s *AgentService) StopSession(id int64) error {
	if s.runtimes != nil {
		s.mu.Lock()
		runtime := s.runtimes[id]
		delete(s.runtimes, id)
		s.mu.Unlock()
		if runtime == nil {
			return nil
		}
		return runtime.stopSessionSingle(id)
	}
	return s.stopSessionSingle(id)
}

func (s *AgentService) stopSessionSingle(id int64) error {
	s.mu.Lock()
	if s.activeSessionID != id {
		s.mu.Unlock()
		return nil
	}
	s.pendingRestart = false
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	err := s.adapter.Stop()
	agentID := s.activeAgent
	s.finishExecutionLocked("active")
	s.activeSessionID = 0
	s.activeSessionDir = ""
	s.activeSession = ""
	s.mu.Unlock()
	if s.runtimeRelease != nil {
		s.runtimeRelease(agentID, id)
	}
	application.Get().Event.Emit("agent:state", map[string]any{
		"running": false, "processRunning": false, "codingToSessionId": id,
	})
	return err
}

// Restart stops the running Pi process and respawns it against the same agent
// and session so on-disk changes (e.g. a newly materialized RTK extension) are
// picked up. If the agent is mid-task, the restart is deferred until the current
// turn finishes, to avoid killing an in-flight execution. With no active session
// the stop is enough and the next StartPrompt applies the changes.
func (s *AgentService) Restart() error {
	if s.runtimes != nil {
		s.mu.Lock()
		runtimes := make([]*AgentService, 0, len(s.runtimes))
		for _, runtime := range s.runtimes {
			runtimes = append(runtimes, runtime)
		}
		s.mu.Unlock()
		var result error
		for _, runtime := range runtimes {
			if err := runtime.restartSingle(); err != nil {
				result = errors.Join(result, err)
			}
		}
		return result
	}
	return s.restartSingle()
}

func (s *AgentService) restartSingle() error {
	s.mu.Lock()
	req := s.restartRequestLocked()
	tools := s.activeTools
	busy := !s.execTurnStart.IsZero()
	// If the agent is currently executing a task, defer the respawn until the
	// turn ends. Do not cancel or stop the current Pi process on this path.
	if busy {
		s.pendingRestart = true
		s.pendingReq = req
		s.pendingTools = tools
		s.mu.Unlock()
		application.Get().Event.Emit("agent:restart_deferred", map[string]any{"reason": "busy"})
		return nil
	}
	s.mu.Unlock()
	return s.performRestart(req, tools)
}

// restartRequestLocked captures enough state to recreate the current Pi process
// without injecting a user message. The caller must hold s.mu.
func (s *AgentService) restartRequestLocked() PromptRequest {
	sessionPath := s.activeSession
	if sessionPath == "" && s.activeSessionID > 0 {
		if session, ok, _ := s.store.Store().SessionByID(s.activeSessionID); ok {
			sessionPath = session.SessionPath
		}
	}
	return PromptRequest{
		AgentID:     s.activeAgent,
		Mode:        s.activeMode,
		WorkDir:     s.activeDir,
		SessionID:   s.activeSessionID,
		SessionPath: sessionPath,
		SkillPath:   s.activeSkill,
	}
}

// performRestart stops the current Pi process and recreates it for req. Both an
// immediate restart and a restart deferred until agent_end use this path so the
// frontend always receives the same completion event.
func (s *AgentService) performRestart(req PromptRequest, toolsEnabled bool) error {
	cfg := s.store.Get()
	profile, hasProfile := cfg.Agent(req.AgentID)

	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	stopErr := s.adapter.Stop()
	s.finishExecutionLocked("active")
	s.activeSessionID = 0
	s.activeSessionDir = ""
	s.activeSession = ""

	var restartErr error
	running := false
	switch {
	case stopErr != nil:
		restartErr = stopErr
	case req.SessionPath == "":
		// There is no resumable session yet. Leaving the process stopped is
		// intentional; the next prompt will start it with the latest extensions.
	case !hasProfile:
		restartErr = fmt.Errorf("agent not found: %s", req.AgentID)
	default:
		restartErr = s.startAdapter(req, cfg, profile, toolsEnabled)
		running = restartErr == nil
	}
	s.mu.Unlock()

	// `agent:state.running` represents an active turn in the frontend, not
	// whether the idle Pi subprocess exists. A reload never starts a new turn.
	state := map[string]any{
		"running": false, "processRunning": running, "codingToSessionId": req.SessionID,
	}
	result := map[string]any{"success": restartErr == nil, "processRunning": running}
	if restartErr != nil {
		state["error"] = restartErr.Error()
		result["error"] = restartErr.Error()
	}
	application.Get().Event.Emit("agent:state", state)
	application.Get().Event.Emit("agent:restart_done", result)
	return restartErr
}

func (s *AgentService) Close() error {
	if s.runtimes != nil {
		s.mu.Lock()
		runtimes := make([]*AgentService, 0, len(s.runtimes))
		for _, runtime := range s.runtimes {
			runtimes = append(runtimes, runtime)
		}
		s.runtimes = map[int64]*AgentService{}
		s.mu.Unlock()
		var result error
		for _, runtime := range runtimes {
			if err := runtime.closeSingle(); err != nil {
				result = errors.Join(result, err)
			}
		}
		return result
	}
	return s.closeSingle()
}

func (s *AgentService) closeSingle() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pendingRestart = false
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	err := s.adapter.Stop()
	s.finishExecutionLocked("active")
	return err
}

// finishExecutionLocked freezes the current turn duration and persists the
// cumulative value. The caller must hold s.mu.
func (s *AgentService) finishExecutionLocked(status string) {
	s.disarmFirstResponseWatchdogLocked()
	s.disarmAllToolWatchdogsLocked()
	// Follow-up suppression is scoped to one failed model turn. Every path that
	// finishes, stops, restarts, or observes an exited adapter funnels through
	// this method, so clearing it here prevents a missing agent_settled event
	// from suppressing the next ordinary user turn.
	s.abortFollowUp = false
	// 任何结束/收尾路径都退出"等待子 agent"状态，避免状态残留导致后续
	// 终止路径误判或新回合进入等待态。
	s.waitingSubagents = false
	if !s.execTurnStart.IsZero() {
		s.execAccumulatedMs += time.Since(s.execTurnStart).Milliseconds()
		s.execTurnStart = time.Time{}
	}
	if s.activeSessionID > 0 && s.store != nil {
		_ = s.store.Store().UpdateSession(s.activeSessionID, map[string]any{
			"status": status, "exec_duration_ms": s.execAccumulatedMs,
		})
	}
}
