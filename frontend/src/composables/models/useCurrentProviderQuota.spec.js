import assert from 'node:assert/strict'
import test from 'node:test'

import {
  formatProviderQuotaText,
  providerQuotaKind
} from '../../utils/providerQuotaPresentation.js'

const labels = {
  providerQuotaFiveHours: '5h',
  providerQuotaWeek: 'Week',
  providerQuotaMonth: 'Month',
  providerQuotaBalance: 'Balance'
}

test('identifies supported provider quota adapters', () => {
  const isCodex = provider => provider?.vendor === 'openai-codex'
  assert.equal(providerQuotaKind({ vendor: 'openai-codex' }, isCodex), 'chatgpt')
  assert.equal(providerQuotaKind({ baseUrl: 'https://opencode.ai/zen/go' }, isCodex), 'opencode')
  assert.equal(providerQuotaKind({ baseUrl: 'https://api.deepseek.com/v1' }, isCodex), 'deepseek')
  assert.equal(providerQuotaKind({ baseUrl: 'https://example.com' }, isCodex), '')
})

test('formats ChatGPT quota windows by plan type', () => {
  assert.equal(formatProviderQuotaText({
    kind: 'chatgpt', planType: 'plus', rolling: { percent: 40.4 }, weekly: { percent: 70.6 }
  }, labels, 'en-US'), '5h 40% · Week 71%')
  assert.equal(formatProviderQuotaText({
    kind: 'chatgpt', planType: 'pro', rolling: { percent: 40 }, weekly: { percent: 70 }
  }, labels, 'en-US'), 'Week 70%')
})

test('formats OpenCode windows and DeepSeek balances', () => {
  assert.equal(formatProviderQuotaText({
    kind: 'opencode', rolling: { percent: 10 }, weekly: { percent: 20 }, monthly: { percent: 30 }
  }, labels, 'en-US'), '5h 10% · Week 20% · Month 30%')
  assert.equal(formatProviderQuotaText({
    kind: 'deepseek', balances: [{ currency: 'CNY', totalBalance: 12 }]
  }, labels, 'en-US'), 'Balance CNY 12.00')
})
