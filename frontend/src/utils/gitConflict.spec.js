import assert from 'node:assert/strict'
import test from 'node:test'
import { buildT } from '../i18n.js'
import { parseGitConflictPoints } from './gitConflict.js'

test('parses multiple LF and diff3 conflict points with exact replacement offsets', () => {
  const source = [
    'before',
    '<<<<<<< HEAD',
    'ours one',
    '||||||| base',
    'base one',
    '=======',
    'theirs one',
    '>>>>>>> feature',
    'middle',
    '<<<<<<< HEAD',
    'ours two',
    '=======',
    'theirs two',
    '>>>>>>> feature',
    'after',
    '',
  ].join('\n')
  const points = parseGitConflictPoints(source)
  assert.equal(points.length, 2)
  assert.equal(points[0].ours, 'ours one\n')
  assert.equal(points[0].base, 'base one\n')
  assert.equal(points[0].theirs, 'theirs one\n')
  assert.equal(source.slice(points[1].start, points[1].end).startsWith('<<<<<<< HEAD'), true)
  assert.equal(source.slice(0, points[1].start) + 'resolved\n' + source.slice(points[1].end), source.replace(source.slice(points[1].start, points[1].end), 'resolved\n'))
})

test('preserves CRLF content inside one conflict point', () => {
  const source = 'before\r\n<<<<<<< HEAD\r\nours\r\n=======\r\ntheirs\r\n>>>>>>> branch\r\nafter\r\n'
  const [point] = parseGitConflictPoints(source)
  assert.equal(point.ours, 'ours\r\n')
  assert.equal(point.theirs, 'theirs\r\n')
  assert.equal(point.startLine, 2)
  assert.equal(point.endLine, 6)
})

test('new conflict resolver labels resolve in both locales', () => {
  const keys = [
    'gitConflictAskAi', 'gitConflictAiResolveAll', 'gitConflictAiExplain',
    'gitConflictAiResolvePoint', 'gitConflictAiAllHint', 'gitConflictAiPointHint',
    'gitPromptConflictTitle', 'gitConflictBothDeleted', 'gitUnresolvedConflicts',
  ]
  for (const locale of ['zh-CN', 'en-US']) {
    const t = buildT(locale)
    for (const key of keys) assert.equal(typeof t[key], 'string', `${locale}: ${key}`)
  }
})
