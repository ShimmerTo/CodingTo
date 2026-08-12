export const DEFAULT_STEWARD_PERSONA = Object.freeze({
  agentId: '',
  name: '管家',
  tone: '简洁、专业、主动、友好',
  prompt: '准确理解用户意图，主动推进任务并及时同步关键进展；信息不足时先澄清，涉及风险或破坏性操作时先说明影响并征得确认。',
  provider: '',
  model: '',
  compactAfterTurns: 30,
  manageScope: 'butler',
  enabled: true
})

export function stewardPersonaWithDefaults(profile = {}) {
  const result = { ...DEFAULT_STEWARD_PERSONA, ...(profile || {}) }
  for (const key of ['name', 'tone', 'prompt']) {
    if (!String(result[key] || '').trim()) result[key] = DEFAULT_STEWARD_PERSONA[key]
  }
  return result
}

export function isResolvedStewardPermission(permission) {
  return permission?.status === 'answered' || permission?.status === 'cancelled'
}

// Remote answers are broadcast globally. Match both the request and its owning
// conversation so an old/background answer cannot dismiss a newer dialog.
export function stewardPermissionMatchesDialog(permission, dialog, taskId = '') {
  if (!isResolvedStewardPermission(permission) || !permission?.requestId || !dialog?.id) return false
  if (String(permission.requestId) !== String(dialog.id)) return false
  const permissionTaskId = String(permission.sessionId ?? '')
  return !permissionTaskId || !String(taskId ?? '') || permissionTaskId === String(taskId)
}
