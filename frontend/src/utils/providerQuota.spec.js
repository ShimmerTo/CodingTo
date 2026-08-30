import assert from 'node:assert/strict'
import test from 'node:test'

import { buildT } from '../i18n.js'
import {
  clearProviderQuotaCache,
  fetchProviderQuota,
  getProviderQuotaCache,
  normalizeChatGPTUsage
} from './providerQuota.js'

const rolling = { percent: 73, resetSeconds: 3600 }
const weekly = { percent: 48, resetSeconds: 86400 }

test('quota labels with hour durations resolve in both locales', () => {
  const zh = buildT('zh-CN')
  const en = buildT('en-US')
  assert.equal(zh.providerQuotaFiveHours, '5小时')
  assert.equal(en.providerQuotaFiveHours, '5h')
  assert.equal(zh.chatgptFiveHourQuota, '5 小时剩余额度')
  assert.equal(en.chatgptFiveHourQuota, '5-hour remaining quota')
})

test('ChatGPT Plus keeps the 5-hour and weekly quota windows', () => {
  assert.deepEqual(normalizeChatGPTUsage({ planType: ' PLUS ', rolling, weekly }), {
    planType: 'plus',
    rolling,
    weekly
  })
})

test('ChatGPT Pro keeps only the weekly quota window', () => {
  assert.deepEqual(normalizeChatGPTUsage({ planType: 'pro', rolling, weekly }), {
    planType: 'pro',
    weekly
  })
})

test('unknown ChatGPT plans conservatively keep only the weekly quota window', () => {
  assert.deepEqual(normalizeChatGPTUsage({ rolling, weekly }), {
    planType: '',
    weekly
  })
  assert.equal(normalizeChatGPTUsage({ planType: 'plus', rolling }), null)
})

test('provider quota cache is shared and avoids duplicate fresh requests', async () => {
  const providerName = 'test-opencode-cache'
  clearProviderQuotaCache('opencode', providerName)
  let calls = 0
  const fetcher = async () => ({ kind: 'opencode', weekly: { percent: ++calls } })

  const first = await fetchProviderQuota({ kind: 'opencode', providerName, fetcher })
  const second = await fetchProviderQuota({ kind: 'opencode', providerName, fetcher })

  assert.equal(calls, 1)
  assert.deepEqual(second, first)
  assert.deepEqual(getProviderQuotaCache('opencode', providerName)?.data, first)
  clearProviderQuotaCache('opencode', providerName)
})

test('failed quota refresh preserves the last cached value', async () => {
  const providerName = 'test-deepseek-cache'
  const cached = { kind: 'deepseek', balances: [{ currency: 'CNY', totalBalance: '8.00' }] }
  clearProviderQuotaCache('deepseek', providerName)
  await fetchProviderQuota({ kind: 'deepseek', providerName, fetcher: async () => cached })

  const refreshed = await fetchProviderQuota({
    kind: 'deepseek',
    providerName,
    force: true,
    fetcher: async () => { throw new Error('temporary failure') }
  })

  assert.deepEqual(refreshed, cached)
  assert.deepEqual(getProviderQuotaCache('deepseek', providerName)?.data, cached)
  clearProviderQuotaCache('deepseek', providerName)
})
