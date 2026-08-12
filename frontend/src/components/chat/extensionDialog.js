export const BROWSER_IDENTITY_DIALOG_TITLE = '选择浏览器身份'
export const SECURE_BROWSER_PROFILE_DIALOG_TITLE = '__CODINGTO_SECURE_BROWSER_PROFILE__'
export const PLAN_CONFIRM_DIALOG_PREFIX = '__CODINGTO_PLAN_CONFIRM__:'
export const DCG_CONFIRM_DIALOG_PREFIX = '__CODINGTO_DCG_CONFIRM__:'

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
