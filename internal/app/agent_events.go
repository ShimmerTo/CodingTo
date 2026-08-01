package app

import (
	"encoding/json"
	"log"
	"sort"
	"strconv"
	"time"

	"codingto/internal/piagent"

	"github.com/wailsapp/wails/v3/pkg/application"
)

func (s *AgentService) forwardEvents(adapter *piagent.Adapter, sessionID int64, sessionDir string) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	events := adapter.Events()
	merger := newStreamEventMerger()
	var flushTimer *time.Timer
	var flushC <-chan time.Time
	stopFlushTimer := func() {
		if flushTimer != nil {
			flushTimer.Stop()
			flushTimer = nil
			flushC = nil
		}
	}
	defer stopFlushTimer()
	flushPending := func() {
		merger.flush(func(event map[string]any) {
			s.dispatchEvent(adapter, sessionID, sessionDir, event, nil)
		})
	}
	for {
		select {
		case evt, ok := <-events:
			if !ok {
				flushPending()
				goto closed
			}
			var payload any
			if err := json.Unmarshal(evt.Raw, &payload); err != nil {
				payload = map[string]any{"type": "raw", "data": string(evt.Raw)}
			}
			event, isMap := payload.(map[string]any)
			if !isMap {
				// 与不可合并事件同理：先排空缓冲增量，保证前端按序观察。
				flushPending()
				stopFlushTimer()
				application.Get().Event.Emit("agent:event", payload)
				continue
			}
			event["_recordedAt"] = time.Now().UnixMilli()
			event["codingToSessionId"] = sessionID
			s.mu.Lock()
			if s.activeSessionID == sessionID && s.activeChangeNode != "" {
				event["changeNodeId"] = s.activeChangeNode
			}
			s.mu.Unlock()
			// 流式增量事件（text/thinking/toolcall delta）按 token 逐条到达，
			// 先合并缓冲再按窗口批量转发，避免 WebView 桥接消息洪泛。
			if key := streamMergeKey(event); key != "" {
				if merger.add(key, event) {
					flushTimer = time.NewTimer(streamMergeWindow)
					flushC = flushTimer.C
				}
				continue
			}
			// 生命周期与不可合并事件必须观察到此前缓冲的全部增量，
			// 先按序排空合并缓冲再处理本事件。
			flushPending()
			stopFlushTimer()
			s.dispatchEvent(adapter, sessionID, sessionDir, event, evt.Raw)
		case <-flushC:
			flushPending()
			stopFlushTimer()
		case <-ticker.C:
			s.emitExecProgressFor(sessionID)
		}
	}

closed:
	if adapter.IsRunning() {
		return
	}
	s.mu.Lock()
	interruptedNodeID := ""
	if s.activeSessionID == sessionID {
		interruptedNodeID = s.activeChangeNode
		s.activeChangeNode = ""
	}
	s.finishExecutionLocked("active")
	s.disarmUIWatchdogLocked("")
	s.mu.Unlock()
	if interruptedNodeID != "" {
		_ = finishChangeNode(sessionDir, interruptedNodeID, "interrupted", time.Now().UnixMilli())
	}
	state := map[string]any{
		"running": false, "processRunning": false, "codingToSessionId": sessionID,
	}
	if err := adapter.ExitError(); err != nil {
		state["error"] = err.Error()
	}
	application.Get().Event.Emit("agent:state", state)
}

// dispatchEvent 处理单条已填充元数据（_recordedAt / codingToSessionId /
// changeNodeId）的事件：推进会话状态机、持久化、精简并转发前端。raw 为事件
// 原始 JSON；合并产生的事件没有原始字节（传 nil），仅在提取 agent_end 错误
// 时按需重新序列化。
func (s *AgentService) dispatchEvent(adapter *piagent.Adapter, sessionID int64, sessionDir string, event map[string]any, raw json.RawMessage) {
	recordedAt := intValue(event["_recordedAt"])
	if recordedAt == 0 {
		recordedAt = time.Now().UnixMilli()
		event["_recordedAt"] = recordedAt
	}
	nodeID := stringValue(event["changeNodeId"])
	eventType := stringValue(event["type"])

	var restartReq PromptRequest
	var restartTools bool
	restartAfterEvent := false
	completedNodeID := ""
	s.mu.Lock()
	if event["type"] == "response" && event["command"] == "get_state" {
		if data, ok := event["data"].(map[string]any); ok {
			if path, ok := data["sessionFile"].(string); ok && path != "" {
				if s.activeSessionID == sessionID {
					s.activeSession = path
				}
				if s.pendingRestart && s.pendingReq.SessionID == sessionID {
					s.pendingReq.SessionPath = path
				}
				if sessionID > 0 {
					_ = s.store.Store().UpdateSession(sessionID, map[string]any{"session_path": path})
				}
			}
		}
	}
	if s.activeSessionID == sessionID && s.activeChangeNode != "" {
		willRetry, _ := event["willRetry"].(bool)
		if eventType == "auto_retry_start" || (eventType == "agent_end" && willRetry) {
			s.armFirstResponseWatchdogLocked(sessionID, s.activeChangeNode)
		} else if firstResponseObserved(event) {
			s.disarmFirstResponseWatchdogLocked()
		}
	}
	// An interactive extension UI request blocks Pi until the frontend
	// answers. Arm the watchdog so a dialog the frontend never renders
	// (or a lost frontend) cannot wedge the agent forever; the frontend
	// disarms it by acknowledging or answering the request.
	if isInteractiveUIEvent(event) {
		s.armUIWatchdogLocked(sessionID, sessionDir, stringValue(event["id"]))
	}
	if eventType == "agent_end" && s.activeSessionID == sessionID {
		willRetry, _ := event["willRetry"].(bool)
		if !willRetry {
			completedNodeID = s.activeChangeNode
			s.activeChangeNode = ""
		}
	}
	// agent_end only marks the end of one low-level run. Pi may
	// continue with an automatic retry, compaction, or queued work.
	// agent_settled is the authoritative end of the session-level
	// operation and therefore the point where a new prompt or
	// deferred restart becomes safe.
	if eventType == "agent_settled" && s.activeSessionID == sessionID {
		s.finishExecutionLocked("active")
		s.disarmUIWatchdogLocked("")
		if s.pendingRestart {
			restartReq = s.pendingReq
			if restartReq.SessionPath == "" {
				restartReq.SessionPath = s.activeSession
			}
			restartTools = s.pendingTools
			restartAfterEvent = true
			s.pendingRestart = false
		}
	}
	s.mu.Unlock()
	if completedNodeID != "" {
		status := "completed"
		if raw == nil {
			raw, _ = json.Marshal(event)
		}
		if agentEndErrorMessage(raw) != "" {
			status = "error"
		}
		if err := finishChangeNode(sessionDir, completedNodeID, status, recordedAt); err != nil {
			log.Printf("[session %d] finish change node: %v", sessionID, err)
		}
		summary, err := readChangeSummary(sessionDir, completedNodeID)
		if err != nil {
			log.Printf("[session %d] read completed change summary: %v", sessionID, err)
			// Still emit a completion notice for every prompt. The
			// sidebar refresh can resolve the node if only the
			// lightweight summary failed to load.
			summary = ChangeSummary{
				NodeID: completedNodeID, Status: status,
				Files: []FileChangeSummary{},
			}
		}
		event["changeSummary"] = summary
	}
	if err := s.appendEvent(sessionDir, event); err != nil {
		log.Printf("[session %d] append event: %v", sessionID, err)
	}
	if eventType == "tool_execution_update" {
		if subagent := findSubagentEvent(event); subagent != nil {
			slimSubagentStreamEvent(subagent)
			subagent["codingToSessionId"] = sessionID
			if nodeID != "" {
				subagent["parentNodeId"] = nodeID
			}
			application.Get().Event.Emit("subagent:event", subagent)
		}
	}
	if documentID, page, preview := documentPreviewRequest(event); preview {
		application.Get().Event.Emit("document:preview", map[string]any{
			"codingToSessionId": sessionID, "documentId": documentID, "page": page,
		})
	}
	if eventType == "agent_settled" && !restartAfterEvent {
		// Streamed usage is per-message and provider-shaped. Session stats
		// are Pi's canonical cumulative token and context-window view.
		_ = adapter.SendCommand(mustJSON(map[string]string{
			"id": "codingto-session-stats", "type": "get_session_stats",
		}))
	}
	application.Get().Event.Emit("agent:event", event)
	if eventType == "agent_settled" && !restartAfterEvent {
		application.Get().Event.Emit("agent:state", map[string]any{
			"running": false, "processRunning": true,
			"codingToSessionId": sessionID,
		})
	}
	if restartAfterEvent {
		if err := s.performRestart(restartReq, restartTools); err != nil {
			log.Printf("[agent] deferred restart failed: %v", err)
		}
	}
}

// streamMergeWindow 是流式增量事件的合并窗口。Pi 的 text/thinking/toolcall
// delta 按 token 逐条到达（子 agent 事件还会经 subagent:event 与 agent:event
// 双通道各发一份），10 分钟级任务可产生数万条桥接消息；前端每条都要重建
// detail/timeline 并重渲染，渲染速度跟不上到达速度时消息在 WebView 渲染进程
// 内排队积压，最终 OOM。80ms 批量转发把消息数量压缩一到两个数量级，而人眼
// 对打字机效果的感知阈值远高于此。
const streamMergeWindow = 80 * time.Millisecond

type pendingStreamEvent struct {
	event map[string]any
	seq   int
}

// streamEventMerger 缓冲可合并的流式增量事件。仅在 forwardEvents 的单个
// goroutine 内使用，无需加锁。
type streamEventMerger struct {
	pending map[string]*pendingStreamEvent
	seq     int
}

func newStreamEventMerger() *streamEventMerger {
	return &streamEventMerger{pending: map[string]*pendingStreamEvent{}}
}

// add 把事件并入缓冲；返回缓冲此前是否为空（调用方据此启动 flush 定时器，
// 非空时定时器已在运行，窗口自首个事件起算，保证延迟有界）。
func (m *streamEventMerger) add(key string, event map[string]any) bool {
	empty := len(m.pending) == 0
	m.seq++
	if existing, exists := m.pending[key]; exists {
		mergeStreamEvent(existing.event, event)
		return empty
	}
	m.pending[key] = &pendingStreamEvent{event: event, seq: m.seq}
	return empty
}

// flush 按到达顺序排空缓冲，对每条 merged 事件回调 dispatch。
func (m *streamEventMerger) flush(dispatch func(map[string]any)) {
	if len(m.pending) == 0 {
		return
	}
	ordered := make([]*pendingStreamEvent, 0, len(m.pending))
	for _, item := range m.pending {
		ordered = append(ordered, item)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].seq < ordered[j].seq })
	m.pending = make(map[string]*pendingStreamEvent, len(ordered))
	for _, item := range ordered {
		dispatch(item.event)
	}
}

// streamMergeKey 返回可合并流式事件的合并键（同键事件才可合并）；不可合并
// 的事件返回空串。合并粒度：主 agent 按 delta 类型，toolcall 再按
// contentIndex 区分并行工具调用；子 agent 事件再按 runId 隔离。
// 增量字段名（assistantMessageEvent / messageEvent）随前缀入键，两种命名
// 天然分桶，mergeAssistantUpdate 因此无需跨命名防御分支。
func streamMergeKey(event map[string]any) string {
	switch stringValue(event["type"]) {
	case "message_update":
		field, update := assistantUpdateTarget(event)
		return deltaMergeKey("main|"+field, update)
	case "tool_execution_update":
		subagent := findSubagentEvent(event)
		if subagent == nil {
			return ""
		}
		// event 为 JSON 字符串的防御分支不参与合并（解析-合并-再序列化
		// 成本高于直接转发，且该形态实际不出现）。
		inner, _ := subagent["event"].(map[string]any)
		if inner == nil || stringValue(inner["type"]) != "message_update" {
			return ""
		}
		// 空 runId 不参与合并，避免不同子 agent 落入同一桶。
		runID := stringValue(subagent["runId"])
		if runID == "" {
			return ""
		}
		field, update := assistantUpdateTarget(inner)
		return deltaMergeKey("sub:"+runID+"|"+field, update)
	}
	return ""
}

func deltaMergeKey(prefix string, update map[string]any) string {
	switch stringValue(update["type"]) {
	case "text_delta", "thinking_delta":
		// 前端只消费 delta 字符串增量，可安全拼接。
		if _, ok := update["delta"].(string); !ok {
			return ""
		}
		return prefix + "|" + stringValue(update["type"])
	case "toolcall_delta", "tool_call_delta":
		// 前端经 partial（累积快照）定位并读取工具调用，保留最新一条即
		// 等价；没有 partial 的异常事件不参与合并。
		if _, exists := update["partial"]; !exists {
			return ""
		}
		// contentIndex 区分并行工具调用；取不到时不合并，避免不同调用的
		// 快照落进同一桶互相覆盖。
		index, ok := update["contentIndex"].(float64)
		if !ok {
			return ""
		}
		return prefix + "|toolcall|" + strconv.Itoa(int(index))
	}
	return ""
}

// assistantUpdateTarget 返回 message_update 事件内 assistant 增量所在的
// 字段名与对象（兼容 assistantMessageEvent / messageEvent 两种命名）。
func assistantUpdateTarget(event map[string]any) (string, map[string]any) {
	if _, exists := event["assistantMessageEvent"]; exists {
		return "assistantMessageEvent", mapValue(event["assistantMessageEvent"])
	}
	return "messageEvent", mapValue(event["messageEvent"])
}

// mergeStreamEvent 把 src 合并进 dst（就地修改）。顶层元数据取最新；
// 子 agent 事件合并双方嵌套的内部 event（findSubagentEvent 返回浅拷贝，
// 其 event 字段与外层负载共享同一 map，就地合并即写回 dst）。
func mergeStreamEvent(dst, src map[string]any) {
	if value, exists := src["_recordedAt"]; exists {
		dst["_recordedAt"] = value
	}
	if value, exists := src["changeNodeId"]; exists {
		dst["changeNodeId"] = value
	}
	switch stringValue(dst["type"]) {
	case "message_update":
		mergeAssistantUpdate(dst, src)
	case "tool_execution_update":
		dstSub, srcSub := findSubagentEvent(dst), findSubagentEvent(src)
		if dstSub == nil || srcSub == nil {
			return
		}
		dstInner, _ := dstSub["event"].(map[string]any)
		srcInner, _ := srcSub["event"].(map[string]any)
		if dstInner != nil && srcInner != nil {
			mergeAssistantUpdate(dstInner, srcInner)
		}
	}
}

// mergeAssistantUpdate 把 src 的 assistant 增量合并进 dst。合并键已包含
// 增量字段名，同键事件命名必然一致，无需跨命名防御。
func mergeAssistantUpdate(dst, src map[string]any) {
	_, dstUpdate := assistantUpdateTarget(dst)
	_, srcUpdate := assistantUpdateTarget(src)
	switch stringValue(dstUpdate["type"]) {
	case "text_delta", "thinking_delta":
		dstUpdate["delta"] = stringValue(dstUpdate["delta"]) + stringValue(srcUpdate["delta"])
		if partial, exists := srcUpdate["partial"]; exists {
			dstUpdate["partial"] = partial
		}
	case "toolcall_delta", "tool_call_delta":
		// partial 为累积快照，最新一条已包含全部历史增量。
		if partial, exists := srcUpdate["partial"]; exists {
			dstUpdate["partial"] = partial
		}
		if delta, exists := srcUpdate["delta"]; exists {
			dstUpdate["delta"] = delta
		}
	}
}

// slimSubagentStreamEvent 在转发子 agent 流式事件给前端之前，剔除其中的累积
// 快照字段。Pi 的 message_update 事件会携带整个 partial 助理消息（到目前为止
// 累积的全部 thinking/text/toolCall 块），按 token 逐条转发时负载随输出长度
// 平方级膨胀：打开详情窗口时卡片与弹窗双重渲染拖慢 WebView 渲染线程，桥接
// 消息在 WebView 内排队，每条都是 O(N) 快照，几分钟即可耗尽渲染进程内存。
// 前端 timeline 只消费增量 delta 与 toolCall 块，其余在此剥离。findSubagentEvent
// 返回的 map 与父级 tool_execution_update 负载共享嵌套 event 对象，就地修改
// 同时精简随后发出的 agent:event。非 toolCall 块替换为仅含 type 的占位（而非
// 删除），保持 contentIndex 与 content 数组下标对齐，避免并行工具调用错位。
func slimSubagentStreamEvent(subagent map[string]any) {
	raw := subagent["event"]
	event, _ := raw.(map[string]any)
	if text, ok := raw.(string); ok {
		parsed := map[string]any{}
		if json.Unmarshal([]byte(text), &parsed) != nil {
			return
		}
		event = parsed
		defer func() {
			if slimmed, err := json.Marshal(event); err == nil {
				subagent["event"] = string(slimmed)
			}
		}()
	}
	if event == nil || stringValue(event["type"]) != "message_update" {
		return
	}
	update, _ := event["assistantMessageEvent"].(map[string]any)
	if update == nil {
		update, _ = event["messageEvent"].(map[string]any)
	}
	if update == nil {
		return
	}
	switch stringValue(update["type"]) {
	case "toolcall_start", "toolcall_delta", "toolcall_end",
		"tool_call_start", "tool_call_delta", "tool_call_end":
		partial, _ := update["partial"].(map[string]any)
		if partial == nil {
			return
		}
		content, _ := partial["content"].([]any)
		if content == nil {
			return
		}
		for index, block := range content {
			item, _ := block.(map[string]any)
			if item == nil {
				continue
			}
			if blockType := stringValue(item["type"]); blockType != "toolCall" && blockType != "tool_call" {
				content[index] = map[string]any{"type": item["type"]}
			}
		}
	default:
		// text_delta / thinking_delta 等增量事件只需 delta，无需累积快照。
		delete(update, "partial")
	}
}

func findSubagentEvent(event map[string]any) map[string]any {
	queue := []any{event}
	for len(queue) > 0 {
		value := queue[0]
		queue = queue[1:]
		switch current := value.(type) {
		case map[string]any:
			if stringValue(current["kind"]) == "subagent_event" {
				result := make(map[string]any, len(current))
				for key, field := range current {
					result[key] = field
				}
				return result
			}
			for _, field := range current {
				queue = append(queue, field)
			}
		case []any:
			queue = append(queue, current...)
		}
		if len(queue) > 256 {
			break
		}
	}
	return nil
}

func (s *AgentService) appendEvent(sessionDir string, event any) error {
	s.eventLogMu.Lock()
	defer s.eventLogMu.Unlock()
	persisted, keep := compactSessionEvent(event)
	if !keep {
		return nil
	}
	return appendSessionEventWithDurability(sessionDir, persisted, sessionEventNeedsSync(persisted))
}

func (s *AgentService) emitExecProgressFor(expectedSessionID int64) {
	s.mu.Lock()
	total := s.execAccumulatedMs
	running := !s.execTurnStart.IsZero()
	if running {
		total += time.Since(s.execTurnStart).Milliseconds()
	}
	sessionID := s.activeSessionID
	s.mu.Unlock()
	if sessionID == 0 || sessionID != expectedSessionID || total == 0 {
		return
	}
	application.Get().Event.Emit("agent:event", map[string]any{
		"type": "exec_progress", "totalMs": total, "running": running,
		"sessionId": sessionID, "codingToSessionId": sessionID,
	})
}
