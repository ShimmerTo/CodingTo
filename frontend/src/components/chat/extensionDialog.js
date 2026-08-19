export const BROWSER_IDENTITY_DIALOG_TITLE = '选择浏览器身份'
export const SECURE_BROWSER_PROFILE_DIALOG_TITLE = '__CODINGTO_SECURE_BROWSER_PROFILE__'
export const PLAN_CONFIRM_DIALOG_PREFIX = '__CODINGTO_PLAN_CONFIRM__:'
export const DCG_CONFIRM_DIALOG_PREFIX = '__CODINGTO_DCG_CONFIRM__:'
export const DCG_META_PREFIX = '__CODINGTO_DCG_META__:'

const DCG_PACK_LABELS_ZH = {
  'core.git': 'Git 危险操作',
  'core.filesystem': '文件系统破坏操作',
  'system.disk': '磁盘与分区操作',
  'windows.filesystem': 'Windows 文件系统操作',
  'windows.system': 'Windows 系统操作',
  'containers.docker': 'Docker 容器操作',
  'kubernetes.kubectl': 'Kubernetes 集群操作',
  'database.postgresql': 'PostgreSQL 数据库操作',
  'infrastructure.terraform': 'Terraform 基础设施操作'
}

const DCG_PATTERN_LABELS_ZH = {
  'reset-hard': '强制重置并丢弃未提交修改',
  'checkout-discard': '丢弃工作区文件修改',
  'restore-worktree': '恢复并覆盖工作区文件',
  'clean-force': '强制删除未跟踪文件',
  'push-force': '强制推送并改写远端历史',
  'branch-force-delete': '强制删除 Git 分支',
  'stash-drop': '永久删除暂存记录',
  'stash-clear': '清空全部暂存记录',
  'rm-rf-root-home': '递归删除根目录或用户目录',
  'rm-rf-general': '递归强制删除文件',
  'redirect-truncate-dynamic-path': '覆盖动态路径文件内容'
}

const SEVERITY_ZH = { critical: '灾难级', high: '高危', medium: '中危', low: '低危' }
const MODE_ZH = { deny: '拒绝执行', ask: '要求人工确认', warn: '警告后放行', log: '仅记录' }

export function extensionDialogTitle(dialog) {
  const title = String(dialog?.title || '')
  if (title.startsWith(PLAN_CONFIRM_DIALOG_PREFIX)) return title.slice(PLAN_CONFIRM_DIALOG_PREFIX.length)
  if (title.startsWith(DCG_CONFIRM_DIALOG_PREFIX)) return title.slice(DCG_CONFIRM_DIALOG_PREFIX.length)
  return title
}

export function isPlanConfirmationDialog(dialog) {
  return dialog?.method === 'confirm'
    && String(dialog?.title || '').startsWith(PLAN_CONFIRM_DIALOG_PREFIX)
}

export function isDCGConfirmationDialog(dialog) {
  return dialog?.method === 'confirm'
    && String(dialog?.title || '').startsWith(DCG_CONFIRM_DIALOG_PREFIX)
}

function legacyDCGValue(message, label) {
  const prefix = `${label}：`
  const block = String(message || '').split(/\r?\n\s*\r?\n/).find(item => item.startsWith(prefix))
  return String(block?.slice(prefix.length) || '').trim()
}

// New managed bridges append one machine-readable line. Older running agents
// are still parsed from their human-readable labels so an application update
// improves an already-open approval without forcing an agent restart.
export function dcgDialogDetails(dialog) {
  if (!isDCGConfirmationDialog(dialog)) return null
  const message = String(dialog?.message || '')
  const markerAt = message.lastIndexOf(DCG_META_PREFIX)
  if (markerAt >= 0) {
    const raw = message.slice(markerAt + DCG_META_PREFIX.length).trim().split(/\r?\n/, 1)[0]
    try {
      const parsed = JSON.parse(raw)
      return { ...parsed, legacy: false }
    } catch { /* fall through to the legacy labels */ }
  }
  const command = legacyDCGValue(message, '危险命令')
  const reason = legacyDCGValue(message, '检测原因')
  const ruleId = legacyDCGValue(message, '规则')
  const remediation = legacyDCGValue(message, '建议')
  const [packId = '', patternName = ''] = ruleId.split(':')
  return { command, reason, ruleId, remediation, packId, patternName, legacy: true }
}

export function dcgRulePresentation(details, language = 'zh-CN') {
  const ruleId = String(details?.ruleId || '')
  const [rulePack = '', rulePattern = ''] = ruleId.split(':')
  const packId = String(details?.packId || rulePack)
  const patternName = String(details?.patternName || rulePattern)
  const chinese = language === 'zh-CN'
  const packLabel = chinese ? (DCG_PACK_LABELS_ZH[packId] || packId || '自定义安全规则') : (packId || 'Custom safety rule')
  const ruleLabel = chinese ? (DCG_PATTERN_LABELS_ZH[patternName] || patternName || ruleId || '危险命令规则') : (patternName || ruleId || 'Dangerous-command rule')
  const severity = String(details?.severity || '')
  const mode = String(details?.mode || 'deny')
  return {
    packId,
    patternName,
    packLabel,
    ruleLabel,
    severityLabel: chinese ? (SEVERITY_ZH[severity] || severity || '高危') : (severity || 'high'),
    modeLabel: chinese ? (MODE_ZH[mode] || mode || '拒绝执行') : (mode || 'deny')
  }
}

export function isBrowserProfileDialog(dialog) {
  const title = String(dialog?.title || '')
  return title === SECURE_BROWSER_PROFILE_DIALOG_TITLE
    || (dialog?.method === 'select' && title.startsWith(BROWSER_IDENTITY_DIALOG_TITLE))
}

// Browser Profile adds an object option that is a UI action rather than a
// value for the extension. The frontend must consume it locally and open the
// profile form; sending its label/value back would be interpreted as a Profile
// Key and fail validation.
export function isBrowserProfileCreateOption(option) {
  return Boolean(option && typeof option === 'object' && option.createProfile === true)
}

export function browserProfileCreateTarget(option) {
  return isBrowserProfileCreateOption(option) ? String(option.targetUrl || '').trim() : ''
}

// Cancelling either of these host-owned dialogs rejects the operation itself,
// not merely the UI. Resolve the pending extension promise first, then abort
// the current turn so the model cannot reinterpret cancellation as a follow-up.
export function shouldAbortAfterExtensionResponse(dialog, payload, context = {}) {
  const browserIdentityCancelled = Boolean(
    payload?.cancelled
    && dialog?.method === 'select'
    && dialog?.title?.startsWith(BROWSER_IDENTITY_DIALOG_TITLE)
  )
  // hasPendingPlan keeps cancellation correct for a plan dialog emitted by an
  // already-running 1.0.0 extension before the new marker is materialized.
  const planRejected = Boolean(dialog?.method === 'confirm'
    && (isPlanConfirmationDialog(dialog) || context.hasPendingPlan)
    && (payload?.cancelled || payload?.confirmed === false))
  return browserIdentityCancelled || planRejected
}
