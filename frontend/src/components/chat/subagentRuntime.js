const MAX_RUNTIME_EVENTS = 200
const MAX_TIMELINE_ITEMS = 200
const TERMINAL_STATUSES = new Set(['completed', 'failed', 'aborted', 'timeout'])

export function parseSubagentEvent(value) {
  if (typeof value !== 'string') return value
  try {
    return JSON.parse(value)
  } catch {
    return value
  }
}

export function resolvedSubagentStatus(outputStatus, runtimeStatus, fallback = '') {
  const output = typeof outputStatus === 'object' ? String(outputStatus?.status || '') : String(outputStatus || '')
  const runtimeValue = runtimeStatus && typeof runtimeStatus === 'object' ? runtimeStatus : null
  // 对象形式的 runtime 只取 status 字段；status 缺失（如子 agent 创建瞬间首个
  // subagent_run_started 事件不带 status）时视为无状态，绝不把整个对象字符串化
  // 成 '[object Object]' 当作有效状态返回，否则卡片会误显示失败态。
  const runtime = runtimeValue ? String(runtimeValue.status || '') : String(runtimeStatus || '')
  // A durable/follow-up terminal state wins over the transient abort request.
  if (runtime && runtime !== 'running' && runtime !== 'aborted_requested') return runtime
  if (output && output !== 'running') return output
  if (runtimeValue?.abortRequested && runtime === 'running') return 'aborted_requested'
  return runtime || output || fallback
}

function mergeRuntimeStatus(previous, incoming) {
  const oldStatus = String(previous?.status || '')
  const nextStatus = String(incoming?.status || '')
  if (TERMINAL_STATUSES.has(oldStatus) && nextStatus === 'running') return oldStatus
  return nextStatus || oldStatus
}

export function mergeSubagentRuntime(detail, payload) {
  const previousSubagent = detail?.subagent && typeof detail.subagent === 'object' ? detail.subagent : {}
  const normalized = {
    ...previousSubagent,
    ...payload,
    event: parseSubagentEvent(payload?.event),
    receivedAt: Date.now()
  }
  normalized.status = mergeRuntimeStatus(previousSubagent, normalized)
  // Keep the visual abort-requested transition until a real terminal payload
  // arrives, but never let it turn a completed/failed run back into running.
  if (previousSubagent.abortRequested && normalized.status === 'running' && payload?.abortRequested == null) {
    normalized.abortRequested = true
  }
  if (TERMINAL_STATUSES.has(normalized.status)) delete normalized.abortRequested

  const previous = Array.isArray(detail?.subagentEvents) ? detail.subagentEvents : []
  const events = normalized.event == null
    ? previous
    : [...previous, normalized].slice(-MAX_RUNTIME_EVENTS)
  const timeline = normalized.event == null
    ? limitTimeline(detail?.subagentTimeline || [])
    : limitTimeline(appendSubagentTimeline(detail?.subagentTimeline, normalized.event))
  return {
    ...(detail || {}),
    subagent: normalized,
    subagentEvents: events,
    subagentTimeline: timeline,
    subagentUI: applySubagentUIEvent(detail?.subagentUI, normalized.event, normalized.receivedAt)
  }
}

// 阻断式扩展 UI 状态随事件流增量维护，避免受 subagentEvents 滑动窗口
// （MAX_RUNTIME_EVENTS）截断后丢失待应答的对话框或计划挂件。
function applySubagentUIEvent(state, rawEvent, receivedAt = Date.now()) {
  const previous = state && typeof state === 'object'
      ? {
        widgets: { ...(state.widgets || {}) },
        widgetUpdatedAt: { ...(state.widgetUpdatedAt || {}) },
        widgetClearedAt: { ...(state.widgetClearedAt || {}) },
        ...(state.dialog ? { dialog: state.dialog } : {}),
        ...(state.dialogUpdatedAt ? { dialogUpdatedAt: state.dialogUpdatedAt } : {}),
        ...(state.dialogClearedAt ? { dialogClearedAt: state.dialogClearedAt } : {})
      }
      : { widgets: {}, widgetUpdatedAt: {}, widgetClearedAt: {} }
  const event = parseSubagentEvent(rawEvent)
  if (!event || typeof event !== 'object') return previous
  const eventAt = Number(event._recordedAt || receivedAt) || receivedAt
  if (event.type === 'extension_ui_request') {
    if (event.method === 'setWidget' && event.widgetKey) {
      if (event.widgetLines == null) {
        delete previous.widgets[event.widgetKey]
        previous.widgetClearedAt[event.widgetKey] = eventAt
      } else {
        previous.widgets[event.widgetKey] = event.widgetLines
        previous.widgetUpdatedAt[event.widgetKey] = eventAt
      }
    } else if (['select', 'confirm', 'input', 'editor'].includes(event.method)) {
      previous.dialog = event
      previous.dialogUpdatedAt = eventAt
      delete previous.dialogClearedAt
    }
  } else if (event.type === 'subagent_ui_response' && previous.dialog?.id === event.id) {
    delete previous.dialog
    previous.dialogClearedAt = eventAt
  }
  return previous
}

export function mergeSubagentUIState(current, history) {
  const live = current && typeof current === 'object' ? current : { widgets: {} }
  const old = history && typeof history === 'object' ? history : { widgets: {} }
  const liveWidgets = live.widgets || {}
  const oldWidgets = old.widgets || {}
  const liveUpdatedAt = live.widgetUpdatedAt || {}
  const oldUpdatedAt = old.widgetUpdatedAt || {}
  const liveClearedAt = live.widgetClearedAt || {}
  const oldClearedAt = old.widgetClearedAt || {}
  const widgets = {}
  const widgetUpdatedAt = {}
  const widgetClearedAt = {}
  const widgetKeys = new Set([
    ...Object.keys(oldWidgets), ...Object.keys(liveWidgets),
    ...Object.keys(oldUpdatedAt), ...Object.keys(liveUpdatedAt),
    ...Object.keys(oldClearedAt), ...Object.keys(liveClearedAt)
  ])
  for (const key of widgetKeys) {
    const oldUpdate = Number(oldUpdatedAt[key] || 0)
    const liveUpdate = Number(liveUpdatedAt[key] || 0)
    const updateAt = Math.max(oldUpdate, liveUpdate)
    const clearAt = Math.max(Number(oldClearedAt[key] || 0), Number(liveClearedAt[key] || 0))
    if (updateAt) widgetUpdatedAt[key] = updateAt
    if (clearAt) widgetClearedAt[key] = clearAt
    if (clearAt && clearAt >= updateAt) continue
    if (Object.prototype.hasOwnProperty.call(liveWidgets, key) && liveUpdate >= oldUpdate) {
      widgets[key] = liveWidgets[key]
    } else if (Object.prototype.hasOwnProperty.call(oldWidgets, key)) {
      widgets[key] = oldWidgets[key]
    } else if (Object.prototype.hasOwnProperty.call(liveWidgets, key)) {
      widgets[key] = liveWidgets[key]
    }
  }
  const result = {
    widgets,
    widgetUpdatedAt,
    widgetClearedAt,
    ...(live.dialog ? { dialog: live.dialog } : {}),
    ...(live.dialogUpdatedAt ? { dialogUpdatedAt: live.dialogUpdatedAt } : {}),
    ...(live.dialogClearedAt ? { dialogClearedAt: live.dialogClearedAt } : {})
  }
  const liveDialogAt = Number(live.dialog?._recordedAt || live.dialogUpdatedAt || 0)
  const historyDialogAt = Number(old.dialog?._recordedAt || 0)
  if (old.dialog && (!result.dialog || historyDialogAt > liveDialogAt)) {
    const clearedAt = Number(live.dialogClearedAt || 0)
    if (historyDialogAt > clearedAt) result.dialog = old.dialog
  }
  return result
}

function limitTimeline(value) {
  const items = Array.isArray(value) ? value : []
  return items.length > MAX_TIMELINE_ITEMS ? items.slice(-MAX_TIMELINE_ITEMS) : items
}

function timelineText(value) {
  if (value == null || value === '') return ''
  if (typeof value === 'string') {
    try {
      const parsed = JSON.parse(value)
      return typeof parsed === 'string' ? parsed : JSON.stringify(parsed, null, 2)
    } catch {
      return value
    }
  }
  try {
    return JSON.stringify(value, null, 2)
  } catch {
    return String(value)
  }
}

function messageParts(message) {
  const content = message?.content
  if (typeof content === 'string') return { text: content, thinking: '', tools: [] }
  if (!Array.isArray(content)) return { text: '', thinking: '', tools: [] }
  const text = []
  const thinking = []
  const tools = []
  for (const block of content) {
    if (block?.type === 'text' && block.text) text.push(block.text)
    else if (block?.type === 'thinking' && block.thinking) thinking.push(block.thinking)
    else if (['toolCall', 'tool_call'].includes(block?.type)) tools.push(block)
  }
  return { text: text.join('\n'), thinking: thinking.join('\n'), tools }
}

function nextTimelineID(items, kind) {
  return `${kind}-${items.filter(item => item.kind === kind).length + 1}`
}

function upsertTimelineText(items, kind, text, complete = false) {
  if (!text) return items
  const index = items.findLastIndex(item => item.kind === kind && !item.complete)
  if (index < 0) {
    return [...items, {
      id: nextTimelineID(items, kind),
      kind,
      text,
      complete
    }]
  }
  const next = [...items]
  next[index] = {
    ...next[index],
    text: complete ? text : `${next[index].text || ''}${text}`,
    complete: next[index].complete || complete
  }
  return next
}

function completeTimelineText(items, kind, canonicalText = '') {
  const index = items.findLastIndex(item => item.kind === kind && !item.complete)
  if (index < 0) {
    if (canonicalText && items.some(item => (
      item.kind === kind && item.complete && item.text === canonicalText
    ))) return items
    return canonicalText
      ? [...items, {
          id: nextTimelineID(items, kind),
          kind,
          text: canonicalText,
          complete: true
        }]
      : items
  }
  const next = [...items]
  next[index] = {
    ...next[index],
    text: canonicalText || next[index].text,
    complete: true
  }
  return next
}

function upsertTimelineTool(items, source, complete = false) {
  const toolCallId = String(source?.toolCallId || source?.id || '')
  const name = String(source?.toolName || source?.name || source?.toolCall?.name || 'tool')
  const args = source?.args ?? source?.input ?? source?.arguments ?? source?.partialArgs
  // 工具执行结果（tool_execution_end 携带），归属到对应工具调用内展示。
  const output = source?.output ?? source?.result
  const index = toolCallId
    ? items.findIndex(item => item.kind === 'tool' && item.toolCallId === toolCallId)
    : items.findLastIndex(item => item.kind === 'tool' && !item.complete && item.name === name)
  if (index < 0) {
    return [...items, {
      id: nextTimelineID(items, 'tool'),
      kind: 'tool',
      toolCallId,
      name,
      input: args,
      output,
      text: timelineText(args),
      startedAt: Number(source?._recordedAt || source?.startedAt || Date.now()),
      complete
    }]
  }
  const next = [...items]
  next[index] = {
    ...next[index],
    name: name || next[index].name,
    input: args == null ? next[index].input : args,
    output: output == null ? next[index].output : output,
    text: args == null ? next[index].text : timelineText(args),
    complete: next[index].complete || complete
  }
  return next
}

function toolFromMessageUpdate(update) {
  const partial = update?.partial
  const blocks = Array.isArray(partial?.content) ? partial.content : []
  // 优先用 contentIndex 精确定位本次流式更新的 toolCall，
  // 避免并行多个工具调用时误取第一个 block 导致输入/结果错位。
  const index = Number(update?.contentIndex)
  if (Number.isInteger(index) && index >= 0 && index < blocks.length) {
    const indexed = blocks[index]
    if (['toolCall', 'tool_call'].includes(indexed?.type)) return indexed
  }
  const block = blocks.find(value => ['toolCall', 'tool_call'].includes(value?.type))
  if (block) return block
  if (update?.toolCall || update?.toolCallId || update?.name) {
    return update.toolCall || update
  }
  return null
}

function appendSubagentTimeline(value, rawEvent) {
  const event = parseSubagentEvent(rawEvent)
  if (!event || typeof event !== 'object') return Array.isArray(value) ? value : []
  let items = Array.isArray(value) ? value : []
  const type = String(event.type || '')

  if (type === 'message_update') {
    const update = event.assistantMessageEvent || event.messageEvent || {}
    const updateType = String(update.type || '')
    if (updateType === 'thinking_delta') {
      return upsertTimelineText(items, 'thinking', update.delta || '')
    }
    if (updateType === 'thinking_end') {
      return completeTimelineText(items, 'thinking')
    }
    if (updateType === 'text_delta') {
      return upsertTimelineText(items, 'content', update.delta || '')
    }
    if (['toolcall_start', 'tool_call_start', 'toolcall_delta', 'tool_call_delta', 'toolcall_end', 'tool_call_end'].includes(updateType)) {
      const tool = toolFromMessageUpdate(update)
      return tool ? upsertTimelineTool(items, tool) : items
    }
    return items
  }

  if (type === 'message_end') {
    const message = event.message
    const role = String(message?.role || 'assistant')
    // toolResult 是工具执行结果：按 toolCallId 归属到对应工具调用内展示，
    // 若按 assistant 文本追加，结果会“逃逸”到工具展示框外渲染。
    if (role === 'toolResult') {
      return upsertTimelineTool(items, {
        toolCallId: message.toolCallId,
        toolName: message.toolName,
        output: messageParts(message).text
      }, true)
    }
    // 用户消息（追问）以 user 身份入列，与历史回填的形态保持一致。
    if (role === 'user') {
      const text = messageParts(message).text
      return text
        ? [...items, { id: nextTimelineID(items, 'user'), kind: 'user', text, complete: true }]
        : items
    }
    const parts = messageParts(message)
    items = completeTimelineText(items, 'thinking', parts.thinking)
    items = completeTimelineText(items, 'content', parts.text)
    for (const tool of parts.tools) items = upsertTimelineTool(items, tool)
    return items
  }

  if (type === 'tool_execution_start') return upsertTimelineTool(items, event)
  if (type === 'tool_execution_end') return upsertTimelineTool(items, event, true)

  if (type === 'agent_end' && !items.some(item => item.kind === 'content')) {
    const messages = Array.isArray(event.messages) ? event.messages : []
    for (const message of messages) {
      if (message?.role !== 'assistant') continue
      const parts = messageParts(message)
      items = completeTimelineText(items, 'thinking', parts.thinking)
      items = completeTimelineText(items, 'content', parts.text)
      for (const tool of parts.tools) items = upsertTimelineTool(items, tool)
    }
  }
  return items
}

export function subagentTimeline(value) {
  if (Array.isArray(value?.subagentTimeline)) {
    return limitTimeline(value.subagentTimeline).filter(item => (
      item.kind !== 'thinking' || item.complete
    ))
  }
  const events = Array.isArray(value?.subagentEvents) ? value.subagentEvents : []
  const items = events.reduce((timeline, item) => (
    appendSubagentTimeline(timeline, item?.event ?? item)
  ), [])
  return limitTimeline(items).filter(item => item.kind !== 'thinking' || item.complete)
}

const PLAN_TOOL_NAMES = ['codingto_plan_present', 'codingto_plan_update']

// 实时 timeline（含未完成 thinking）优先；历史消息无该字段时从持久化事件重建
// （按既有设计仅保留已完成的 thinking）。
export function subagentTimelineItems(detail) {
  const live = detail?.subagentTimeline
  if (Array.isArray(live) && live.length) return live
  return subagentTimeline(detail)
}

// 把 getSubagentTranscript 返回的历史消息一次性转换为 timeline 项并前置填充
// （幂等）。用于历史回放/运行中重新打开页面后补齐早期内容；填充后实时事件
// 继续在 timeline 上增量追加，无需轮询 transcript。
// 回填是异步的，请求发出后可能有实时事件先落进 timeline：此时历史项前置
// 合并而非丢弃，与实时项完全相同的条目（竞态窗口内被双方各记一次）去重。
export function backfillSubagentTimeline(detail, messages) {
  const base = detail && typeof detail === 'object' ? detail : {}
  if (base.subagentBackfilled) return base
  const existing = Array.isArray(base.subagentTimeline) ? base.subagentTimeline : []
  const items = []
  for (const message of Array.isArray(messages) ? messages : []) {
    if (message?.role === 'user') {
      const text = String(message.content || '').trim()
      if (text) items.push({ id: `history-${items.length}`, kind: 'user', text, complete: true })
      continue
    }
    if (message?.role === 'assistant') {
      const thinking = String(message.thinkingContent || '')
      const text = String(message.content || '')
      if (thinking.trim()) items.push({ id: `history-${items.length}`, kind: 'thinking', text: thinking, complete: true })
      if (text.trim()) items.push({ id: `history-${items.length}`, kind: 'content', text, complete: true })
      continue
    }
    if (message?.role === 'tool') {
      const toolDetail = message.detail || {}
      const input = toolDetail.args ?? toolDetail.input
      items.push({
        id: `history-${items.length}`,
        kind: 'tool',
        toolCallId: String(toolDetail.toolCallId || toolDetail.id || ''),
        name: String(toolDetail.toolName || toolDetail.name || message.content || 'tool'),
        input,
        output: toolDetail.output ?? toolDetail.result,
        text: timelineText(input),
        startedAt: Number(toolDetail.startedAt || 0),
        complete: true
      })
    }
  }
  if (!existing.length) {
    return { ...base, subagentTimeline: limitTimeline(items), subagentBackfilled: true }
  }
  const liveKeys = new Set(existing.map(timelineItemKey))
  const history = items.filter(item => !liveKeys.has(timelineItemKey(item)))
  return { ...base, subagentTimeline: limitTimeline([...history, ...existing]), subagentBackfilled: true }
}

// 工具项优先按 toolCallId 判重，其余按 kind+完成态+文本；仅用于回填历史
// 与竞态实时项之间的精确去重，不做模糊匹配。
function timelineItemKey(item) {
  if (item.kind === 'tool' && item.toolCallId) return `tool:${item.toolCallId}`
  return `${item.kind}:${item.complete ? 1 : 0}:${item.text || ''}`
}

// timeline 项 → ChatMessageItem 消息对象。卡片与详情弹窗共用，保证两侧渲染一致。
// thinkingOpen 默认值在组件内按 item.complete 处理（运行中展开、完成自动折叠）。
// timeline 项是不可变更新（未变化的项保持对象引用），按引用缓存消息对象，
// 避免每个流式 token 都重建全部消息导致嵌套组件整体重渲染。
const timelineMessageCache = new WeakMap()

function timelineMessage(item) {
  const cached = timelineMessageCache.get(item)
  if (cached) return cached
  let message
  if (item.kind === 'user') {
    message = { id: `subagent-${item.id}`, role: 'user', content: item.text, live: false }
  } else if (item.kind === 'tool') {
    message = {
      id: `subagent-${item.id}`,
      role: 'tool',
      content: item.name,
      live: !item.complete,
      detail: {
        type: item.complete ? 'tool_execution_end' : 'tool_execution_start',
        status: item.complete ? 'done' : 'running',
        toolCallId: item.toolCallId,
        name: item.name,
        toolName: item.name,
        args: item.input,
        output: item.output,
        startedAt: item.startedAt
      }
    }
  } else if (item.kind === 'thinking') {
    // live 跟随完成态：流式期间为 true，ChatMessageItem 的思考区自动下滚
    // （watch 要求 live && thinkingOpen）与呼吸动画才会生效。
    message = {
      id: `subagent-${item.id}`,
      role: 'assistant',
      content: '',
      thinkingContent: item.text,
      thinkingComplete: item.complete,
      live: !item.complete
    }
  } else {
    message = {
      id: `subagent-${item.id}`,
      role: 'assistant',
      content: item.text,
      thinkingContent: '',
      live: !item.complete
    }
  }
  timelineMessageCache.set(item, message)
  return message
}

export function subagentTimelineMessages(detail, { includeUser = false } = {}) {
  return subagentTimelineItems(detail)
    .filter(item => (includeUser || item.kind !== 'user') && !PLAN_TOOL_NAMES.includes(item.name))
    .map(timelineMessage)
}

export function subagentActivity(payload, labels) {
  const event = parseSubagentEvent(payload?.event)
  if (!event || typeof event !== 'object') return ''
  const type = String(event.type || '')
  if (type === 'tool_execution_start' || type === 'tool_execution_update' || type === 'tool_execution_end') {
    const name = event.toolName || event.name || event.toolCall?.name || labels.tool
    return `${labels.tool} · ${name}`
  }
  if (type === 'message_update') {
    const update = event.assistantMessageEvent || event.messageEvent || {}
    if (String(update.type || '').startsWith('thinking_')) return labels.thinking
    if (String(update.type || '').startsWith('text_')) return labels.responding
  }
  if (type === 'message_end' || type === 'agent_end') return labels.responding
  return ''
}

export function subagentUIState(value) {
  if (value?.subagentUI) {
    const state = value.subagentUI
    return {
      widgets: { ...(state.widgets || {}) },
      ...(state.dialog ? { dialog: state.dialog } : {})
    }
  }
  if (value?.widgets || value?.dialog) {
    return {
      widgets: { ...(value.widgets || {}) },
      ...(value.dialog ? { dialog: value.dialog } : {})
    }
  }
  const events = Array.isArray(value?.subagentEvents) ? value.subagentEvents : []
  const state = { widgets: {} }
  for (const item of events) {
    const event = parseSubagentEvent(item?.event ?? item)
    if (!event || typeof event !== 'object') continue
    if (event.type === 'extension_ui_request') {
      if (event.method === 'setWidget' && event.widgetKey) {
        if (event.widgetLines == null) delete state.widgets[event.widgetKey]
        else state.widgets[event.widgetKey] = event.widgetLines
      } else if (['select', 'confirm', 'input', 'editor'].includes(event.method)) {
        state.dialog = event
      }
    } else if (
      event.type === 'subagent_ui_response'
      && state.dialog?.id === event.id
    ) {
      delete state.dialog
    }
  }
  return state
}

export function planItemsFromLines(lines) {
  if (!Array.isArray(lines)) return []
  return lines.map((line, index) => {
    const text = String(line)
    return {
      step: index + 1,
      text: text.replace(/^[☐☑✓○]\s*/, '').replace(/^~~|~~$/g, '').trim(),
      completed: /^[☑✓]/.test(text) || /~~.+~~/.test(text)
    }
  })
}

export function subagentPreviewText(value) {
  const events = Array.isArray(value?.subagentEvents) ? value.subagentEvents : []
  let text = ''
  for (const item of events) {
    const event = parseSubagentEvent(item?.event ?? item)
    if (!event || typeof event !== 'object') continue
    if (event.type === 'message_update') {
      const update = event.assistantMessageEvent || event.messageEvent || {}
      if (update.type === 'text_delta') text += update.delta || ''
    } else if (event.type === 'message_end') {
      // 仅采纳 assistant 消息作为预览文本，避免 toolResult/user 混入。
      if (event.message?.role && event.message.role !== 'assistant') continue
      const content = event.message?.content
      if (typeof content === 'string') text = content
      else if (Array.isArray(content)) {
        const completed = content
          .filter(block => block?.type === 'text')
          .map(block => block.text || '')
          .join('\n')
        if (completed) text = completed
      }
    }
  }
  return text.trim()
}
