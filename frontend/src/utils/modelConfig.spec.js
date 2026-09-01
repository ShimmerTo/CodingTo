import assert from 'node:assert/strict'
import test from 'node:test'

import {
  compatBooleanValue,
  compatStringValue,
  formatCompat,
  modelRequestRoute,
  normalizeProvider,
  setCompatBoolean,
  setCompatString
} from './modelConfig.js'

test('reads, writes, and removes compatibility values', () => {
  const target = { compat: [] }
  assert.equal(compatBooleanValue(target, 'supportsStore'), '')
  setCompatBoolean(target, 'supportsStore', 'false')
  assert.equal(compatBooleanValue(target, 'supportsStore'), 'false')
  setCompatBoolean(target, 'supportsStore', '')
  assert.equal(compatBooleanValue(target, 'supportsStore'), '')

  setCompatString(target, 'thinkingFormat', 'openai')
  assert.equal(compatStringValue(target, 'thinkingFormat'), 'openai')
  setCompatString(target, 'thinkingFormat', '')
  assert.equal(compatStringValue(target, 'thinkingFormat'), '')
  assert.equal(formatCompat(target), '{}')
})

test('builds model request routes from provider and model overrides', () => {
  assert.equal(modelRequestRoute({ baseUrl: 'https://api.example.com/v1/' }, { api: 'openai-responses' }), 'https://api.example.com/v1/responses')
  assert.equal(modelRequestRoute({ baseUrl: 'https://api.example.com' }, { baseUrl: '/custom/', api: 'anthropic-messages' }), 'https://api.example.com/custom/messages')
  assert.equal(modelRequestRoute({}, { api: 'openai-completions' }), '—')
})

test('normalizes legacy providers and model defaults', () => {
  const provider = normalizeProvider({
    type: 'anthropic',
    enabled: false,
    api: '',
    models: [{ id: 'model-a', capabilities: { toolCall: false } }]
  })

  assert.equal(provider.enabled, false)
  assert.equal(provider.api, '')
  assert.deepEqual(provider.compat, {})
  assert.deepEqual(provider.models[0], {
    id: 'model-a',
    api: 'anthropic-messages',
    baseUrl: '',
    input: ['text'],
    maxTokens: 16384,
    capabilities: { toolCall: false },
    compat: {}
  })
})
