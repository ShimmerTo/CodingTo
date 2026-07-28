function firstNumber(...values) {
  for (const value of values) {
    const number = Number(value)
    if (Number.isFinite(number)) return number
  }
  return 0
}

export function createCompactionMessage(event = {}, id = '') {
  return {
    id: id || `compaction-${Date.now()}`,
    role: 'compaction',
    status: 'running',
    reason: event.reason || 'manual',
    tokensBefore: 0,
    estimatedTokensAfter: 0,
    error: '',
    aborted: false,
    createdAt: firstNumber(event._recordedAt, event.recordedAt, Date.now())
  }
}

export function completeCompactionMessage(message, event = {}) {
  const result = event.result || event.data || {}
  const error = String(event.errorMessage || event.error || '')
  Object.assign(message, {
    status: error ? 'error' : (event.aborted ? 'aborted' : 'completed'),
    reason: event.reason || message.reason || 'manual',
    tokensBefore: firstNumber(result.tokensBefore, message.tokensBefore),
    estimatedTokensAfter: firstNumber(result.estimatedTokensAfter, message.estimatedTokensAfter),
    error,
    aborted: Boolean(event.aborted),
    completedAt: firstNumber(event._recordedAt, event.recordedAt, Date.now())
  })
  return message
}

export function compactionMessageText(message, t) {
  const automatic = message.reason !== 'manual'
  if (message.status === 'running') {
    return automatic ? t.compactionAutoRunning : t.compactionManualRunning
  }
  if (message.status === 'error') {
    return t.compactionFailed.replace('{error}', message.error || t.compactionUnknownError)
  }
  if (message.status === 'aborted') {
    return automatic ? t.compactionAutoAborted : t.compactionManualAborted
  }
  const key = automatic ? 'compactionAutoCompleted' : 'compactionManualCompleted'
  return t[key]
    .replace('{before}', Number(message.tokensBefore || 0).toLocaleString())
    .replace('{after}', Number(message.estimatedTokensAfter || 0).toLocaleString())
}
