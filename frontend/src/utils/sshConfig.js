// Reconcile a masked SaveConfig response without replacing the SSH editor's
// live draft object. Older responses preserve newer edits; the latest response
// may apply backend normalization in place while keeping object identity.
export function reconcileSshConfigResult(configs, currentDraft, requestDraft, requestRevision, currentRevision, normalize) {
  if (!Array.isArray(configs) || !currentDraft) return configs
  const index = configs.findIndex(item => item.id === currentDraft.id)
  if (index < 0) return configs
  if (currentDraft === requestDraft && requestRevision === currentRevision) {
    Object.assign(currentDraft, normalize(configs[index]))
  }
  const reconciled = configs.slice()
  reconciled[index] = currentDraft
  return reconciled
}
