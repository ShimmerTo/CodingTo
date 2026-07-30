import {
  Calculator, Code, Database, FileText, FolderOpen, Globe, Image,
  ListTodo, PenLine, Search, TerminalSquare, Wrench
} from 'lucide-vue-next'

const TOOL_ICONS = {
  bash: TerminalSquare, shell: TerminalSquare, terminal: TerminalSquare, command: TerminalSquare,
  run: TerminalSquare, exec: TerminalSquare, sh: TerminalSquare, powershell: TerminalSquare,
  read: Search, view: FileText, open: FileText, cat: FileText, get: FileText,
  write: PenLine, edit: PenLine, create: PenLine, save: PenLine, update: PenLine, patch: PenLine,
  modify: PenLine, delete: PenLine, remove: PenLine, mkdir: PenLine, touch: PenLine,
  search: Search, grep: Search, find: Search, glob: Search, ripgrep: Search, lookup: Search,
  web: Globe, fetch: Globe, http: Globe, browse: Globe, url: Globe, crawl: Globe,
  code: Code, lint: Code, format: Code, refactor: Code, compile: Code, build: Code,
  list: FolderOpen, ls: FolderOpen, dir: FolderOpen, tree: FolderOpen, files: FolderOpen,
  todo: ListTodo, task: ListTodo, plan: ListTodo, checklist: ListTodo,
  sql: Database, query: Database, db: Database,
  image: Image, img: Image, screenshot: Image, draw: Image, paint: Image,
  calc: Calculator, compute: Calculator, math: Calculator
}

const TOOL_KEYWORD_ICONS = [
  [/bash|shell|terminal|command|^run$|exec|sh$|powershell/i, TerminalSquare],
  [/read|view|open|^cat$|^get$|load|fetch_file/i, FileText],
  [/write|edit|creat|sav|updat|patch|modif|delet|remov|mkdir|touch|rename|move/i, PenLine],
  [/search|grep|find|glob|ripgrep|lookup/i, Search],
  [/web|http|browser|browse|url|crawl|scrap/i, Globe],
  [/code|lint|format|refactor|compil|build|^run_/i, Code],
  [/list|^ls$|dir|tree|folder|file/i, FolderOpen],
  [/todo|task|plan|checklist|step/i, ListTodo],
  [/sql|query|database|^db$/i, Database],
  [/image|img|screenshot|draw|paint|picture|photo/i, Image],
  [/calc|comput|math|eval/i, Calculator]
]

function asObject(value) {
  if (typeof value === 'string') {
    try {
      const parsed = JSON.parse(value)
      if (parsed && typeof parsed === 'object') return parsed
    } catch {
      // Keep the original string when it is not JSON.
    }
  }
  return value
}

const GENERIC_TOOL_NAMES = new Set(['tool', 'mcp', 'gateway', 'function'])
const SUMMARY_IGNORED_KEYS = new Set(['tool', 'toolName', 'tool_name'])
const SENSITIVE_KEYS = /^(?:api[_-]?key|access[_-]?token|auth(?:orization)?|cookie|password|passwd|private[_-]?key|secret)$/i

function normalizedInput(message) {
  let value = toolInput(message)
  // Some gateways encode both the whole input and its `args` field as JSON.
  // Unwrap only a few levels so malformed/self-referential data stays harmless.
  for (let depth = 0; depth < 3; depth += 1) {
    const parsed = asObject(value)
    if (parsed === value) break
    value = parsed
  }
  return value
}

function inputToolName(message) {
  const input = normalizedInput(message)
  if (!input || typeof input !== 'object' || Array.isArray(input)) return ''
  return typeof input.tool === 'string' ? input.tool.trim()
    : typeof input.toolName === 'string' ? input.toolName.trim()
      : typeof input.tool_name === 'string' ? input.tool_name.trim()
      : ''
}

function findObject(value, predicate) {
  const queue = [value]
  const seen = new Set()
  while (queue.length && seen.size < 256) {
    const current = asObject(queue.shift())
    if (!current || typeof current !== 'object' || seen.has(current)) continue
    seen.add(current)
    if (predicate(current)) return current
    if (Array.isArray(current)) queue.push(...current)
    else queue.push(...Object.values(current))
  }
  return null
}

function compactText(value, limit = 64) {
  const text = String(value ?? '').replace(/\s+/g, ' ').trim()
  return text.length > limit ? `${text.slice(0, limit - 1)}…` : text
}

function compactValue(value, key) {
  if (SENSITIVE_KEYS.test(key)) return '••••'
  const parsed = asObject(value)
  if (typeof parsed === 'string') return compactText(parsed)
  if (parsed == null || typeof parsed === 'number' || typeof parsed === 'boolean') return String(parsed)
  try {
    return compactText(JSON.stringify(parsed))
  } catch {
    return compactText(parsed)
  }
}

function objectSummary(input) {
  // MCP gateways commonly wrap the useful arguments in `{ tool, args }`.
  // Prefer those arguments, while still handling arbitrary parameter objects.
  if (Object.hasOwn(input, 'args')) {
    const args = asObject(input.args)
    if (args && typeof args === 'object') return objectSummary(args)
    if (args != null && args !== '') return compactText(args, 120)
  }

  const entries = Object.entries(input).filter(([key, value]) => (
    !SUMMARY_IGNORED_KEYS.has(key) && value != null && value !== ''
  ))
  if (!entries.length) return ''

  const preferredKey = ['path', 'file_path', 'command', 'cmd', 'query'].find(key => (
    Object.hasOwn(input, key) && input[key] != null && input[key] !== ''
  ))
  if (preferredKey) return compactText(input[preferredKey], 120)

  const parts = entries.slice(0, 3).map(([key, value]) => `${key}: ${compactValue(value, key)}`)
  if (entries.length > 3) parts.push(`+${entries.length - 3}`)
  return compactText(parts.join(' · '), 140)
}

function isHttpUrl(value) {
  return typeof value === 'string' && /^https?:\/\//i.test(value.trim())
}

export function toolName(message) {
  const detail = message.detail || {}
  const declaredName = detail.toolCall?.name || detail.name || detail.toolName || message.content || ''
  const nestedName = inputToolName(message)
  // A gateway name conveys less than the concrete nested tool it dispatches.
  if (nestedName && (!declaredName || GENERIC_TOOL_NAMES.has(String(declaredName).trim().toLowerCase()))) return nestedName
  return String(declaredName || nestedName || 'Tool').trim()
}

export function toolInput(message) {
  const detail = message.detail || {}
  return detail.args ?? detail.input ?? detail.arguments ?? detail.toolCall?.arguments
}

export function toolOutput(message) {
  const detail = message.detail || {}
  return detail.output ?? detail.result
}

export function toolStatus(message) {
  return message.detail?.status === 'done' || message.detail?.type === 'tool_execution_end' ? 'done' : 'running'
}

export function isSubagentRunTool(message) {
  if (message.role !== 'tool' || toolName(message) !== 'codingto_subagent') return false

  const actionInput = findObject(normalizedInput(message), value => typeof value.action === 'string')
  if (actionInput) return actionInput.action.trim().toLowerCase() === 'run'

  return Boolean(findObject(message.detail, value => (
    value.kind === 'subagent_event' && typeof value.runId === 'string' && value.runId
  )))
}

export function toolDuration(message, now = Date.now()) {
  const detail = message.detail || {}
  if (detail.durationMs) return detail.durationMs
  if (detail.status !== 'done' && detail.startedAt) return now - detail.startedAt
  return 0
}

export function toolSummary(message) {
  const input = normalizedInput(message)
  if (input == null || input === '') return ''
  if (typeof input === 'string') return compactText(input, 120)
  if (Array.isArray(input)) return compactValue(input, 'input')
  if (typeof input === 'object') return objectSummary(input)
  return compactText(input, 120)
}

function editArguments(message) {
  const name = toolName(message)
  if (!/(?:^|[_.:/-])edit(?:$|[_.:/-])/i.test(name)) return null

  let input = normalizedInput(message)
  // MCP-style gateway calls keep the concrete edit arguments in `args`.
  for (let depth = 0; depth < 3; depth += 1) {
    input = asObject(input)
    if (!input || typeof input !== 'object' || Array.isArray(input)) return null
    const isGateway = ['tool', 'toolName', 'tool_name'].some(key => Object.hasOwn(input, key))
    if (!isGateway || !Object.hasOwn(input, 'args')) break
    input = input.args
  }
  input = asObject(input)
  if (!input || typeof input !== 'object' || Array.isArray(input)) return null

  const candidates = Array.isArray(input.edits) ? input.edits : [input]
  const inputPath = input.path ?? input.filePath ?? input.file_path ?? input.filename ?? input.file
  const paths = new Set()
  for (const candidate of [input, ...candidates]) {
    if (!candidate || typeof candidate !== 'object' || Array.isArray(candidate)) continue
    const path = candidate.path ?? candidate.filePath ?? candidate.file_path ?? candidate.filename ?? candidate.file
    if (typeof path === 'string' && path.trim()) paths.add(path.trim())
  }
  const edits = candidates.flatMap(candidate => {
    if (!candidate || typeof candidate !== 'object' || Array.isArray(candidate)) return []
    const oldText = candidate.oldText ?? candidate.old_text ?? candidate.oldString ?? candidate.old_string
    const newText = candidate.newText ?? candidate.new_text ?? candidate.newString ?? candidate.new_string
    const path = candidate.path ?? candidate.filePath ?? candidate.file_path ?? candidate.filename ?? candidate.file ?? inputPath
    return typeof oldText === 'string' && typeof newText === 'string'
      ? [{ oldText, newText, path: typeof path === 'string' ? path.trim() : '' }]
      : []
  })
  return edits.length ? { edits, files: paths.size || 1 } : null
}

function textLines(text) {
  if (!text) return []
  const lines = text.replace(/\r\n?/g, '\n').split('\n')
  if (lines.at(-1) === '') lines.pop()
  return lines
}

function countChangedLines(oldText, newText) {
  const oldLines = textLines(oldText)
  const newLines = textLines(newText)
  let start = 0
  while (start < oldLines.length && start < newLines.length && oldLines[start] === newLines[start]) start += 1

  let oldEnd = oldLines.length
  let newEnd = newLines.length
  while (oldEnd > start && newEnd > start && oldLines[oldEnd - 1] === newLines[newEnd - 1]) {
    oldEnd -= 1
    newEnd -= 1
  }
  const before = oldLines.slice(start, oldEnd)
  const after = newLines.slice(start, newEnd)
  if (!before.length || !after.length) return { added: after.length, deleted: before.length }

  // Myers' shortest-edit-path algorithm counts line insertions/deletions while
  // avoiding the quadratic memory cost of an LCS matrix for large edits.
  const oldCount = before.length
  const newCount = after.length
  const max = oldCount + newCount
  const offset = max + 1
  const furthestX = new Int32Array(max * 2 + 3)
  for (let distance = 0; distance <= max; distance += 1) {
    for (let diagonal = -distance; diagonal <= distance; diagonal += 2) {
      const index = offset + diagonal
      let x
      if (diagonal === -distance || (diagonal !== distance && furthestX[index - 1] < furthestX[index + 1])) {
        x = furthestX[index + 1]
      } else {
        x = furthestX[index - 1] + 1
      }
      let y = x - diagonal
      while (x < oldCount && y < newCount && before[x] === after[y]) {
        x += 1
        y += 1
      }
      furthestX[index] = x
      if (x >= oldCount && y >= newCount) {
        return {
          added: (distance + newCount - oldCount) / 2,
          deleted: (distance + oldCount - newCount) / 2
        }
      }
    }
  }
  return { added: newCount, deleted: oldCount }
}

function backtrackDiff(trace, oldLines, newLines) {
  const result = []
  let x = oldLines.length
  let y = newLines.length

  for (let distance = trace.length - 1; distance >= 0; distance -= 1) {
    const furthest = trace[distance]
    const diagonal = x - y
    const down = diagonal === -distance || (
      diagonal !== distance && (furthest.get(diagonal - 1) ?? -1) < (furthest.get(diagonal + 1) ?? -1)
    )
    const previousDiagonal = down ? diagonal + 1 : diagonal - 1
    const previousX = furthest.get(previousDiagonal) ?? 0
    const previousY = previousX - previousDiagonal

    while (x > previousX && y > previousY) {
      result.push({ kind: 'context', text: oldLines[x - 1] })
      x -= 1
      y -= 1
    }
    if (distance === 0) break
    if (x === previousX) {
      result.push({ kind: 'added', text: newLines[y - 1] })
      y -= 1
    } else {
      result.push({ kind: 'deleted', text: oldLines[x - 1] })
      x -= 1
    }
  }
  return result.reverse()
}

function unifiedDiffLines(oldText, newText) {
  const oldLines = textLines(oldText)
  const newLines = textLines(newText)
  const max = oldLines.length + newLines.length
  let rawLines = []

  if (!oldLines.length) {
    rawLines = newLines.map(text => ({ kind: 'added', text }))
  } else if (!newLines.length) {
    rawLines = oldLines.map(text => ({ kind: 'deleted', text }))
  } else {
    const furthest = new Map([[1, 0]])
    const trace = []
    let complete = false
    for (let distance = 0; distance <= max && !complete; distance += 1) {
      trace.push(new Map(furthest))
      for (let diagonal = -distance; diagonal <= distance; diagonal += 2) {
        const down = diagonal === -distance || (
          diagonal !== distance && (furthest.get(diagonal - 1) ?? -1) < (furthest.get(diagonal + 1) ?? -1)
        )
        let x = down ? (furthest.get(diagonal + 1) ?? 0) : (furthest.get(diagonal - 1) ?? 0) + 1
        let y = x - diagonal
        while (x < oldLines.length && y < newLines.length && oldLines[x] === newLines[y]) {
          x += 1
          y += 1
        }
        furthest.set(diagonal, x)
        if (x >= oldLines.length && y >= newLines.length) {
          rawLines = backtrackDiff(trace, oldLines, newLines)
          complete = true
          break
        }
      }
    }
  }

  let oldNumber = 0
  let newNumber = 0
  return rawLines.map(line => {
    if (line.kind !== 'added') oldNumber += 1
    if (line.kind !== 'deleted') newNumber += 1
    return {
      ...line,
      oldNumber: line.kind === 'added' ? null : oldNumber,
      newNumber: line.kind === 'deleted' ? null : newNumber
    }
  })
}

export function toolEditDiff(message) {
  const parsed = editArguments(message)
  if (!parsed) return null
  return {
    files: parsed.files,
    edits: parsed.edits.map((edit, index) => ({
      index,
      path: edit.path,
      oldLineCount: textLines(edit.oldText).length,
      newLineCount: textLines(edit.newText).length,
      lines: unifiedDiffLines(edit.oldText, edit.newText)
    }))
  }
}

// 将单个 edit 的纵向 unified 行转换为左右对比（split）结构。
// 每个 row 为：
//   - { kind: 'context', text, oldNumber, newNumber }  —— 未改动的上下文，左右一致
//   - { kind: 'change', left, right }                  —— 改动行，left/right 为 {num,text}|null
function buildEditSideBySide(edit) {
  const rows = []
  const lines = edit.lines
  let i = 0
  while (i < lines.length) {
    const line = lines[i]
    if (line.kind === 'context') {
      rows.push({ kind: 'context', text: line.text, oldNumber: line.oldNumber, newNumber: line.newNumber })
      i += 1
      continue
    }
    if (line.kind === 'deleted') {
      const dels = []
      while (i < lines.length && lines[i].kind === 'deleted') { dels.push(lines[i]); i += 1 }
      const adds = []
      while (i < lines.length && lines[i].kind === 'added') { adds.push(lines[i]); i += 1 }
      const max = Math.max(dels.length, adds.length)
      for (let k = 0; k < max; k += 1) {
        const left = dels[k] ? { num: dels[k].oldNumber, text: dels[k].text } : null
        const right = adds[k] ? { num: adds[k].newNumber, text: adds[k].text } : null
        rows.push({ kind: 'change', left, right })
      }
      continue
    }
    if (line.kind === 'added') {
      while (i < lines.length && lines[i].kind === 'added') {
        rows.push({ kind: 'change', left: null, right: { num: lines[i].newNumber, text: lines[i].text } })
        i += 1
      }
      continue
    }
    i += 1
  }
  return rows
}

// 与 toolEditDiff 结构一致，但每个 edit 额外携带 split 对比所需的 rows，
// 供对话详情里的左右对比视图使用。
export function toolEditDiffSideBySide(message) {
  const diff = toolEditDiff(message)
  if (!diff) return null
  return {
    files: diff.files,
    edits: diff.edits.map(edit => ({ ...edit, rows: buildEditSideBySide(edit) }))
  }
}

export function toolLineChanges(message) {
  const editArgumentsResult = editArguments(message)
  if (!editArgumentsResult) return null
  return editArgumentsResult.edits.reduce((total, edit) => {
    const changed = countChangedLines(edit.oldText, edit.newText)
    total.added += changed.added
    total.deleted += changed.deleted
    return total
  }, { files: editArgumentsResult.files, added: 0, deleted: 0 })
}

export function toolUrl(message) {
  const output = asObject(toolOutput(message))
  const details = output && output.details
  if (details) {
    if (isHttpUrl(details.data?.url)) return details.data.url.trim()
    if (isHttpUrl(details.subcommand)) return details.subcommand.trim()
    if (isHttpUrl(details.pageChangeSummary?.url)) return details.pageChangeSummary.url.trim()
    if (isHttpUrl(details.sessionTabTarget?.url)) return details.sessionTabTarget.url.trim()
  }

  const input = asObject(toolInput(message))
  const sources = []
  if (Array.isArray(input)) sources.push(input)
  else if (input && typeof input === 'object') {
    if (Array.isArray(input.args)) sources.push(input.args)
    for (const key of ['url', 'href', 'link', 'uri', 'address']) {
      if (isHttpUrl(input[key])) return input[key].trim()
    }
  }
  for (const source of sources) {
    for (const item of source) {
      if (isHttpUrl(item)) return item.trim()
    }
  }
  return ''
}

export function toolUrlTitle(message) {
  const output = asObject(toolOutput(message))
  const details = output && output.details
  if (!details) return ''
  if (isHttpUrl(details.data?.url)) return details.data?.title || ''
  if (details.sessionTabTarget?.title) return details.sessionTabTarget.title
  if (details.pageChangeSummary?.title) return details.pageChangeSummary.title
  return ''
}

export function toolIcon(message) {
  const name = toolName(message)
  if (name && TOOL_ICONS[name]) return TOOL_ICONS[name]
  for (const [pattern, icon] of TOOL_KEYWORD_ICONS) {
    if (name && pattern.test(name)) return icon
  }
  return Wrench
}

// 读取文件类工具（read/view/cat/get/load 等）：让读到的内容直接展示，
// 并把 limit/offset 等参数显示在文件名右侧。
const READ_TOOL_NAMES = /^(?:read|read_file|readfile|view|cat|get|get_file|load|load_file|fetch_file|show|display|print)$/i
const READ_PATH_KEYS = ['filePath', 'path', 'file_path', 'filename', 'file', 'absPath', 'abs_path']
const READ_PARAM_KEYS = ['limit', 'offset', 'startLine', 'endLine']

function normalizeReadInput(message) {
  const obj = normalizedInput(message)
  return obj && typeof obj === 'object' && !Array.isArray(obj) ? obj : {}
}

export function isReadTool(message) {
  if (!message || toolEditDiff(message)) return false
  const name = toolName(message)
  if (!name || !READ_TOOL_NAMES.test(name)) return false
  const input = normalizeReadInput(message)
  return READ_PATH_KEYS.some(key => typeof input[key] === 'string' && input[key].trim())
}

export function readToolMeta(message) {
  if (!isReadTool(message)) return null
  const input = normalizeReadInput(message)
  const path = READ_PATH_KEYS.map(key => input[key]).find(value => typeof value === 'string' && value.trim()) || ''
  const params = []
  for (const key of READ_PARAM_KEYS) {
    const value = input[key]
    if (value != null && String(value).trim() !== '') params.push(`${key} ${value}`)
  }
  return { path, params }
}

// 读取工具的输出常被包成 { content: [{ type, text|data, ... }] } 的 JSON。
// 这里把内容拆成文本块 / 图片块，避免把 JSON 骨架展示出来，
// 也不会丢失调 type 为非 text 的内容（如 image）。
export function readToolBlocks(message) {
  if (!isReadTool(message)) return null
  const raw = toolOutput(message)
  if (raw == null || raw === '') return null

  const parsed = asObject(raw)
  if (typeof parsed === 'string') return [{ kind: 'text', text: parsed }]
  if (!parsed || typeof parsed !== 'object') return null

  const content = parsed.content
  if (Array.isArray(content) && content.length) {
    const blocks = []
    for (const item of content) {
      if (!item || typeof item !== 'object') {
        if (typeof item === 'string' && item !== '') blocks.push({ kind: 'text', text: item })
        continue
      }
      const type = item.type
      if (type === 'text') {
        if (typeof item.text === 'string' && item.text !== '') blocks.push({ kind: 'text', text: item.text })
      } else if (type === 'image') {
        if (item.data && item.mimeType) blocks.push({ kind: 'image', data: item.data, mimeType: item.mimeType })
      } else {
        // 未知类型：尽量以文本呈现，避免内容被吞掉。
        if (typeof item.text === 'string' && item.text !== '') blocks.push({ kind: 'text', text: item.text })
        else blocks.push({ kind: 'text', text: JSON.stringify(item) })
      }
    }
    if (blocks.length) return blocks
  }

  if (typeof parsed.result === 'string' && parsed.result.trim()) return [{ kind: 'text', text: parsed.result }]
  if (typeof parsed.output === 'string' && parsed.output.trim()) return [{ kind: 'text', text: parsed.output }]

  // 退化处理：原样 JSON，避免吞掉内容。
  return [{ kind: 'text', text: typeof raw === 'string' ? raw : JSON.stringify(parsed, null, 2) }]
}
