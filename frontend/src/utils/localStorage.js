/**
 * localStorage 持久化工具函数。
 * 从 App.vue 抽取，均为纯函数，不依赖 Vue 响应式状态。
 */

const DRAFT_PREFIX = 'codingto:draft:'
const EXEC_PLAN_KEY = 'codingto:exec-plan:'
const PLAN_ITEMS_KEY = 'codingto:plan-items:'
const EXT_DIALOG_KEY = 'codingto:ext-dialog:'

// --- Draft (输入框草稿) ---

export function draftKeyForEnv(envId) {
  return DRAFT_PREFIX + (envId || '_none')
}

export function loadDraftForEnv(envId) {
  try {
    return localStorage.getItem(draftKeyForEnv(envId)) || ''
  } catch {
    return ''
  }
}

export function persistDraftForEnv(envId, text) {
  try {
    const key = draftKeyForEnv(envId)
    if (text && String(text).length) localStorage.setItem(key, text)
    else localStorage.removeItem(key)
  } catch {}
}

// --- Execution Plan (执行计划) ---

export function persistExecPlan(taskId, plan) {
  try {
    if (!taskId) return
    if (plan && plan.length) localStorage.setItem(EXEC_PLAN_KEY + taskId, JSON.stringify(plan))
    else localStorage.removeItem(EXEC_PLAN_KEY + taskId)
  } catch {}
}

export function restoreExecPlan(taskId) {
  if (!taskId) return []
  try {
    const raw = localStorage.getItem(EXEC_PLAN_KEY + taskId)
    return raw ? JSON.parse(raw) : []
  } catch {
    return []
  }
}

// --- Plan Items (待确认计划) ---

export function persistPlanItems(taskId, items) {
  try {
    if (!taskId) return
    if (items && items.length) localStorage.setItem(PLAN_ITEMS_KEY + taskId, JSON.stringify(items))
    else localStorage.removeItem(PLAN_ITEMS_KEY + taskId)
  } catch {}
}

export function restorePlanItems(taskId) {
  if (!taskId) return []
  try {
    const raw = localStorage.getItem(PLAN_ITEMS_KEY + taskId)
    return raw ? JSON.parse(raw) : []
  } catch {
    return []
  }
}

// --- Extension Dialog (扩展交互对话框) ---

export function persistExtDialogForTask(taskId, dialog) {
  try {
    if (!taskId) return
    if (dialog) localStorage.setItem(EXT_DIALOG_KEY + taskId, JSON.stringify(dialog))
    else localStorage.removeItem(EXT_DIALOG_KEY + taskId)
  } catch {}
}

export function clearPersistedExtDialog(taskId) {
  try { localStorage.removeItem(EXT_DIALOG_KEY + (taskId || '')) } catch {}
}

export function readPersistedExtDialog(taskId) {
  try {
    const raw = taskId ? localStorage.getItem(EXT_DIALOG_KEY + taskId) : null
    return raw ? JSON.parse(raw) : null
  } catch {
    return null
  }
}
