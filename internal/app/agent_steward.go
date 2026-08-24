package app

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// relaySubagentIfManaged forwards a nested subagent UI request to the steward
// when the session is bot-managed.
func (s *AgentService) relaySubagentIfManaged(sessionID int64, subagent map[string]any) {
	if s.stewardHooks == nil || s.stewardHooks.relaySubagentPermission == nil {
		return
	}
	if s.stewardHooks.isBotManaged != nil && !s.stewardHooks.isBotManaged(sessionID) {
		return
	}
	s.stewardHooks.relaySubagentPermission(sessionID, subagent)
}

// stewardHooks wires the steward service into AgentService event dispatch.
// All callbacks are invoked outside the service lock. relayPermission returns
// whether the request was relayed to the bot (so the caller knows the
// frontend dialog stays as a fallback).
type stewardHooks struct {
	isBotManaged            func(sessionID int64) bool
	relayPermission         func(sessionID int64, event map[string]any) bool
	relaySubagentPermission func(sessionID int64, subagent map[string]any) bool
	onAgentEvent            func(sessionID int64, event map[string]any)
	onTaskSettled           func(sessionID int64, event map[string]any)
}

func (s *AgentService) notifyStewardTaskSettled(hooks *stewardHooks, sessionID int64, sessionDir string, event map[string]any) {
	if hooks == nil || hooks.onTaskSettled == nil {
		return
	}
	if hooks.isBotManaged != nil && !hooks.isBotManaged(sessionID) {
		return
	}
	hooks.onTaskSettled(sessionID, s.settledEventWithReply(sessionID, sessionDir, event))
}

// SetStewardHooks installs the steward integration hooks.
func (s *AgentService) SetStewardHooks(hooks *stewardHooks) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stewardHooks = hooks
}

// SetPinnedSession marks a session whose runtime must never be evicted when
// the runtime pool is full.
func (s *AgentService) SetPinnedSession(sessionID int64, pinned bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pinnedSessions == nil {
		s.pinnedSessions = map[int64]bool{}
	}
	if pinned {
		s.pinnedSessions[sessionID] = true
	} else {
		delete(s.pinnedSessions, sessionID)
	}
}

// ArmIdleReclaim stops the runtime for sessionID after it has been idle for
// timeout. The next prompt recreates and resumes it from the session file.
// Arming again replaces the previous timer; the timer is also disarmed by any
// new prompt through disarmIdleReclaimLocked.
func (s *AgentService) ArmIdleReclaim(sessionID int64, timeout time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.idleTimer != nil {
		s.idleTimer.Stop()
	}
	s.idleSession = sessionID
	s.idleTimeout = timeout
	s.idleTimer = time.AfterFunc(timeout, func() {
		s.mu.Lock()
		id := s.idleSession
		s.idleSession = 0
		s.idleTimer = nil
		s.mu.Unlock()
		if id > 0 {
			_ = s.StopSession(id)
		}
	})
}

// disarmIdleReclaimLocked cancels the pending idle reclaim timer. Caller must
// hold s.mu.
func (s *AgentService) disarmIdleReclaimLocked() {
	if s.idleTimer != nil {
		s.idleTimer.Stop()
		s.idleTimer = nil
	}
	s.idleSession = 0
}

// stewardTurnRecovery inspects the durable session event log for one resident
// dispatch token. A started-but-unsettled turn is deliberately left uncertain
// so startup recovery never repeats an operation with unknown side effects.
func (s *AgentService) stewardTurnRecovery(sessionID int64, dispatchToken string) (started, settled bool, err error) {
	if sessionID <= 0 || dispatchToken == "" {
		return false, false, nil
	}
	session, ok, err := s.store.Store().SessionByID(sessionID)
	if err != nil || !ok {
		return false, false, err
	}
	file, err := os.Open(filepath.Join(session.SessionDir, sessionEventFile))
	if os.IsNotExist(err) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		var event map[string]any
		if json.Unmarshal(scanner.Bytes(), &event) != nil || stringValue(event["_stewardDispatchToken"]) != dispatchToken {
			continue
		}
		switch stringValue(event["type"]) {
		case "user_text", "message_start":
			started = true
		case "agent_settled":
			started, settled = true, true
		}
	}
	return started, settled, scanner.Err()
}
