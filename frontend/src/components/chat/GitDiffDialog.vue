<script setup>
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import {
  ArrowDown, ArrowLeft, ArrowRight, ArrowUp, Brain, Columns2, FileCode2, Image as ImageIcon,
  LoaderCircle, MessageSquarePlus, PackageOpen, Rows3, Settings2, Sparkles, X
} from 'lucide-vue-next'
import {
  generateSessionGitFileAnalysis, getSessionGitCommitFileDetail, getSessionGitCompareFileDetail, getSessionGitFileDetail
} from '../../backend.js'
import GitAIPromptDialog from '../GitAIPromptDialog.vue'
import { renderMarkdown } from './chatFormatters.js'

const GIT_ANALYSIS_MODEL_CACHE_KEY = 'codingto:git-analysis-model'

function cachedAnalysisModel() {
  try { return localStorage.getItem(GIT_ANALYSIS_MODEL_CACHE_KEY) || '' } catch { return '' }
}

const props = defineProps({
  open: { type: Boolean, default: false },
  sessionId: { type: Number, required: true },
  scope: { type: String, required: true },
  files: { type: Array, default: () => [] },
  index: { type: Number, default: 0 },
  baseBranch: { type: String, default: '' },
  compareLeft: { type: String, default: '' },
  compareRight: { type: String, default: '' },
  commit: { type: String, default: '' },
  t: { type: Object, required: true },
  language: { type: String, default: 'zh-CN' },
  modelOptions: { type: Array, default: () => [] },
  selectedModelValue: { type: String, default: '' }
})

const emit = defineEmits(['close', 'update:index', 'add-selection-to-chat'])
const loading = ref(false)
const error = ref('')
const detail = ref(null)
const layout = ref(loadLayout())
const analysisModel = ref(cachedAnalysisModel())
const analysisPromptOpen = ref(false)
const analysisVisible = ref(false)
const analysisLoading = ref(false)
const analysisError = ref('')
const analysis = ref('')
const splitOldPane = ref(null)
const splitGutter = ref(null)
const splitNewPane = ref(null)
const splitOldContent = ref(null)
const unifiedPane = ref(null)
const diffBody = ref(null)
const selectionMenu = ref(null)
const selectionMenuEl = ref(null)
const activeChangeIndex = ref(-1)
let requestNonce = 0
let analysisRequestNonce = 0
let splitScrollLock = false

const activeFile = computed(() => props.files[props.index] || null)
const activeScope = computed(() => activeFile.value?.diffScope || props.scope)
const positionLabel = computed(() => `${Math.min(props.index + 1, props.files.length)} / ${props.files.length}`)
const isHorizontal = computed(() => layout.value === 'horizontal')
const enabledModels = computed(() => props.modelOptions.filter(option => !option.disabled))
const analysisModelGroups = computed(() => {
  const groups = {}
  for (const option of props.modelOptions) (groups[option.provider] ||= []).push(option)
  return Object.entries(groups).map(([provider, options]) => ({ provider, options }))
})
const canAnalyze = computed(() => (
  !!analysisModel.value && !loading.value && detail.value?.kind === 'text' &&
  !!detail.value?.hunks?.length && !detail.value?.fromNodeSnapshot
))
const renderedAnalysis = computed(() => renderMarkdown(analysis.value))
const fullSplitRows = computed(() => buildFullSplitRows(detail.value))
const splitSides = computed(() => buildSplitView(fullSplitRows.value))
const fullUnifiedRows = computed(() => buildFullUnifiedRows(fullSplitRows.value))
const changeCount = computed(() => detail.value?.hunks?.length || 0)
const overviewMarkers = computed(() => {
  const rows = isHorizontal.value ? splitSides.value : fullUnifiedRows.value
  const total = Math.max(1, rows.length)
  return (detail.value?.hunks || []).map((hunk, index) => {
    let first = rows.findIndex(row => row.changeIndex === index)
    if (first < 0) first = Math.min(index, total - 1)
    const hasAdded = (hunk.lines || []).some(line => line.kind === 'added')
    const hasDeleted = (hunk.lines || []).some(line => line.kind === 'deleted')
    const kind = hasAdded && hasDeleted ? 'modified' : hasAdded ? 'added' : 'deleted'
    return {
      index,
      kind,
      style: {
        top: `${(first / total) * 100}%`,
        height: '3px',
      },
    }
  })
})

function loadLayout() {
  try {
    return localStorage.getItem('codingto:git-diff-layout') === 'horizontal' ? 'horizontal' : 'vertical'
  } catch {
    return 'vertical'
  }
}

function setLayout(value) {
  layout.value = value === 'vertical' ? 'vertical' : 'horizontal'
  try { localStorage.setItem('codingto:git-diff-layout', layout.value) } catch { /* 存储不可用时仅保持本次窗口状态 */ }
}

// 切换文件/布局时先把三栏滚动位置归零，避免残留旧偏移或越界值
watch([detail, isHorizontal], () => {
  for (const pane of [splitOldPane.value, splitGutter.value, splitNewPane.value, unifiedPane.value]) {
    if (!pane) continue
    pane.scrollTop = 0
    pane.scrollLeft = 0
  }
}, { flush: 'post' })

watch(detail, async value => {
  activeChangeIndex.value = value?.hunks?.length ? 0 : -1
  await nextTick()
  if (activeChangeIndex.value >= 0) scrollToChange(activeChangeIndex.value)
}, { flush: 'post' })

watch(isHorizontal, async () => {
  await nextTick()
  if (activeChangeIndex.value >= 0) scrollToChange(activeChangeIndex.value)
}, { flush: 'post' })

function resetAnalysis() {
  analysisRequestNonce += 1
  analysisVisible.value = false
  analysisLoading.value = false
  analysisError.value = ''
  analysis.value = ''
}

function cacheAnalysisModel() {
  const option = props.modelOptions.find(item => item.value === analysisModel.value && !item.disabled)
  if (!option) return
  try { localStorage.setItem(GIT_ANALYSIS_MODEL_CACHE_KEY, option.value) } catch { /* 缓存不可用时仍保留当前选择 */ }
}

watch(
  // 包含 props.files：节点产物入口每次点击都会传入新数组（引用变化），
  // 确保跨节点点击同路径同 index 的文件时也会重载，避免显示陈旧快照。
  [() => props.open, () => props.index, () => activeFile.value?.path, activeScope, () => props.files, () => props.baseBranch, () => props.compareLeft, () => props.compareRight, () => props.commit],
  loadDetail,
  { immediate: true }
)

async function loadDetail() {
  if (!props.open || !activeFile.value?.path) return
  closeSelectionMenu()
  const nonce = ++requestNonce
  resetAnalysis()
  loading.value = true
  error.value = ''
  detail.value = null
  try {
    const result = activeScope.value === 'commit'
      ? await getSessionGitCommitFileDetail(props.sessionId, props.commit, activeFile.value.path, props.language)
      : activeScope.value === 'compare'
        ? await getSessionGitCompareFileDetail(props.sessionId, props.compareLeft, props.compareRight, activeFile.value.path)
        : await getSessionGitFileDetail(props.sessionId, activeScope.value, activeFile.value.path, props.baseBranch)
    if (nonce === requestNonce) detail.value = result
  } catch (cause) {
    if (nonce === requestNonce) {
      // 节点产物入口的文件附带编辑快照 hunks：git 实时对比不可用
      // （非 git 仓库 / 文件已提交）时回退渲染节点快照 diff。
      const file = activeFile.value
      if (file?.hunks?.length) {
        detail.value = {
          path: file.path,
          oldPath: file.oldPath || '',
          scope: activeScope.value,
          status: file.status,
          kind: 'text',
          hunks: file.hunks,
          added: file.added || 0,
          deleted: file.deleted || 0,
          fromNodeSnapshot: true,
          before: { lineCount: maxHunkLine(file.hunks, 'oldNumber') },
          after: { lineCount: maxHunkLine(file.hunks, 'newNumber') }
        }
      } else {
        error.value = String(cause)
      }
    }
  } finally {
    if (nonce === requestNonce) loading.value = false
  }
}

watch(
  () => [props.open, props.selectedModelValue, props.modelOptions],
  ([open, selected]) => {
    if (!open) {
      closeSelectionMenu()
      resetAnalysis()
      analysisPromptOpen.value = false
      return
    }
    if (enabledModels.value.some(option => option.value === analysisModel.value)) return
    const cached = cachedAnalysisModel()
    analysisModel.value = enabledModels.value.some(option => option.value === cached)
      ? cached
      : enabledModels.value.some(option => option.value === selected)
        ? selected
        : enabledModels.value[0]?.value || ''
    cacheAnalysisModel()
  },
  { immediate: true }
)

async function analyzeCurrentFile() {
  if (analysisLoading.value || !canAnalyze.value) return
  const selected = props.modelOptions.find(option => option.value === analysisModel.value && !option.disabled)
  if (!selected) return
  const nonce = ++analysisRequestNonce
  analysisVisible.value = true
  analysisLoading.value = true
  analysisError.value = ''
  analysis.value = ''
  try {
    const result = await generateSessionGitFileAnalysis({
      sessionId: Number(props.sessionId),
      scope: activeScope.value,
      path: activeFile.value.path,
      baseBranch: props.baseBranch,
      left: props.compareLeft,
      right: props.compareRight,
      commit: props.commit,
      language: props.language,
      provider: selected.provider,
      model: selected.model,
    })
    if (nonce === analysisRequestNonce) analysis.value = result?.analysis || ''
  } catch (cause) {
    if (nonce === analysisRequestNonce) analysisError.value = String(cause)
  } finally {
    if (nonce === analysisRequestNonce) analysisLoading.value = false
  }
}

function maxHunkLine(hunks, key) {
  let max = 0
  for (const hunk of hunks || []) {
    for (const line of hunk.lines || []) {
      const value = Number(line[key]) || 0
      if (value > max) max = value
    }
  }
  return max
}

function navigate(delta) {
  const next = props.index + delta
  if (next < 0 || next >= props.files.length) return
  emit('update:index', next)
}

function navigateChange(delta) {
  if (!changeCount.value) return
  const current = activeChangeIndex.value < 0 ? (delta > 0 ? -1 : changeCount.value) : activeChangeIndex.value
  const next = Math.max(0, Math.min(changeCount.value - 1, current + delta))
  if (next === activeChangeIndex.value) return
  activeChangeIndex.value = next
  void nextTick(() => scrollToChange(next))
}

function scrollToChange(index) {
  // 切换一律瞬时定位：smooth 动画期间对侧同步会打断/拉回，造成“切换失效”与抖动
  if (isHorizontal.value) {
    const target = splitOldContent.value?.querySelector(`[data-change-index="${index}"]`)
    if (!target || !splitOldPane.value) return
    const top = Math.max(0, target.offsetTop - 18)
    for (const pane of [splitOldPane.value, splitGutter.value, splitNewPane.value]) {
      if (pane) pane.scrollTop = top
    }
    return
  }
  const target = unifiedPane.value?.querySelector(`[data-change-index="${index}"]`)
  if (!target || !unifiedPane.value) return
  unifiedPane.value.scrollTop = Math.max(0, target.offsetTop - 18)
}

function activateOverviewMarker(index) {
  activeChangeIndex.value = index
  void nextTick(() => scrollToChange(index))
}

function overviewMarkerLabel(marker) {
  const labels = {
    added: props.t.gitDiffMarkerAdded,
    modified: props.t.gitDiffMarkerModified,
    deleted: props.t.gitDiffMarkerDeleted,
  }
  return `${labels[marker.kind]} · ${marker.index + 1} / ${changeCount.value}`
}

function onKeydown(event) {
  if (!props.open) return
  if (analysisPromptOpen.value) return
  if (event.key === 'Escape') {
    if (selectionMenu.value) {
      closeSelectionMenu()
      return
    }
    emit('close')
    return
  }
  if (['INPUT', 'SELECT', 'TEXTAREA', 'BUTTON'].includes(event.target?.tagName)) return
  if (event.key === 'F7') {
    event.preventDefault()
    navigateChange(event.shiftKey ? -1 : 1)
    return
  }
  if (event.key === 'ArrowLeft') navigate(-1)
  if (event.key === 'ArrowRight') navigate(1)
}

// 在 diff 内容区选中文本后，于鼠标位置弹出「添加到对话」菜单
function onDiffMouseUp(event) {
  const selection = window.getSelection()
  const text = selection?.toString().trim()
  const body = event.target.closest?.('.git-diff-dialog__body')
  const inBody = node => {
    if (!node) return false
    const element = node.nodeType === Node.TEXT_NODE ? node.parentElement : node
    return !!element?.closest?.('.git-diff-dialog__body')
  }
  if (!text || !body || !inBody(selection?.anchorNode) || !inBody(selection?.focusNode)) {
    selectionMenu.value = null
    return
  }
  const menuWidth = 180
  const menuHeight = 42
  selectionMenu.value = {
    left: Math.max(8, Math.min(event.clientX, window.innerWidth - menuWidth - 8)),
    top: event.clientY + menuHeight + 8 > window.innerHeight ? Math.max(8, event.clientY - menuHeight - 4) : event.clientY + 4,
    text,
  }
}

function closeSelectionMenu() {
  selectionMenu.value = null
}

function onDocumentMouseDown(event) {
  if (selectionMenuEl.value && !selectionMenuEl.value.contains(event.target)) closeSelectionMenu()
}

function addSelectionToChat() {
  if (!selectionMenu.value?.text) return
  emit('add-selection-to-chat', selectionMenu.value.text)
  closeSelectionMenu()
}

onMounted(() => {
  window.addEventListener('keydown', onKeydown)
  document.addEventListener('mousedown', onDocumentMouseDown)
})
onBeforeUnmount(() => {
  window.removeEventListener('keydown', onKeydown)
  document.removeEventListener('mousedown', onDocumentMouseDown)
})

// 三栏垂直滚动同步：改动块行高一致，锁内直接对齐 scrollTop。
// 来源 pane 先置位再广播，防止反向回调把自己拉回造成抖动。
function syncSplitScroll(source) {
  if (splitScrollLock) return
  splitScrollLock = true
  const top = source.scrollTop
  for (const pane of [splitOldPane.value, splitGutter.value, splitNewPane.value]) {
    // 容差比较：缩放下 scrollTop 可能为小数，无变化时不写回，避免回写触发新的 scroll 事件形成回环
    if (pane && pane !== source && Math.abs(pane.scrollTop - top) > 0.5) pane.scrollTop = top
  }
  splitScrollLock = false
}

function onSplitScroll(side) {
  const source = side === 'new' ? splitNewPane.value : splitOldPane.value
  if (source) syncSplitScroll(source)
}

// 左栏设为 overflow-y: hidden，不渲染竖向滚动条，但仍可用 scrollTop 编程滚动，
// 因此三栏垂直同步照常工作。代价是左栏自身不响应滚轮，
// 这里把滚轮增量转发给右栏，由右栏滚动驱动同步，保证在左栏上滚轮同样能滚动整个对比区。
function onSplitOldWheel(event) {
  const target = splitNewPane.value
  if (!target || !event.deltaY) return
  event.preventDefault()
  target.scrollTop += event.deltaY
}

function lineClass(kind) {
  return kind === 'added' ? 'is-added' : kind === 'deleted' ? 'is-deleted' : 'is-context'
}

// 三栏分栏模式下，按当前行左右两侧内容判断是修改 / 纯新增 / 纯删除，
// 用于把红/绿色带从一侧连续穿过 gutter 连到另一侧。
function splitRowClass(row) {
  const hasOld = row.left?.kind === 'change'
  const hasNew = row.right?.kind === 'change'
  if (hasOld && hasNew) return 'is-modified'
  if (hasNew) return 'is-added-row'
  if (hasOld) return 'is-deleted-row'
  return ''
}

function lineSign(kind) {
  return kind === 'added' ? '+' : kind === 'deleted' ? '-' : ' '
}

function formatSize(bytes) {
  const value = Number(bytes) || 0
  if (value < 1024) return `${value} B`
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KB`
  return `${(value / (1024 * 1024)).toFixed(1)} MB`
}

function formatDate(value) {
  if (!Number(value)) return '—'
  return new Date(Number(value)).toLocaleString()
}

function versionTitle(side) {
  return side === 'before' ? props.t.gitBeforeChange : props.t.gitAfterChange
}

function buildSplitRows(lines) {
  const rows = []
  let index = 0
  while (index < (lines || []).length) {
    const line = lines[index]
    if (line.kind === 'context') {
      rows.push({ kind: 'context', text: line.text, oldNumber: line.oldNumber, newNumber: line.newNumber })
      index += 1
      continue
    }
    if (line.kind === 'deleted') {
      const deleted = []
      while (lines[index]?.kind === 'deleted') deleted.push(lines[index++])
      const added = []
      while (lines[index]?.kind === 'added') added.push(lines[index++])
      const count = Math.max(deleted.length, added.length)
      for (let rowIndex = 0; rowIndex < count; rowIndex += 1) {
        rows.push({
          kind: 'change',
          left: deleted[rowIndex] ? { number: deleted[rowIndex].oldNumber, text: deleted[rowIndex].text } : null,
          right: added[rowIndex] ? { number: added[rowIndex].newNumber, text: added[rowIndex].text } : null,
        })
      }
      continue
    }
    if (line.kind === 'added') {
      rows.push({ kind: 'change', left: null, right: { number: line.newNumber, text: line.text } })
    }
    index += 1
  }
  return rows
}

function splitFileLines(text) {
  if (typeof text !== 'string' || text === '') return []
  const normalized = text.replace(/\r\n/g, '\n').replace(/\r/g, '\n')
  const lines = normalized.split('\n')
  if (normalized.endsWith('\n')) lines.pop()
  return lines
}

function lineParts(before, after) {
  const left = String(before ?? '')
  const right = String(after ?? '')
  const maxInlineDiffCharacters = 4000
  if (!left && !right) return { left: [], right: [] }
  if (left.length + right.length > maxInlineDiffCharacters) {
    return {
      left: left ? [{ text: left, changed: true }] : [],
      right: right ? [{ text: right, changed: true }] : [],
    }
  }
  const leftChars = Array.from(left)
  const rightChars = Array.from(right)
  let prefix = 0
  while (prefix < leftChars.length && prefix < rightChars.length && leftChars[prefix] === rightChars[prefix]) prefix += 1
  let suffix = 0
  while (
    suffix < leftChars.length - prefix && suffix < rightChars.length - prefix &&
    leftChars[leftChars.length - suffix - 1] === rightChars[rightChars.length - suffix - 1]
  ) suffix += 1
  const parts = (chars, middleEnd) => [
    { text: chars.slice(0, prefix).join(''), changed: false },
    { text: chars.slice(prefix, middleEnd).join(''), changed: true },
    { text: suffix ? chars.slice(chars.length - suffix).join('') : '', changed: false },
  ].filter(part => part.text !== '')
  return {
    left: parts(leftChars, leftChars.length - suffix),
    right: parts(rightChars, rightChars.length - suffix),
  }
}

function decorateChangedRow(row, changeIndex, first, last) {
  const leftText = row.left?.text || ''
  const rightText = row.right?.text || ''
  const inline = lineParts(leftText, rightText)
  return {
    ...row,
    changeIndex,
    changeStart: first,
    changeEnd: last,
    leftParts: inline.left,
    rightParts: inline.right,
  }
}

function buildFullSplitRows(fileDetail) {
  if (!fileDetail?.hunks?.length) return []
  const beforeLines = splitFileLines(fileDetail.before?.text)
  const afterLines = splitFileLines(fileDetail.after?.text)
  const hasFullText = typeof fileDetail.before?.text === 'string' || typeof fileDetail.after?.text === 'string'
  const rows = []
  let oldCursor = 1
  let newCursor = 1

  for (let changeIndex = 0; changeIndex < fileDetail.hunks.length; changeIndex += 1) {
    const hunk = fileDetail.hunks[changeIndex]
    const hunkLines = hunk.lines || []
    const firstOld = hunkLines.find(line => line.oldNumber)?.oldNumber || oldCursor
    const firstNew = hunkLines.find(line => line.newNumber)?.newNumber || newCursor
    if (hasFullText) {
      while (oldCursor < firstOld && newCursor < firstNew) {
        rows.push({ kind: 'context', text: beforeLines[oldCursor - 1] ?? afterLines[newCursor - 1] ?? '', oldNumber: oldCursor, newNumber: newCursor })
        oldCursor += 1
        newCursor += 1
      }
    }

    const hunkRows = buildSplitRows(hunkLines)
    for (let hunkRowIndex = 0; hunkRowIndex < hunkRows.length; hunkRowIndex += 1) {
      const row = hunkRows[hunkRowIndex]
      if (row.kind === 'context') {
        rows.push(row)
        oldCursor = (row.oldNumber || oldCursor) + 1
        newCursor = (row.newNumber || newCursor) + 1
        continue
      }
      rows.push(decorateChangedRow(
        row,
        changeIndex,
        hunkRows[hunkRowIndex - 1]?.kind !== 'change',
        hunkRows[hunkRowIndex + 1]?.kind !== 'change'
      ))
      if (row.left?.number) oldCursor = row.left.number + 1
      if (row.right?.number) newCursor = row.right.number + 1
    }
  }

  if (hasFullText) {
    while (oldCursor <= beforeLines.length || newCursor <= afterLines.length) {
      rows.push({
        kind: 'context',
        text: beforeLines[oldCursor - 1] ?? afterLines[newCursor - 1] ?? '',
        oldNumber: oldCursor <= beforeLines.length ? oldCursor : 0,
        newNumber: newCursor <= afterLines.length ? newCursor : 0,
      })
      if (oldCursor <= beforeLines.length) oldCursor += 1
      if (newCursor <= afterLines.length) newCursor += 1
    }
  }
  return rows
}

// JetBrains 式三栏对比：旧代码 | 行号 gutter | 新代码。
// 变更块按行一一对应，缺失侧用占位行补齐；行号集中在中间 gutter，
// 且 gutter 与代码区域共用背景色，一眼看出新增/删除范围。
function buildSplitView(rows) {
  const result = []
  let index = 0
  while (index < rows.length) {
    const row = rows[index]
    if (row.kind === 'context') {
      result.push({
        kind: 'context',
        oldNumber: row.oldNumber,
        newNumber: row.newNumber,
        changeIndex: null,
        changeStart: false,
        changeEnd: false,
        left: { text: row.text, parts: null, label: '', kind: 'context' },
        right: { text: row.text, parts: null, label: '', kind: 'context' },
      })
      index += 1
      continue
    }

    const changeIndex = row.changeIndex
    const blockRows = []
    while (rows[index]?.kind === 'change' && rows[index]?.changeIndex === changeIndex) blockRows.push(rows[index++])
    const blockLeft = []
    const blockRight = []
    for (const blockRow of blockRows) {
      if (blockRow.left) blockLeft.push({ number: blockRow.left.number, text: blockRow.left.text, parts: blockRow.leftParts })
      if (blockRow.right) blockRight.push({ number: blockRow.right.number, text: blockRow.right.text, parts: blockRow.rightParts })
    }
    const blockHeight = Math.max(blockLeft.length, blockRight.length)
    for (let rowIndex = 0; rowIndex < blockHeight; rowIndex += 1) {
      const first = rowIndex === 0
      const last = rowIndex === blockHeight - 1
      const leftRow = blockLeft[rowIndex]
      const rightRow = blockRight[rowIndex]
      const item = {
        kind: 'change',
        changeIndex,
        changeStart: first,
        changeEnd: last,
        oldNumber: leftRow?.number || 0,
        newNumber: rightRow?.number || 0,
      }
      if (leftRow) {
        item.left = { text: leftRow.text, parts: leftRow.parts, label: '', kind: 'change' }
      } else {
        // 本侧缺失（对侧为新增）→ 绿色占位，首行带 +N 标签
        item.left = { text: '', parts: null, label: first ? `+${blockHeight}` : '', kind: 'placeholder' }
      }
      if (rightRow) {
        item.right = { text: rightRow.text, parts: rightRow.parts, label: '', kind: 'change' }
      } else {
        // 本侧缺失（对侧为删除）→ 红色占位，首行带 -N 标签
        item.right = { text: '', parts: null, label: first ? `-${blockHeight}` : '', kind: 'placeholder' }
      }
      result.push(item)
    }
  }
  return result
}

function buildFullUnifiedRows(rows) {
  const result = []
  for (const row of rows) {
    if (row.kind === 'context') {
      result.push({ ...row, parts: [{ text: row.text, changed: false }] })
      continue
    }
    const hasBothSides = !!row.left && !!row.right
    if (row.left) {
      result.push({
        ...row,
        kind: 'deleted',
        text: row.left.text,
        oldNumber: row.left.number,
        newNumber: 0,
        parts: row.leftParts,
        changeEnd: hasBothSides ? false : row.changeEnd,
      })
    }
    if (row.right) {
      result.push({
        ...row,
        kind: 'added',
        text: row.right.text,
        oldNumber: 0,
        newNumber: row.right.number,
        parts: row.rightParts,
        changeStart: hasBothSides ? false : row.changeStart,
      })
    }
  }
  return result
}
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="git-diff-backdrop" @pointerdown.self="emit('close')">
      <section class="git-diff-dialog" role="dialog" aria-modal="true" :aria-label="t.gitCompareTitle">
        <header class="git-diff-dialog__head">
          <span class="git-diff-dialog__icon">
            <ImageIcon v-if="detail?.kind === 'image'" :size="18" />
            <PackageOpen v-else-if="detail?.kind === 'binary'" :size="18" />
            <FileCode2 v-else :size="18" />
          </span>
          <span class="git-diff-dialog__title">
            <strong :title="activeFile?.path">{{ activeFile?.path }}</strong>
            <small>
              {{ activeScope === 'staged' ? t.gitStagedChanges : activeScope === 'unstaged' || activeScope === 'untracked' ? t.gitUnstagedChanges : activeScope === 'worktree' ? t.gitWorkspaceChanges : activeScope === 'commit' ? t.gitCommitChanges : activeScope === 'compare' ? t.gitManagerCompare : t.gitBranchChanges }}
              <template v-if="activeScope === 'branch' && baseBranch"> · {{ baseBranch }}</template>
              <template v-if="activeScope === 'compare' && compareLeft && compareRight"> · {{ compareLeft }} ↔ {{ compareRight }}</template>
              <template v-if="activeScope === 'commit' && commit"> · {{ commit.slice(0, 8) }}</template>
            </small>
          </span>
          <span v-if="detail" class="git-diff-dialog__counts">
            <strong class="change-count change-count--added">+{{ detail.added || 0 }}</strong>
            <strong class="change-count change-count--deleted">-{{ detail.deleted || 0 }}</strong>
          </span>
          <nav v-if="detail?.kind === 'text' && changeCount" class="git-diff-dialog__change-nav" :aria-label="t.gitDifferenceNavigation">
            <button type="button" :disabled="activeChangeIndex <= 0" :title="t.gitPreviousDifference" @click="navigateChange(-1)"><ArrowUp :size="15" /></button>
            <span>{{ activeChangeIndex + 1 }} / {{ changeCount }}</span>
            <button type="button" :disabled="activeChangeIndex >= changeCount - 1" :title="t.gitNextDifference" @click="navigateChange(1)"><ArrowDown :size="15" /></button>
          </nav>
          <div class="git-diff-dialog__layout" :aria-label="t.gitDiffLayout">
            <button type="button" :class="{ active: isHorizontal }" :title="t.gitDiffLayoutHorizontal" :aria-pressed="isHorizontal" @click="setLayout('horizontal')">
              <Columns2 :size="15" />
            </button>
            <button type="button" :class="{ active: !isHorizontal }" :title="t.gitDiffLayoutVertical" :aria-pressed="!isHorizontal" @click="setLayout('vertical')">
              <Rows3 :size="15" />
            </button>
          </div>
          <label class="git-diff-dialog__model" :title="enabledModels.length ? t.gitAnalysisModel : t.gitAnalysisModelUnavailable">
            <Brain :size="15" />
            <select v-model="analysisModel" :aria-label="t.gitAnalysisModel" :disabled="analysisLoading || !enabledModels.length" @change="cacheAnalysisModel">
              <option v-if="!enabledModels.length" value="">{{ t.gitAnalysisModelUnavailable }}</option>
              <optgroup v-for="group in analysisModelGroups" :key="group.provider" :label="group.provider">
                <option v-for="option in group.options" :key="option.value" :value="option.value" :disabled="option.disabled">
                  {{ option.model || option.label }}
                </option>
              </optgroup>
            </select>
          </label>
          <button class="git-diff-dialog__prompt" type="button" :title="t.gitEditPrompt" :aria-label="t.gitEditPrompt" :disabled="analysisLoading" @click="analysisPromptOpen = true">
            <Settings2 :size="15" />
          </button>
          <button
            class="git-diff-dialog__analyze"
            type="button"
            :title="canAnalyze ? t.gitAnalyzeChange : t.gitAnalysisRequiresText"
            :disabled="analysisLoading || !canAnalyze"
            @click="analyzeCurrentFile"
          >
            <LoaderCircle v-if="analysisLoading" class="spin" :size="15" />
            <Sparkles v-else :size="15" />
            <span>{{ analysisLoading ? t.gitAnalyzingChange : t.gitAnalyzeChange }}</span>
          </button>
          <nav class="git-diff-dialog__nav" :aria-label="t.gitChangeNavigation">
            <button type="button" :disabled="index <= 0" :title="t.gitPreviousChange" @click="navigate(-1)">
              <ArrowLeft :size="16" />
            </button>
            <span>{{ positionLabel }}</span>
            <button type="button" :disabled="index >= files.length - 1" :title="t.gitNextChange" @click="navigate(1)">
              <ArrowRight :size="16" />
            </button>
          </nav>
          <button class="git-diff-dialog__close" type="button" :title="t.close" @click="emit('close')">
            <X :size="18" />
          </button>
        </header>

        <div class="git-diff-dialog__content" :class="{ 'has-analysis': analysisVisible }">
          <main ref="diffBody" class="git-diff-dialog__body" :class="{ 'is-text-diff': detail?.kind === 'text', 'is-split-text': detail?.kind === 'text' && isHorizontal }" @mouseup="onDiffMouseUp">
            <div v-if="loading" class="git-diff-dialog__state">
              <LoaderCircle class="spin" :size="20" />
              <span>{{ t.gitLoadingCompare }}</span>
            </div>
            <div v-else-if="error" class="git-diff-dialog__state is-error">{{ error }}</div>

            <template v-else-if="detail">
              <div v-if="detail.kind === 'text'" class="git-text-diff">
                <div class="git-diff-version-bar">
                  <span>{{ t.gitBeforeChange }} · {{ detail.before.lineCount || 0 }} {{ t.gitLines }}</span>
                  <span v-if="isHorizontal" aria-hidden="true"></span>
                  <span>{{ t.gitAfterChange }} · {{ detail.after.lineCount || 0 }} {{ t.gitLines }}</span>
                </div>
                <div v-if="detail.hunks?.length" class="git-diff-viewport">
                  <div v-if="isHorizontal" class="git-split-compare">
                    <div
                      ref="splitOldPane"
                      class="git-split-pane is-old"
                      @scroll.passive="onSplitScroll('old')"
                      @wheel="onSplitOldWheel"
                    >
                      <div ref="splitOldContent" class="git-split-pane__content">
                        <div
                          v-for="(row, rowIndex) in splitSides"
                          :key="rowIndex"
                          class="git-split-row is-old-side"
                          :class="[splitRowClass(row), `is-${row.kind}`, { 'is-active-change': row.changeIndex === activeChangeIndex, 'is-change-start': row.changeStart, 'is-change-end': row.changeEnd }]"
                          :data-change-index="row.changeStart ? row.changeIndex : undefined"
                        >
                          <div class="git-split-cell is-old" :class="[`is-${row.left.kind}`]">
                            <code v-if="row.left.parts">
                              <span v-for="(part, partIndex) in row.left.parts" :key="partIndex" :class="{ 'is-inline-change': part.changed }">{{ part.text }}</span>
                            </code>
                            <span v-else-if="row.left.label" class="git-split-cell__label">{{ row.left.label }}</span>
                            <code v-else>{{ row.left.text || '\u00A0' }}</code>
                          </div>
                        </div>
                      </div>
                    </div>
                    <div ref="splitGutter" class="git-split-pane is-gutter-pane" aria-hidden="true">
                      <div class="git-split-pane__content">
                        <div
                          v-for="(row, rowIndex) in splitSides"
                          :key="rowIndex"
                          class="git-split-row is-gutter-side"
                          :class="[splitRowClass(row), `is-${row.kind}`, { 'is-active-change': row.changeIndex === activeChangeIndex, 'is-change-start': row.changeStart, 'is-change-end': row.changeEnd }]"
                        >
                          <div
                            class="git-split-cell is-gutter"
                            :class="[`is-${row.kind}`, { 'is-only-old': row.oldNumber && !row.newNumber, 'is-only-new': row.newNumber && !row.oldNumber }]"
                          >
                            <span class="git-split-gutter__old">{{ row.oldNumber || '' }}</span>
                            <span class="git-split-gutter__new">{{ row.newNumber || '' }}</span>
                          </div>
                        </div>
                      </div>
                    </div>
                    <div ref="splitNewPane" class="git-split-pane is-new" @scroll.passive="onSplitScroll('new')">
                      <div class="git-split-pane__content">
                        <div
                          v-for="(row, rowIndex) in splitSides"
                          :key="rowIndex"
                          class="git-split-row is-new-side"
                          :class="[splitRowClass(row), `is-${row.kind}`, { 'is-active-change': row.changeIndex === activeChangeIndex, 'is-change-start': row.changeStart, 'is-change-end': row.changeEnd }]"
                        >
                          <div class="git-split-cell is-new" :class="[`is-${row.right.kind}`]">
                            <code v-if="row.right.parts">
                              <span v-for="(part, partIndex) in row.right.parts" :key="partIndex" :class="{ 'is-inline-change': part.changed }">{{ part.text }}</span>
                            </code>
                            <span v-else-if="row.right.label" class="git-split-cell__label">{{ row.right.label }}</span>
                            <code v-else>{{ row.right.text || '\u00A0' }}</code>
                          </div>
                        </div>
                      </div>
                    </div>
                  </div>
                  <div v-else ref="unifiedPane" class="git-unified-pane">
                    <div
                      v-for="(row, rowIndex) in fullUnifiedRows"
                      :key="rowIndex"
                      class="git-diff-line"
                      :class="[lineClass(row.kind), { 'is-active-change': row.changeIndex === activeChangeIndex, 'is-change-start': row.changeStart, 'is-change-end': row.changeEnd }]"
                      :data-change-index="row.changeStart ? row.changeIndex : undefined"
                    >
                      <span class="git-diff-line__number">{{ row.oldNumber || '' }}</span>
                      <span class="git-diff-line__number">{{ row.newNumber || '' }}</span>
                      <span class="git-diff-line__sign">{{ lineSign(row.kind) }}</span>
                      <code><span v-for="(part, partIndex) in row.parts" :key="partIndex" :class="{ 'is-inline-change': part.changed }">{{ part.text }}</span></code>
                    </div>
                  </div>
                  <nav class="git-diff-overview" :aria-label="t.gitDifferenceNavigation">
                    <button
                      v-for="marker in overviewMarkers"
                      :key="marker.index"
                      type="button"
                      class="git-diff-overview__marker"
                      :class="[`is-${marker.kind}`, { 'is-active': marker.index === activeChangeIndex }]"
                      :style="marker.style"
                      :title="overviewMarkerLabel(marker)"
                      :aria-label="overviewMarkerLabel(marker)"
                      @click="activateOverviewMarker(marker.index)"
                    ></button>
                  </nav>
                </div>
                <div v-else class="git-diff-dialog__state">{{ t.gitNoDiffContent }}</div>
              </div>

              <div v-else-if="detail.kind === 'image'" class="git-visual-compare" :class="{ 'is-vertical': !isHorizontal }">
              <article v-for="side in ['before', 'after']" :key="side" class="git-version-card">
                <header>{{ versionTitle(side) }}</header>
                <div class="git-image-preview">
                  <img
                    v-if="detail[side].exists && detail[side].imageData"
                    :src="detail[side].imageData"
                    :alt="`${versionTitle(side)} ${detail.path}`"
                  />
                  <span v-else-if="!detail[side].exists">{{ t.gitFileDoesNotExist }}</span>
                  <span v-else>{{ t.gitImagePreviewUnavailable }}</span>
                </div>
                <dl class="git-file-metadata">
                  <div><dt>{{ t.gitResolution }}</dt><dd>{{ detail[side].width && detail[side].height ? `${detail[side].width} × ${detail[side].height}` : '—' }}</dd></div>
                  <div><dt>{{ t.gitFileSize }}</dt><dd>{{ formatSize(detail[side].size) }}</dd></div>
                  <div><dt>{{ t.gitMimeType }}</dt><dd>{{ detail[side].mimeType || detail.mimeType || '—' }}</dd></div>
                  <div><dt>{{ t.gitPermissions }}</dt><dd>{{ detail[side].permissions || '—' }}</dd></div>
                  <div><dt>{{ t.gitCreatedAt }}</dt><dd>{{ formatDate(detail[side].createdAt) }}</dd></div>
                  <div><dt>{{ t.gitModifiedAt }}</dt><dd>{{ formatDate(detail[side].modifiedAt) }}</dd></div>
                </dl>
              </article>
              </div>

              <div v-else class="git-binary-compare" :class="{ 'is-vertical': !isHorizontal }">
              <article v-for="side in ['before', 'after']" :key="side" class="git-version-card">
                <header>{{ versionTitle(side) }}</header>
                <div v-if="!detail[side].exists" class="git-version-card__missing">{{ t.gitFileDoesNotExist }}</div>
                <dl v-else class="git-file-metadata">
                  <div><dt>{{ t.gitFileSize }}</dt><dd>{{ formatSize(detail[side].size) }}</dd></div>
                  <div><dt>{{ t.gitMimeType }}</dt><dd>{{ detail[side].mimeType || detail.mimeType || '—' }}</dd></div>
                  <div><dt>{{ t.gitPermissions }}</dt><dd>{{ detail[side].permissions || '—' }}</dd></div>
                  <div><dt>{{ t.gitCreatedAt }}</dt><dd>{{ formatDate(detail[side].createdAt) }}</dd></div>
                  <div><dt>{{ t.gitModifiedAt }}</dt><dd>{{ formatDate(detail[side].modifiedAt) }}</dd></div>
                </dl>
              </article>
              </div>
            </template>
          </main>

          <aside v-if="analysisVisible" class="git-analysis" :aria-label="t.gitAnalysisTitle">
            <header>
              <span><Brain :size="16" />{{ t.gitAnalysisTitle }}</span>
              <button type="button" :title="t.close" @click="analysisVisible = false"><X :size="15" /></button>
            </header>
            <div v-if="analysisLoading" class="git-analysis__state">
              <LoaderCircle class="spin" :size="18" />{{ t.gitAnalyzingChange }}
            </div>
            <div v-else-if="analysisError" class="git-analysis__state is-error">{{ analysisError }}</div>
            <div v-else-if="analysis" class="git-analysis__result message-markdown" v-html="renderedAnalysis"></div>
          </aside>
        </div>

        <footer class="git-diff-dialog__foot">
          <span v-if="detail?.fromNodeSnapshot">{{ t.gitNodeSnapshotHint }}</span>
          <span v-else>{{ t.gitNavigationHint }}</span>
          <span v-if="detail?.oldPath">{{ detail.oldPath }} → {{ detail.path }}</span>
        </footer>
      </section>
    </div>
  </Teleport>
  <Teleport to="body">
    <div
      v-if="selectionMenu"
      ref="selectionMenuEl"
      class="git-diff-selection-menu"
      role="menu"
      :aria-label="t.gitAddToChat"
      :style="{ left: `${selectionMenu.left}px`, top: `${selectionMenu.top}px` }"
      @mousedown.stop
      @click.stop
    >
      <button type="button" role="menuitem" :title="t.gitAddToChat" @click="addSelectionToChat">
        <MessageSquarePlus :size="15" />
        <span>{{ t.gitAddToChat }}</span>
      </button>
    </div>
  </Teleport>
  <GitAIPromptDialog v-model="analysisPromptOpen" kind="file_analysis" :language="language" :t="t" />
</template>

<style scoped>
.git-diff-backdrop { position: fixed; z-index: 1300; inset: 0; display: grid; place-items: center; padding: 24px; background: rgb(8 9 9 / .5); backdrop-filter: blur(2px); }
.git-diff-selection-menu { position: fixed; z-index: 1500; min-width: 180px; padding: 5px; border: 1px solid var(--border); border-radius: 9px; color: var(--text); background: var(--surface); box-shadow: var(--shadow); }
.git-diff-selection-menu button { width: 100%; min-height: 32px; display: flex; align-items: center; gap: 9px; padding: 6px 8px; border: 0; border-radius: 6px; color: var(--muted); background: transparent; cursor: pointer; font: inherit; font-size: var(--fs-12); text-align: left; }
.git-diff-selection-menu button:hover, .git-diff-selection-menu button:focus-visible { color: var(--text); background: var(--hover); outline: none; }
.git-diff-selection-menu button svg { color: var(--faint); }
.git-diff-dialog { width: min(1180px, 96vw); height: min(820px, 92vh); min-height: 480px; display: flex; flex-direction: column; overflow: hidden; border: 1px solid var(--border); border-radius: 14px; background: var(--surface); box-shadow: 0 28px 90px rgb(0 0 0 / .3); }
.git-diff-dialog__head { flex: 0 0 auto; min-height: 62px; display: flex; align-items: center; gap: 10px; padding: 10px 14px; border-bottom: 1px solid var(--border); }
.git-diff-dialog__icon { flex: 0 0 auto; width: 34px; height: 34px; display: grid; place-items: center; border-radius: 9px; color: var(--accent); background: color-mix(in srgb, var(--accent) 12%, transparent); }
.git-diff-dialog__title { flex: 1 1 auto; min-width: 0; display: flex; flex-direction: column; gap: 2px; }
.git-diff-dialog__title strong { overflow: hidden; color: var(--text); font: var(--fs-13)/1.4 Consolas, "Cascadia Mono", monospace; text-overflow: ellipsis; white-space: nowrap; }
.git-diff-dialog__title small { color: var(--muted); font-size: var(--fs-12); }
.git-diff-dialog__counts { flex: 0 0 auto; display: flex; gap: 8px; }
.git-diff-dialog__change-nav { flex: 0 0 auto; height: 30px; display: flex; align-items: center; gap: 2px; padding: 2px; border: 1px solid var(--border); border-radius: 8px; background: var(--surface-2); }
.git-diff-dialog__change-nav button { width: 25px; height: 24px; display: grid; place-items: center; padding: 0; border: 0; border-radius: 6px; color: var(--muted); background: transparent; cursor: pointer; }
.git-diff-dialog__change-nav button:hover:not(:disabled) { color: var(--accent); background: var(--surface); }
.git-diff-dialog__change-nav button:disabled { opacity: .32; cursor: default; }
.git-diff-dialog__change-nav span { min-width: 38px; color: var(--muted); font-size: var(--fs-11); text-align: center; font-variant-numeric: tabular-nums; }
.git-diff-dialog__layout { flex: 0 0 auto; display: flex; padding: 2px; border: 1px solid var(--border); border-radius: 8px; background: var(--surface-2); }
.git-diff-dialog__layout button { width: 28px; height: 26px; display: grid; place-items: center; padding: 0; border: 0; border-radius: 6px; color: var(--faint); background: transparent; cursor: pointer; }
.git-diff-dialog__layout button:hover { color: var(--text); }
.git-diff-dialog__layout button.active { color: var(--accent); background: var(--surface); box-shadow: 0 1px 3px rgb(0 0 0 / .12); }
.git-diff-dialog__model { flex: 0 1 190px; min-width: 110px; height: 32px; display: flex; align-items: center; gap: 6px; padding: 0 7px; border: 1px solid var(--border); border-radius: 8px; color: var(--muted); background: var(--surface); }
.git-diff-dialog__model select { min-width: 0; width: 100%; border: 0; outline: 0; color: var(--text); background: transparent; font-size: var(--fs-12); cursor: pointer; }
.git-diff-dialog__model:has(select:disabled) { opacity: .55; }
.git-diff-dialog__analyze { flex: 0 0 auto; height: 32px; display: flex; align-items: center; gap: 6px; padding: 0 10px; border: 1px solid color-mix(in srgb, var(--accent) 45%, var(--border)); border-radius: 8px; color: var(--accent); background: color-mix(in srgb, var(--accent) 8%, var(--surface)); font-size: var(--fs-12); cursor: pointer; }
.git-diff-dialog__analyze:hover:not(:disabled) { background: color-mix(in srgb, var(--accent) 14%, var(--surface)); }
.git-diff-dialog__analyze:disabled { opacity: .45; cursor: default; }
.git-diff-dialog__prompt { flex: 0 0 auto; width: 30px; height: 30px; display: grid; place-items: center; padding: 0; border: 1px solid var(--border); border-radius: 8px; color: var(--muted); background: var(--surface); cursor: pointer; }
.git-diff-dialog__prompt:hover:not(:disabled) { color: var(--text); background: var(--hover); }
.git-diff-dialog__prompt:disabled { opacity: .45; cursor: default; }
.git-diff-dialog__nav { flex: 0 0 auto; display: flex; align-items: center; gap: 4px; }
.git-diff-dialog__nav button,.git-diff-dialog__close { width: 32px; height: 32px; display: grid; place-items: center; border: 0; border-radius: 8px; color: var(--muted); background: transparent; cursor: pointer; }
.git-diff-dialog__nav button:hover:not(:disabled),.git-diff-dialog__close:hover { color: var(--text); background: var(--hover); }
.git-diff-dialog__nav button:disabled { opacity: .35; cursor: default; }
.git-diff-dialog__nav span { min-width: 52px; color: var(--muted); font-size: var(--fs-12); text-align: center; font-variant-numeric: tabular-nums; }
.git-diff-dialog__content { flex: 1 1 auto; min-height: 0; display: flex; }
.git-diff-dialog__body { flex: 1 1 auto; min-width: 0; min-height: 0; overflow: auto; background: color-mix(in srgb, var(--surface-2) 52%, var(--surface)); }
.git-diff-dialog__body.is-text-diff { overflow: hidden; }
.git-text-diff { --split-row-height: 20px; --split-gutter-width: 104px; min-height: 100%; }
.git-diff-dialog__body.is-text-diff .git-text-diff { height: 100%; min-height: 0; display: flex; flex-direction: column; overflow: hidden; }
.git-diff-dialog__state { min-height: 180px; display: flex; align-items: center; justify-content: center; gap: 8px; color: var(--muted); font-size: var(--fs-13); }
.git-diff-dialog__state.is-error { color: var(--danger); }
.git-diff-dialog__foot { flex: 0 0 auto; min-height: 38px; display: flex; justify-content: space-between; gap: 16px; padding: 9px 14px; border-top: 1px solid var(--border); color: var(--faint); background: var(--surface); font-size: var(--fs-12); }
.git-diff-version-bar { position: sticky; top: 0; z-index: 1; display: grid; grid-template-columns: 1fr 1fr; padding: 8px 16px; border-bottom: 1px solid var(--border); color: var(--muted); background: var(--surface); font-size: var(--fs-12); }
/* 分栏模式下表头与三栏网格对齐：旧版本 | gutter 占位 | 新版本 */
.git-diff-dialog__body.is-split-text .git-diff-version-bar { grid-template-columns: minmax(0, 1fr) var(--split-gutter-width) minmax(0, 1fr); }
.git-diff-version-bar span:last-child { text-align: right; }
.git-diff-hunk + .git-diff-hunk { border-top: 1px solid var(--border); }
.git-diff-hunk__head { padding: 7px 14px; color: #667ab0; background: rgb(80 110 175 / .09); font: var(--fs-12)/1.5 Consolas, "Cascadia Mono", monospace; }
.git-diff-viewport { position: relative; min-width: 0; min-height: 0; flex: 1 1 auto; overflow: hidden; }
.git-unified-pane { width: 100%; height: 100%; overflow: auto; overscroll-behavior: contain; padding-right: 15px; }
.git-diff-overview { position: absolute; z-index: 6; top: 4px; right: 7px; bottom: 12px; width: 8px; border-left: 1px solid color-mix(in srgb, var(--border) 55%, transparent); background: color-mix(in srgb, var(--surface) 82%, transparent); pointer-events: none; }
.git-diff-overview__marker { position: absolute; left: 1px; width: 7px; min-height: 2px; padding: 0; border: 0; border-radius: 1px; cursor: pointer; opacity: .72; pointer-events: auto; transition: opacity .12s ease, transform .12s ease; }
.git-diff-overview__marker:hover,.git-diff-overview__marker:focus-visible,.git-diff-overview__marker.is-active { z-index: 1; opacity: 1; transform: scaleX(1.35); outline: none; }
.git-diff-overview__marker.is-added { background: #22a05a; }
.git-diff-overview__marker.is-modified { background: #4b8fe2; }
.git-diff-overview__marker.is-deleted { background: #dc5a52; }
/* --- JetBrains 式三栏：旧代码 | 行号 gutter | 新代码 ---
   总宽度锁定为弹窗宽度（grid 用 minmax(0,1fr)），左右两栏各自横向滚动，
   中间 gutter 列固定不参与横向滚动；三栏垂直滚动由脚本同步。 */
.git-split-compare {
  width: 100%;
  height: 100%;
  flex: 1 1 auto;
  min-height: 0;
  display: grid;
  grid-template-columns: minmax(0, 1fr) var(--split-gutter-width) minmax(0, 1fr);
  /* 行高必须钉死为容器高度：否则内容超出时 auto 行会被撑高，
     各栏不再内部滚动，而是整个网格被 overflow:hidden 裁掉。 */
  grid-template-rows: minmax(0, 1fr);
  overflow: hidden;
  font-family: Consolas, "Cascadia Mono", monospace;
  font-size: var(--fs-12);
}
.git-split-pane { min-width: 0; min-height: 0; overflow: auto; scrollbar-gutter: auto; overscroll-behavior: contain; }
/* 中间 gutter 列：跟随垂直同步但自身不可滚动、无滚动条 */
.git-split-pane.is-gutter-pane { overflow: hidden; border-inline: 1px solid var(--border-soft); background: var(--surface); }
/* 竖向滚动条全局只有一条：保留在右侧新代码栏（对应弹窗最右侧）。

   左栏用 overflow-y: hidden 直接不渲染竖滚动条，不依赖 ::-webkit-scrollbar
   （本组件样式为 scoped，且 base.css 有全局滚动条样式，伪元素方案实测未能生效），
   也无需额外的裁剪容器。overflow-y: hidden 的元素仍是滚动容器，
   scrollTop 可编程设置，因此三栏垂直同步照常工作；横向滚动条独立保留。
   左栏不响应滚轮的问题由 onSplitOldWheel 转发到右栏解决。 */
.git-split-pane.is-old { overflow-x: auto; overflow-y: hidden; }
/* 每栏内容按最长行撑开，超出部分由所在 pane 的横向滚动条承担 */
.git-split-pane__content { position: relative; min-width: max-content; }
/* 三栏行高必须完全一致，否则垂直同步后会错位 */
.git-split-row { position: relative; height: var(--split-row-height); line-height: var(--split-row-height); }
.git-split-cell { position: relative; height: 100%; min-width: 0; }
.git-split-cell code { display: block; height: 100%; padding: 0 12px; color: var(--text); white-space: pre; }
.git-split-cell.is-old code { padding-left: 14px; }
.git-split-cell.is-new code { padding-right: 14px; }
.git-split-cell__label { display: block; padding: 0 14px; color: var(--faint); font-weight: 600; font-size: var(--fs-11); user-select: none; }

/* 上下文行：gutter 与代码区同为中性底，形成安静的阅读区 */
.git-split-cell.is-context { background: var(--diff-context-bg); }
.git-split-cell.is-gutter.is-context { background: rgb(120 120 115 / .09); }

/* 修改行：左删除色 → gutter 渐变 → 右新增色，色带跨三栏连续 */
.git-split-row.is-modified .git-split-cell.is-old.is-change { background: var(--diff-del-bg); box-shadow: inset 3px 0 0 var(--diff-del-fg); }
.git-split-row.is-modified .git-split-cell.is-new.is-change { background: var(--diff-add-bg); box-shadow: inset -3px 0 0 var(--diff-add-fg); }
.git-split-row.is-modified .git-split-cell.is-gutter { background-image: linear-gradient(90deg, var(--diff-del-gutter) 0 50%, var(--diff-add-gutter) 50% 100%); }

/* 纯新增行：整行（左占位 + gutter + 右代码）铺满淡绿色，从左连到右 */
.git-split-row.is-added-row .git-split-cell.is-placeholder { background: var(--diff-add-bg); background-image: none; box-shadow: none; }
.git-split-row.is-added-row .git-split-cell.is-gutter { background: var(--diff-add-gutter); background-image: none; }
.git-split-row.is-added-row .git-split-cell.is-new.is-change { background: var(--diff-add-bg); box-shadow: inset -3px 0 0 var(--diff-add-fg); }

/* 纯删除行：整行（左代码 + gutter + 右占位）铺满淡红色，从右连到左 */
.git-split-row.is-deleted-row .git-split-cell.is-placeholder { background: var(--diff-del-bg); background-image: none; box-shadow: none; }
.git-split-row.is-deleted-row .git-split-cell.is-gutter { background: var(--diff-del-gutter); background-image: none; }
.git-split-row.is-deleted-row .git-split-cell.is-old.is-change { background: var(--diff-del-bg); box-shadow: inset 3px 0 0 var(--diff-del-fg); }

/* 占位格不增加左右边框，避免把变更块框成黑边 */
.git-split-cell.is-old.is-placeholder,
.git-split-cell.is-new.is-placeholder { border: 0; }
/* 失衡计数标签颜色跟随新增/删除色 */
.git-split-row.is-added-row .git-split-cell__label { color: var(--diff-add-fg); }
.git-split-row.is-deleted-row .git-split-cell__label { color: var(--diff-del-fg); }

/* gutter 双行号：旧号右对齐、新号左对齐，缺失时留空保持列对齐 */
.git-split-cell.is-gutter { display: grid; grid-template-columns: 1fr 1fr; color: var(--faint); user-select: none; font-variant-numeric: tabular-nums; }
.git-split-gutter__old { padding-right: 8px; overflow: hidden; text-align: right; }
.git-split-gutter__new { padding-left: 8px; overflow: hidden; text-align: left; }
.git-split-row.is-modified .git-split-cell.is-gutter .git-split-gutter__old { color: var(--diff-del-fg); }
.git-split-row.is-modified .git-split-cell.is-gutter .git-split-gutter__new { color: var(--diff-add-fg); }
.git-split-row.is-added-row .git-split-cell.is-gutter .git-split-gutter__old,
.git-split-row.is-added-row .git-split-cell.is-gutter .git-split-gutter__new { color: var(--diff-add-fg); }
.git-split-row.is-deleted-row .git-split-cell.is-gutter .git-split-gutter__old,
.git-split-row.is-deleted-row .git-split-cell.is-gutter .git-split-gutter__new { color: var(--diff-del-fg); }

/* 行内差异高亮 */
.git-split-cell.is-old .is-inline-change { border-radius: 2px; background: color-mix(in srgb, var(--diff-del-fg) 30%, transparent); }
.git-split-cell.is-new .is-inline-change { border-radius: 2px; background: color-mix(in srgb, var(--diff-add-fg) 30%, transparent); }

/* 当前改动块：不用边框，改用 inset 阴影做极淡的强调色叠加，保持 JetBrains 式清爽。
   必须与左右两侧的 3px 强调条 box-shadow 共存，所以写成多 shadow 列表。 */
.git-split-row.is-active-change .git-split-cell.is-old.is-change { box-shadow: inset 3px 0 0 var(--diff-del-fg), inset 0 0 0 9999px color-mix(in srgb, var(--accent) 8%, transparent); }
.git-split-row.is-active-change .git-split-cell.is-new.is-change { box-shadow: inset -3px 0 0 var(--diff-add-fg), inset 0 0 0 9999px color-mix(in srgb, var(--accent) 8%, transparent); }
.git-split-row.is-active-change .git-split-cell.is-context,
.git-split-row.is-active-change .git-split-cell.is-placeholder,
.git-split-row.is-active-change .git-split-cell.is-gutter { box-shadow: inset 0 0 0 9999px color-mix(in srgb, var(--accent) 8%, transparent); }


.git-diff-line { position: relative; min-width: max-content; display: grid; grid-template-columns: 48px 48px 18px minmax(580px, 1fr); font: var(--fs-12)/1.62 Consolas, "Cascadia Mono", monospace; }
.git-diff-line.is-added { background: var(--diff-add-bg); box-shadow: inset 3px 0 0 var(--diff-add-fg); }
.git-diff-line.is-deleted { background: var(--diff-del-bg); box-shadow: inset 3px 0 0 var(--diff-del-fg); }
.git-diff-line__number { padding: 1px 8px; color: var(--faint); background: rgb(120 120 115 / .05); text-align: right; user-select: none; font-variant-numeric: tabular-nums; }
.git-diff-line__sign { padding-top: 1px; color: var(--faint); text-align: center; user-select: none; }
.git-diff-line.is-added .git-diff-line__sign { color: #299764; }
.git-diff-line.is-deleted .git-diff-line__sign { color: #d14b42; }
.git-diff-line code { display: block; padding: 1px 16px 1px 4px; color: var(--text); white-space: pre; }
.git-diff-line.is-added .is-inline-change { border-radius: 2px; background: color-mix(in srgb, var(--diff-add-fg) 28%, transparent); }
.git-diff-line.is-deleted .is-inline-change { border-radius: 2px; background: color-mix(in srgb, var(--diff-del-fg) 28%, transparent); }
/* 纵向模式当前改动块同样改用 inset 阴影叠加，不画黑框 */
.git-diff-line.is-active-change.is-added { box-shadow: inset 3px 0 0 var(--diff-add-fg), inset 0 0 0 9999px color-mix(in srgb, var(--accent) 8%, transparent); }
.git-diff-line.is-active-change.is-deleted { box-shadow: inset 3px 0 0 var(--diff-del-fg), inset 0 0 0 9999px color-mix(in srgb, var(--accent) 8%, transparent); }
.git-diff-line.is-active-change.is-context { box-shadow: inset 0 0 0 9999px color-mix(in srgb, var(--accent) 8%, transparent); }
.git-visual-compare,.git-binary-compare { min-height: 100%; display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 14px; padding: 16px; }
.git-visual-compare.is-vertical,.git-binary-compare.is-vertical { grid-template-columns: 1fr; }
.git-analysis { flex: 0 0 min(360px, 34vw); min-width: 280px; display: flex; flex-direction: column; border-left: 1px solid var(--border); background: var(--surface); }
.git-analysis > header { flex: 0 0 auto; min-height: 42px; display: flex; align-items: center; justify-content: space-between; gap: 8px; padding: 7px 10px 7px 12px; border-bottom: 1px solid var(--border-soft); }
.git-analysis > header span { display: flex; align-items: center; gap: 7px; color: var(--text); font-size: var(--fs-13); font-weight: 600; }
.git-analysis > header button { width: 28px; height: 28px; display: grid; place-items: center; padding: 0; border: 0; border-radius: 7px; color: var(--muted); background: transparent; cursor: pointer; }
.git-analysis > header button:hover { color: var(--text); background: var(--hover); }
.git-analysis__state { flex: 1 1 auto; min-height: 160px; display: flex; align-items: center; justify-content: center; gap: 8px; padding: 20px; color: var(--muted); font-size: var(--fs-13); text-align: center; }
.git-analysis__state.is-error { color: var(--danger); }
.git-analysis__result { flex: 1 1 auto; min-height: 0; overflow: auto; padding: 14px 16px 24px; color: var(--text); font-size: var(--fs-13); line-height: 1.65; }
.git-analysis__result :deep(h1),.git-analysis__result :deep(h2),.git-analysis__result :deep(h3) { margin: 14px 0 7px; font-size: var(--fs-14); }
.git-analysis__result :deep(h1:first-child),.git-analysis__result :deep(h2:first-child),.git-analysis__result :deep(h3:first-child) { margin-top: 0; }
.git-analysis__result :deep(p),.git-analysis__result :deep(ul),.git-analysis__result :deep(ol) { margin: 7px 0; }
.git-analysis__result :deep(code) { padding: 1px 4px; border-radius: 4px; background: var(--code-bg); font-family: Consolas, "Cascadia Mono", monospace; }
.git-version-card { min-width: 0; overflow: hidden; border: 1px solid var(--border); border-radius: 11px; background: var(--surface); }
.git-version-card > header { padding: 10px 12px; border-bottom: 1px solid var(--border-soft); color: var(--text); font-size: var(--fs-13); font-weight: 600; }
.git-image-preview { min-height: 300px; display: grid; place-items: center; padding: 14px; color: var(--faint); background-image: linear-gradient(45deg, var(--surface-2) 25%, transparent 25%),linear-gradient(-45deg, var(--surface-2) 25%, transparent 25%),linear-gradient(45deg, transparent 75%, var(--surface-2) 75%),linear-gradient(-45deg, transparent 75%, var(--surface-2) 75%); background-position: 0 0,0 8px,8px -8px,-8px 0; background-size: 16px 16px; font-size: var(--fs-12); }
.git-image-preview img { max-width: 100%; max-height: 460px; object-fit: contain; }
.git-file-metadata { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0; margin: 0; }
.git-file-metadata > div { min-width: 0; padding: 10px 12px; border-top: 1px solid var(--border-soft); }
.git-file-metadata > div:nth-child(odd) { border-right: 1px solid var(--border-soft); }
.git-file-metadata dt { color: var(--faint); font-size: var(--fs-12); }
.git-file-metadata dd { margin: 4px 0 0; overflow-wrap: anywhere; color: var(--text); font: var(--fs-12)/1.45 Consolas, "Cascadia Mono", monospace; }
.git-version-card__missing { min-height: 220px; display: grid; place-items: center; color: var(--faint); font-size: var(--fs-12); }
@media (max-width: 760px) {
  .git-diff-backdrop { padding: 0; }
  .git-diff-dialog { width: 100vw; height: 100vh; max-height: none; border: 0; border-radius: 0; }
  .git-diff-dialog__counts { display: none; }
  .git-diff-dialog__change-nav span { display: none; }
  .git-diff-dialog__model { flex-basis: 120px; }
  .git-diff-dialog__analyze span { display: none; }
  .git-diff-dialog__content.has-analysis { flex-direction: column; }
  .git-analysis { flex: 0 0 42%; min-width: 0; border-top: 1px solid var(--border); border-left: 0; }
  .git-visual-compare,.git-binary-compare { grid-template-columns: 1fr; }
}
</style>
