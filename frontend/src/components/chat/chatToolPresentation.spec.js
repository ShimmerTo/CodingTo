import assert from 'node:assert/strict'
import test from 'node:test'

import { extensionIcon } from '../../extensionIcons.js'
import { toolFilePath, toolIcon } from './chatToolPresentation.js'

test('extracts clickable paths from base read and edit tools', () => {
  assert.equal(toolFilePath({
    role: 'tool', content: 'read', detail: { input: { path: 'src/main.go' }, status: 'done' }
  }), 'src/main.go')
  assert.equal(toolFilePath({
    role: 'tool', content: 'edit', detail: {
      input: { path: 'src/main.go', oldText: 'before', newText: 'after' }, status: 'done'
    }
  }), 'src/main.go')
})

test('does not expose unrelated path arguments as workspace-file actions', () => {
  assert.equal(toolFilePath({
    role: 'tool', content: 'bash', detail: { input: { path: 'src/main.go', command: 'pwd' }, status: 'done' }
  }), '')
})

test('uses stable dedicated icons for built-in extension tool families', () => {
  const cases = [
    ['codingto_browser_prepare', 'browser-profile'],
    ['codingto_browser_execute', 'browser-profile'],
    ['codingto_db', 'db'],
    ['codingto_document', 'document'],
    ['codingto_memory_search', 'memory'],
    ['codingto_memory_write_history', 'memory'],
    ['codingto_plan_update', 'plan'],
    ['skills_list', 'skills-list'],
    ['codingto_ssh', 'ssh'],
    ['codingto_subagent', 'subagent'],
    ['codingto_steward_list_sessions', 'steward'],
    ['agent_browser', 'browser-native'],
  ]

  for (const [tool, extension] of cases) {
    const message = { role: 'tool', content: tool, detail: { status: 'done' } }
    assert.equal(toolIcon(message), extensionIcon(extension), `${tool} should use the ${extension} icon`)
  }
})

test('recognizes namespaced built-in tools before generic action keywords', () => {
  const message = { role: 'tool', content: 'functions.codingto_memory_search', detail: { status: 'done' } }
  assert.equal(toolIcon(message), extensionIcon('memory'))
})
