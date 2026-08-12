package steward

import (
	"fmt"
	"sort"
	"strings"
)

// stewardLogPreview keeps diagnostics useful without writing message content,
// permission answers, task text, profile keys, or provider errors to disk.
// IDs and lengths are sufficient to correlate lifecycle failures.
func stewardLogPreview(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "\r", " "), "\n", " "))
	if value == "" {
		return ""
	}
	return fmt.Sprintf("<redacted chars=%d>", len([]rune(value)))
}

func (s *Service) logInbound(msg InboundMessage, route string) {
	s.logger.Printf("inbound: channel=%d platform=%s sender=%q thread=%q route=%s content=%s", msg.ChannelID, msg.Platform, msg.SenderID, msg.ThreadID, route, stewardLogPreview(msg.Text))
}

// OnAgentEvent logs high-value lifecycle events for a steward-managed or
// resident steward session. It intentionally ignores text/thinking/tool-call
// deltas: those can arrive once per token and would make the console slower
// than the execution being diagnosed.
func (s *Service) OnAgentEvent(sessionID int64, event map[string]any) {
	managed := s.IsBotManaged(sessionID)
	resident := s.IsStewardSession(sessionID)
	if !managed && !resident {
		return
	}
	// Plan widgets are the non-blocking half of codingto_plan_present. Capture
	// them before the following confirm event is relayed so the bot receives the
	// actual ordered steps instead of only “N steps”.
	s.capturePlan(sessionID, "", event)
	eventType := str(event["type"])
	scope := "task"
	if resident {
		scope = "resident"
	}
	switch eventType {
	case "message_start":
		s.logger.Printf("agent event: scope=%s session=%d type=%s", scope, sessionID, eventType)
	case "tool_execution_start":
		if resident {
			s.markResidentTool(sessionID)
		}
		s.logger.Printf("agent event: scope=%s session=%d type=%s tool=%q toolCallId=%q", scope, sessionID, eventType, str(event["toolName"]), str(event["toolCallId"]))
	case "tool_execution_end":
		s.logger.Printf("agent event: scope=%s session=%d type=%s tool=%q toolCallId=%q error=%q", scope, sessionID, eventType, str(event["toolName"]), str(event["toolCallId"]), stewardLogPreview(str(event["errorMessage"])))
	case "agent_end":
		s.logger.Printf("agent event: scope=%s session=%d type=%s willRetry=%t error=%q", scope, sessionID, eventType, boolValue(event["willRetry"]), stewardEventError(event))
	case "agent_settled":
		s.logger.Printf("agent event: scope=%s session=%d type=%s", scope, sessionID, eventType)
		if resident && !boolValue(event["waitingSubagents"]) {
			dispatchToken := str(event["_stewardDispatchToken"])
			eventID, channelID, msg, fallback := s.finishResidentTurn(sessionID, dispatchToken)
			if fallback {
				if err := s.SendToChannel(channelID, msg); err != nil {
					s.logger.Printf("resident silent turn fallback failed: session=%d channel=%d error=%v", sessionID, channelID, err)
				}
			}
			s.completeActiveEvent(eventID, dispatchToken)
		}
	case "auto_retry_start":
		s.logger.Printf("agent event: scope=%s session=%d type=%s", scope, sessionID, eventType)
	case "error":
		s.logger.Printf("agent event: scope=%s session=%d type=%s error=%q", scope, sessionID, eventType, stewardEventError(event))
	case "response":
		// Responses are mostly internal acknowledgements. Keep only the command
		// name so session stats and model-control failures are diagnosable.
		command := str(event["command"])
		if command == "" {
			return
		}
		s.logger.Printf("agent event: scope=%s session=%d type=%s command=%q", scope, sessionID, eventType, command)
	}
}

func stewardEventError(event map[string]any) string {
	for _, key := range []string{"errorMessage", "error", "message"} {
		if value := stewardLogPreview(str(event[key])); value != "" {
			return value
		}
	}
	return ""
}

func boolValue(value any) bool {
	result, _ := value.(bool)
	return result
}

func stewardArgNames(args map[string]any) string {
	keys := make([]string, 0, len(args))
	for key := range args {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

func formatStewardPromptKind(sessionID int64, message string) string {
	return fmt.Sprintf("session=%d thinking=high text=%q", sessionID, stewardLogPreview(message))
}
