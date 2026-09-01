<script setup>
import { computed, nextTick, ref, watch } from 'vue'
import {
  ChevronRight, Download, FileCode2, FilePlus2, FileText, FileX2,
  GitBranch, Image as ImageIcon, LoaderCircle, RefreshCw, X
} from 'lucide-vue-next'
import { openSessionArtifact } from '../../backend.js'
import GitDiffDialog from './GitDiffDialog.vue'

const props = defineProps({
  open: { type: Boolean, default: false },
  sessionId: { type: Number, required: true },
  changes: { type: Object, default: () => ({ root: '', nodes: [], files: [], added: 0, deleted: 0 }) },
  loading: { type: Boolean, default: false },
  focusRequest: { type: Object, default: null },
  // 变更消息行尾斜箭头发起的 Git 对比请求：定位节点/文件后复用 GitDiffDialog 打开。
  diffRequest: { type: Object, default: null },
  t: { type: Object, required: true },
  language: { type: String, default: 'zh-CN' },
  modelOptions: { type: Array, default: () => [] },
  selectedModelValue: { type: String, default: '' }
})

const emit = defineEmits(['close', 'refresh', 'artifact-error'])
const tabs = computed(() => [
  { id: 'artifacts', label: props.t.changesTabArtifacts }
])
const activeTab = ref('artifacts')
const resizing = ref(false)
const SIDEBAR_MIN = 300
const SIDEBAR_MAX = 760
const SIDEBAR_WIDTH_KEY = 'codingto:right-sidebar-width'
// 拖动宽度持久化到前端 localStorage：刷新/重启后保持用户调整的宽度。
function loadSavedSidebarWidth() {
  try {
    const raw = Number(localStorage.getItem(SIDEBAR_WIDTH_KEY))
    if (Number.isFinite(raw) && raw >= SIDEBAR_MIN && raw <= SIDEBAR_MAX) return raw
  } catch { /* 存储不可用时回退默认宽度 */ }
  return 390
}
function persistSidebarWidth(value) {
  try { localStorage.setItem(SIDEBAR_WIDTH_KEY, String(value)) } catch { /* ignore */ }
}
const width = ref(loadSavedSidebarWidth())
// 视图状态：list=时间线列表；文件 diff 通过 GitDiffDialog 弹窗查看（不再就地展开）
const view = ref({ type: 'list' })
const changeNodes = computed(() => props.changes?.nodes || [])
const expandedNodes = ref(new Set())
// 节点按 startedAt 升序（时间线从早到晚）
const timelineNodes = computed(() =>
  [...changeNodes.value].sort((a, b) => (Number(a.startedAt) || 0) - (Number(b.startedAt) || 0))
)
const changedFileCount = computed(() => new Set(changeNodes.value.flatMap(node => (node.files || []).map(file => file.path))).size)
const browserArtifactCount = computed(() => changeNodes.value.reduce((sum, node) => sum + (node.browserArtifacts?.length || 0), 0))
const inputArtifactCount = computed(() => changeNodes.value.reduce((sum, node) => sum + (node.inputArtifacts?.length || 0), 0))
const outputArtifactCount = computed(() => changeNodes.value.reduce((sum, node) => sum + (node.outputArtifacts?.length || 0), 0))
const gitDialog = ref({ open: false, scope: 'worktree', files: [], index: 0 })

watch(
  () => props.sessionId,
  () => {
    gitDialog.value = { open: false, scope: 'worktree', files: [], index: 0 }
  }
)

// 展开状态独立于后台刷新：节点持续新增文件时，保留用户正在查看的节点。
// 切换会话或节点消失时，仅清理已经不在当前数据中的展开项。
watch(changeNodes, (nodes) => {
  const nodeIds = new Set(nodes.map(node => node.id))
  expandedNodes.value = new Set([...expandedNodes.value].filter(id => nodeIds.has(id)))
})

async function focusChangedFile() {
  const request = props.focusRequest
  if (!request?.path && !request?.nodeId) return
  activeTab.value = 'artifacts'
  view.value = { type: 'list' }
  if (request.nodeId) {
    expandedNodes.value = new Set([...expandedNodes.value, request.nodeId])
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

// 变更消息斜箭头：定位节点/文件后弹出 Git 对比框。右侧边栏数据是异步刷新的，
// 节点可能晚于请求到达，因此 watch 节点摘要，数据就绪后自动重试一次。
let diffRequestHandledNonce = 0
function openRequestedFileDiff() {
  const request = props.diffRequest
  if (!request?.path || !request.nodeId) return
  const node = changeNodes.value.find(item => String(item.id) === String(request.nodeId))
  if (!node || diffRequestHandledNonce === request.nonce) return
  activeTab.value = 'artifacts'
  view.value = { type: 'list' }
  expandedNodes.value = new Set([...expandedNodes.value, request.nodeId])
  const files = sortedFiles(node)
  // 同一节点内主/子代理可能修改同一路径文件，必须连同 source 一起匹配，
  // 否则会命中排序后的第一个同 path 文件，打开错误来源的快照。
  const index = files.findIndex(item =>
    item.path === request.path && sourceKey(item.source) === sourceKey(request.source)
  )
  // 节点文件被后端剔除（如已还原到基线）时暂不打开：不标记 handled，
  // 保留 pending 等待后续节点摘要变化重试（配合侧边栏关闭时清空请求避免幽灵弹窗）。
  if (index === -1) return
  diffRequestHandledNonce = request.nonce
  openNodeFileDiff(node, files[index], index)
}
watch(
  [
    () => props.diffRequest?.nonce,
    () => changeNodes.value.map(node =>
      `${node.id}:${(node.files || []).map(file => file.path).join(',')}`
    ).join('|')
  ],
  openRequestedFileDiff
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

// 点击文件：弹出 Git 对比框（复用 git tab 的 GitDiffDialog），不再在侧边栏就地展开。
// scope 固定为 worktree（节点编辑即工作区未提交变更）；文件附带节点快照 hunks，
// 供 GitDiffDialog 在 git 实时对比不可用（非 git 仓库 / 文件已提交）时回退渲染。
function sourceKey(source) {
  return source ? `${source.agentKey || ''}:${source.runId || ''}:${source.nodeId || ''}` : 'main'
}
function fileKey(nodeId, path, source) {
  return `${nodeId}::${sourceKey(source)}::${path}`
}
function openNodeFileDiff(node, file, index) {
  const files = sortedFiles(node).map(item => ({
    path: item.path,
    oldPath: item.oldPath || '',
    status: item.status,
    added: item.added || 0,
    deleted: item.deleted || 0,
    binary: item.binary || false,
    hunks: item.hunks || []
  }))
  gitDialog.value = { open: true, scope: 'worktree', files, index }
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
    persistSidebarWidth(width.value)
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
                  <template v-for="(file, fileIndex) in sortedFiles(node)" :key="fileKey(node.id, file.path, file.source)">
                    <button
                      class="timeline__file"
                      type="button"
                      :title="file.path"
                      :data-node-id="node.id"
                      :data-file-path="file.path"
                      @click="openNodeFileDiff(node, file, fileIndex)"
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
                    </button>
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

    </div>
  </aside>
  <GitDiffDialog
    :open="gitDialog.open"
    :session-id="sessionId"
    :scope="gitDialog.scope"
    :files="gitDialog.files"
    :index="gitDialog.index"
    :base-branch="''"
    :language="language"
    :model-options="modelOptions"
    :selected-model-value="selectedModelValue"
    :t="t"
    @close="gitDialog = { ...gitDialog, open: false }"
    @update:index="gitDialog = { ...gitDialog, index: $event }"
  />
</template>

<style scoped src="../../styles/chat/right-sidebar.css"></style>
