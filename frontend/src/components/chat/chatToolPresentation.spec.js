import assert from 'node:assert/strict'
import test from 'node:test'

import { toolFilePath } from './chatToolPresentation.js'

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
