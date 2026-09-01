import assert from 'node:assert/strict'
import test from 'node:test'

import {
  messageText,
  parsePlanItems,
  parsePlanLines,
  parsePlanStepsFromMessage,
  PLAN_STEPS_IN_MESSAGE,
  stripPlanStepsMarker
} from './planMessages.js'

test('reads text from string and block message content', () => {
  assert.equal(messageText({ content: 'hello' }), 'hello')
  assert.equal(messageText({ content: [
    { type: 'text', text: 'first' },
    { type: 'image', data: 'ignored' },
    { type: 'text', text: 'second' }
  ] }), 'first\nsecond')
  assert.equal(messageText(null), '')
})

test('parses numbered plan text in Chinese and English', () => {
  assert.deepEqual(parsePlanItems('计划：\n1. 准备\n2. ☑ 完成'), [
    { step: 1, text: '准备', completed: false },
    { step: 2, text: '完成', completed: true }
  ])
  assert.deepEqual(parsePlanItems('Plan:\n1) Build\n2) ~~Verify~~'), [
    { step: 1, text: 'Build', completed: false },
    { step: 2, text: 'Verify', completed: true }
  ])
})

test('does not treat unrelated numbered prose as a plan', () => {
  assert.deepEqual(parsePlanItems('1. ordinary text'), [])
  assert.deepEqual(parsePlanItems('1. ☐ explicit task'), [
    { step: 1, text: 'explicit task', completed: false }
  ])
})

test('converts widget lines while preserving completion state', () => {
  assert.deepEqual(parsePlanLines(['☐ first', '☑ second', '~~third~~']), [
    { step: 1, text: 'first', completed: false },
    { step: 2, text: 'second', completed: true },
    { step: 3, text: 'third', completed: true }
  ])
  assert.deepEqual(parsePlanLines(null), [])
})

test('parses and strips embedded structured plan steps', () => {
  const message = `Approve?\n${PLAN_STEPS_IN_MESSAGE}${JSON.stringify([
    { index: 1, text: ' first ', completed: false },
    { index: 0, text: 'invalid', completed: false },
    { index: 2, text: 'second', completed: true }
  ])}`
  assert.deepEqual(parsePlanStepsFromMessage(message), [
    { step: 1, text: 'first', completed: false },
    { step: 2, text: 'second', completed: true }
  ])
  assert.equal(stripPlanStepsMarker(message), 'Approve?')
})

test('rejects malformed embedded plan data without changing plain messages', () => {
  const malformed = `Approve?${PLAN_STEPS_IN_MESSAGE}{bad json}`
  assert.equal(parsePlanStepsFromMessage(malformed), null)
  assert.equal(parsePlanStepsFromMessage('Approve?'), null)
  assert.equal(stripPlanStepsMarker('Approve?'), 'Approve?')
  assert.equal(stripPlanStepsMarker(null), null)
})
