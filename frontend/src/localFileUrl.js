// localFileURL converts an absolute native path into a standards-compliant
// file URL. URL.pathname performs the required escaping for spaces, Unicode,
// #, and ? without corrupting a Windows drive-letter colon.
export function localFileURL(path) {
  const value = String(path || '').trim()
  if (!value) return ''
  if (/^file:/i.test(value)) return new URL(value).href

  const normalized = value.replaceAll('\\', '/')
  if (normalized.startsWith('//')) {
    const [host, ...parts] = normalized.slice(2).split('/')
    if (!host) return ''
    const result = new URL(`file://${host}/`)
    result.pathname = `/${parts.join('/')}`
    return result.href
  }

  const result = new URL('file:///')
  result.pathname = /^[A-Za-z]:\//.test(normalized) ? `/${normalized}` : normalized
  return result.href
}
