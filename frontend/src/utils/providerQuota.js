// OpenCode reports used percentages; the provider list displays remaining quota.
export function normalizeOpenCodeUsage(usage) {
  const toRemainingWindow = window => {
    if (!window) return window
    const usedPercent = Number(window.percent)
    const remainingPercent = Number.isFinite(usedPercent)
      ? Math.max(0, Math.min(100, 100 - usedPercent))
      : 0
    return { ...window, percent: remainingPercent }
  }

  return {
    ...usage,
    rolling: toRemainingWindow(usage?.rolling),
    weekly: toRemainingWindow(usage?.weekly),
    monthly: toRemainingWindow(usage?.monthly)
  }
}
