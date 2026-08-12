package steward

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"codingto/internal/store"
)

const (
	stewardPriorityNatural    = 10
	stewardPriorityResult     = 20
	stewardPriorityPermission = 40
)

// enqueueStewardEvent persists and schedules one resident-steward turn. The
// queue serializes prompts to the single Pi conversation; waiting permissions
// never occupy the resident runtime after their notification has been sent.
func (s *Service) enqueueStewardEvent(event store.StewardEvent) (store.StewardEvent, error) {
	created, err := s.store.CreateStewardEvent(event)
	if err != nil {
		return store.StewardEvent{}, err
	}
	s.mu.Lock()
	s.eventQueue = append(s.eventQueue, created)
	s.sortEventQueueLocked()
	shouldDispatch := !s.eventDispatching && s.activeEvent == nil && !s.shuttingDown
	s.mu.Unlock()
	if shouldDispatch {
		s.launchBackground(s.dispatchNextStewardEvent)
	}
	return created, nil
}

func (s *Service) reloadStewardEvents() {
	events, err := s.store.ListDispatchableStewardEvents()
	if err != nil {
		s.logger.Printf("reload steward events failed: %v", err)
		return
	}
	for i := range events {
		if events[i].Status == "processing" {
			recovery, recoveryErr := s.app.RecoverStewardTurn(s.stewardSession, events[i].DispatchToken)
			switch {
			case recoveryErr != nil:
				events[i].Status = "recovery_pending"
				_ = s.store.UpdateStewardEvent(events[i].ID, map[string]any{"status": "recovery_pending", "last_error": recoveryErr.Error(), "lease_until": 0})
			case recovery.Settled:
				events[i].Status = "delivered"
				_ = s.store.UpdateStewardEvent(events[i].ID, map[string]any{"status": "delivered", "last_error": "", "lease_until": 0, "processed_at": time.Now().UnixMilli()})
			case !recovery.Started && events[i].DispatchToken != "":
				events[i].Status = "queued"
				events[i].DispatchToken = ""
				events[i].LeaseUntil = 0
				_ = s.store.UpdateStewardEvent(events[i].ID, map[string]any{"status": "queued", "dispatch_token": "", "lease_until": 0, "last_error": "recovered before prompt start"})
			default:
				events[i].Status = "recovery_pending"
				_ = s.store.UpdateStewardEvent(events[i].ID, map[string]any{"status": "recovery_pending", "last_error": "previous dispatch outcome is uncertain", "lease_until": 0})
			}
		}
	}
	dispatchable := events[:0]
	for _, event := range events {
		if event.Status == "queued" {
			dispatchable = append(dispatchable, event)
		}
	}
	s.mu.Lock()
	s.eventQueue = dispatchable
	s.sortEventQueueLocked()
	s.mu.Unlock()
}

func (s *Service) sortEventQueueLocked() {
	sort.SliceStable(s.eventQueue, func(i, j int) bool {
		if s.eventQueue[i].Priority != s.eventQueue[j].Priority {
			return s.eventQueue[i].Priority > s.eventQueue[j].Priority
		}
		if s.eventQueue[i].CreatedAt != s.eventQueue[j].CreatedAt {
			return s.eventQueue[i].CreatedAt < s.eventQueue[j].CreatedAt
		}
		return s.eventQueue[i].ID < s.eventQueue[j].ID
	})
}

// dispatchNextStewardEvent starts at most one resident turn. Completion is
// observed through agent_settled, which releases the queue and schedules the
// next durable event.
func (s *Service) dispatchNextStewardEvent() {
	s.mu.Lock()
	if s.eventDispatching || s.activeEvent != nil || len(s.eventQueue) == 0 {
		s.mu.Unlock()
		return
	}
	if !s.profile.Enabled {
		s.mu.Unlock()
		return
	}
	event := s.eventQueue[0]
	s.eventQueue = s.eventQueue[1:]
	s.eventDispatching = true
	agentID := s.stewardAgent
	agentName := s.stewardAgentName
	sessionID := s.stewardSession
	s.mu.Unlock()
	event.Attempt++
	event.DispatchToken = fmt.Sprintf("steward-event-%d-%s", event.ID, randomToken()[:16])
	event.LeaseUntil = time.Now().Add(s.eventLeaseInterval).UnixMilli()
	promptText := event.PromptText
	fallbackText := event.FallbackText
	if event.Kind == "task_result" {
		if reminder := s.renderPendingApprovalReminder(event.ChannelID, event.Sender, event.Thread); reminder != "" {
			promptText += "\n\n" + reminder + "\n发送任务结果时必须同时提醒用户这些待批准事项，不要省略工作项编号。"
			fallbackText += "\n\n" + reminder
		}
	}
	failureEvent := event
	failureEvent.FallbackText = fallbackText

	if agentID == "" {
		s.failStewardEvent(failureEvent, "steward agent is not configured")
		return
	}
	if sessionID == 0 {
		created, err := s.ensureStewardSession(agentID, agentName)
		if err != nil {
			s.failStewardEvent(failureEvent, err.Error())
			return
		}
		sessionID = created
	}

	msg := InboundMessage{
		ChannelID: event.ChannelID, SenderID: event.Sender, ThreadID: event.Thread,
		ReceiveIDType: event.ReceiveIDType, ReplyToMessageID: event.ReplyToMessageID,
		Text: promptText,
	}
	s.mu.Lock()
	active := event
	s.activeEvent = &active
	s.current = &msg
	s.residentTurns[sessionID] = &residentTurn{
		ChannelID: event.ChannelID, ThreadID: event.Thread, ReceiveIDType: event.ReceiveIDType,
		ReplyToMessageID: event.ReplyToMessageID, Text: promptText, StartedAt: time.Now(),
		EventID: event.ID, FallbackText: fallbackText,
		DispatchToken: event.DispatchToken,
	}
	s.eventDispatching = false
	s.mu.Unlock()
	if err := s.store.UpdateStewardEvent(event.ID, map[string]any{
		"status": "processing", "last_error": "", "dispatch_token": event.DispatchToken,
		"attempt": event.Attempt, "lease_until": event.LeaseUntil,
	}); err != nil {
		s.clearResidentTurn(sessionID)
		s.failStewardEvent(failureEvent, err.Error())
		return
	}

	if s.trackResidentTurn(sessionID) {
		if err := s.compactResident(sessionID); err != nil {
			s.logger.Printf("resident compact failed: session=%d error=%v", sessionID, err)
		}
	}
	prompt := fmt.Sprintf("%s\n[管家队列事件：%s]\n%s%s", s.personaPrompt(), event.Kind, promptText, s.recentOutboundPrompt(event.ChannelID, event.Thread))
	if err := s.app.StartStewardPrompt(sessionID, prompt, event.DispatchToken); err != nil {
		if strings.Contains(err.Error(), "already running") {
			s.requeueActiveEvent(event, sessionID, err)
			return
		}
		s.clearResidentTurn(sessionID)
		s.failStewardEvent(failureEvent, err.Error())
		return
	}
	s.armEventLease(event.DispatchToken)
	s.logger.Printf("steward event accepted: event=%d kind=%s session=%d", event.ID, event.Kind, sessionID)
}

func (s *Service) requeueActiveEvent(event store.StewardEvent, sessionID int64, cause error) {
	s.mu.Lock()
	delete(s.residentTurns, sessionID)
	s.current = nil
	s.activeEvent = nil
	s.eventDispatching = false
	s.stopEventLeaseLocked()
	event.Status = "queued"
	s.eventQueue = append(s.eventQueue, event)
	s.sortEventQueueLocked()
	s.mu.Unlock()
	_ = s.store.UpdateStewardEvent(event.ID, map[string]any{"status": "queued", "last_error": cause.Error(), "dispatch_token": "", "lease_until": 0})
	state := s.app.SessionRuntimeState(sessionID)
	if state.Known && state.Running {
		time.AfterFunc(2*time.Second, func() { s.launchBackground(s.dispatchNextStewardEvent) })
	} else {
		s.launchBackground(s.dispatchNextStewardEvent)
	}
}

func (s *Service) failStewardEvent(event store.StewardEvent, reason string) {
	s.mu.Lock()
	s.activeEvent = nil
	s.current = nil
	s.eventDispatching = false
	s.stopEventLeaseLocked()
	s.mu.Unlock()
	if strings.TrimSpace(event.FallbackText) != "" && event.ChannelID > 0 {
		_ = s.SendToChannel(event.ChannelID, OutboundMessage{
			ThreadID: event.Thread, ReceiveIDType: event.ReceiveIDType,
			ReplyToMessageID: event.ReplyToMessageID, Text: event.FallbackText, Markdown: true,
		})
	}
	_ = s.store.UpdateStewardEvent(event.ID, map[string]any{
		"status": "failed", "last_error": reason, "lease_until": 0, "processed_at": time.Now().UnixMilli(),
	})
	s.logger.Printf("steward event failed: event=%d kind=%s error=%s", event.ID, event.Kind, stewardLogPreview(reason))
	s.launchBackground(s.dispatchNextStewardEvent)
}

func (s *Service) completeActiveEvent(eventID int64, dispatchToken string) {
	s.mu.Lock()
	if s.activeEvent == nil || eventID <= 0 || dispatchToken == "" ||
		s.activeEvent.ID != eventID || s.activeEvent.DispatchToken != dispatchToken {
		s.mu.Unlock()
		return
	}
	s.activeEvent = nil
	s.eventDispatching = false
	s.stopEventLeaseLocked()
	s.mu.Unlock()
	_ = s.store.UpdateStewardEvent(eventID, map[string]any{
		"status": "delivered", "last_error": "", "lease_until": 0, "processed_at": time.Now().UnixMilli(),
	})
	s.launchBackground(s.dispatchNextStewardEvent)
}

func (s *Service) stopEventLeaseLocked() {
	if s.eventLeaseTimer != nil {
		s.eventLeaseTimer.Stop()
		s.eventLeaseTimer = nil
	}
}

func (s *Service) armEventLease(dispatchToken string) {
	s.mu.Lock()
	if s.shuttingDown || s.activeEvent == nil || s.activeEvent.DispatchToken != dispatchToken {
		s.mu.Unlock()
		return
	}
	s.stopEventLeaseLocked()
	interval := s.eventLeaseInterval
	s.eventLeaseTimer = time.AfterFunc(interval, func() {
		s.launchBackground(func() { s.checkEventLease(dispatchToken) })
	})
	s.mu.Unlock()
}

func (s *Service) checkEventLease(dispatchToken string) {
	s.mu.Lock()
	if s.activeEvent == nil || s.activeEvent.DispatchToken != dispatchToken || s.shuttingDown {
		s.mu.Unlock()
		return
	}
	event := *s.activeEvent
	sessionID := s.stewardSession
	s.mu.Unlock()

	state := s.app.SessionRuntimeState(sessionID)
	if state.Known && state.Running {
		leaseUntil := time.Now().Add(s.eventLeaseInterval).UnixMilli()
		_ = s.store.UpdateStewardEvent(event.ID, map[string]any{"lease_until": leaseUntil})
		s.armEventLease(dispatchToken)
		return
	}

	s.mu.Lock()
	if s.activeEvent == nil || s.activeEvent.DispatchToken != dispatchToken {
		s.mu.Unlock()
		return
	}
	turn := s.residentTurns[sessionID]
	delete(s.residentTurns, sessionID)
	s.current = nil
	s.activeEvent = nil
	s.eventDispatching = false
	s.stopEventLeaseLocked()
	s.mu.Unlock()

	if !state.Known {
		_ = s.store.UpdateStewardEvent(event.ID, map[string]any{
			"status": "recovery_pending", "last_error": "runtime state unavailable after lease expiry", "lease_until": 0,
		})
	} else if turn != nil && turn.hasProgress() {
		_ = s.store.UpdateStewardEvent(event.ID, map[string]any{
			"status": "delivered", "last_error": "settled event missing; recovered from observed progress",
			"lease_until": 0, "processed_at": time.Now().UnixMilli(),
		})
	} else {
		if strings.TrimSpace(event.FallbackText) != "" && event.ChannelID > 0 {
			_ = s.SendToChannel(event.ChannelID, OutboundMessage{
				ThreadID: event.Thread, ReceiveIDType: event.ReceiveIDType,
				ReplyToMessageID: event.ReplyToMessageID, Text: event.FallbackText, Markdown: true,
			})
		}
		_ = s.store.UpdateStewardEvent(event.ID, map[string]any{
			"status": "failed", "last_error": "settled event missing after runtime became idle",
			"lease_until": 0, "processed_at": time.Now().UnixMilli(),
		})
	}
	s.launchBackground(s.dispatchNextStewardEvent)
}
