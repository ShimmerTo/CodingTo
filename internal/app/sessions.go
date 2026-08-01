package app

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"codingto/internal/store"
	"codingto/internal/subagentbridge"
)

const sessionEventFile = "codingto_events.jsonl"

var subagentRunIDPattern = regexp.MustCompile(`^run-[A-Za-z0-9_-]{8,96}$`)

// Session is the lightweight database record shown in the shared conversation
// list. Full message and tool-flow history remains in the session JSONL file.
type Session struct {
	ID             int64  `json:"id"`
	AgentID        string `json:"agentId"`
	EnvironmentID  string `json:"environmentId"`
	Title          string `json:"title"`
	SessionDir     string `json:"sessionDir"`
	SessionPath    string `json:"sessionPath"`
	Provider       string `json:"provider"`
	Model          string `json:"model"`
	Status         string `json:"status"`
	ExecDurationMs int64  `json:"execDurationMs"`
	CreatedAt      int64  `json:"createdAt"`
	UpdatedAt      int64  `json:"updatedAt"`
}

type CreateSessionRequest struct {
	AgentID       string `json:"agentId"`
	EnvironmentID string `json:"environmentId"`
	Title         string `json:"title"`
	Provider      string `json:"provider"`
	Model         string `json:"model"`
}

// SessionTokenStats is the cumulative token usage for a conversation. codingto
// aggregates it from each assistant message's usage payload in the session
// event file. (We previously relied solely on Pi's get_session_stats command,
// which was not returning usable data, so the composer always showed zeros.)
type SessionTokenStats struct {
	Input      int64 `json:"input"`
	Cached     int64 `json:"cached"`
	CacheWrite int64 `json:"cacheWrite"`
	Output     int64 `json:"output"`
	Total      int64 `json:"total"`
}

// SessionContextUsage mirrors the context-window view surfaced in the composer.
type SessionContextUsage struct {
	Tokens        int64 `json:"tokens"`
	ContextWindow int64 `json:"contextWindow"`
	Percent       int   `json:"percent"`
}

type SessionHistory struct {
	Messages     []map[string]any    `json:"messages"`
	TokenStats   SessionTokenStats   `json:"tokenStats"`
	ContextUsage SessionContextUsage `json:"contextUsage"`
	SubagentUI   map[string]any      `json:"subagentUi,omitempty"`
}

type SubagentUIResponse struct {
	ID        string `json:"id"`
	Value     any    `json:"value,omitempty"`
	Confirmed *bool  `json:"confirmed,omitempty"`
	Cancelled bool   `json:"cancelled,omitempty"`
}

func (a *App) ListSessions() ([]Session, error) {
	items, err := a.store.Store().ListSessions()
	if err != nil {
		return nil, err
	}
	result := make([]Session, 0, len(items))
	for _, item := range items {
		result = append(result, sessionFromStore(item))
	}
	return result, nil
}

func (a *App) CreateSession(req CreateSessionRequest) (Session, error) {
	cfg := a.store.Get()
	profile, ok := cfg.Agent(req.AgentID)
	if !ok {
		return Session{}, fmt.Errorf("agent not found: %s", req.AgentID)
	}
	if strings.TrimSpace(req.Provider) == "" {
		req.Provider = profile.DefaultProvider
	}
	if strings.TrimSpace(req.Model) == "" {
		req.Model = profile.DefaultModel
	}
	if req.Provider == "" || req.Model == "" {
		req.Provider, req.Model = cfg.DefaultProvider, cfg.DefaultModel
	}
	title := truncateRunes(strings.TrimSpace(req.Title), 50)
	if title == "" {
		title = time.Now().Format("2006-01-02 15:04:05")
	}
	item, err := a.store.Store().CreateSession(store.Session{
		AgentID:       req.AgentID,
		EnvironmentID: req.EnvironmentID,
		Title:         title,
		Provider:      req.Provider,
		Model:         req.Model,
		Status:        "active",
	})
	if err != nil {
		return Session{}, err
	}
	sessionDir := filepath.Join(cfg.SessionDir, fmt.Sprintf("s%d", item.ID))
	if err := ensurePrivateDir(sessionDir); err != nil {
		_ = a.store.Store().DeleteSession(item.ID)
		return Session{}, fmt.Errorf("create session log directory: %w", err)
	}
	if err := a.store.Store().UpdateSession(item.ID, map[string]any{"session_dir": sessionDir}); err != nil {
		_ = os.RemoveAll(sessionDir)
		_ = a.store.Store().DeleteSession(item.ID)
		return Session{}, err
	}
	item.SessionDir = sessionDir
	return sessionFromStore(item), nil
}

func (a *App) GetSessionHistory(id int64) (SessionHistory, error) {
	item, ok, err := a.store.Store().SessionByID(id)
	if err != nil {
		return SessionHistory{}, err
	}
	if !ok {
		return SessionHistory{}, errors.New("session not found")
	}
	messages := readSessionMessages(item.SessionDir)
	tokenStats, contextUsage := readSessionTokenStats(item.SessionDir)
	return SessionHistory{
		Messages:     messages,
		TokenStats:   tokenStats,
		ContextUsage: contextUsage,
	}, nil
}

func (a *App) GetSubagentTranscript(sessionID int64, runID string) (SessionHistory, error) {
	runDir, err := a.subagentRunDir(sessionID, runID)
	if err != nil {
		return SessionHistory{}, err
	}
	if info, err := os.Stat(runDir); err != nil || !info.IsDir() {
		return SessionHistory{}, errors.New("subagent run not found")
	}
	// History is rebuilt from Pi's own session file. A legacy codingto_events.jsonl
	// used to accumulate every streaming event (each message_update carried a
	// cumulative content snapshot), ballooning to gigabytes and stalling this
	// endpoint, so an oversized leftover is dropped before reading.
	if info, err := os.Stat(filepath.Join(runDir, sessionEventFile)); err == nil && info.Size() > 100<<20 {
		_ = os.Remove(filepath.Join(runDir, sessionEventFile))
	}
	messages := ParseSubagentPiSession(runDir)
	tokenStats, contextUsage := readSubagentTokenStats(runDir)
	return SessionHistory{
		Messages: messages, TokenStats: tokenStats, ContextUsage: contextUsage,
		SubagentUI: readSubagentUIState(runDir),
	}, nil
}

func (a *App) RespondSubagentUI(sessionID int64, runID string, response SubagentUIResponse) error {
	runDir, err := a.subagentRunDir(sessionID, runID)
	if err != nil {
		return err
	}
	response.ID = strings.TrimSpace(response.ID)
	if response.ID == "" || len(response.ID) > 512 {
		return errors.New("invalid subagent UI request id")
	}
	state := readSubagentUIState(runDir)
	dialog := mapValue(state["dialog"])
	if stringValue(dialog["id"]) != response.ID {
		return errors.New("subagent UI request is no longer pending")
	}
	responseDir := filepath.Join(runDir, ".ui-responses")
	if err := ensurePrivateDir(responseDir); err != nil {
		return err
	}
	raw, err := json.Marshal(response)
	if err != nil {
		return err
	}
	finalPath := filepath.Join(responseDir, subagentUIResponseFileName(response.ID))
	tempPath := finalPath + ".tmp"
	if err := os.WriteFile(tempPath, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tempPath, finalPath); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	return nil
}

// AckSubagentUI records that the frontend rendered an interactive dialog for a
// running subagent. It mirrors the parent agent's extension_ui_ack: the bridge's
// UI timeout only bounds the ack window, so after this call the user may take
// as long as they need to answer without the dialog being force-cancelled. The
// ack is dropped into the run's ack mailbox and never reaches the subagent; it
// is not an answer.
func (a *App) AckSubagentUI(sessionID int64, runID, requestID string) error {
	runDir, err := a.subagentRunDir(sessionID, runID)
	if err != nil {
		return err
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" || len(requestID) > 512 {
		return errors.New("invalid subagent UI request id")
	}
	state := readSubagentUIState(runDir)
	dialog := mapValue(state["dialog"])
	if stringValue(dialog["id"]) != requestID {
		return errors.New("subagent UI request is no longer pending")
	}
	ackDir := filepath.Join(runDir, ".ui-acks")
	if err := ensurePrivateDir(ackDir); err != nil {
		return err
	}
	raw, err := json.Marshal(map[string]string{"id": requestID})
	if err != nil {
		return err
	}
	finalPath := filepath.Join(ackDir, subagentUIResponseFileName(requestID))
	tempPath := finalPath + ".tmp"
	if err := os.WriteFile(tempPath, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tempPath, finalPath); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	return nil
}

// AbortSubagent stops a running subagent by dropping an abort marker into its
// run directory. The bridge's run loop polls the marker and terminates the
// subagent Pi process directly, so cancellation does not depend on the parent
// agent's tool-execution abort chain (which can be wedged if the parent Pi is
// blocked).
func (a *App) AbortSubagent(sessionID int64, runID string) error {
	runDir, err := a.subagentRunDir(sessionID, runID)
	if err != nil {
		return err
	}
	// Only stop runs that are actually in progress; a finished run must not be
	// left with a marker that could confuse later reads. A failed read (run.json
	// still being written, or a transient rename gap) also writes the marker:
	// the marker is idempotent, run IDs are unique, and the bridge only polls it
	// and removes it when the run ends, so a rejected abort is worse than a
	// harmless stale marker.
	record, err := subagentbridge.ReadRunRecord(filepath.Join(runDir, "run.json"))
	if err == nil && record.Status != "running" {
		return nil
	}
	marker := filepath.Join(runDir, ".abort")
	if err := os.WriteFile(marker, []byte("1"), 0o600); err != nil {
		return err
	}
	return nil
}

func subagentUIResponseFileName(requestID string) string {
	sum := sha256.Sum256([]byte(requestID))
	return fmt.Sprintf("%x.json", sum)
}

func (a *App) subagentRunDir(sessionID int64, runID string) (string, error) {
	if !subagentRunIDPattern.MatchString(runID) {
		return "", errors.New("invalid subagent run id")
	}
	item, ok, err := a.store.Store().SessionByID(sessionID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", errors.New("session not found")
	}
	root, err := filepath.Abs(filepath.Join(item.SessionDir, "subagents"))
	if err != nil {
		return "", err
	}
	runDir, err := filepath.Abs(filepath.Join(root, runID))
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, runDir)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return "", errors.New("subagent transcript path escapes session")
	}
	return runDir, nil
}

func readSubagentUIState(runDir string) map[string]any {
	state := map[string]any{"widgets": map[string]any{}}
	file, err := os.Open(filepath.Join(runDir, sessionEventFile))
	if err != nil {
		return state
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var event map[string]any
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		switch stringValue(event["type"]) {
		case "extension_ui_request":
			method := stringValue(event["method"])
			if method == "setWidget" {
				key := stringValue(event["widgetKey"])
				if key == "" {
					continue
				}
				widgets := mapValue(state["widgets"])
				if event["widgetLines"] == nil {
					delete(widgets, key)
				} else {
					widgets[key] = event["widgetLines"]
				}
				state["widgets"] = widgets
			} else if method == "select" || method == "confirm" || method == "input" || method == "editor" {
				state["dialog"] = event
			}
		case "subagent_ui_response":
			dialog := mapValue(state["dialog"])
			if stringValue(dialog["id"]) == stringValue(event["id"]) {
				delete(state, "dialog")
			}
		}
	}
	return state
}

// readSessionTokenStats aggregates cumulative token usage from the agent's own
// session event file (the *.jsonl written by Pi, which carries a `usage` payload
// per assistant message). codingto's own codingto_events.jsonl only stores
// streaming deltas, so Pi's file is the authoritative source here. This lets
// historical conversations show token stats without depending on Pi's
// get_session_stats command at read time.
func readSessionTokenStats(sessionDir string) (SessionTokenStats, SessionContextUsage) {
	var stats SessionTokenStats
	var lastContextTokens int64
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		return stats, SessionContextUsage{}
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		if entry.Name() == sessionEventFile {
			continue
		}
		f, err := os.Open(filepath.Join(sessionDir, entry.Name()))
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 64*1024), 64*1024*1024)
		for scanner.Scan() {
			var event map[string]any
			if json.Unmarshal(scanner.Bytes(), &event) != nil {
				continue
			}
			if stringValue(event["type"]) != "message" {
				continue
			}
			// In Pi's session file the message payload (including role and the
			// per-turn usage) is nested under the top-level "message" key, not
			// at the event root. See an actual *.jsonl line:
			//   {"type":"message","message":{"role":"assistant","usage":{...}}}
			msg := mapValue(event["message"])
			if stringValue(msg["role"]) != "assistant" {
				continue
			}
			usage := mapValue(msg["usage"])
			if len(usage) == 0 {
				continue
			}
			stats.Input += intValue(usage["input"])
			stats.Cached += intValue(usage["cacheRead"])
			stats.CacheWrite += intValue(usage["cacheWrite"])
			stats.Output += intValue(usage["output"])
			stats.Total += intValue(usage["totalTokens"])
			if t := intValue(usage["totalTokens"]); t > 0 {
				lastContextTokens = t
			}
		}
		f.Close()
	}
	// The per-turn usage is cumulative for that turn, so the last one also
	// reflects the most recent context size. We surface it as a token count
	// only; the context window itself is reported by Pi's get_session_stats at
	// runtime, so we leave ContextWindow/Percent at zero here.
	return stats, SessionContextUsage{Tokens: lastContextTokens}
}

func (a *App) DeleteSession(id int64) error {
	item, ok, err := a.store.Store().SessionByID(id)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if err := a.agent.StopSession(id); err != nil {
		return err
	}
	if item.SessionDir != "" {
		if filepath.Base(filepath.Clean(item.SessionDir)) != fmt.Sprintf("s%d", id) {
			return fmt.Errorf("refuse to delete unexpected session directory: %s", item.SessionDir)
		}
		if err := os.RemoveAll(item.SessionDir); err != nil {
			return err
		}
	}
	return a.store.Store().DeleteSession(id)
}

func sessionFromStore(item store.Session) Session {
	return Session{
		ID:             item.ID,
		AgentID:        item.AgentID,
		EnvironmentID:  item.EnvironmentID,
		Title:          item.Title,
		SessionDir:     item.SessionDir,
		SessionPath:    item.SessionPath,
		Provider:       item.Provider,
		Model:          item.Model,
		Status:         item.Status,
		ExecDurationMs: item.ExecDurationMs,
		CreatedAt:      item.CreateTime,
		UpdatedAt:      item.UpdateTime,
	}
}

func truncateRunes(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max]) + "..."
}

func appendSessionEvent(sessionDir string, event any) error {
	return appendSessionEventWithDurability(sessionDir, event, true)
}

func appendSessionEventWithDurability(sessionDir string, event any, durable bool) error {
	if sessionDir == "" {
		return nil
	}
	if err := ensurePrivateDir(sessionDir); err != nil {
		return err
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return err
	}
	eventPath := filepath.Join(sessionDir, sessionEventFile)
	file, err := os.OpenFile(eventPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := os.Chmod(eventPath, 0o600); err != nil {
		return err
	}
	if _, err := file.Write(append(raw, '\n')); err != nil {
		return err
	}
	if durable {
		return file.Sync()
	}
	return nil
}

// compactSessionEvent removes cumulative streaming payloads before they reach
// disk. The live UI still receives the original event. Canonical message_end
// and tool execution events preserve completed history, while small text and
// thinking deltas retain crash recovery for an in-progress response.
func compactSessionEvent(event any) (any, bool) {
	value, ok := event.(map[string]any)
	if !ok {
		return event, true
	}
	eventType := stringValue(value["type"])
	if eventType == "tool_execution_update" {
		if subagentUpdateNeedsPersistence(value) {
			return event, true
		}
		return nil, false
	}
	if eventType != "message_update" {
		return event, true
	}

	update := mapValue(value["assistantMessageEvent"])
	updateType := stringValue(update["type"])
	switch updateType {
	case "toolcall_start", "tool_call_start", "toolcall_delta", "tool_call_delta", "toolcall_end", "tool_call_end",
		"text_start", "text_end":
		// message_end contains the canonical tool call or final text, and
		// tool_execution_start contains the executable tool arguments.
		return nil, false
	case "text_delta", "thinking_delta", "thinking_start", "thinking_end", "error":
	default:
		return nil, false
	}

	compacted := map[string]any{
		"type": eventType,
		"assistantMessageEvent": map[string]any{
			"type": updateType,
		},
	}
	for _, key := range []string{"_recordedAt", "codingToSessionId", "sessionId", "changeNodeId"} {
		if field, exists := value[key]; exists {
			compacted[key] = field
		}
	}
	compactedUpdate := compacted["assistantMessageEvent"].(map[string]any)
	for _, key := range []string{"delta", "error", "content", "message"} {
		if field, exists := update[key]; exists {
			compactedUpdate[key] = field
		}
	}
	return compacted, true
}

func subagentUpdateNeedsPersistence(event map[string]any) bool {
	subagent := findSubagentEvent(event)
	if subagent == nil {
		return false
	}
	raw, hasEvent := subagent["event"]
	if !hasEvent || raw == nil {
		// The first update carries the run ID needed to reopen the transcript
		// after switching away from an active conversation.
		return true
	}
	child := mapValue(raw)
	if text, ok := raw.(string); ok {
		_ = json.Unmarshal([]byte(text), &child)
	}
	switch stringValue(child["type"]) {
	case "extension_ui_request", "subagent_ui_response":
		return true
	default:
		return false
	}
}

func sessionEventNeedsSync(event any) bool {
	value, ok := event.(map[string]any)
	if !ok {
		return true
	}
	switch stringValue(value["type"]) {
	case "user_text", "message_end", "tool_execution_end", "agent_end", "agent_settled",
		"compaction_start", "compaction_end":
		return true
	default:
		return false
	}
}

func readSessionMessages(sessionDir string) []map[string]any {
	file, err := os.Open(filepath.Join(sessionDir, sessionEventFile))
	if err != nil {
		return []map[string]any{}
	}
	defer file.Close()

	messages := []map[string]any{}
	activeAssistant := -1
	toolIndexes := map[string]int{}
	thinkingStartMs := map[int]int64{}
	activeCompaction := -1
	scanner := bufio.NewScanner(file)
	// A user event may contain up to 100 MB of raw image data encoded as base64.
	// Keep the history reader aligned with the prompt limits so valid image
	// turns are not silently dropped after reopening a conversation.
	scanner.Buffer(make([]byte, 64*1024), 160*1024*1024)
	for scanner.Scan() {
		var event map[string]any
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		eventType := stringValue(event["type"])
		recordedAt := intValue(event["_recordedAt"])
		switch eventType {
		case "compaction_start":
			messages = append(messages, map[string]any{
				"id": fmt.Sprintf("history-compaction-%d", len(messages)), "role": "compaction",
				"status": "running", "reason": firstNonEmptyString(stringValue(event["reason"]), "manual"),
				"tokensBefore": int64(0), "estimatedTokensAfter": int64(0),
				"error": "", "aborted": false, "createdAt": recordedAt,
			})
			activeCompaction = len(messages) - 1
		case "compaction_end":
			if activeCompaction < 0 || activeCompaction >= len(messages) {
				messages = append(messages, map[string]any{
					"id": fmt.Sprintf("history-compaction-%d", len(messages)), "role": "compaction",
					"status": "running", "reason": firstNonEmptyString(stringValue(event["reason"]), "manual"),
					"tokensBefore": int64(0), "estimatedTokensAfter": int64(0),
					"error": "", "aborted": false, "createdAt": recordedAt,
				})
				activeCompaction = len(messages) - 1
			}
			result := mapValue(event["result"])
			errorMessage := stringValue(event["errorMessage"])
			status := "completed"
			if errorMessage != "" {
				status = "error"
			} else if aborted, _ := event["aborted"].(bool); aborted {
				status = "aborted"
			}
			messages[activeCompaction]["status"] = status
			messages[activeCompaction]["reason"] = firstNonEmptyString(
				stringValue(event["reason"]), stringValue(messages[activeCompaction]["reason"]), "manual",
			)
			messages[activeCompaction]["tokensBefore"] = intValue(result["tokensBefore"])
			messages[activeCompaction]["estimatedTokensAfter"] = intValue(result["estimatedTokensAfter"])
			messages[activeCompaction]["error"] = errorMessage
			messages[activeCompaction]["aborted"] = status == "aborted"
			messages[activeCompaction]["completedAt"] = recordedAt
			activeCompaction = -1
		case "user_text":
			content := stringValue(event["message"])
			if displayMessage, ok := event["displayMessage"]; ok {
				content = stringValue(displayMessage)
			}
			messages = append(messages, map[string]any{
				"id": fmt.Sprintf("history-user-%d", len(messages)), "role": "user",
				"content": content, "createdAt": recordedAt,
				"images": event["images"], "attachments": event["attachments"],
			})
			activeAssistant = -1
			toolIndexes = map[string]int{}
		case "agent_start":
			activeAssistant = -1
			toolIndexes = map[string]int{}
		case "message_update":
			update := mapValue(event["assistantMessageEvent"])
			switch stringValue(update["type"]) {
			case "text_delta", "thinking_delta", "thinking_start":
				if activeAssistant < 0 {
					messages = append(messages, map[string]any{
						"id":   fmt.Sprintf("history-assistant-%d", len(messages)),
						"role": "assistant", "content": "", "thinkingContent": "",
						"createdAt": recordedAt,
					})
					activeAssistant = len(messages) - 1
				}
				if stringValue(update["type"]) == "text_delta" {
					messages[activeAssistant]["content"] = stringValue(messages[activeAssistant]["content"]) + stringValue(update["delta"])
				} else if stringValue(update["type"]) == "thinking_delta" {
					messages[activeAssistant]["thinkingContent"] = stringValue(messages[activeAssistant]["thinkingContent"]) + stringValue(update["delta"])
				} else if stringValue(update["type"]) == "thinking_start" {
					thinkingStartMs[activeAssistant] = recordedAt
				}
			case "toolcall_start", "tool_call_start", "toolcall_delta", "tool_call_delta", "toolcall_end", "tool_call_end":
				appendHistoryTool(&messages, toolIndexes, update, recordedAt)
			case "thinking_end":
				if activeAssistant >= 0 {
					if start, ok := thinkingStartMs[activeAssistant]; ok && start > 0 {
						messages[activeAssistant]["thinkingDurationMs"] = recordedAt - start
					}
				}
			}
		case "tool_execution_start":
			id := toolID(event)
			if id == "" {
				appendHistoryTool(&messages, toolIndexes, event, recordedAt)
				break
			}
			if index, ok := toolIndexes[id]; ok {
				// The tool message may already exist (created from an earlier
				// streaming "toolcall_start" event) but without its real
				// arguments. Merge the execution payload so the command/path
				// and tool name are preserved for the history view.
				detail := mapValue(messages[index]["detail"])
				for key, value := range event {
					detail[key] = value
				}
				if input := firstPresent(event["args"], event["input"], event["arguments"]); input != nil {
					detail["input"] = input
				}
				if name := stringValue(event["toolName"]); name != "" {
					messages[index]["content"] = name
					detail["name"] = name
				}
				messages[index]["detail"] = detail
			} else {
				appendHistoryTool(&messages, toolIndexes, event, recordedAt)
			}
		case "tool_execution_update", "tool_execution_end":
			id := toolID(event)
			index, ok := toolIndexes[id]
			if !ok {
				appendHistoryTool(&messages, toolIndexes, event, recordedAt)
				index, ok = toolIndexes[id]
			}
			if ok {
				detail := mapValue(messages[index]["detail"])
				for key, value := range event {
					detail[key] = value
				}
				if subagent := findSubagentEvent(event); subagent != nil {
					detail["subagent"] = subagent
					if subagent["event"] != nil {
						events, _ := detail["subagentEvents"].([]any)
						detail["subagentEvents"] = append(events, subagent)
					}
				}
				if eventType == "tool_execution_end" {
					detail["status"] = "done"
					detail["endedAt"] = recordedAt
					detail["durationMs"] = recordedAt - intValue(detail["startedAt"])
				}
				messages[index]["detail"] = detail
			}
		case "message_end":
			message := mapValue(event["message"])
			if stringValue(message["role"]) != "assistant" {
				continue
			}
			text, thinking := piMessageContent(message)
			if activeAssistant < 0 && (text != "" || thinking != "") {
				messages = append(messages, map[string]any{
					"id":   fmt.Sprintf("history-assistant-%d", len(messages)),
					"role": "assistant", "content": text, "thinkingContent": thinking,
					"createdAt": recordedAt,
				})
				activeAssistant = len(messages) - 1
			} else if activeAssistant >= 0 {
				if text != "" {
					messages[activeAssistant]["content"] = text
				}
				if thinking != "" {
					messages[activeAssistant]["thinkingContent"] = thinking
				}
			}
			activeAssistant = -1
		case "agent_end":
			if summary := mapValue(event["changeSummary"]); len(summary) > 0 {
				nodeID := stringValue(summary["nodeId"])
				messages = append(messages, map[string]any{
					"id": fmt.Sprintf("history-changes-%s", nodeID), "role": "changes",
					"changes": summary, "createdAt": recordedAt,
				})
			}
			activeAssistant = -1
			toolIndexes = map[string]int{}
		case "error":
			message := stringValue(event["error"])
			if message == "" {
				message = stringValue(mapValue(event["error"])["message"])
			}
			if message != "" {
				messages = append(messages, map[string]any{
					"id": fmt.Sprintf("history-error-%d", len(messages)), "role": "error",
					"content": message, "createdAt": recordedAt,
				})
			}
		}
	}
	// A history read can happen while this conversation continues in the
	// background. Mark an unterminated streamed assistant message so the
	// frontend can resume appending deltas to it after the user returns.
	if activeAssistant >= 0 && activeAssistant < len(messages) {
		messages[activeAssistant]["live"] = true
	}
	return messages
}

// subagentPiSessionFile locates Pi's own session file for a subagent run
// (e.g. 2026-07-31T12-17-20-992Z_codingto-subagent-run-<runID>.jsonl). Pi
// appends one record per completed message, so the file stays compact while
// carrying the full user/assistant/toolResult history — the source of truth
// for the subagent details view.
func subagentPiSessionFile(runDir string) string {
	matches, err := filepath.Glob(filepath.Join(runDir, "*codingto-subagent-*.jsonl"))
	if err != nil {
		return ""
	}
	for _, match := range matches {
		if strings.HasSuffix(match, sessionEventFile) {
			continue
		}
		return match
	}
	return ""
}

// ParseSubagentPiSession rebuilds the message list shown in the subagent details
// dialog from Pi's session file. Pending tool calls from assistant messages are
// matched FIFO against the following toolResult records, mirroring the shape
// produced by readSessionMessages so the frontend renders it identically.
// It is exported (not a Wails binding — only App methods are bound) so the
// version-controlled tests under ./test can exercise it black-box.
func ParseSubagentPiSession(runDir string) []map[string]any {
	path := subagentPiSessionFile(runDir)
	if path == "" {
		return []map[string]any{}
	}
	file, err := os.Open(path)
	if err != nil {
		return []map[string]any{}
	}
	defer file.Close()

	messages := []map[string]any{}
	pendingTools := []map[string]any{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 160*1024*1024)
	for scanner.Scan() {
		var event map[string]any
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		if stringValue(event["type"]) != "message" {
			continue
		}
		msg := mapValue(event["message"])
		createdAt := parsePiEventTime(stringValue(event["timestamp"]))
		switch stringValue(msg["role"]) {
		case "user":
			text, _ := piMessageContent(msg)
			messages = append(messages, map[string]any{
				"id": fmt.Sprintf("subagent-user-%d", len(messages)), "role": "user",
				"content": text, "createdAt": createdAt,
			})
		case "assistant":
			text, thinking := piMessageContent(msg)
			for _, block := range piContentBlocks(msg) {
				if stringValue(block["type"]) != "toolCall" {
					continue
				}
				arguments := firstPresent(block["arguments"], block["partialArgs"])
				pendingTools = append(pendingTools, map[string]any{
					"id": stringValue(block["id"]), "name": stringValue(block["name"]),
					"arguments": arguments,
				})
			}
			if text != "" || thinking != "" {
				messages = append(messages, map[string]any{
					"id": fmt.Sprintf("subagent-assistant-%d", len(messages)), "role": "assistant",
					"content": text, "thinkingContent": thinking, "createdAt": createdAt,
				})
			}
		case "toolResult":
			call := map[string]any{}
			if len(pendingTools) > 0 {
				call = pendingTools[0]
				pendingTools = pendingTools[1:]
			}
			output, _ := piMessageContent(msg)
			name := firstNonEmptyString(stringValue(msg["toolName"]), stringValue(call["name"]))
			status := "done"
			if boolValue(msg["isError"]) {
				status = "error"
			}
			detail := map[string]any{
				"toolCallId": firstNonEmptyString(stringValue(msg["toolCallId"]), stringValue(call["id"])),
				"name":       name,
				"args":       call["arguments"],
				"input":      call["arguments"],
				"output":     output,
				"status":     status,
				"startedAt":  createdAt,
				"endedAt":    createdAt,
			}
			messages = append(messages, map[string]any{
				"id": fmt.Sprintf("subagent-tool-%d", len(messages)), "role": "tool",
				"content": name, "detail": detail, "createdAt": createdAt,
			})
		}
	}
	return messages
}

// readSubagentTokenStats aggregates usage from Pi's session file, matching the
// shape of readSessionTokenStats.
func readSubagentTokenStats(runDir string) (SessionTokenStats, SessionContextUsage) {
	path := subagentPiSessionFile(runDir)
	stats := SessionTokenStats{}
	lastContextTokens := int64(0)
	if path == "" {
		return stats, SessionContextUsage{}
	}
	file, err := os.Open(path)
	if err != nil {
		return stats, SessionContextUsage{}
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 64*1024*1024)
	for scanner.Scan() {
		var event map[string]any
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		if stringValue(event["type"]) != "message" {
			continue
		}
		msg := mapValue(event["message"])
		if stringValue(msg["role"]) != "assistant" {
			continue
		}
		usage := mapValue(msg["usage"])
		if len(usage) == 0 {
			continue
		}
		stats.Input += intValue(usage["input"])
		stats.Cached += intValue(usage["cacheRead"])
		stats.CacheWrite += intValue(usage["cacheWrite"])
		stats.Output += intValue(usage["output"])
		stats.Total += intValue(usage["totalTokens"])
		if t := intValue(usage["totalTokens"]); t > 0 {
			lastContextTokens = t
		}
	}
	return stats, SessionContextUsage{Tokens: lastContextTokens}
}

func piContentBlocks(message map[string]any) []map[string]any {
	content, _ := message["content"].([]any)
	blocks := make([]map[string]any, 0, len(content))
	for _, raw := range content {
		if block := mapValue(raw); block != nil {
			blocks = append(blocks, block)
		}
	}
	return blocks
}

func parsePiEventTime(value string) int64 {
	if value == "" {
		return 0
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return 0
	}
	return parsed.UnixMilli()
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func appendHistoryTool(messages *[]map[string]any, indexes map[string]int, event map[string]any, recordedAt int64) {
	call := eventToolCall(event)
	id := toolID(event)
	if id == "" {
		id = stringValue(call["id"])
	}
	if id == "" {
		return
	}
	if _, exists := indexes[id]; exists {
		return
	}
	name := stringValue(event["toolName"])
	if name == "" {
		name = stringValue(event["name"])
	}
	if call := mapValue(event["toolCall"]); name == "" {
		name = stringValue(call["name"])
	}
	if name == "" {
		name = stringValue(call["name"])
	}
	detail := make(map[string]any, len(event)+4)
	for key, value := range event {
		detail[key] = value
	}
	detail["toolCallId"] = id
	if input := firstPresent(call["arguments"], call["partialArgs"], event["args"], event["input"], event["arguments"]); input != nil {
		detail["input"] = input
	}
	detail["status"] = "running"
	detail["startedAt"] = recordedAt
	*messages = append(*messages, map[string]any{
		"id": fmt.Sprintf("history-tool-%s", id), "role": "tool",
		"content": name, "detail": detail, "createdAt": recordedAt,
	})
	indexes[id] = len(*messages) - 1
}

func toolID(event map[string]any) string {
	for _, key := range []string{"toolCallId", "id"} {
		if value := stringValue(event[key]); value != "" {
			return value
		}
	}
	return stringValue(mapValue(event["toolCall"])["id"])
}

func eventToolCall(event map[string]any) map[string]any {
	if call := mapValue(event["toolCall"]); stringValue(call["id"]) != "" {
		return call
	}
	partial := mapValue(event["partial"])
	content, _ := partial["content"].([]any)
	if index := int(intValue(event["contentIndex"])); index >= 0 && index < len(content) {
		if call := mapValue(content[index]); stringValue(call["type"]) == "toolCall" && stringValue(call["id"]) != "" {
			return call
		}
	}
	for index := len(content) - 1; index >= 0; index-- {
		call := mapValue(content[index])
		if stringValue(call["type"]) == "toolCall" && stringValue(call["id"]) != "" {
			return call
		}
	}
	return map[string]any{}
}

func firstPresent(values ...any) any {
	for _, value := range values {
		if value != nil && value != "" {
			return value
		}
	}
	return nil
}

func piMessageContent(message map[string]any) (string, string) {
	var textParts, thinkingParts []string
	content, _ := message["content"].([]any)
	for _, rawBlock := range content {
		block := mapValue(rawBlock)
		switch stringValue(block["type"]) {
		case "text":
			textParts = append(textParts, stringValue(block["text"]))
		case "thinking":
			thinkingParts = append(thinkingParts, stringValue(block["thinking"]))
		}
	}
	return strings.Join(textParts, ""), strings.Join(thinkingParts, "")
}

func mapValue(value any) map[string]any {
	if result, ok := value.(map[string]any); ok {
		return result
	}
	return map[string]any{}
}

func boolValue(value any) bool {
	result, ok := value.(bool)
	return ok && result
}

func stringValue(value any) string {
	if result, ok := value.(string); ok {
		return result
	}
	return ""
}

func intValue(value any) int64 {
	switch number := value.(type) {
	case int64:
		return number
	case int:
		return int64(number)
	case float64:
		return int64(number)
	}
	return 0
}
