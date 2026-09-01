import assert from 'node:assert/strict'
import test from 'node:test'

import { localizeError, safeClone, withTimeout } from './appHelpers.js'

test('clones JSON-safe values without sharing nested references', () => {
  const source = { nested: { value: 1 } }
  const clone = safeClone(source)
  clone.nested.value = 2
  assert.equal(source.nested.value, 1)
})

test('resolves and rejects promises through the timeout wrapper', async () => {
  assert.equal(await withTimeout(Promise.resolve('ok'), 50, 'timeout'), 'ok')
  await assert.rejects(withTimeout(Promise.reject(new Error('failed')), 50, 'timeout'), /failed/)
})

test('rejects a promise when the hard timeout elapses', async () => {
  await assert.rejects(withTimeout(new Promise(() => {}), 5, 'too slow'), /too slow/)
})

test('localizes known backend errors and preserves unknown details', () => {
  assert.equal(localizeError('invalid base URL'), '基础域名无效')
  assert.equal(localizeError('something unexpected'), 'something unexpected')
  assert.equal(localizeError(''), '')
})
