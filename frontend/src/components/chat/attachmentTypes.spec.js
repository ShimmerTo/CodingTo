import assert from 'node:assert/strict'
import test from 'node:test'

import {
  attachmentKindFromMime,
  attachmentMime,
  isNativePathAttachment,
  toAttachmentInputs
} from './attachmentTypes.js'

test('classifies attachment MIME types for composer previews', () => {
  assert.equal(attachmentKindFromMime('image/png'), 'image')
  assert.equal(attachmentKindFromMime('audio/mpeg'), 'audio')
  assert.equal(attachmentKindFromMime('video/mp4'), 'video')
  assert.equal(attachmentKindFromMime('application/pdf'), 'document')
  assert.equal(attachmentKindFromMime('application/octet-stream'), 'other')
})

test('uses supplied MIME metadata before extension inference', () => {
  assert.equal(attachmentMime({ name: 'photo.jpg', type: 'IMAGE/WEBP' }), 'image/webp')
  assert.equal(attachmentMime({ name: 'report.PDF' }), 'application/pdf')
  assert.equal(attachmentMime({ name: 'unknown.bin' }), 'application/octet-stream')
})

test('recognizes native paths and builds backend attachment inputs', () => {
  assert.equal(isNativePathAttachment({ path: ' C:\\work\\file.txt ' }), true)
  assert.equal(isNativePathAttachment({ path: '   ' }), false)
  assert.deepEqual(toAttachmentInputs([{
    path: '', name: 'note.txt', mimeType: 'text/plain', data: 'bm90ZQ==', imagePreview: 'ignored'
  }]), [{
    path: '', name: 'note.txt', mimeType: 'text/plain', data: 'bm90ZQ=='
  }])
})
