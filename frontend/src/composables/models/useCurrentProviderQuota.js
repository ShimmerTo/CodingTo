import { computed, onBeforeUnmount, ref, watch } from 'vue'

import { chatgptUsage, getProviderBalance, getProviderUsage } from '../../backend.js'
import {
  fetchProviderQuota,
  getProviderQuotaCache,
  normalizeChatGPTUsage,
  normalizeOpenCodeUsage,
  PROVIDER_QUOTA_CACHE_MS
} from '../../utils/providerQuota.js'
import { formatProviderQuotaText, providerQuotaKind } from '../../utils/providerQuotaPresentation.js'

// Owns the current conversation provider quota, cache refresh, and timer lifecycle.
export function useCurrentProviderQuota({
  activeTaskId,
  config,
  isOpenAICodexProvider,
  modelOptions,
  selectedModelValue,
  t
}) {
  const currentProviderQuota = ref(null)
  let providerQuotaTimer = null
  const currentQuotaProvider = computed(() => {
    const option = modelOptions.value.find(item => item.value === selectedModelValue.value)
    return config.providers.find(provider => provider.name === option?.provider) || null
  })

  async function queryProviderQuota(provider, kind) {
    if (kind === 'chatgpt') {
      const usage = normalizeChatGPTUsage(await chatgptUsage())
      return usage ? { kind, ...usage } : null
    }
    if (kind === 'opencode') {
      const usage = normalizeOpenCodeUsage(await getProviderUsage(provider.name))
      return usage?.rolling && usage?.weekly && usage?.monthly
        ? { kind, rolling: usage.rolling, weekly: usage.weekly, monthly: usage.monthly }
        : null
    }
    if (kind === 'deepseek') {
      const balance = await getProviderBalance(provider.name)
      return balance?.available && balance?.balances?.length ? { ...balance, kind } : null
    }
    return null
  }

  async function refreshCurrentProviderQuota({ force = false } = {}) {
    const provider = currentQuotaProvider.value
    const kind = providerQuotaKind(provider, isOpenAICodexProvider)
    if (!provider || !kind) {
      currentProviderQuota.value = null
      return
    }

    const cached = getProviderQuotaCache(kind, provider.name)
    currentProviderQuota.value = cached?.data ?? null
    if (!force && cached?.expiresAt > Date.now()) return

    const data = await fetchProviderQuota({
      kind,
      providerName: provider.name,
      force,
      fetcher: () => queryProviderQuota(provider, kind)
    })
    const latestProvider = currentQuotaProvider.value
    if (
      latestProvider?.name === provider.name
      && providerQuotaKind(latestProvider, isOpenAICodexProvider) === kind
    ) {
      currentProviderQuota.value = data
    }
  }

  const currentProviderQuotaText = computed(() => formatProviderQuotaText(
    currentProviderQuota.value,
    t.value,
    config.preferences.language
  ))

  function startProviderQuotaRefresh() {
    if (providerQuotaTimer != null) return
    providerQuotaTimer = window.setInterval(() => {
      void refreshCurrentProviderQuota({ force: true })
    }, PROVIDER_QUOTA_CACHE_MS)
  }

  function stopProviderQuotaRefresh() {
    if (providerQuotaTimer != null) window.clearInterval(providerQuotaTimer)
    providerQuotaTimer = null
  }

  watch(
    () => `${activeTaskId.value}\u0000${currentQuotaProvider.value?.name || ''}\u0000${selectedModelValue.value}`,
    () => { void refreshCurrentProviderQuota() },
    { immediate: true }
  )
  onBeforeUnmount(stopProviderQuotaRefresh)

  return {
    currentProviderQuotaText,
    refreshCurrentProviderQuota,
    startProviderQuotaRefresh
  }
}
