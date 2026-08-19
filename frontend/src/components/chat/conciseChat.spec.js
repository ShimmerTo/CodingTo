import assert from 'node:assert/strict'
import test from 'node:test'
import { buildConciseRenderList, conciseBlockDuration, countConciseSteps, hasThinkingContent, hasThinkingTrace } from './conciseChat.js'

function msg(id, role, extra = {}) {
  return { id, role, ...extra }
}

test('consecutive thinking and tool calls fold into one block', () => {
  const list = buildConciseRenderList([
    msg('t1', 'tool', { detail: { name: 'bash' } }),
    msg('a1', 'assistant', { thinkingContent: 'deep thought' }),
    msg('t2', 'tool', { detail: { name: 'read' } }),
  ])
  assert.equal(list.length, 1)
  assert.equal(list[0].type, 'block')
  assert.deepEqual(list[0].items.map(item => item.kind), ['tool', 'thinking', 'tool'])
})

test('thinking followed by content splits: thinking folds, content renders normally', () => {
  const list = buildConciseRenderList([
    msg('t1', 'tool', { detail: { name: 'bash' } }),
    msg('a1', 'assistant', { thinkingContent: 'reasoning', content: 'answer' }),
    msg('t2', 'tool', { detail: { name: 'read' } }),
  ])
  assert.equal(list.length, 3)
  assert.deepEqual(list[0], { type: 'block', items: [{ kind: 'tool', message: list[0].items[0].message }, { kind: 'thinking', message: list[0].items[1].message }] })
  assert.equal(list[0].items[1].message.id, 'a1')
  assert.equal(list[1].type, 'message')
  assert.equal(list[1].message.id, 'a1')
  assert.equal(list[2].type, 'block')
  assert.equal(list[2].items[0].kind, 'tool')
})

test('content-only and special messages break blocks and render as-is', () => {
  const list = buildConciseRenderList([
    msg('a1', 'assistant', { thinkingContent: 'one' }),
    msg('c1', 'changes', { changes: { files: [] } }),
    msg('a2', 'assistant', { content: 'plain answer' }),
    msg('e1', 'error', { content: 'boom' }),
    msg('t1', 'tool', { detail: { name: 'bash' } }),
  ])
  assert.equal(list.length, 5)
  assert.deepEqual(list.map(entry => entry.type), ['block', 'message', 'message', 'message', 'block'])
  assert.equal(list[1].message.id, 'c1')
  assert.equal(list[2].message.id, 'a2')
  assert.equal(list[3].message.id, 'e1')
})

test('empty or whitespace-only thinking with no trace is not folded', () => {
  assert.equal(hasThinkingContent(msg('a', 'assistant', { thinkingContent: '\u200B \u200B' })), false)
  assert.equal(hasThinkingContent(msg('a', 'assistant', { thinkingContent: '' })), false)
  assert.equal(hasThinkingContent(msg('a', 'assistant', { thinkingContent: 'real' })), true)
  const list = buildConciseRenderList([
    msg('a1', 'assistant', { thinkingContent: '\u200B' }),
    msg('a2', 'assistant', { content: 'answer' }),
  ])
  assert.equal(list.length, 2)
  assert.deepEqual(list.map(entry => entry.type), ['message', 'message'])
})

test('thinking without returned content still folds when it has thinking traces', () => {
  // 有思考痕迹（thinking 标记 / thinkingStartedAt / thinkingDurationMs）但
  // 未返回思考内容的消息，同样应归入折叠块，而不是渲染成空白块。
  assert.equal(hasThinkingTrace(msg('a', 'assistant', { thinking: true })), true)
  assert.equal(hasThinkingTrace(msg('a', 'assistant', { thinkingStartedAt: 1000 })), true)
  assert.equal(hasThinkingTrace(msg('a', 'assistant', { thinkingDurationMs: 1200 })), true)
  assert.equal(hasThinkingTrace(msg('a', 'assistant', { content: 'plain' })), false)
  assert.equal(hasThinkingTrace(msg('a', 'tool', { detail: {} })), false)
  const list = buildConciseRenderList([
    msg('t1', 'tool', { detail: { name: 'bash' } }),
    msg('a1', 'assistant', { thinking: true }),
    msg('a2', 'assistant', { thinkingDurationMs: 800 }),
    msg('t2', 'tool', { detail: { name: 'read' } }),
  ])
  assert.equal(list.length, 1)
  assert.equal(list[0].type, 'block')
  assert.deepEqual(list[0].items.map(item => item.kind), ['tool', 'thinking', 'thinking', 'tool'])
  assert.equal(countConciseSteps([msg('a1', 'assistant', { thinking: true })]), 1)
})

test('countConciseSteps tallies thinking and tool calls', () => {
  const messages = [
    msg('u', 'user', { content: 'q' }),
    msg('t1', 'tool', { detail: {} }),
    msg('a1', 'assistant', { thinkingContent: 'x' }),
    msg('a2', 'assistant', { thinkingContent: 'y', content: 'answer' }),
    msg('c1', 'changes', { changes: {} }),
    msg('t2', 'tool', { detail: {} }),
  ]
  assert.equal(countConciseSteps(messages), 4)
  assert.equal(countConciseSteps([]), 0)
})

test('conciseBlockDuration spans the earliest start to the latest end', () => {
  const items = [
    { kind: 'tool', message: msg('t1', 'tool', { detail: { status: 'done', startedAt: 1_000, endedAt: 5_000, durationMs: 4_000 } }) },
    { kind: 'tool', message: msg('t2', 'tool', { detail: { status: 'done', startedAt: 2_000, endedAt: 3_000, durationMs: 1_000 } }) },
  ]
  assert.equal(conciseBlockDuration(items), 4_000)
})

test('conciseBlockDuration supports persisted thinking and live tools', () => {
  const persistedThinking = [
    { kind: 'thinking', message: msg('a1', 'assistant', { createdAt: 10_000, thinkingDurationMs: 2_500 }) },
  ]
  assert.equal(conciseBlockDuration(persistedThinking), 2_500)

  const liveTool = [
    { kind: 'tool', message: msg('t1', 'tool', { detail: { status: 'running', startedAt: 20_000 } }) },
  ]
  assert.equal(conciseBlockDuration(liveTool, 23_200), 3_200)
})
