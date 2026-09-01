const MIME_BY_EXTENSION = {
  png: 'image/png', jpg: 'image/jpeg', jpeg: 'image/jpeg', gif: 'image/gif',
  webp: 'image/webp', bmp: 'image/bmp', svg: 'image/svg+xml', avif: 'image/avif',
  pdf: 'application/pdf', txt: 'text/plain', md: 'text/markdown', csv: 'text/csv',
  json: 'application/json', doc: 'application/msword',
  docx: 'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
  xls: 'application/vnd.ms-excel',
  xlsx: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
  ppt: 'application/vnd.ms-powerpoint',
  pptx: 'application/vnd.openxmlformats-officedocument.presentationml.presentation',
  mp3: 'audio/mpeg', wav: 'audio/wav', m4a: 'audio/mp4',
  mp4: 'video/mp4', webm: 'video/webm', mov: 'video/quicktime'
}

// Maps an attachment MIME type to the composer preview category.
export function attachmentKindFromMime(mime) {
  if (!mime) return 'other'
  if (mime.startsWith('image/')) return 'image'
  if (mime.startsWith('audio/')) return 'audio'
  if (mime.startsWith('video/')) return 'video'
  if (mime.startsWith('text/') || /(pdf|word|excel|powerpoint|spreadsheet|presentation|officedocument|opendocument)/i.test(mime)) return 'document'
  return 'other'
}

// Resolves an attachment MIME type from supplied metadata or its file name.
export function attachmentMime(file) {
  const supplied = String(file?.type || file?.mimeType || '').toLowerCase()
  if (supplied) return supplied
  const extension = String(file?.name || '').split('.').pop()?.toLowerCase()
  return MIME_BY_EXTENSION[extension] || 'application/octet-stream'
}

// Reports whether an attachment can be sent by native filesystem path.
export function isNativePathAttachment(file) {
  return typeof file?.path === 'string' && file.path.trim() !== ''
}

// Converts composer attachment entries to the backend prompt input shape.
export function toAttachmentInputs(list) {
  return list.map(attachment => ({
    path: attachment.path || '',
    name: attachment.name,
    mimeType: attachment.mimeType,
    data: attachment.data || ''
  }))
}
