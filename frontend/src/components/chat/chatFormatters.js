import MarkdownIt from 'markdown-it'

const markdownRenderer = new MarkdownIt({
  breaks: true,
  html: false,
  linkify: true
})
// 仅自动识别带协议的链接。模糊匹配会把 README.md、goteams-docs.zip 等
// 文件名误判为网址（.md/.zip 均为真实 TLD），点击后跳到无效地址。
markdownRenderer.linkify.set({ fuzzyLink: false })

function escapeHtml(value) {
  return String(value ?? '').replace(/[&<>"']/g, character => ({
    '&': '&amp;',
    '<': '&lt;',
    '>': '&gt;',
    '"': '&quot;',
    "'": '&#39;'
  })[character])
}

const codeCopyIcon = '<svg viewBox="0 0 24 24" aria-hidden="true" focusable="false"><rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>'

function addCodeCopyButtons(html, labels) {
  const copyLabel = escapeHtml(labels?.copy || 'Copy')
  const copiedLabel = escapeHtml(labels?.copied || 'Copied')
  // markdown-it escapes code content, so a generated code block cannot contain
  // an unescaped </code>. Wrapping the rendered fence keeps this compatible
  // with the existing v-html renderer and avoids reparsing Markdown per block.
  return html.replace(/<pre><code(?: class="[^"]*")?>[\s\S]*?<\/code><\/pre>/g, block => (
    `<div class="message-code-block">` +
      `<button class="message-code-copy" type="button" data-code-copy ` +
        `data-copy-label="${copyLabel}" data-copied-label="${copiedLabel}" ` +
        `title="${copyLabel}" aria-label="${copyLabel}">${codeCopyIcon}</button>` +
      block +
    `</div>`
  ))
}

export function renderMarkdown(content, options = {}) {
  const html = markdownRenderer.render(String(content || ''))
  return options.codeCopy ? addCodeCopyButtons(html, options.codeCopy) : html
}

export function imageSrc(image) {
  if (image?.imagePreview) return image.imagePreview
  if (image?.src) return image.src
  return image?.mimeType ? `data:${image.mimeType};base64,${image.data}` : ''
}

export function formatFileSize(bytes) {
  const n = Number(bytes || 0)
  if (!n || n < 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB']
  let value = n
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit += 1
  }
  const rounded = value >= 100 || unit === 0 ? Math.round(value) : Math.round(value * 10) / 10
  return `${rounded} ${units[unit]}`
}

export function formatDuration(ms) {
  const totalSeconds = Math.max(0, Math.floor(Number(ms || 0) / 1000))
  const hours = Math.floor(totalSeconds / 3600)
  const minutes = Math.floor((totalSeconds % 3600) / 60)
  const seconds = totalSeconds % 60
  if (hours) return `${hours}h ${minutes}m ${seconds}s`
  if (minutes) return `${minutes}m ${seconds}s`
  return `${seconds}s`
}

export function formatTokenCount(value) {
  const count = Number(value) || 0
  if (count >= 1_000_000) return `${(count / 1_000_000).toFixed(1).replace(/\.0$/, '')}M`
  if (count >= 1_000) return `${(count / 1_000).toFixed(1).replace(/\.0$/, '')}K`
  return String(count)
}

export function formatCacheHitRate(usage) {
  const input = Math.max(0, Number(usage?.input) || 0)
  const cached = Math.max(0, Number(usage?.cached ?? usage?.cacheRead) || 0)
  const cacheWrite = Math.max(0, Number(usage?.cacheWrite) || 0)
  const inputSideTokens = input + cached + cacheWrite
  if (inputSideTokens <= 0) return '0%'
  const percent = Math.min(100, (cached / inputSideTokens) * 100)
  return `${percent.toFixed(1).replace(/\.0$/, '')}%`
}

export function formatDetail(value) {
  if (value == null || value === '') return ''
  if (typeof value === 'string') return value
  try {
    return JSON.stringify(value, null, 2)
  } catch {
    return String(value)
  }
}
