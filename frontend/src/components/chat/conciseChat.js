// 精简对话：把连续的思考过程与工具调用折叠为「摘要块」的纯逻辑。
// 与 ChatMessageItem 渲染思考块使用同一判据（跳过零宽字符后的空思考）。
export function hasThinkingContent(message) {
  return Boolean(
    message && message.role === 'assistant' && message.thinkingContent
    && message.thinkingContent.replace(/\u200B/g, '').trim()
  )
}

// 判断一条 assistant 消息是否属于「思考过程」：不仅 thinkingContent 非空算，
// 只要带有思考痕迹（thinking 标记 / thinkingStartedAt / thinkingDurationMs /
// thinkingEndedAt）即使没有返回思考内容，也应归入折叠块，避免渲染成空白块。
export function hasThinkingTrace(message) {
  if (!message || message.role !== 'assistant') return false
  if (hasThinkingContent(message)) return true
  return Boolean(
    message.thinking === true
    || message.thinkingStartedAt != null
    || message.thinkingDurationMs != null
    || message.thinkingEndedAt != null
  )
}

// 以「思考输出的 content」为边界折叠思考与工具调用：
// - 纯思考（无 content）与工具调用归入同一摘要块；
// - 思考结束后有 content 输出时，思考归入摘要块、content 单独正常展示；
// - changes/compaction/error 等消息保持原样，并作为块的边界。
//
// 返回渲染列表：{ type: 'block', items: [{ kind: 'thinking'|'tool', message }] }
// 或 { type: 'message', message }。
export function buildConciseRenderList(agentMessages) {
  const renderList = []
  let block = null
  const flushBlock = () => {
    if (block && block.items.length) renderList.push({ type: 'block', items: block.items })
    block = null
  }
  const pushBlockItem = (kind, message) => {
    if (!block) block = { items: [] }
    block.items.push({ kind, message })
  }
  for (const message of agentMessages) {
    if (message.role === 'tool') {
      pushBlockItem('tool', message)
    } else if (hasThinkingTrace(message)) {
      pushBlockItem('thinking', message)
      if (message.content) {
        flushBlock()
        renderList.push({ type: 'message', message })
      }
    } else {
      flushBlock()
      renderList.push({ type: 'message', message })
    }
  }
  flushBlock()
  return renderList
}

// 当前消息流中「思考 + 工具调用」的总次数，用于未开启精简时的过度提示。
export function countConciseSteps(messages) {
  let count = 0
  for (const message of messages) {
    if (message.role === 'tool') count += 1
    else if (hasThinkingTrace(message)) count += 1
  }
  return count
}

function conciseItemLive(item) {
  const message = item?.message || {}
  if (item?.kind === 'thinking') return Boolean(message.live) && !message.content
  const detail = message.detail || {}
  return detail.status !== 'done' && detail.type !== 'tool_execution_end'
}

function conciseItemStart(item) {
  const message = item?.message || {}
  if (item?.kind === 'thinking') {
    return Number(message.thinkingStartedAt || message.createdAt || 0)
  }
  return Number(message.detail?.startedAt || message.createdAt || 0)
}

function conciseItemEnd(item, now) {
  const message = item?.message || {}
  if (conciseItemLive(item)) return Number(now || 0)

  if (item?.kind === 'thinking') {
    const start = conciseItemStart(item)
    const duration = Number(message.thinkingDurationMs || 0)
    return Number(message.thinkingEndedAt || message.endedAt || (start && duration ? start + duration : start) || 0)
  }

  const detail = message.detail || {}
  const start = conciseItemStart(item)
  const duration = Number(detail.durationMs || 0)
  return Number(detail.endedAt || (start && duration ? start + duration : start) || 0)
}

// 摘要块总执行时长：从块内最早开始时间算到最晚结束时间。
// 使用 min/max 可正确覆盖并行工具调用；历史思考只有 createdAt + duration 时也能回算。
export function conciseBlockDuration(items, now = 0) {
  let earliestStart = 0
  let latestEnd = 0
  for (const item of items || []) {
    const start = conciseItemStart(item)
    const end = conciseItemEnd(item, now)
    if (start > 0 && (!earliestStart || start < earliestStart)) earliestStart = start
    if (end > latestEnd) latestEnd = end
  }
  return earliestStart && latestEnd >= earliestStart ? latestEnd - earliestStart : 0
}
