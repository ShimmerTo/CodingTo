// i18n index + parser.
// Translations live in separate files per locale:
//   ./i18n/zh-CN.js  (export const zh_cn = {...})
//   ./i18n/en-US.js  (export const en_us = {...})
// Each file is flat, single-level, underscore-separated keys (NO locale prefix).
// This file re-adds the locale prefix at runtime and exposes buildT().

import { zh_CN } from './i18n/zh-CN.js'
import { en_US } from './i18n/en-US.js'

// Flat, locale-prefixed map: 'zh_cn_command' -> '...'.
function withPrefix(prefix, obj) {
  const out = {}
  for (const k of Object.keys(obj)) out[`${prefix}_${k}`] = obj[k]
  return out
}

export const messages = {
  ...withPrefix('zh_cn', zh_CN),
  ...withPrefix('en_us', en_US),
}

// Convert a camelCase key used by call sites into the snake_case key stored above.
function camelToSnake(str) {
  return String(str).replace(/([A-Z])/g, '_$1').toLowerCase()
}

// Map a locale string (e.g. 'zh-CN') to its key prefix (e.g. 'zh_cn').
export function localeToPrefix(locale) {
  const map = { 'zh-CN': 'zh_cn', 'en-US': 'en_us' }
  return map[locale] || String(locale || 'zh-CN').toLowerCase().replace(/-/g, '_')
}

// Build a translator object for the given locale. Call sites keep using camelCase
// keys (e.g. t.pluginsTitle) which resolve to snake_case keys automatically.
export function buildT(locale) {
  const prefix = localeToPrefix(locale)
  return new Proxy({}, {
    get(_, prop) {
      if (typeof prop !== 'string') return undefined
      return messages[`${prefix}_${camelToSnake(prop)}`]
    }
  })
}
