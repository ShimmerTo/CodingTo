import assert from 'node:assert/strict'
import test from 'node:test'

import {
  browserProfileCreateTarget,
  dcgDialogDetails,
  dcgRulePresentation,
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

test('parses structured DCG metadata and presents the matched policy in Chinese', () => {
  const meta = {
    command: 'git reset --hard HEAD~1', reason: 'destroys changes',
    ruleId: 'core.git:reset-hard', packId: 'core.git', patternName: 'reset-hard',
    severity: 'critical', mode: 'deny'
  }
  const dialog = {
    method: 'confirm',
    title: '__CODINGTO_DCG_CONFIRM__:危险命令需要授权',
    message: `危险命令：\ngit reset --hard HEAD~1\n\n__CODINGTO_DCG_META__:${JSON.stringify(meta)}`
  }
  const details = dcgDialogDetails(dialog)
  const presentation = dcgRulePresentation(details, 'zh-CN')
  assert.equal(details.ruleId, 'core.git:reset-hard')
  assert.equal(presentation.packLabel, 'Git 危险操作')
  assert.equal(presentation.ruleLabel, '强制重置并丢弃未提交修改')
  assert.equal(presentation.severityLabel, '灾难级')
})

test('parses legacy DCG confirmation messages from labels', () => {
  const details = dcgDialogDetails({
    method: 'confirm', title: '__CODINGTO_DCG_CONFIRM__:危险命令需要授权',
    message: '危险命令：\ngit reset --hard HEAD~1\n\n检测原因：destroys changes\n\n规则：core.git:reset-hard\n\n建议：git stash first\n\n是否同意执行此命令？'
  })
  assert.equal(details.command, 'git reset --hard HEAD~1')
  assert.equal(details.ruleId, 'core.git:reset-hard')
  assert.equal(details.remediation, 'git stash first')
  assert.equal(details.legacy, true)
})
