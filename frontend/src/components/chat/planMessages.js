// Marker used to append structured plan steps to a confirmation message.
export const PLAN_STEPS_IN_MESSAGE = '__CODINGTO_PLAN_STEPS__'

// Returns the visible text contained in a Pi message.
export function messageText(message) {
  if (!message) return ''
  if (typeof message.content === 'string') return message.content
  if (!Array.isArray(message.content)) return ''
  return message.content
    .filter(block => block?.type === 'text')
    .map(block => block.text || '')
    .join('\n')
}

// Parses numbered plan items from assistant-authored Markdown text.
export function parsePlanItems(text) {
  if (!text) return []
  const lines = String(text).split(/\r?\n/)
  const items = []
  let inPlan = false
  for (const rawLine of lines) {
    const line = rawLine.replace(/\*\*/g, '').trim()
    if (/^(plan|计划(?:步骤)?)\s*[:：]/i.test(line)) {
      inPlan = true
      continue
    }
    const match = line.match(/^(\d+)[.)、]\s*(?:[☐☑✓○]\s*)?(.+)$/)
    if (match && (inPlan || /[☐☑✓○]/.test(line))) {
      items.push({
        step: Number(match[1]),
        text: match[2].replace(/^~~|~~$/g, '').trim(),
        completed: /[☑✓]/.test(line) || /~~.+~~/.test(line)
      })
    } else if (inPlan && items.length && line && !/^[-*]\s/.test(line)) {
      break
    }
  }
  return items
}

// Converts widget plan lines into the plan item view model.
export function parsePlanLines(lines) {
  if (!Array.isArray(lines)) return []
  return lines.map((line, index) => ({
    step: index + 1,
    text: String(line).replace(/^[☐☑✓○]\s*/, '').replace(/^~~|~~$/g, '').trim(),
    completed: /^[☑✓]/.test(String(line)) || /~~.+~~/.test(String(line))
  }))
}

// Reads structured plan steps embedded at the end of a confirmation message.
export function parsePlanStepsFromMessage(message) {
  if (!message) return null
  const index = String(message).indexOf(PLAN_STEPS_IN_MESSAGE)
  if (index < 0) return null
  try {
    const raw = JSON.parse(String(message).slice(index + PLAN_STEPS_IN_MESSAGE.length))
    if (!Array.isArray(raw) || raw.length === 0) return null
    const steps = raw
      .map(step => ({
        step: Number(step?.index ?? 0),
        text: String(step?.text ?? '').trim(),
        completed: Boolean(step?.completed)
      }))
      .filter(step => step.step > 0 && step.text)
    return steps.length ? steps : null
  } catch {
    return null
  }
}

// Removes the structured plan suffix while preserving messages without it.
export function stripPlanStepsMarker(message) {
  const index = String(message || '').indexOf(PLAN_STEPS_IN_MESSAGE)
  return index >= 0 ? String(message).slice(0, index).trim() : message
}
