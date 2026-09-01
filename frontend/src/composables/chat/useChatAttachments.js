import { onBeforeUnmount, ref } from 'vue'

import {
  attachmentKindFromMime,
  attachmentMime,
  isNativePathAttachment
} from '../../components/chat/attachmentTypes.js'

const MAX_ATTACHMENT_COUNT = 10
const MAX_ATTACHMENT_BYTES = 50 * 1024 * 1024
const MAX_TOTAL_ATTACHMENT_BYTES = 100 * 1024 * 1024

// Owns chat attachment validation, file reads, previews, and cleanup.
export function useChatAttachments({ t, pushToast }) {
  const attachments = ref([])
  const attachmentReadsPending = ref(0)
  const activeReaders = new Set()
  let disposed = false

  function revokePreview(preview) {
    if (String(preview || '').startsWith('blob:')) URL.revokeObjectURL(preview)
  }

  async function onAddAttachments(files) {
    if (!files || !files.length) return
    const acceptedFiles = []
    let total = attachments.value.reduce((sum, item) => sum + (Number(item.size) || 0), 0)
    for (const file of Array.from(files)) {
      if (attachments.value.length + acceptedFiles.length >= MAX_ATTACHMENT_COUNT) {
        pushToast('error', t.value.attachmentErrorCount?.replace('{count}', String(MAX_ATTACHMENT_COUNT)) || `最多 ${MAX_ATTACHMENT_COUNT} 个附件`)
        break
      }
      if (file.size > MAX_ATTACHMENT_BYTES) {
        pushToast('error', t.value.attachmentErrorSize?.replace('{name}', file.name) || `${file.name} 超过大小限制`)
        continue
      }
      if (total + file.size > MAX_TOTAL_ATTACHMENT_BYTES) {
        pushToast('error', t.value.attachmentErrorTotal || '附件总大小超过限制')
        break
      }
      acceptedFiles.push(file)
      total += file.size
    }
    if (!acceptedFiles.length) return

    const pendingItems = acceptedFiles.map(file => {
      const mimeType = attachmentMime(file)
      const kind = attachmentKindFromMime(mimeType)
      const nativePath = isNativePathAttachment(file)
      return {
        id: crypto.randomUUID(),
        path: nativePath ? file.path : '',
        name: file.name,
        mimeType,
        kind,
        size: file.size,
        data: '',
        reading: !nativePath,
        imagePreview: !nativePath && kind === 'image' && typeof URL !== 'undefined' && typeof URL.createObjectURL === 'function'
          ? URL.createObjectURL(file)
          : ''
      }
    })
    attachments.value = attachments.value.concat(pendingItems)

    const reads = []
    for (let index = 0; index < acceptedFiles.length; index++) {
      const file = acceptedFiles[index]
      const pending = pendingItems[index]
      if (isNativePathAttachment(file)) continue
      reads.push(new Promise(resolve => {
        const reader = new FileReader()
        activeReaders.add(reader)
        const finish = result => {
          activeReaders.delete(reader)
          resolve(result)
        }
        reader.onload = () => {
          const dataUrl = String(reader.result || '')
          const comma = dataUrl.indexOf(',')
          const data = comma >= 0 ? dataUrl.slice(comma + 1) : dataUrl
          finish({
            id: pending.id,
            data,
            reading: false,
            imagePreview: pending.kind === 'image' ? `data:${pending.mimeType};base64,${data}` : ''
          })
        }
        reader.onerror = () => finish({ id: pending.id, error: true })
        reader.onabort = () => finish({ id: pending.id, error: true })
        reader.readAsDataURL(file)
      }))
    }
    if (!reads.length) return

    attachmentReadsPending.value += 1
    try {
      const results = await Promise.all(reads)
      for (const pending of pendingItems) revokePreview(pending.imagePreview)
      if (disposed) return
      const byID = new Map(results.map(item => [item.id, item]))
      attachments.value = attachments.value.flatMap(item => {
        const result = byID.get(item.id)
        if (!result) return [item]
        if (result.error) return []
        return [{ ...item, ...result }]
      })
      if (results.some(item => item.error)) {
        pushToast('error', t.value.attachmentReadError || '无法读取部分附件')
      }
    } finally {
      attachmentReadsPending.value = Math.max(0, attachmentReadsPending.value - 1)
    }
  }

  function onRemoveAttachment(index) {
    if (index < 0 || index >= attachments.value.length) return
    revokePreview(attachments.value[index]?.imagePreview)
    attachments.value.splice(index, 1)
    attachments.value = attachments.value.slice()
  }

  onBeforeUnmount(() => {
    disposed = true
    for (const reader of activeReaders) reader.abort()
    activeReaders.clear()
    for (const attachment of attachments.value) revokePreview(attachment.imagePreview)
  })

  return {
    attachments,
    attachmentReadsPending,
    onAddAttachments,
    onRemoveAttachment
  }
}
