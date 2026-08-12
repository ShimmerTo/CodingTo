import assert from 'node:assert/strict'
import test from 'node:test'

import {
  browserProfileCreateTarget,
  isBrowserProfileCreateOption,
  isBrowserProfileDialog,
  SECURE_BROWSER_PROFILE_DIALOG_TITLE
} from './extensionDialog.js'

test('recognizes Browser Profile create action without treating normal options as actions', () => {
  const createOption = {
    label: '+ 新建 Profile',
    value: '+ 新建 Profile',
    createProfile: true,
    targetUrl: 'https://example.com/login'
  }

  assert.equal(isBrowserProfileCreateOption(createOption), true)
  assert.equal(browserProfileCreateTarget(createOption), 'https://example.com/login')
  assert.equal(isBrowserProfileCreateOption({ label: 'work', value: 'work' }), false)
  assert.equal(isBrowserProfileCreateOption('+ 新建 Profile'), false)
})

test('normalizes a missing Browser Profile target URL to an empty string', () => {
  assert.equal(browserProfileCreateTarget({ createProfile: true }), '')
  assert.equal(browserProfileCreateTarget(null), '')
})

test('recognizes Browser Profile selection and secure creation dialogs', () => {
  assert.equal(isBrowserProfileDialog({ method: 'select', title: '选择浏览器身份（example.com）' }), true)
  assert.equal(isBrowserProfileDialog({ method: 'input', title: SECURE_BROWSER_PROFILE_DIALOG_TITLE }), true)
  assert.equal(isBrowserProfileDialog({ method: 'confirm', title: '普通确认' }), false)
})
