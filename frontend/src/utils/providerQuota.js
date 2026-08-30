export const PROVIDER_QUOTA_CACHE_MS = 60 * 1000

const providerQuotaCache = new Map()
const providerQuotaRequests = new Map()

function providerQuotaCacheKey(kind, providerName = '') {
  // ChatGPT authorization and quota are global, independent of the configured
  // provider alias. Other APIs use provider names to select their credentials.
  return kind === 'chatgpt' ? kind : `${kind}\u0000${providerName}`
}

// Return the cache entry even after expiry so callers can keep stale data visible
// while a background refresh is in progress.
export function getProviderQuotaCache(kind, providerName = '') {
  return providerQuotaCache.get(providerQuotaCacheKey(kind, providerName)) || null
}

export function clearProviderQuotaCache(kind, providerName = '') {
  providerQuotaCache.delete(providerQuotaCacheKey(kind, providerName))
}

// Reuse quota requests across the main navigation and Models page. A failed
// refresh briefly extends the previous entry so transient provider failures do
// not blank already displayed quota or trigger a request loop.
export async function fetchProviderQuota({ kind, providerName = '', fetcher, force = false }) {
  const key = providerQuotaCacheKey(kind, providerName)
  const cached = providerQuotaCache.get(key)
  if (!force && cached?.expiresAt > Date.now()) return cached.data

  const pending = providerQuotaRequests.get(key)
  if (pending) return pending

  let request
  request = Promise.resolve()
    .then(fetcher)
    .then(data => {
      providerQuotaCache.set(key, { data, expiresAt: Date.now() + PROVIDER_QUOTA_CACHE_MS })
      return data
    })
    .catch(() => {
      const data = providerQuotaCache.get(key)?.data ?? null
      providerQuotaCache.set(key, { data, expiresAt: Date.now() + PROVIDER_QUOTA_CACHE_MS })
      return data
    })
    .finally(() => {
      if (providerQuotaRequests.get(key) === request) providerQuotaRequests.delete(key)
    })
  providerQuotaRequests.set(key, request)
  return request
}

// ChatGPT Plus exposes an applicable 5-hour window. Pro and unknown plans only
// display the weekly window so an unrecognized response cannot overstate quota.
export function normalizeChatGPTUsage(usage) {
  if (!usage?.weekly) return null
  const planType = typeof usage.planType === 'string' ? usage.planType.trim().toLowerCase() : ''
  return {
    planType,
    weekly: usage.weekly,
    ...(planType === 'plus' && usage.rolling ? { rolling: usage.rolling } : {})
  }
}

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
