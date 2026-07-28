const MAX_RUNTIME_EVENTS = 200

export function parseSubagentEvent(value) {
  if (typeof value !== 'string') return value
  try {
    return JSON.parse(value)
  } catch {
    return value
  }
}

export function mergeSubagentRuntime(detail, payload) {
  const normalized = {
    ...payload,
    event: parseSubagentEvent(payload?.event),
    receivedAt: Date.now()
  }
  const previous = Array.isArray(detail?.subagentEvents) ? detail.subagentEvents : []
  const events = normalized.event == null
    ? previous
    : [...previous, normalized].slice(-MAX_RUNTIME_EVENTS)
  const timeline = normalized.event == null
    ? (detail?.subagentTimeline || [])
    : appendSubagentTimeline(detail?.subagentTimeline, normalized.event)
  return {
    ...(detail || {}),
    subagent: normalized,
    subagentEvents: events,
    subagentTimeline: timeline
  }
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
    text: args == null ? next[index].text : timelineText(args),
    complete: next[index].complete || complete
  }
  return next
}

function toolFromMessageUpdate(update) {
  const partial = update?.partial
  const blocks = Array.isArray(partial?.content) ? partial.content : []
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
    if (['toolcall_start', 'tool_call_start', 'toolcall_end', 'tool_call_end'].includes(updateType)) {
      const tool = toolFromMessageUpdate(update)
      return tool ? upsertTimelineTool(items, tool) : items
    }
    return items
  }

  if (type === 'message_end') {
    const parts = messageParts(event.message)
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
    return value.subagentTimeline.filter(item => (
      item.kind !== 'thinking' || item.complete
    ))
  }
  const events = Array.isArray(value?.subagentEvents) ? value.subagentEvents : []
  const items = events.reduce((timeline, item) => (
    appendSubagentTimeline(timeline, item?.event ?? item)
  ), [])
  return items.filter(item => item.kind !== 'thinking' || item.complete)
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
