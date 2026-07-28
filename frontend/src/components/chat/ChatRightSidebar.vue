<script setup>
import { computed, nextTick, ref, watch } from 'vue'
import {
  ChevronRight, Download, FileCode2, FilePlus2, FileText, FileX2,
  GitBranch, Image as ImageIcon, LoaderCircle, RefreshCw, X
} from 'lucide-vue-next'
import { getSessionGitSnapshot, openSessionArtifact } from '../../backend.js'
import GitDiffDialog from './GitDiffDialog.vue'

const props = defineProps({
  open: { type: Boolean, default: false },
  sessionId: { type: Number, required: true },
  changes: { type: Object, default: () => ({ root: '', nodes: [], files: [], added: 0, deleted: 0 }) },
  loading: { type: Boolean, default: false },
  focusRequest: { type: Object, default: null },
  t: { type: Object, required: true }
})

const emit = defineEmits(['close', 'refresh', 'artifact-error'])
const tabs = computed(() => [
  { id: 'artifacts', label: props.t.changesTabArtifacts },
  { id: 'git', label: props.t.changesTabGit },
  { id: 'web', label: props.t.changesTabWeb }
])
const activeTab = ref('artifacts')
const width = ref(390)
const resizing = ref(false)
const SIDEBAR_MIN = 300
const SIDEBAR_MAX = 760
// 下钻视图：list=时间线列表；node=节点详情（文件 diff 改为就地展开，不再跳转独立页面）
const view = ref({ type: 'list' })
const changeNodes = computed(() => props.changes?.nodes || [])
const expandedNodes = ref(new Set())
const expandedFiles = ref(new Set())
// 节点按 startedAt 升序（时间线从早到晚）
const timelineNodes = computed(() =>
  [...changeNodes.value].sort((a, b) => (Number(a.startedAt) || 0) - (Number(b.startedAt) || 0))
)
const changedFileCount = computed(() => new Set(changeNodes.value.flatMap(node => (node.files || []).map(file => file.path))).size)
const browserArtifactCount = computed(() => changeNodes.value.reduce((sum, node) => sum + (node.browserArtifacts?.length || 0), 0))
const inputArtifactCount = computed(() => changeNodes.value.reduce((sum, node) => sum + (node.inputArtifacts?.length || 0), 0))
const outputArtifactCount = computed(() => changeNodes.value.reduce((sum, node) => sum + (node.outputArtifacts?.length || 0), 0))
const gitOverride = ref(null)
const gitLoading = ref(false)
const gitLoaded = ref(false)
const selectedBase = ref('')
const gitDialog = ref({ open: false, scope: 'worktree', files: [], index: 0 })
let gitRequestNonce = 0
const gitSnapshot = computed(() => (
  gitOverride.value || {
    isRepository: false,
    baseBranches: [],
    worktree: { files: [], added: 0, deleted: 0 },
    branch: { files: [], added: 0, deleted: 0 }
  }
))
const gitWorktree = computed(() => gitSnapshot.value.worktree || { files: [], added: 0, deleted: 0 })
const gitBranch = computed(() => gitSnapshot.value.branch || { files: [], added: 0, deleted: 0 })

watch(
  () => props.sessionId,
  () => {
    gitRequestNonce++
    gitOverride.value = null
    gitLoading.value = false
    gitLoaded.value = false
    selectedBase.value = ''
    gitDialog.value = { open: false, scope: 'worktree', files: [], index: 0 }
    if (props.open && activeTab.value === 'git') void refreshGitSnapshot()
  }
)

watch(
  activeTab,
  tab => {
    if (props.open && tab === 'git') void refreshGitSnapshot()
  }
)

watch(
  () => props.open,
  open => {
    if (open && activeTab.value === 'git') void refreshGitSnapshot()
  }
)

// 展开状态独立于后台刷新：节点持续新增文件时，保留用户正在查看的节点和 diff。
// 切换会话或文件消失时，仅清理已经不在当前数据中的展开项。
watch(changeNodes, (nodes) => {
  const nodeIds = new Set(nodes.map(node => node.id))
  expandedNodes.value = new Set([...expandedNodes.value].filter(id => nodeIds.has(id)))

  const currentFiles = new Set(nodes.flatMap(node =>
    (node.files || []).map(file => fileKey(node.id, file.path, file.source))
  ))
  expandedFiles.value = new Set([...expandedFiles.value].filter(key => currentFiles.has(key)))
})

async function focusChangedFile() {
  const request = props.focusRequest
  if (!request?.path && !request?.nodeId) return
  activeTab.value = 'artifacts'
  view.value = { type: 'list' }
  if (request.nodeId) {
    const node = changeNodes.value.find(item => item.id === request.nodeId)
    const file = node?.files?.find(item => (
      item.path === request.path &&
      (!request.source?.runId || item.source?.runId === request.source.runId)
    ))
    expandedNodes.value = new Set([...expandedNodes.value, request.nodeId])
    if (request.path) {
      expandedFiles.value = new Set([
        ...expandedFiles.value,
        fileKey(request.nodeId, request.path, file?.source)
      ])
    }
  }
  await nextTick()
  if (request.path) {
    const row = [...document.querySelectorAll('.timeline__file')].find(element =>
      element.dataset.nodeId === String(request.nodeId || '') &&
      element.dataset.filePath === String(request.path)
    )
    row?.scrollIntoView({ behavior: 'smooth', block: 'center' })
    row?.focus({ preventScroll: true })
  } else {
    // 仅定位节点（如 document download_list 完成）：展开节点并滚动到节点头部。
    const article = [...document.querySelectorAll('.timeline__node')].find(element =>
      element.dataset.nodeId === String(request.nodeId)
    )
    article?.scrollIntoView({ behavior: 'smooth', block: 'start' })
  }
}

watch(
  [
    () => props.focusRequest?.nonce,
    () => changeNodes.value.map(node =>
      `${node.id}:${(node.files || []).map(file => file.path).join(',')}` +
      `:${(node.outputArtifacts || []).map(item => item.name).join(',')}`
    ).join('|')
  ],
  focusChangedFile
)

function formatText(template, values) {
  return Object.entries(values).reduce(
    (result, [key, value]) => result.replace(`{${key}}`, value),
    String(template || '')
  )
}

function sortedFiles(node) {
  return [...(node.files || [])].sort((a, b) => (
    String(a.path).localeCompare(String(b.path)) || sourceKey(a.source).localeCompare(sourceKey(b.source))
  ))
}

function nodeHasFileChanges(node) {
  return Boolean(node.files && node.files.length)
}

function nodeHasContent(node) {
  return nodeHasFileChanges(node) || Boolean(node.browserArtifacts?.length) ||
    Boolean(node.inputArtifacts?.length) || Boolean(node.documentArtifacts?.length) ||
    Boolean(node.outputArtifacts?.length) || Boolean(node.subagentArtifacts?.length)
}

function nodeTotals(node) {
  let added = 0
  let deleted = 0
  for (const file of node.files || []) {
    if (!file.binary) {
      added += Number(file.added) || 0
      deleted += Number(file.deleted) || 0
    }
  }
  return { added, deleted }
}

function nodeStatusLabel(node) {
  return node.status === 'running' ? props.t.changesNodeRunning : props.t.changesNodeDone
}

// 点击节点：就地展开/收起其文件列表，不跳转到独立节点页
function isNodeOpen(id) {
  return expandedNodes.value.has(id)
}
function toggleNode(id) {
  const next = new Set(expandedNodes.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  expandedNodes.value = next
}

// 点击文件：就地展开/收起 diff，不跳转到独立文件页
function sourceKey(source) {
  return source ? `${source.agentKey || ''}:${source.runId || ''}:${source.nodeId || ''}` : 'main'
}
function fileKey(nodeId, path, source) {
  return `${nodeId}::${sourceKey(source)}::${path}`
}
function isFileOpen(nodeId, path, source) {
  return expandedFiles.value.has(fileKey(nodeId, path, source))
}
function toggleFile(nodeId, path, source) {
  const key = fileKey(nodeId, path, source)
  const next = new Set(expandedFiles.value)
  if (next.has(key)) next.delete(key)
  else next.add(key)
  expandedFiles.value = next
}

function filename(path) {
  return String(path).split('/').pop() || path
}

function dirname(path) {
  const parts = String(path).split('/')
  parts.pop()
  return parts.length ? `${parts.join('/')}/` : ''
}

function concatPath(path) {
  const name = filename(path)
  const dir = dirname(path)
  return { name, dir }
}

function statusLabel(status) {
  return status === 'added' ? 'A' : status === 'deleted' ? 'D' : 'M'
}

function gitStatusLabel(status) {
  if (status === 'added') return 'A'
  if (status === 'deleted') return 'D'
  if (status === 'renamed') return 'R'
  if (status === 'untracked') return 'U'
  return 'M'
}

function gitFileState(file) {
  const states = []
  if (file.untracked) states.push(props.t.gitUntracked)
  else {
    if (file.staged) states.push(props.t.gitStaged)
    if (file.unstaged) states.push(props.t.gitUnstaged)
  }
  return states.join(' · ')
}

async function refreshGitSnapshot(baseBranch = selectedBase.value) {
  const sessionId = props.sessionId
  const requestNonce = ++gitRequestNonce
  gitLoading.value = true
  try {
    const snapshot = await getSessionGitSnapshot(sessionId, baseBranch || '')
    if (requestNonce !== gitRequestNonce || sessionId !== props.sessionId) return
    gitOverride.value = snapshot
    gitLoaded.value = true
    selectedBase.value = snapshot?.baseBranch || ''
    if (gitDialog.value.open && gitDialog.value.scope === 'branch') {
      gitDialog.value = { ...gitDialog.value, files: snapshot?.branch?.files || [], index: 0 }
    }
  } catch (error) {
    if (requestNonce === gitRequestNonce && sessionId === props.sessionId) {
      gitLoaded.value = true
      emit('artifact-error', String(error))
    }
  } finally {
    if (requestNonce === gitRequestNonce) gitLoading.value = false
  }
}

function selectBaseBranch(event) {
  selectedBase.value = event.target.value
  refreshGitSnapshot(selectedBase.value)
}

function openGitDiff(scope, files, index) {
  gitDialog.value = { open: true, scope, files, index }
}

function fileIcon(status) {
  return status === 'added' ? FilePlus2 : status === 'deleted' ? FileX2 : FileCode2
}

function artifactIcon(kind) {
  if (kind === 'image') return ImageIcon
  if (kind === 'pdf' || kind === 'markdown' || kind === 'document') return FileText
  return Download
}

function sourceLabel(node, source) {
  if (!source) return ''
  const run = (node.subagentRuns || []).find(item => item.runId === source.runId)
  return `${run?.agentName || source.agentKey || props.t.subagentUnknown} · ${source.runId || ''}`
}

function artifactKindLabel(kind) {
  if (kind === 'image') return props.t.browserArtifactImage
  if (kind === 'document') return props.t.inputArtifactDocument
  if (kind === 'audio') return props.t.inputArtifactAudio
  if (kind === 'video') return props.t.inputArtifactVideo
  if (kind === 'pdf') return props.t.browserArtifactPdf
  if (kind === 'markdown') return props.t.browserArtifactMarkdown
  if (kind === 'download') return props.t.browserArtifactDownload
  return props.t.browserArtifactOther
}

async function openArtifact(artifact) {
  try {
    await openSessionArtifact(artifact.absPath)
  } catch (error) {
    emit('artifact-error', String(error))
  }
}

function formatSize(bytes) {
  const value = Number(bytes) || 0
  if (value < 1024) return `${value} B`
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KB`
  return `${(value / (1024 * 1024)).toFixed(1)} MB`
}

function nodeTime(node) {
  const value = Number(node.startedAt)
  if (!value) return ''
  return new Date(value).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

function lineClass(kind) {
  return kind === 'added' ? 'is-added' : kind === 'deleted' ? 'is-deleted' : 'is-context'
}

function lineSign(kind) {
  return kind === 'added' ? '+' : kind === 'deleted' ? '-' : ' '
}

function startResize(event) {
  event.preventDefault()
  resizing.value = true
  const startX = event.clientX
  const startWidth = width.value
  const onMove = (moveEvent) => {
    width.value = Math.min(SIDEBAR_MAX, Math.max(SIDEBAR_MIN, startWidth + (startX - moveEvent.clientX)))
  }
  const onUp = () => {
    document.removeEventListener('pointermove', onMove)
    document.removeEventListener('pointerup', onUp)
    document.body.style.cursor = ''
    document.body.style.userSelect = ''
    resizing.value = false
  }
  document.addEventListener('pointermove', onMove)
  document.addEventListener('pointerup', onUp)
  document.body.style.cursor = 'col-resize'
  document.body.style.userSelect = 'none'
}
</script>

<template>
  <aside
    class="chat-right-side"
    :class="{ 'is-open': open, 'is-resizing': resizing }"
    :style="open ? { width: `${width}px`, flexBasis: `${width}px` } : null"
  >
    <div class="chat-right-side__handle" @pointerdown="startResize"></div>
    <div class="chat-right-side__head">
      <span class="chat-right-side__title"><GitBranch :size="15" />{{ t.changesTitle }}</span>
      <button class="topbar-btn" type="button" :title="t.rightSidebarClose" :aria-label="t.rightSidebarClose" @click="emit('close')">
        <X :size="16" />
      </button>
    </div>
    <div class="chat-right-side__tabs">
      <button
        v-for="tab in tabs"
        :key="tab.id"
        type="button"
        class="chat-right-side__tab"
        :class="{ 'is-active': activeTab === tab.id }"
        @click="activeTab = tab.id"
      >{{ tab.label }}</button>
    </div>
    <div class="chat-right-side__body">
      <template v-if="activeTab === 'artifacts'">
        <section class="change-overview">
          <div class="change-overview__summary">
            <strong>{{ timelineNodes.length }}</strong>
            <span>{{ t.changesNodeCount }}</span>
            <span class="change-overview__separator">·</span>
            <small>{{ formatText(t.changeFileCount, { count: changedFileCount }) }}</small>
            <small v-if="browserArtifactCount">· {{ formatText(t.browserArtifactCount, { count: browserArtifactCount }) }}</small>
            <small v-if="inputArtifactCount">· {{ formatText(t.inputArtifactCount, { count: inputArtifactCount }) }}</small>
            <small v-if="outputArtifactCount">· {{ formatText(t.outputArtifactCount, { count: outputArtifactCount }) }}</small>
          </div>
          <div class="change-overview__counts">
            <strong class="change-count change-count--added">+{{ changes.added || 0 }}</strong>
            <strong class="change-count change-count--deleted">-{{ changes.deleted || 0 }}</strong>
            <button type="button" :title="t.refreshChanges" :aria-label="t.refreshChanges" :disabled="loading" @click="emit('refresh')">
              <LoaderCircle v-if="loading" class="spin" :size="14" />
              <RefreshCw v-else :size="14" />
            </button>
          </div>
        </section>
        <p v-if="changes.root" class="change-root" :title="changes.root">{{ changes.root }}</p>

        <!-- 时间线列表 -->
        <template v-if="view.type === 'list'">
          <div v-if="timelineNodes.length" class="timeline">
          <article v-for="node in timelineNodes" :key="node.id" class="timeline__node" :data-node-id="node.id" :class="{ 'timeline__node--running': node.status === 'running' }">
            <button
              class="timeline__node-row"
              type="button"
              :class="{ 'is-static': !nodeHasContent(node), 'is-open': nodeHasContent(node) && isNodeOpen(node.id) }"
              :aria-expanded="nodeHasContent(node) && isNodeOpen(node.id)"
              @click="nodeHasContent(node) && toggleNode(node.id)"
            >
                <ChevronRight v-if="nodeHasContent(node)" class="timeline__node-chevron" :size="14" />
                <span v-else class="timeline__node-chevron-spacer"></span>
                <span class="timeline__node-main">
                  <strong :title="node.prompt">{{ node.prompt || t.changesUntitledNode }}</strong>
                  <span class="timeline__node-meta">
                    <small v-if="nodeTime(node)">{{ nodeTime(node) }}</small>
                    <small>{{ formatText(t.changeFileCount, { count: sortedFiles(node).length }) }}</small>
                    <small v-if="node.browserArtifacts?.length">{{ formatText(t.browserArtifactCount, { count: node.browserArtifacts.length }) }}</small>
                    <small v-if="node.inputArtifacts?.length">{{ formatText(t.inputArtifactCount, { count: node.inputArtifacts.length }) }}</small>
                    <small v-if="node.outputArtifacts?.length">{{ formatText(t.outputArtifactCount, { count: node.outputArtifacts.length }) }}</small>
                  </span>
                </span>
                <span class="timeline__node-side">
                  <span class="timeline__node-numbers">
                    <span v-if="nodeTotals(node).added" class="change-count change-count--added">+{{ nodeTotals(node).added }}</span>
                    <span v-if="nodeTotals(node).deleted" class="change-count change-count--deleted">-{{ nodeTotals(node).deleted }}</span>
                  </span>
                  <span v-if="node.status === 'running'" class="timeline__node-running">{{ t.changesNodeRunning }}</span>
                </span>
              </button>

              <div v-if="nodeHasContent(node) && isNodeOpen(node.id)" class="timeline__children">
                <section v-if="sortedFiles(node).length" class="timeline__section">
                  <div class="timeline__section-head">
                    <span>{{ t.changesTabFiles }}</span>
                    <small>{{ sortedFiles(node).length }}</small>
                  </div>
                  <template v-for="file in sortedFiles(node)" :key="fileKey(node.id, file.path, file.source)">
                    <button
                      class="timeline__file"
                      type="button"
                      :class="{ 'is-open': isFileOpen(node.id, file.path, file.source) }"
                      :title="file.path"
                      :data-node-id="node.id"
                      :data-file-path="file.path"
                      :aria-expanded="isFileOpen(node.id, file.path, file.source)"
                      @click="toggleFile(node.id, file.path, file.source)"
                    >
                      <component :is="fileIcon(file.status)" class="timeline__file-icon" :class="`is-${file.status}`" :size="14" />
                      <span class="timeline__file-path">
                        <strong>{{ concatPath(file.path).name }}</strong>
                        <small v-if="concatPath(file.path).dir">{{ concatPath(file.path).dir }}</small>
                        <small v-if="file.source" class="timeline__file-source">{{ sourceLabel(node, file.source) }}</small>
                      </span>
                      <span class="timeline__file-numbers">
                        <span v-if="file.binary" class="change-file__binary">BIN</span>
                        <template v-else>
                          <span class="change-count change-count--added">+{{ file.added }}</span>
                          <span class="change-count change-count--deleted">-{{ file.deleted }}</span>
                        </template>
                      </span>
                      <ChevronRight class="timeline__file-chevron" :size="13" />
                    </button>
                    <div v-if="isFileOpen(node.id, file.path, file.source)" class="timeline__filediff">
                      <p v-if="file.binary" class="change-file__notice">{{ t.binaryChangeNotice }}</p>
                      <template v-else-if="file.hunks?.length">
                        <div v-for="(hunk, index) in file.hunks" :key="index" class="diff-hunk">
                          <div class="diff-hunk__header">{{ hunk.header }}</div>
                          <div v-for="(line, lineIndex) in hunk.lines" :key="lineIndex" class="diff-line" :class="lineClass(line.kind)">
                            <span class="diff-line__number">{{ line.oldNumber || '' }}</span>
                            <span class="diff-line__number">{{ line.newNumber || '' }}</span>
                            <span class="diff-line__sign">{{ lineSign(line.kind) }}</span>
                            <code>{{ line.text }}</code>
                          </div>
                        </div>
                      </template>
                      <p v-else class="change-file__notice">{{ t.fileChangedNotice }}</p>
                    </div>
                  </template>
                </section>

                <section v-if="node.inputArtifacts?.length" class="timeline__section">
                  <div class="timeline__section-head">
                    <span>{{ t.changesInputArtifacts }}</span>
                    <small>{{ node.inputArtifacts.length }}</small>
                  </div>
                  <button
                    v-for="artifact in node.inputArtifacts"
                    :key="artifact.relPath || artifact.absPath || artifact.name"
                    class="timeline__artifact"
                    type="button"
                    :title="t.openInputArtifact"
                    :aria-label="t.openInputArtifact"
                    @click="openArtifact(artifact)"
                  >
                    <component :is="artifactIcon(artifact.kind)" class="timeline__artifact-icon" :size="14" />
                    <span class="timeline__artifact-main">
                      <span class="timeline__artifact-name" :title="artifact.name">{{ artifact.name }}</span>
                      <small>{{ artifactKindLabel(artifact.kind) }} · {{ formatSize(artifact.size) }}</small>
                    </span>
                  </button>
                </section>

                <section v-if="node.documentArtifacts?.length" class="timeline__section">
                  <div class="timeline__section-head">
                    <span>{{ t.changesDocumentArtifacts }}</span>
                    <small>{{ node.documentArtifacts.length }}</small>
                  </div>
                  <button
                    v-for="artifact in node.documentArtifacts"
                    :key="artifact.documentId"
                    class="timeline__artifact"
                    type="button"
                    :title="t.openDocumentArtifact"
                    :aria-label="t.openDocumentArtifact"
                    @click="openArtifact(artifact)"
                  >
                    <FileText class="timeline__artifact-icon" :size="14" />
                    <span class="timeline__artifact-main">
                      <span class="timeline__artifact-name" :title="artifact.name || artifact.documentId">{{ artifact.name || artifact.documentId }}</span>
                      <small>{{ t.documentArtifactParsed }}</small>
                    </span>
                  </button>
                </section>

                <section v-if="node.browserArtifacts?.length" class="timeline__section">
                  <div class="timeline__section-head">
                    <span>{{ t.changesBrowserArtifacts }}</span>
                    <small>{{ node.browserArtifacts.length }}</small>
                  </div>
                  <button
                    v-for="artifact in node.browserArtifacts"
                    :key="artifact.relPath || artifact.absPath || artifact.name"
                    class="timeline__artifact"
                    type="button"
                    :title="t.openBrowserArtifact"
                    :aria-label="t.openBrowserArtifact"
                    @click="openArtifact(artifact)"
                  >
                    <component :is="artifactIcon(artifact.kind)" class="timeline__artifact-icon" :size="14" />
                    <span class="timeline__artifact-main">
                      <span class="timeline__artifact-name" :title="artifact.name">{{ artifact.name }}</span>
                      <small>{{ artifactKindLabel(artifact.kind) }} · {{ formatSize(artifact.size) }}</small>
                    </span>
                  </button>
                </section>

                <section v-if="node.outputArtifacts?.length" class="timeline__section">
                  <div class="timeline__section-head">
                    <span>{{ t.changesOutputArtifacts }}</span>
                    <small>{{ node.outputArtifacts.length }}</small>
                  </div>
                  <button
                    v-for="artifact in node.outputArtifacts"
                    :key="artifact.relPath || artifact.absPath || artifact.name"
                    class="timeline__artifact"
                    type="button"
                    :title="t.openOutputArtifact"
                    :aria-label="t.openOutputArtifact"
                    @click="openArtifact(artifact)"
                  >
                    <component :is="artifactIcon(artifact.kind)" class="timeline__artifact-icon" :size="14" />
                    <span class="timeline__artifact-main">
                      <span class="timeline__artifact-name" :title="artifact.name">{{ artifact.name }}</span>
                      <small>{{ artifactKindLabel(artifact.kind) }} · {{ formatSize(artifact.size) }}</small>
                    </span>
                  </button>
                </section>

                <section v-if="node.subagentArtifacts?.length" class="timeline__section">
                  <div class="timeline__section-head">
                    <span>{{ t.subagentArtifacts }}</span>
                    <small>{{ node.subagentArtifacts.length }}</small>
                  </div>
                  <button
                    v-for="artifact in node.subagentArtifacts"
                    :key="`${artifact.source?.runId}:${artifact.relPath || artifact.absPath}`"
                    class="timeline__artifact"
                    type="button"
                    :title="artifact.absPath"
                    @click="openArtifact(artifact)"
                  >
                    <component :is="artifactIcon(artifact.kind)" class="timeline__artifact-icon" :size="14" />
                    <span class="timeline__artifact-main">
                      <span class="timeline__artifact-name">{{ artifact.name }}</span>
                      <small>{{ sourceLabel(node, artifact.source) }} · {{ formatSize(artifact.size) }}</small>
                    </span>
                  </button>
                </section>
              </div>
            </article>
          </div>

          <div v-else class="change-empty">
            <div><GitBranch :size="20" /></div>
            <strong>{{ t.noChangesTitle }}</strong>
            <p>{{ t.noChangesHint }}</p>
          </div>
        </template>
      </template>

      <template v-else-if="activeTab === 'git'">
        <div class="git-panel">
          <div class="git-panel__toolbar">
            <span v-if="gitSnapshot.currentBranch" class="git-panel__branch">
              <GitBranch :size="13" />
              {{ gitSnapshot.currentBranch }}
            </span>
            <span v-else></span>
            <button type="button" :title="t.refreshChanges" :aria-label="t.refreshChanges" :disabled="gitLoading" @click="refreshGitSnapshot()">
              <LoaderCircle v-if="gitLoading" class="spin" :size="14" />
              <RefreshCw v-else :size="14" />
            </button>
          </div>
          <p v-if="gitSnapshot.root" class="git-panel__root" :title="gitSnapshot.root">{{ gitSnapshot.root }}</p>

          <div v-if="gitLoading && !gitLoaded" class="git-panel__not-repository">
            <LoaderCircle class="spin" :size="22" />
          </div>

          <div v-else-if="!gitSnapshot.isRepository" class="git-panel__not-repository">
            <GitBranch :size="22" />
            <strong>{{ t.gitNotRepository }}</strong>
            <p>{{ t.gitNotRepositoryHint }}</p>
          </div>

          <template v-else>
            <section class="git-section">
              <header class="git-section__head">
                <div>
                  <strong>{{ t.gitWorkspaceChanges }}</strong>
                  <small>{{ t.gitWorktreeHint }}</small>
                </div>
                <span class="git-section__total">{{ gitWorktree.files?.length || 0 }}</span>
              </header>
              <div class="git-section__summary">
                <span>{{ formatText(t.gitFileCount, { count: gitWorktree.files?.length || 0 }) }}</span>
                <span class="change-count change-count--added">+{{ gitWorktree.added || 0 }}</span>
                <span class="change-count change-count--deleted">-{{ gitWorktree.deleted || 0 }}</span>
              </div>
              <div v-if="gitWorktree.files?.length" class="git-file-list">
                <div
                  v-for="(file, fileIndex) in gitWorktree.files"
                  :key="file.path"
                  class="git-file"
                  tabindex="0"
                  :title="t.gitDoubleClickCompare"
                  @dblclick="openGitDiff('worktree', gitWorktree.files, fileIndex)"
                  @keydown.enter="openGitDiff('worktree', gitWorktree.files, fileIndex)"
                >
                  <span class="git-file__status" :class="`is-${file.status}`">{{ gitStatusLabel(file.status) }}</span>
                  <span class="git-file__path">
                    <strong>{{ concatPath(file.path).name }}</strong>
                    <small v-if="concatPath(file.path).dir">{{ concatPath(file.path).dir }}</small>
                    <small v-if="gitFileState(file)" class="git-file__state">{{ gitFileState(file) }}</small>
                  </span>
                  <span class="git-file__numbers">
                    <span v-if="file.binary" class="change-file__binary">BIN</span>
                    <template v-else>
                      <span v-if="file.added" class="change-count change-count--added">+{{ file.added }}</span>
                      <span v-if="file.deleted" class="change-count change-count--deleted">-{{ file.deleted }}</span>
                    </template>
                  </span>
                </div>
              </div>
              <p v-else class="git-section__empty">{{ t.gitCleanWorktree }}</p>
            </section>

            <section class="git-section">
              <header class="git-section__head">
                <div>
                  <strong>{{ t.gitBranchChanges }}</strong>
                  <label class="git-base-picker">
                    <span>{{ t.gitBaseBranch }}</span>
                    <select
                      :value="selectedBase || gitSnapshot.baseBranch"
                      :disabled="gitLoading || !gitSnapshot.baseBranches?.length"
                      @change="selectBaseBranch"
                    >
                      <option v-if="!gitSnapshot.baseBranches?.length" value="">{{ t.gitBaseUnavailable }}</option>
                      <option v-for="branch in gitSnapshot.baseBranches" :key="branch" :value="branch">{{ branch }}</option>
                    </select>
                  </label>
                  <small v-if="gitSnapshot.baseBranch">{{ gitSnapshot.currentBranch || 'HEAD' }} → {{ gitSnapshot.baseBranch }}</small>
                </div>
                <span class="git-section__total">{{ gitBranch.files?.length || 0 }}</span>
              </header>
              <div class="git-section__summary">
                <span v-if="gitSnapshot.baseBranch">
                  {{ formatText(t.gitAheadBehind, { ahead: gitSnapshot.ahead || 0, behind: gitSnapshot.behind || 0 }) }}
                </span>
                <span v-else>{{ t.gitBaseUnavailable }}</span>
                <span class="change-count change-count--added">+{{ gitBranch.added || 0 }}</span>
                <span class="change-count change-count--deleted">-{{ gitBranch.deleted || 0 }}</span>
              </div>
              <div v-if="gitBranch.files?.length" class="git-file-list">
                <div
                  v-for="(file, fileIndex) in gitBranch.files"
                  :key="file.path"
                  class="git-file"
                  tabindex="0"
                  :title="t.gitDoubleClickCompare"
                  @dblclick="openGitDiff('branch', gitBranch.files, fileIndex)"
                  @keydown.enter="openGitDiff('branch', gitBranch.files, fileIndex)"
                >
                  <span class="git-file__status" :class="`is-${file.status}`">{{ gitStatusLabel(file.status) }}</span>
                  <span class="git-file__path">
                    <strong>{{ concatPath(file.path).name }}</strong>
                    <small v-if="concatPath(file.path).dir">{{ concatPath(file.path).dir }}</small>
                  </span>
                  <span class="git-file__numbers">
                    <span v-if="file.binary" class="change-file__binary">BIN</span>
                    <template v-else>
                      <span v-if="file.added" class="change-count change-count--added">+{{ file.added }}</span>
                      <span v-if="file.deleted" class="change-count change-count--deleted">-{{ file.deleted }}</span>
                    </template>
                  </span>
                </div>
              </div>
              <p v-else class="git-section__empty">
                {{ gitSnapshot.baseBranch ? t.gitNoBranchChanges : t.gitBaseUnavailableHint }}
              </p>
            </section>
          </template>
        </div>
      </template>

      <p v-else class="chat-right-side__placeholder">{{ formatText(t.changesTabPlaceholder, { tab: tabs.find(tab => tab.id === activeTab)?.label || '' }) }}</p>
    </div>
  </aside>
  <GitDiffDialog
    :open="gitDialog.open"
    :session-id="sessionId"
    :scope="gitDialog.scope"
    :files="gitDialog.files"
    :index="gitDialog.index"
    :base-branch="selectedBase || gitSnapshot.baseBranch || ''"
    :t="t"
    @close="gitDialog = { ...gitDialog, open: false }"
    @update:index="gitDialog = { ...gitDialog, index: $event }"
  />
</template>

<style scoped src="../../styles/chat/right-sidebar.css"></style>
