export const BROWSER_IDENTITY_DIALOG_TITLE = '选择浏览器身份'
export const PLAN_CONFIRM_DIALOG_PREFIX = '__CODINGTO_PLAN_CONFIRM__:'

export function extensionDialogTitle(dialog) {
  const title = String(dialog?.title || '')
  return title.startsWith(PLAN_CONFIRM_DIALOG_PREFIX)
    ? title.slice(PLAN_CONFIRM_DIALOG_PREFIX.length)
    : title
}

export function isPlanConfirmationDialog(dialog) {
  return dialog?.method === 'confirm'
    && String(dialog?.title || '').startsWith(PLAN_CONFIRM_DIALOG_PREFIX)
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
