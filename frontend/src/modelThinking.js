const VALID_THINKING_LEVELS = new Set(['off', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'])

export function defaultThinkingLevelForModel(model) {
  if (!model?.reasoning) return 'off'

  const configured = String(model.defaultThinkingLevel || '').trim()
  return VALID_THINKING_LEVELS.has(configured) ? configured : 'medium'
}
