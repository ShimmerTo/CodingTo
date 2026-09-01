// Identifies which quota adapter can serve a provider.
export function providerQuotaKind(provider, isOpenAICodexProvider) {
  if (isOpenAICodexProvider(provider)) return 'chatgpt'
  if (/opencode\.ai\/zen\/go/i.test(provider?.baseUrl || '')) return 'opencode'
  if (/^https:\/\/api\.deepseek\.com(?:[/:]|$)/i.test((provider?.baseUrl || '').trim())) return 'deepseek'
  return ''
}

function quotaPercent(window) {
  const value = Number(window?.percent)
  return Number.isFinite(value) ? `${Math.max(0, Math.min(100, Math.round(value)))}%` : ''
}

// Formats a normalized provider quota for the compact sidebar label.
export function formatProviderQuotaText(quota, labels, language) {
  if (quota?.kind === 'chatgpt') {
    const windows = [
      ...(quota.planType === 'plus' ? [[labels.providerQuotaFiveHours, quotaPercent(quota.rolling)]] : []),
      [labels.providerQuotaWeek, quotaPercent(quota.weekly)]
    ].filter(([, value]) => value)
    return windows.map(([label, value]) => `${label} ${value}`).join(' · ')
  }
  if (quota?.kind === 'opencode') {
    const windows = [
      [labels.providerQuotaFiveHours, quotaPercent(quota.rolling)],
      [labels.providerQuotaWeek, quotaPercent(quota.weekly)],
      [labels.providerQuotaMonth, quotaPercent(quota.monthly)]
    ].filter(([, value]) => value)
    return windows.map(([label, value]) => `${label} ${value}`).join(' · ')
  }
  if (quota?.kind === 'deepseek') {
    const balances = (quota.balances || []).flatMap(balance => {
      const amount = Number(balance?.totalBalance)
      if (!Number.isFinite(amount)) return []
      const value = amount.toLocaleString(language || undefined, {
        minimumFractionDigits: 2,
        maximumFractionDigits: 2
      })
      return [`${balance.currency || ''} ${value}`.trim()]
    })
    return balances.length ? `${labels.providerQuotaBalance} ${balances.join(' · ')}` : ''
  }
  return ''
}
