import assert from 'node:assert/strict'
import test from 'node:test'

import { buildSessionGroups } from './sessionGroups.js'

function groups(overrides = {}) {
  return buildSessionGroups({
    environments: [
      { id: 'a', name: 'Alpha', path: '/alpha' },
      { id: 'b', name: '', path: '/beta' }
    ],
    tasks: [],
    archivedTaskIds: new Set(),
    workspaceOrder: [],
    visibleSessionCounts: {},
    pageSize: 2,
    ungroupedLabel: 'Ungrouped',
    ...overrides
  })
}

test('groups sessions by workspace and keeps orphan sessions', () => {
  const result = groups({
    tasks: [
      { id: 1, environmentId: 'a', title: 'old', createdAt: 10 },
      { id: 2, environmentId: 'missing', title: 'orphan', createdAt: 20 },
      { id: 3, environmentId: 'a', title: 'new', updatedAt: 30 }
    ]
  })

  assert.deepEqual(result.map(group => group.name), ['Alpha', '/beta', 'Ungrouped'])
  assert.deepEqual(result[0].visible.map(task => task.id), [3, 1])
  assert.deepEqual(result[2].visible.map(task => task.id), [2])
})

test('filters archived and steward sessions', () => {
  const result = groups({
    archivedTaskIds: new Set([1]),
    tasks: [
      { id: 1, environmentId: 'a' },
      { id: 2, environmentId: 'a', isSteward: true },
      { id: 3, environmentId: 'a' }
    ]
  })
  assert.deepEqual(result[0].all.map(task => task.id), [3])
})

test('applies workspace priority and per-group pagination', () => {
  const result = groups({
    workspaceOrder: ['b'],
    visibleSessionCounts: { b: 1 },
    tasks: [
      { id: 1, environmentId: 'b', updatedAt: 10 },
      { id: 2, environmentId: 'b', updatedAt: 20 },
      { id: 3, environmentId: 'b', updatedAt: 30 }
    ]
  })

  assert.equal(result[0].id, 'b')
  assert.deepEqual(result[0].visible.map(task => task.id), [3])
  assert.equal(result[0].remaining, 2)
})
