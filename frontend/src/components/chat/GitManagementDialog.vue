<script setup>
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import {
  AlertTriangle, Archive, ArchiveRestore, Brain, Check, ChevronDown, ChevronRight, Download, Folder, FolderOpen,
  Ellipsis, Eye, EyeOff, GitBranch, GitCommitHorizontal, LoaderCircle, MessageSquarePlus, Plus, RefreshCw,
  RotateCcw, Search, Settings2, Sparkles, Trash2, Upload, X
} from 'lucide-vue-next'
import {
  applyGitFileOperation, applyGitFileOperations, compareSessionGitBranches, generateSessionGitCommitMessage,
  getSessionGitOutgoingCommits, getSessionGitRepository,
  onEvent, refreshSessionGitRepository, runSessionGitOperation
} from '../../backend.js'
import { useAppContext } from '../../composables/appContext.js'
import ConfirmDeleteDialog from '../ConfirmDeleteDialog.vue'
import ConfirmDialog from '../ConfirmDialog.vue'
import GitAIPromptDialog from '../GitAIPromptDialog.vue'
import GitConflictDialog from './GitConflictDialog.vue'
import GitDiffDialog from './GitDiffDialog.vue'

const GIT_COMMIT_MODEL_CACHE_KEY = 'codingto:git-commit-model'

function cachedCommitModel() {
  try { return localStorage.getItem(GIT_COMMIT_MODEL_CACHE_KEY) || '' } catch { return '' }
}

function cacheCommitModel(value) {
  if (!value) return
  try { localStorage.setItem(GIT_COMMIT_MODEL_CACHE_KEY, value) } catch { /* 缓存不可用时仍保留当前选择 */ }
}

const props = defineProps({
  open: { type: Boolean, default: false },
  sessionId: { type: Number, required: true },
  t: { type: Object, required: true },
  language: { type: String, default: 'zh-CN' },
  agentRunning: { type: Boolean, default: false },
  workspace: { type: String, default: '' },
  workspaceRevision: { type: Number, default: 0 },
  modelOptions: { type: Array, default: () => [] },
  selectedModelValue: { type: String, default: '' },
})

const emit = defineEmits(['close', 'resolve-conflicts', 'updated', 'add-to-chat', 'add-selection-to-chat'])
const { pushToast } = useAppContext()
const dialogRoot = ref(null)
const loading = ref(false)
const busy = ref('')
const error = ref('')
const activeTab = ref('changes')
const repository = ref(emptyRepository())
const fileFilter = ref('')
const commitMessage = ref('')
const branchFilter = ref('')
const newBranchName = ref('')
const newBranchStartPoint = ref('HEAD')
const showNewBranch = ref(false)
const diffDialog = ref({ open: false, files: [], index: 0, scope: 'worktree', commit: '', compareLeft: '', compareRight: '' })
const conflictDialog = ref({ open: false, files: [], index: 0 })
const outgoingDialogRoot = ref(null)
const outgoingDialogOpen = ref(false)
const outgoingLoading = ref(false)
const outgoingError = ref('')
const outgoingCommits = ref([])
const selectedOutgoingHash = ref('')
const confirmFileOpen = ref(false)
const confirmFileOperation = ref(null)
const resetConfirmOpen = ref(false)
const resetConfirmCommit = ref(null)
const checkoutConfirmOpen = ref(false)
const checkoutConfirmTarget = ref(null)
const gitCommitModel = ref(cachedCommitModel())
const commitModelMenuOpen = ref(false)
const commitModelWrapEl = ref(null)
const commitPromptOpen = ref(false)
const selectedStagedPaths = ref(new Set())
const selectedChangedPaths = ref(new Set())
const selectedBothDeletedPaths = ref(new Set())
const reloadingFileSections = ref(new Set())
const collapsedFolders = ref(new Set())
const collapsedSections = ref(new Set())
const visibleRowLimits = ref({ conflicts: 240, both_deleted: 240, staged: 240, changed: 240 })

// 分支对比 tab 状态
const compareSnapshot = ref(null)
const compareLoading = ref(false)
const compareLoaded = ref(false)
const compareTypeFilter = ref('')
const compareError = ref('')
const comparePickers = reactive({
  left: { open: false, filter: '', selected: '', root: null, input: null },
  right: { open: false, filter: '', selected: '', root: null, input: null },
})
const compareSides = [
  { key: 'left', labelKey: 'gitCompareLeft' },
  { key: 'right', labelKey: 'gitCompareRight' },
]
let compareRequestNonce = 0
const moreMenu = ref(null)
const moreMenuEl = ref(null)
const stashCreateOpen = ref(false)
const stashName = ref('')
const stashIncludeStaged = ref(true)
const stashIncludeChanged = ref(true)
const stashRestoreOpen = ref(false)
const stashRestoreTarget = ref(null)
const stashDeleteOpen = ref(false)
const stashDeleteTarget = ref(null)

const GIT_FILE_ROW_BATCH = 240
const GIT_AUTO_COLLAPSE_FILE_COUNT = 80

function emptyRepository() {
  return {
    isRepository: false, warming: false, root: '', worktreePath: '', currentBranch: '', detached: false,
    head: '', upstream: '', ahead: 0, behind: 0, state: '', hasConflicts: false,
    conflicts: [], worktree: { files: [], added: 0, deleted: 0 }, remotes: [], branches: [], commits: [], stashes: [],
  }
}

function normalizedWorkspace(value) {
  const normalized = String(value || '').trim().replaceAll('\\', '/').replace(/\/$/, '')
  return /^[a-z]:\//i.test(normalized) ? normalized.toLowerCase() : normalized
}

const worktreeFiles = computed(() => repository.value.worktree?.files || [])
const filteredWorktreeFiles = computed(() => filterGitFiles(worktreeFiles.value))
const conflictFiles = computed(() => worktreeFiles.value.filter(file => file.conflicted))
const bothDeletedFiles = computed(() => conflictFiles.value.filter(file => file.conflictStatus === 'DD'))
const regularConflictFiles = computed(() => conflictFiles.value.filter(file => file.conflictStatus !== 'DD'))
const stagedFiles = computed(() => worktreeFiles.value
  .filter(file => file.staged && !file.conflicted)
  .map(file => scopedGitFile(file, 'staged')))
const changedFiles = computed(() => worktreeFiles.value
  .filter(file => (file.unstaged || file.untracked) && !file.conflicted)
  .map(file => scopedGitFile(file, file.untracked ? 'untracked' : 'unstaged')))
const selectableChangedFiles = computed(() => changedFiles.value)
const filteredRegularConflictFiles = computed(() => filterGitFiles(regularConflictFiles.value))
const filteredBothDeletedFiles = computed(() => filterGitFiles(bothDeletedFiles.value))
const filteredStagedFiles = computed(() => filterGitFiles(stagedFiles.value))
const filteredChangedFiles = computed(() => filterGitFiles(changedFiles.value))
const conflictTreeRows = computed(() => buildFileTreeRows(filteredRegularConflictFiles.value, 'conflicts', !!fileFilter.value.trim()))
const bothDeletedTreeRows = computed(() => buildFileTreeRows(filteredBothDeletedFiles.value, 'both_deleted', !!fileFilter.value.trim()))
const stagedTreeRows = computed(() => buildFileTreeRows(filteredStagedFiles.value, 'staged', !!fileFilter.value.trim()))
const changedTreeRows = computed(() => buildFileTreeRows(filteredChangedFiles.value, 'changed', !!fileFilter.value.trim()))
const visibleConflictTreeRows = computed(() => limitedRows('conflicts', conflictTreeRows.value))
const visibleBothDeletedTreeRows = computed(() => limitedRows('both_deleted', bothDeletedTreeRows.value))
const visibleStagedTreeRows = computed(() => limitedRows('staged', stagedTreeRows.value))
const visibleChangedTreeRows = computed(() => limitedRows('changed', changedTreeRows.value))
const selectedBothDeletedFiles = computed(() => bothDeletedFiles.value.filter(file => selectedBothDeletedPaths.value.has(file.path)))
const selectedStagedFiles = computed(() => stagedFiles.value.filter(file => selectedStagedPaths.value.has(file.path)))
const selectedChangedFiles = computed(() => selectableChangedFiles.value.filter(file => selectedChangedPaths.value.has(file.path)))
const selectedTrackedChangedFiles = computed(() => selectedChangedFiles.value.filter(file => !file.untracked))
const selectedUntrackedFiles = computed(() => selectedChangedFiles.value.filter(file => file.untracked))
const stagedSectionReloading = computed(() => reloadingFileSections.value.has('staged'))
const changedSectionReloading = computed(() => reloadingFileSections.value.has('changed'))
const conflictSectionReloading = computed(() => reloadingFileSections.value.has('conflicts'))
const bothDeletedSectionReloading = computed(() => reloadingFileSections.value.has('both_deleted'))
const localBranches = computed(() => filterBranches(repository.value.branches?.filter(branch => !branch.remote) || []))
const remoteBranches = computed(() => filterBranches(repository.value.branches?.filter(branch => branch.remote) || []))
const remoteForPush = computed(() => repository.value.remotes?.find(remote => remote.name === 'origin')?.name || repository.value.remotes?.[0]?.name || '')
const operationsLocked = computed(() => !!busy.value || props.agentRunning)
const selectedOutgoingCommit = computed(() => outgoingCommits.value.find(commit => commit.hash === selectedOutgoingHash.value) || outgoingCommits.value[0] || null)
const enabledCommitModels = computed(() => props.modelOptions.filter(option => !option.disabled))
const commitModelGroups = computed(() => {
  const groups = {}
  for (const option of props.modelOptions) (groups[option.provider] ||= []).push(option)
  return Object.entries(groups).map(([provider, options]) => ({ provider, options }))
})
const selectedCommitModelLabel = computed(() => {
  const selected = props.modelOptions.find(option => option.value === gitCommitModel.value)
  return selected?.model || selected?.label || gitCommitModel.value
})
const selectedStashPaths = computed(() => {
  const paths = new Set()
  if (stashIncludeStaged.value) for (const file of stagedFiles.value) paths.add(file.path)
  if (stashIncludeChanged.value) for (const file of changedFiles.value) paths.add(file.path)
  return [...paths]
})

// 分支对比响应式数据
const compareData = computed(() => compareSnapshot.value || {
  isRepository: false, left: '', right: '', root: '',
  files: [], added: 0, deleted: 0, ahead: 0, behind: 0,
})
const compareFiles = computed(() => compareData.value.files || [])
const compareBranchOptions = computed(() => {
  const all = repository.value.branches || []
  return {
    local: all.filter(branch => !branch.remote),
    remote: all.filter(branch => branch.remote),
  }
})
const filteredCompareOptions = computed(() => {
  const match = (branches, query) => {
    const q = query.trim().toLowerCase()
    return q ? branches.filter(branch => branch.name.toLowerCase().includes(q)) : branches
  }
  return {
    left: {
      local: match(compareBranchOptions.value.local, comparePickers.left.filter),
      remote: match(compareBranchOptions.value.remote, comparePickers.left.filter),
    },
    right: {
      local: match(compareBranchOptions.value.local, comparePickers.right.filter),
      remote: match(compareBranchOptions.value.remote, comparePickers.right.filter),
    },
  }
})
const hasCompareBranches = computed(() => compareBranchOptions.value.local.length > 0 || compareBranchOptions.value.remote.length > 0)
const compareTypeFilterOptions = computed(() => {
  const files = compareFiles.value
  const count = status => files.filter(file => file.status === status).length
  return [
    { key: '', labelKey: 'gitCompareAll', count: files.length },
    { key: 'added', labelKey: 'gitCompareAdded', count: count('added') },
    { key: 'modified', labelKey: 'gitCompareModified', count: count('modified') },
    { key: 'deleted', labelKey: 'gitCompareDeleted', count: count('deleted') },
  ]
})
const filteredCompareFiles = computed(() => {
  const files = compareFiles.value
  if (!compareTypeFilter.value) return files
  return files.filter(file => file.status === compareTypeFilter.value)
})
const compareTreeRows = computed(() => buildFileTreeRows(filteredCompareFiles.value, 'compare', !!compareTypeFilter.value))
const visibleCompareTreeRows = computed(() => limitedRows('compare', compareTreeRows.value))
let outgoingRequestNonce = 0
let repositoryRequestNonce = 0

function scopedGitFile(file, scope) {
  const staged = scope === 'staged'
  return {
    ...file,
    diffScope: scope,
    added: staged ? (file.stagedAdded ?? file.added ?? 0) : (file.unstagedAdded ?? file.added ?? 0),
    deleted: staged ? (file.stagedDeleted ?? file.deleted ?? 0) : (file.unstagedDeleted ?? file.deleted ?? 0),
  }
}

function filterGitFiles(files) {
  const query = fileFilter.value.trim().toLowerCase()
  if (!query) return files
  return files.filter(file => `${file.path || ''} ${file.status || ''} ${file.conflictStatus || ''}`.toLowerCase().includes(query))
}

function limitedRows(scope, rows) {
  return rows.slice(0, visibleRowLimits.value[scope] || GIT_FILE_ROW_BATCH)
}

function remainingRowCount(scope, rows) {
  return Math.max(0, rows.length - (visibleRowLimits.value[scope] || GIT_FILE_ROW_BATCH))
}

function showMoreRows(scope) {
  visibleRowLimits.value = {
    ...visibleRowLimits.value,
    [scope]: (visibleRowLimits.value[scope] || GIT_FILE_ROW_BATCH) + GIT_FILE_ROW_BATCH,
  }
}

function resetVisibleRowLimits() {
  visibleRowLimits.value = { conflicts: GIT_FILE_ROW_BATCH, both_deleted: GIT_FILE_ROW_BATCH, staged: GIT_FILE_ROW_BATCH, changed: GIT_FILE_ROW_BATCH, compare: GIT_FILE_ROW_BATCH }
}

function filterBranches(branches) {
  const query = branchFilter.value.trim().toLowerCase()
  if (!query) return branches
  return branches.filter(branch => `${branch.name} ${branch.subject || ''}`.toLowerCase().includes(query))
}

watch(() => [props.open, props.sessionId], async ([open, sessionId], previous = []) => {
  if (!open) {
    compareRequestNonce += 1
    compareSnapshot.value = null
    compareLoading.value = false
    compareLoaded.value = false
    resetComparePickers()
    repositoryRequestNonce += 1
    outgoingRequestNonce += 1
    outgoingDialogOpen.value = false
    confirmFileOpen.value = false
    confirmFileOperation.value = null
    resetConfirmOpen.value = false
    resetConfirmCommit.value = null
    checkoutConfirmOpen.value = false
    checkoutConfirmTarget.value = null
    commitPromptOpen.value = false
    conflictDialog.value = { open: false, files: [], index: 0 }
    moreMenu.value = null
    return
  }
  if (previous[0] && previous[1] === sessionId) return
  activeTab.value = 'changes'
  fileFilter.value = ''
  selectedStagedPaths.value = new Set()
  selectedChangedPaths.value = new Set()
  selectedBothDeletedPaths.value = new Set()
  collapsedFolders.value = new Set()
  resetVisibleRowLimits()
  compareRequestNonce += 1
  compareSnapshot.value = null
  compareLoading.value = false
  compareLoaded.value = false
  resetComparePickers()
  const cached = cachedCommitModel()
  gitCommitModel.value = enabledCommitModels.value.some(option => option.value === cached)
    ? cached
    : enabledCommitModels.value.some(option => option.value === props.selectedModelValue)
      ? props.selectedModelValue
      : enabledCommitModels.value[0]?.value || ''
  cacheCommitModel(gitCommitModel.value)
  error.value = ''
  const refreshed = await refresh()
  if (!refreshed || !props.open || props.sessionId !== sessionId) return
  initializeLargeListCollapse()
  collapsedSections.value = repository.value.hasConflicts ? new Set(['staged', 'changed']) : new Set()
  await nextTick()
  dialogRoot.value?.focus()
  void revalidate()
}, { immediate: true })

watch(
  () => [props.selectedModelValue, props.modelOptions],
  ([selected]) => {
    if (enabledCommitModels.value.some(option => option.value === gitCommitModel.value)) return
    gitCommitModel.value = enabledCommitModels.value.some(option => option.value === selected)
      ? selected
      : enabledCommitModels.value[0]?.value || ''
    cacheCommitModel(gitCommitModel.value)
  },
  { immediate: true }
)

watch(fileFilter, resetVisibleRowLimits)
watch(() => props.workspaceRevision, () => {
  if (props.open) void refresh(true)
})

watch(activeTab, (tab) => {
  if (tab === 'compare' && props.open && !compareLoaded.value) void runCompare()
})

async function refresh(silent = false) {
  const requestNonce = ++repositoryRequestNonce
  const requestedSessionId = props.sessionId
  if (!silent) loading.value = true
  try {
    const result = await getSessionGitRepository(requestedSessionId)
    if (requestNonce !== repositoryRequestNonce || !props.open || requestedSessionId !== props.sessionId) return false
    repository.value = { ...emptyRepository(), ...result }
    reconcileSelections()
    emit('updated', {
      isRepository: repository.value.isRepository,
      root: repository.value.root,
      currentBranch: repository.value.currentBranch,
      changeCount: worktreeFiles.value.length,
      ahead: repository.value.ahead || 0,
      hasConflicts: repository.value.hasConflicts,
    })
    return true
  } catch (cause) {
    if (requestNonce === repositoryRequestNonce && props.open && requestedSessionId === props.sessionId) error.value = formatError(cause)
    return false
  } finally {
    if (requestNonce === repositoryRequestNonce) loading.value = false
  }
}

// 重新计算后端工作目录缓存。完成后后端广播 git:workspace，事件订阅随即静默刷新弹窗，
// 因此打开弹窗时先渲染缓存模型，再自动替换为实时结果，不会展示过期的预热数据。
async function revalidate(showLoading = false) {
  if (showLoading) loading.value = true
  try {
    await refreshSessionGitRepository(props.sessionId)
  } catch (cause) {
    console.warn('refresh Git repository cache failed', cause)
    if (showLoading) error.value = formatError(cause)
  } finally {
    if (showLoading) loading.value = false
  }
}

function setFileSectionsReloading(scopes, reloading) {
  if (!scopes.length) return
  const next = new Set(reloadingFileSections.value)
  for (const scope of scopes) reloading ? next.add(scope) : next.delete(scope)
  reloadingFileSections.value = next
}

function applyOptimisticFileOperation(op, files) {
  const targets = new Set((files || []).map(file => file.path))
  if (!targets.size) return
  const nextFiles = []
  for (const current of worktreeFiles.value) {
    if (!targets.has(current.path)) {
      nextFiles.push(current)
      continue
    }
    if (op === 'delete_untracked') continue
    if (op === 'discard_tracked') {
      if (current.staged) {
        nextFiles.push({
          ...current,
          unstaged: false,
          untracked: false,
          unstagedAdded: 0,
          unstagedDeleted: 0,
          added: current.stagedAdded || 0,
          deleted: current.stagedDeleted || 0,
        })
      }
      continue
    }
    if (op === 'stage' || op === 'track') {
      nextFiles.push({
        ...current,
        status: current.untracked ? 'added' : current.status,
        staged: true,
        unstaged: false,
        untracked: false,
        stagedAdded: (current.stagedAdded || 0) + (current.unstagedAdded || current.added || 0),
        stagedDeleted: (current.stagedDeleted || 0) + (current.unstagedDeleted || current.deleted || 0),
        unstagedAdded: 0,
        unstagedDeleted: 0,
      })
      continue
    }
    if (op === 'unstage') {
      const becomesUntracked = current.status === 'added'
      nextFiles.push({
        ...current,
        staged: false,
        unstaged: !becomesUntracked,
        untracked: becomesUntracked,
        unstagedAdded: current.stagedAdded || current.added || 0,
        unstagedDeleted: current.stagedDeleted || current.deleted || 0,
        stagedAdded: 0,
        stagedDeleted: 0,
      })
      continue
    }
    if (op === 'resolve_both_deleted') {
      nextFiles.push({ ...current, status: 'deleted', conflictStatus: '', conflicted: false, staged: true, unstaged: false })
      continue
    }
    nextFiles.push(current)
  }
  repository.value = {
    ...repository.value,
    worktree: { ...repository.value.worktree, files: nextFiles },
    conflicts: nextFiles.filter(file => file.conflicted),
    hasConflicts: nextFiles.some(file => file.conflicted),
  }
  reconcileSelections()
  emit('updated', {
    isRepository: repository.value.isRepository,
    root: repository.value.root,
    currentBranch: repository.value.currentBranch,
    changeCount: nextFiles.length,
    ahead: repository.value.ahead || 0,
    hasConflicts: repository.value.hasConflicts,
  })
}

function close() {
  if (busy.value || diffDialog.value.open || conflictDialog.value.open || outgoingDialogOpen.value || confirmFileOpen.value || resetConfirmOpen.value || checkoutConfirmOpen.value || stashCreateOpen.value || stashRestoreOpen.value || stashDeleteOpen.value || commitPromptOpen.value) return
  emit('close')
}

function onDocumentPointerDown(event) {
  if (commitModelMenuOpen.value && commitModelWrapEl.value && !commitModelWrapEl.value.contains(event.target)) commitModelMenuOpen.value = false
  if (moreMenu.value && moreMenuEl.value && !moreMenuEl.value.contains(event.target)) moreMenu.value = null
}

function closeMoreMenu() {
  moreMenu.value = null
}

function positionMoreMenu(event, target, key, menuHeight = 76) {
  if (moreMenu.value?.key === key) {
    closeMoreMenu()
    return
  }
  const rect = event.currentTarget.getBoundingClientRect()
  const menuWidth = 210
  moreMenu.value = {
    ...target,
    key,
    left: Math.max(8, Math.min(rect.right - menuWidth, window.innerWidth - menuWidth - 8)),
    top: rect.bottom + menuHeight + 8 > window.innerHeight ? rect.top - menuHeight - 4 : rect.bottom + 4,
  }
}

function openMoreMenu(event, target) {
  if (operationsLocked.value || target.conflicted) return
  const key = `${target.isDirectory ? 'folder' : 'file'}:${target.path}`
  positionMoreMenu(event, { ...target, kind: 'file' }, key)
}

function openCommitMoreMenu(event, commit) {
  if (operationsLocked.value || repository.value.detached || repository.value.state || !commit?.hash) return
  positionMoreMenu(event, { kind: 'commit', commit }, `commit:${commit.hash}`)
}

function resetCommitFromMoreMenu(hard = false) {
  const commit = moreMenu.value?.commit
  closeMoreMenu()
  if (commit) void requestResetCommit(commit, hard)
}

function selectCommitModel(option) {
  if (!option || option.disabled) return
  gitCommitModel.value = option.value
  cacheCommitModel(option.value)
  commitModelMenuOpen.value = false
}

let offGitWorkspace = null

onMounted(() => {
  document.addEventListener('pointerdown', onDocumentPointerDown)
  document.addEventListener('click', closeCompareSelectIfOutside)
  document.addEventListener('scroll', closeMoreMenu, true)
  window.addEventListener('resize', closeMoreMenu)
  // 后端完成工作目录缓存刷新后推送事件，弹窗借此把缓存模型替换为实时结果。
  offGitWorkspace = onEvent('git:workspace', payload => {
    const expected = normalizedWorkspace(props.workspace || repository.value.worktreePath || repository.value.root)
    if (!expected || normalizedWorkspace(payload?.workspace) !== expected) return
    if (props.open) void refresh(true)
  })
})
onBeforeUnmount(() => {
  document.removeEventListener('pointerdown', onDocumentPointerDown)
  document.removeEventListener('click', closeCompareSelectIfOutside)
  document.removeEventListener('scroll', closeMoreMenu, true)
  window.removeEventListener('resize', closeMoreMenu)
  offGitWorkspace?.()
})

function onKeydown(event) {
  if (event.key !== 'Escape') return
  if (moreMenu.value) {
    closeMoreMenu()
    return
  }
  if (comparePickers.left.open || comparePickers.right.open) {
    closeCompareSelects()
    return
  }
  close()
}

async function runOperation(op, extra = {}, busyKey = op) {
  if (busy.value || props.agentRunning) return false
  const reloadScopes = ['stage_all', 'unstage_all'].includes(op) ? ['staged', 'changed'] : []
  busy.value = busyKey
  setFileSectionsReloading(reloadScopes, true)
  error.value = ''
  try {
    const result = await runSessionGitOperation({ sessionId: props.sessionId, op, language: props.language, ...extra })
    pushToast(result?.hasConflicts || result?.stashKept ? 'info' : 'success', result?.message || props.t.gitOperationCompleted)
    if (op === 'commit') {
      commitMessage.value = ''
    }
    if (op === 'create_branch') {
      newBranchName.value = ''
      newBranchStartPoint.value = 'HEAD'
      showNewBranch.value = false
    }
    await refresh(true)
    if (result?.hasConflicts) {
      activeTab.value = 'changes'
      const conflicts = result?.conflicts?.length ? result.conflicts : repository.value.conflicts
      if (conflicts?.length) openConflict(conflicts, 0)
    }
    return true
  } catch (cause) {
    error.value = formatError(cause)
    await refresh(true)
    return false
  } finally {
    setFileSectionsReloading(reloadScopes, false)
    busy.value = ''
  }
}

async function runFileOperation(op, file) {
  if (busy.value || props.agentRunning) return false
  busy.value = `${op}:${file.path}`
  error.value = ''
  try {
    await applyGitFileOperation(props.sessionId, op, file.path, !!file.isDirectory)
    const successMessage = op === 'ignore'
      ? (file.pendingCommit ? props.t.gitIgnoreAddedPending : props.t.gitIgnoreAdded)
      : props.t.gitOperationCompleted
    applyOptimisticFileOperation(op, [file])
    pushToast('success', successMessage)
    if (op === 'ignore') void refresh(true)
    return true
  } catch (cause) {
    error.value = formatError(cause)
    void refresh(true)
    return false
  } finally {
    busy.value = ''
  }
}

function ignoreMoreMenuTarget() {
  const target = moreMenu.value
  closeMoreMenu()
  if (target) void runFileOperation('ignore', target)
}

function addToChatMenuTarget() {
  const target = moreMenu.value
  closeMoreMenu()
  if (target?.path) emit('add-to-chat', target.path)
}

function fileOperationBusy(path) {
  return !!busy.value && busy.value.endsWith(`:${path}`)
}

function isPendingIgnore(file) {
  return !!file?.ignored && !!file?.staged && file?.status === 'deleted'
}

function folderPendingIgnore(files) {
  return !!files?.length && files.every(isPendingIgnore)
}

function needsUntrackCommit(file) {
  return !file?.untracked && file?.status !== 'added'
}

async function runBatchFileOperation(op, files, scope) {
  const eligibleFiles = op === 'resolve_both_deleted'
    ? (files || []).filter(file => file.conflictStatus === 'DD')
    : (files || []).filter(file => !file.conflicted)
  const paths = Array.from(new Set(eligibleFiles.map(file => file.path)))
  if (busy.value || props.agentRunning || !paths.length) return false
  busy.value = `${op}:batch`
  error.value = ''
  try {
    await applyGitFileOperations(props.sessionId, op, paths)
    const message = op === 'resolve_both_deleted'
      ? props.t.gitBothDeletedResolved.replace('{count}', paths.length)
      : props.t.gitBatchOperationCompleted.replace('{count}', paths.length)
    applyOptimisticFileOperation(op, eligibleFiles)
    pushToast('success', message)
    clearSelectedPaths(scope, paths)
    return true
  } catch (cause) {
    error.value = formatError(cause)
    void refresh(true)
    return false
  } finally {
    busy.value = ''
  }
}

function requestDestructiveFileOperation(file) {
  if (operationsLocked.value || file.conflicted) return
  const isUntracked = !!file.untracked
  confirmFileOperation.value = {
    op: isUntracked ? 'delete_untracked' : 'discard_tracked',
    files: [file],
    title: isUntracked ? props.t.gitConfirmDeleteTitle : props.t.gitConfirmDiscardTitle,
    description: isUntracked ? props.t.gitConfirmDeleteDesc : props.t.gitConfirmDiscardDesc,
  }
  confirmFileOpen.value = true
}

function requestDestructiveBatchOperation(op, files) {
  const targets = Array.from(new Map((files || []).map(file => [file.path, file])).values())
  if (operationsLocked.value || !targets.length) return
  const deleting = op === 'delete_untracked'
  confirmFileOperation.value = {
    op,
    files: targets,
    scope: 'changed',
    title: deleting ? props.t.gitConfirmDeleteTitle : props.t.gitConfirmDiscardTitle,
    description: (deleting ? props.t.gitConfirmDeleteManyDesc : props.t.gitConfirmRevertManyDesc).replace('{count}', targets.length),
  }
  confirmFileOpen.value = true
}

async function confirmDestructiveFileOperation() {
  if (busy.value) return
  const target = confirmFileOperation.value
  if (!target?.files?.length) {
    confirmFileOpen.value = false
    confirmFileOperation.value = null
    return
  }
  const completed = target.files.length === 1
    ? await runFileOperation(target.op, target.files[0])
    : await runBatchFileOperation(target.op, target.files, target.scope || 'changed')
  if (!completed) return
  confirmFileOpen.value = false
  confirmFileOperation.value = null
}

function buildFileTreeRows(files, scope, forceExpanded = false) {
  const root = { folders: new Map(), files: [] }
  for (const file of files) {
    const parts = String(file.path || '').replaceAll('\\', '/').split('/').filter(Boolean)
    const name = parts.pop() || file.path
    let node = root
    let folderPath = ''
    for (const part of parts) {
      folderPath = folderPath ? `${folderPath}/${part}` : part
      if (!node.folders.has(part)) node.folders.set(part, { name: part, path: folderPath, folders: new Map(), files: [] })
      node = node.folders.get(part)
    }
    node.files.push({ ...file, treeName: name })
  }

  const collectFiles = node => [
    ...node.files,
    ...Array.from(node.folders.values()).flatMap(collectFiles),
  ]
  const rows = []
  const compactFolder = folder => {
    const names = [folder.name]
    let node = folder
    while (!node.files.length && node.folders.size === 1) {
      node = node.folders.values().next().value
      names.push(node.name)
    }
    return { node, name: names.join('/'), path: node.path }
  }
  const appendNode = (node, depth) => {
    const folders = Array.from(node.folders.values()).sort((a, b) => a.name.localeCompare(b.name))
    for (const folder of folders) {
      const compacted = compactFolder(folder)
      const key = `${scope}:${compacted.path}`
      const descendants = collectFiles(compacted.node)
      rows.push({ kind: 'folder', key, path: compacted.path, name: compacted.name, depth, files: descendants })
      if (forceExpanded || !collapsedFolders.value.has(key)) appendNode(compacted.node, depth + 1)
    }
    for (const file of [...node.files].sort((a, b) => a.treeName.localeCompare(b.treeName))) {
      rows.push({ kind: 'file', key: `${scope}:${file.path}`, depth, file })
    }
  }
  appendNode(root, 0)
  return rows
}

function collectFolderKeys(files, scope) {
  const keys = new Set()
  const root = { folders: new Map(), files: [] }
  for (const file of files) {
    const parts = String(file.path || '').replaceAll('\\', '/').split('/').filter(Boolean)
    parts.pop()
    let node = root
    let folderPath = ''
    for (const part of parts) {
      folderPath = folderPath ? `${folderPath}/${part}` : part
      if (!node.folders.has(part)) node.folders.set(part, { name: part, path: folderPath, folders: new Map(), files: [] })
      node = node.folders.get(part)
    }
    node.files.push(file)
  }
  const visit = node => {
    for (const folder of node.folders.values()) {
      let compacted = folder
      while (!compacted.files.length && compacted.folders.size === 1) compacted = compacted.folders.values().next().value
      keys.add(`${scope}:${compacted.path}`)
      visit(compacted)
    }
  }
  visit(root)
  return keys
}

function allFolderKeys() {
  return new Set([
    ...collectFolderKeys(regularConflictFiles.value, 'conflicts'),
    ...collectFolderKeys(bothDeletedFiles.value, 'both_deleted'),
    ...collectFolderKeys(stagedFiles.value, 'staged'),
    ...collectFolderKeys(changedFiles.value, 'changed'),
    ...collectFolderKeys(compareFiles.value, 'compare'),
  ])
}

function collapseAllFolders() {
  collapsedFolders.value = allFolderKeys()
  resetVisibleRowLimits()
}

function expandAllFolders() {
  collapsedFolders.value = new Set()
  resetVisibleRowLimits()
}

function initializeLargeListCollapse() {
  if (worktreeFiles.value.length >= GIT_AUTO_COLLAPSE_FILE_COUNT) collapseAllFolders()
}

function selectionRef(scope) {
  if (scope === 'staged') return selectedStagedPaths
  if (scope === 'changed') return selectedChangedPaths
  if (scope === 'both_deleted') return selectedBothDeletedPaths
  throw new Error(`unsupported Git selection scope: ${scope}`)
}

function setSelection(scope, paths, checked) {
  const selection = new Set(selectionRef(scope).value)
  for (const path of paths) checked ? selection.add(path) : selection.delete(path)
  selectionRef(scope).value = selection
}

function toggleFileSelection(scope, file, event) {
  if (file.conflicted && scope !== 'both_deleted') return
  setSelection(scope, [file.path], event.target.checked)
}

function selectableFiles(scope, files) {
  return scope === 'changed' ? files.filter(file => !file.conflicted) : files
}

function toggleFilesSelection(scope, files, event) {
  const targets = selectableFiles(scope, files)
  setSelection(scope, targets.map(file => file.path), event.target.checked)
}

function selectedCount(scope, files) {
  const selection = selectionRef(scope).value
  return selectableFiles(scope, files).filter(file => selection.has(file.path)).length
}

function allFilesSelected(scope, files) {
  const targets = selectableFiles(scope, files)
  return !!targets.length && selectedCount(scope, targets) === targets.length
}

function someFilesSelected(scope, files) {
  const count = selectedCount(scope, files)
  return count > 0 && count < selectableFiles(scope, files).length
}

function toggleFolder(scope, key) {
  const next = new Set(collapsedFolders.value)
  next.has(key) ? next.delete(key) : next.add(key)
  collapsedFolders.value = next
}

function sectionCollapsed(scope) {
  return collapsedSections.value.has(scope)
}

function toggleSection(scope) {
  const next = new Set(collapsedSections.value)
  next.has(scope) ? next.delete(scope) : next.add(scope)
  collapsedSections.value = next
}

function clearSelectedPaths(scope, paths) {
  setSelection(scope, paths, false)
}

function reconcileSelections() {
  const staged = new Set(stagedFiles.value.map(file => file.path))
  const changed = new Set(selectableChangedFiles.value.map(file => file.path))
  const bothDeleted = new Set(bothDeletedFiles.value.map(file => file.path))
  selectedStagedPaths.value = new Set([...selectedStagedPaths.value].filter(path => staged.has(path)))
  selectedChangedPaths.value = new Set([...selectedChangedPaths.value].filter(path => changed.has(path)))
  selectedBothDeletedPaths.value = new Set([...selectedBothDeletedPaths.value].filter(path => bothDeleted.has(path)))
}

function fileIndex(files, file) {
  return Math.max(0, files.findIndex(item => item.path === file.path))
}

async function generateCommitMessage() {
  const selected = props.modelOptions.find(option => option.value === gitCommitModel.value && !option.disabled)
  if (busy.value || props.agentRunning || !stagedFiles.value.length || !selected) return
  busy.value = 'generate'
  error.value = ''
  try {
    const result = await generateSessionGitCommitMessage(props.sessionId, props.language, selected.provider, selected.model)
    commitMessage.value = result?.message || ''
    if (!result?.ai) pushToast('info', result?.notice || props.t.gitCommitGeneratedFallback)
  } catch (cause) {
    error.value = formatError(cause)
  } finally {
    busy.value = ''
  }
}

async function requestResetCommit(commit, hard = false) {
  if (operationsLocked.value || repository.value.detached || repository.value.state || !commit?.hash) return
  if (hard) {
    resetConfirmCommit.value = commit
    resetConfirmOpen.value = true
    return
  }
  const completed = await runOperation('reset_mixed', { commit: commit.hash }, `reset_mixed:${commit.hash}`)
  if (completed && outgoingDialogOpen.value) await openOutgoingDialog()
}

async function confirmHardReset() {
  const commit = resetConfirmCommit.value
  if (!commit?.hash) return
  const completed = await runOperation('reset_hard', { commit: commit.hash }, `reset_hard:${commit.hash}`)
  resetConfirmOpen.value = false
  resetConfirmCommit.value = null
  if (completed && outgoingDialogOpen.value) await openOutgoingDialog()
}

function submitCommit() {
  const message = commitMessage.value.trim()
  if (!message || !stagedFiles.value.length) return
  void runOperation('commit', { message })
}

async function submitCommitAndPush() {
  const message = commitMessage.value.trim()
  if (!message || !stagedFiles.value.length) return
  const completed = await runOperation('commit', { message }, 'commit_and_push')
  if (completed) await openOutgoingDialog()
}

function createBranch() {
  const branch = newBranchName.value.trim()
  if (!branch) return
  void runOperation('create_branch', { branch, startPoint: newBranchStartPoint.value })
}

function openNewBranch(branch) {
  if (operationsLocked.value) return
  newBranchName.value = ''
  newBranchStartPoint.value = branch.fullName || branch.name || 'HEAD'
  showNewBranch.value = true
}

function requestCheckoutBranch(branch, remote = false) {
  if (operationsLocked.value) return
  checkoutConfirmTarget.value = { branch, remote }
  checkoutConfirmOpen.value = true
}

function confirmCheckout() {
  const target = checkoutConfirmTarget.value
  checkoutConfirmOpen.value = false
  checkoutConfirmTarget.value = null
  if (!target) return
  const op = target.remote ? 'checkout_remote' : 'checkout'
  void runOperation(op, { branch: target.branch.name }, `${op}:${target.branch.fullName || target.branch.name}`)
}

function openCreateStash() {
  if (operationsLocked.value || repository.value.hasConflicts || repository.value.state || !worktreeFiles.value.length) return
  stashName.value = ''
  stashIncludeStaged.value = stagedFiles.value.length > 0
  stashIncludeChanged.value = changedFiles.value.length > 0
  stashCreateOpen.value = true
}

async function createStash() {
  const message = stashName.value.trim()
  const paths = selectedStashPaths.value
  if (!message || !paths.length) return
  const completed = await runOperation('stash_create', { message, paths })
  if (!completed) return
  stashCreateOpen.value = false
  stashName.value = ''
}

function requestRestoreStash(stash) {
  if (operationsLocked.value || !stash?.hash) return
  stashRestoreTarget.value = stash
  stashRestoreOpen.value = true
}

async function confirmRestoreStash() {
  const stash = stashRestoreTarget.value
  if (!stash?.hash) return
  const completed = await runOperation('stash_apply', { stashHash: stash.hash }, `stash_apply:${stash.hash}`)
  if (!completed) return
  stashRestoreOpen.value = false
  stashRestoreTarget.value = null
}

function requestDeleteStash(stash) {
  if (operationsLocked.value || !stash?.hash) return
  stashDeleteTarget.value = stash
  stashDeleteOpen.value = true
}

async function confirmDeleteStash() {
  const stash = stashDeleteTarget.value
  if (!stash?.hash) return
  const completed = await runOperation('stash_drop', { stashHash: stash.hash }, `stash_drop:${stash.hash}`)
  if (!completed) return
  stashDeleteOpen.value = false
  stashDeleteTarget.value = null
}

function openDiff(files, index) {
  // 使用列表文件自带的精确对比范围（staged/unstaged/untracked），
  // 缺失时再回退到 worktree；避免未跟踪文件被按 HEAD→工作区对比，
  // 导致整个文件被渲染成新增而与列表里的 +N 计数不符。
  const scope = files?.[index]?.diffScope || ''
  diffDialog.value = { open: true, files, index, scope: scope || 'worktree', commit: '' }
}

function openConflict(files, index) {
  conflictDialog.value = { open: true, files, index }
}

async function onConflictResolved(payload) {
  pushToast('success', payload?.message || props.t.gitConflictResolved)
  conflictDialog.value = { open: false, files: [], index: 0 }
  await refresh(true)
}

function openOutgoingDiff(file) {
  const commit = selectedOutgoingCommit.value
  if (!commit?.hash) return
  diffDialog.value = {
    open: true,
    files: commit.files || [],
    index: Math.max(0, (commit.files || []).findIndex(item => item.path === file.path)),
    scope: 'commit',
    commit: commit.hash,
  }
}

function resetComparePickers() {
  comparePickers.left = { open: false, filter: '', selected: '', root: null, input: null }
  comparePickers.right = { open: false, filter: '', selected: '', root: null, input: null }
  compareTypeFilter.value = ''
  compareError.value = ''
}

async function runCompare() {
  if (compareLoading.value) return
  const nonce = ++compareRequestNonce
  compareLoading.value = true
  compareError.value = ''
  try {
    const result = await compareSessionGitBranches(props.sessionId, comparePickers.left.selected, comparePickers.right.selected)
    if (nonce !== compareRequestNonce || !props.open) return
    compareSnapshot.value = result
    compareLoaded.value = true
    if (result?.left) comparePickers.left.selected = result.left
    if (result?.right) comparePickers.right.selected = result.right
    compareTypeFilter.value = ''
  } catch {
    if (nonce === compareRequestNonce && props.open) compareError.value = props.t.gitCompareFailed
  } finally {
    if (nonce === compareRequestNonce) compareLoading.value = false
  }
}

function toggleCompareSelect(side) {
  if (compareLoading.value) return
  const picker = comparePickers[side]
  if (picker.open) {
    closeCompareSelects()
    return
  }
  closeCompareSelects()
  picker.open = true
  picker.filter = ''
  nextTick(() => picker.input?.focus())
}

function chooseCompareBranch(side, branch) {
  comparePickers[side].selected = branch
  closeCompareSelects()
}

function closeCompareSelects() {
  comparePickers.left.open = false
  comparePickers.right.open = false
  comparePickers.left.filter = ''
  comparePickers.right.filter = ''
}

function closeCompareSelectIfOutside(event) {
  const insideLeft = comparePickers.left.root?.contains(event.target)
  const insideRight = comparePickers.right.root?.contains(event.target)
  if (insideLeft || insideRight) return
  if (comparePickers.left.open || comparePickers.right.open) closeCompareSelects()
}

function onCompareFilterKeydown(side, event) {
  if (event.key === 'Escape') {
    closeCompareSelects()
    return
  }
  if (event.key !== 'Enter') return
  const grouped = filteredCompareOptions.value[side]
  const first = [...grouped.local, ...grouped.remote][0]
  if (first) chooseCompareBranch(side, first.name)
}

function openBranchDiff(files, index) {
  diffDialog.value = {
    open: true,
    files,
    index,
    scope: 'compare',
    commit: '',
    compareLeft: comparePickers.left.selected || compareData.value.left || '',
    compareRight: comparePickers.right.selected || compareData.value.right || '',
  }
}

async function openOutgoingDialog() {
  if (!repository.value.isRepository || repository.value.detached || !repository.value.remotes?.length) return
  const nonce = ++outgoingRequestNonce
  outgoingDialogOpen.value = true
  outgoingLoading.value = true
  outgoingError.value = ''
  outgoingCommits.value = []
  selectedOutgoingHash.value = ''
  await nextTick()
  outgoingDialogRoot.value?.focus()
  try {
    const commits = await getSessionGitOutgoingCommits(props.sessionId)
    if (nonce !== outgoingRequestNonce) return
    outgoingCommits.value = Array.isArray(commits) ? commits : []
    selectedOutgoingHash.value = outgoingCommits.value[0]?.hash || ''
  } catch {
    if (nonce === outgoingRequestNonce) outgoingError.value = props.t.gitOutgoingLoadFailed
  } finally {
    if (nonce === outgoingRequestNonce) outgoingLoading.value = false
  }
}

function closeOutgoingDialog() {
  if (busy.value === 'push') return
  outgoingRequestNonce += 1
  outgoingDialogOpen.value = false
  outgoingLoading.value = false
}

async function pushOutgoingCommits() {
  if (busy.value || props.agentRunning || repository.value.detached || !repository.value.remotes?.length) return
  busy.value = 'push'
  outgoingError.value = ''
  error.value = ''
  let pushed = false
  try {
    const result = await runSessionGitOperation({
      sessionId: props.sessionId,
      op: 'push',
      language: props.language,
      remote: remoteForPush.value,
    })
    pushToast('success', result?.message || props.t.gitOperationCompleted)
    await refresh(true)
    pushed = true
  } catch (cause) {
    outgoingError.value = formatError(cause)
    await refresh(true)
  } finally {
    busy.value = ''
  }
  if (pushed) closeOutgoingDialog()
}

function onOutgoingKeydown(event) {
  if (event.key === 'Escape') closeOutgoingDialog()
}

function handConflictToAgent() {
  emit('resolve-conflicts', {
    state: repository.value.state,
    branch: repository.value.currentBranch,
    root: repository.value.root,
    files: (repository.value.conflicts || []).map(file => file.path),
  })
}

function formatError(cause) {
  const raw = String(cause?.message || cause || '')
  return raw.replace(/^Error:\s*/i, '') || props.t.gitOperationUnknownError
}

function formatDate(timestamp) {
  if (!timestamp) return ''
  try {
    return new Intl.DateTimeFormat(props.language, { month: 'short', day: '2-digit', hour: '2-digit', minute: '2-digit' }).format(new Date(timestamp))
  } catch {
    return ''
  }
}

function formatFullDate(timestamp) {
  if (!timestamp) return ''
  try {
    return new Intl.DateTimeFormat(props.language, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(timestamp))
  } catch {
    return ''
  }
}

function statusLabel(file) {
  if (file.conflicted) {
    const conflictLabels = {
      DD: props.t.gitConflictBothDeleted,
      UU: props.t.gitConflictBothModified,
      AA: props.t.gitConflictBothAdded,
      AU: props.t.gitConflictAddedByUs,
      UA: props.t.gitConflictAddedByThem,
      UD: props.t.gitConflictDeletedByThem,
      DU: props.t.gitConflictDeletedByUs,
    }
    return conflictLabels[file.conflictStatus] || props.t.gitStatusConflicted
  }
  if (isPendingIgnore(file)) return props.t.gitStatusPendingUntrack
  const labels = {
    added: props.t.gitStatusAdded, deleted: props.t.gitStatusDeleted,
    renamed: props.t.gitStatusRenamed, modified: props.t.gitStatusModified,
    untracked: props.t.gitStatusUntracked,
  }
  return labels[file.status] || file.status
}
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="modal-backdrop git-manager-backdrop" @click.self="close">
      <section ref="dialogRoot" class="git-manager" role="dialog" aria-modal="true" :aria-label="t.gitManagerTitle" tabindex="-1" @keydown="onKeydown">
        <header class="git-manager__header">
          <div class="git-manager__identity">
            <span class="git-manager__logo"><GitBranch :size="18" /></span>
            <div>
              <h2>{{ t.gitManagerTitle }}</h2>
              <p :title="repository.root">{{ repository.root || t.gitManagerLoadingRepository }}</p>
            </div>
          </div>
          <div v-if="repository.isRepository" class="git-manager__head-status">
            <span class="git-manager__branch"><GitBranch :size="13" />{{ repository.currentBranch || repository.head || 'HEAD' }}</span>
            <span v-if="repository.ahead > 0 || repository.behind > 0" class="git-manager__sync">
              <span v-if="repository.behind > 0">↓{{ repository.behind }}</span>
              <button
                v-if="repository.ahead > 0"
                type="button"
                :title="t.gitPendingPush.replace('{count}', repository.ahead)"
                :aria-label="t.gitPendingPush.replace('{count}', repository.ahead)"
                @click="openOutgoingDialog"
              >↑{{ repository.ahead }}</button>
            </span>
          </div>
          <button class="git-manager__close" type="button" :title="t.close" :disabled="!!busy" @click="close"><X :size="17" /></button>
        </header>

        <div v-if="repository.warming" class="git-manager__empty">
          <LoaderCircle class="spin" :size="24" />
          <strong>{{ t.gitManagerLoading }}</strong>
        </div>

        <div v-else-if="!loading && !repository.isRepository" class="git-manager__empty">
          <GitBranch :size="30" />
          <strong>{{ t.gitNotRepository }}</strong>
          <p>{{ t.gitNotRepositoryHint }}</p>
        </div>

        <template v-else>
          <div class="git-manager__toolbar">
            <nav class="git-manager__tabs">
              <button :class="{ active: activeTab === 'changes' }" type="button" @click="activeTab = 'changes'">
                {{ t.gitManagerChanges }}<span>{{ worktreeFiles.length }}</span>
              </button>
              <button :class="{ active: activeTab === 'branches' }" type="button" @click="activeTab = 'branches'">
                {{ t.gitManagerBranches }}<span>{{ repository.branches?.length || 0 }}</span>
              </button>
              <button :class="{ active: activeTab === 'compare' }" type="button" @click="activeTab = 'compare'">
                {{ t.gitManagerCompare }}<span>{{ compareFiles.length }}</span>
              </button>
              <button :class="{ active: activeTab === 'history' }" type="button" @click="activeTab = 'history'">
                {{ t.gitManagerHistory }}
              </button>
            </nav>
            <div class="git-manager__remote-actions">
              <button type="button" :title="t.gitFetch" :disabled="operationsLocked || !repository.remotes?.length" @click="runOperation('fetch')">
                <LoaderCircle v-if="busy === 'fetch'" class="spin" :size="14" /><RefreshCw v-else :size="14" />{{ t.gitFetch }}
              </button>
              <button type="button" :title="t.gitPullHint" :disabled="operationsLocked || !repository.upstream" @click="runOperation('pull')">
                <LoaderCircle v-if="busy === 'pull'" class="spin" :size="14" /><Download v-else :size="14" />{{ t.gitPull }}
              </button>
              <button type="button" :title="t.gitPush" :disabled="operationsLocked || !repository.remotes?.length || repository.detached" @click="openOutgoingDialog">
                <Upload :size="14" />{{ t.gitPush }}
              </button>
              <button class="is-icon" type="button" :title="t.refreshChanges" :disabled="!!busy || loading" @click="revalidate(true)">
                <LoaderCircle v-if="loading" class="spin" :size="14" /><RefreshCw v-else :size="14" />
              </button>
            </div>
          </div>

          <div v-if="agentRunning" class="git-manager__running-lock">
            <LoaderCircle class="spin" :size="15" />
            <span><strong>{{ t.gitAgentRunningTitle }}</strong>{{ t.gitAgentRunningHint }}</span>
          </div>
          <div v-if="repository.hasConflicts || repository.state" class="git-manager__conflict">
            <AlertTriangle :size="18" />
            <div>
              <strong>{{ t.gitConflictTitle }}</strong>
              <p>{{ t.gitConflictHint.replace('{count}', repository.conflicts?.length || 0) }}</p>
            </div>
            <button type="button" @click="handConflictToAgent">{{ t.gitConflictAskAgent }}</button>
            <button v-if="repository.state && repository.state !== 'bisect'" class="is-danger" type="button" :disabled="operationsLocked" @click="runOperation('abort')">
              <LoaderCircle v-if="busy === 'abort'" class="spin" :size="14" /><X v-else :size="14" />{{ t.gitAbortOperation }}
            </button>
          </div>
          <p v-if="error" class="git-manager__message is-error" role="alert">{{ error }}</p>

          <div v-if="activeTab === 'changes'" class="git-manager__file-tools">
            <label>
              <Search :size="13" />
              <input v-model="fileFilter" :placeholder="t.gitFilterFiles" />
            </label>
            <span v-if="fileFilter.trim()">{{ t.gitMatchingFiles.replace('{matched}', filteredWorktreeFiles.length).replace('{total}', worktreeFiles.length) }}</span>
            <span v-else>{{ t.gitTotalFiles.replace('{count}', worktreeFiles.length) }}</span>
            <button type="button" :title="t.gitStashCreate" :disabled="operationsLocked || repository.hasConflicts || !!repository.state || !worktreeFiles.length" @click="openCreateStash"><Archive :size="13" />{{ t.gitStashCreate }}</button>
            <button type="button" :title="t.gitCollapseAllFolders" @click="collapseAllFolders"><Folder :size="13" />{{ t.gitCollapseAllFolders }}</button>
            <button type="button" :title="t.gitExpandAllFolders" @click="expandAllFolders"><FolderOpen :size="13" />{{ t.gitExpandAllFolders }}</button>
          </div>

          <div v-if="activeTab === 'changes'" class="git-manager__changes">
            <div class="git-manager__file-pane">
              <section v-if="regularConflictFiles.length" class="git-manager__file-section is-conflicts" :aria-busy="conflictSectionReloading">
                <header>
                  <div class="git-manager__section-title">
                    <button class="git-manager__section-toggle" type="button" :aria-expanded="!sectionCollapsed('conflicts')" :title="sectionCollapsed('conflicts') ? t.gitExpandSection : t.gitCollapseSection" @click="toggleSection('conflicts')">
                      <ChevronRight v-if="sectionCollapsed('conflicts')" :size="14" /><ChevronDown v-else :size="14" />
                    </button>
                    <AlertTriangle :size="15" />
                    <strong>{{ t.gitUnresolvedConflicts }}</strong><span>{{ regularConflictFiles.length }}</span>
                  </div>
                  <div class="git-manager__selection-actions">
                    <button type="button" @click="handConflictToAgent"><Brain :size="13" />{{ t.gitConflictAskAgent }}</button>
                  </div>
                </header>
                <div v-if="!sectionCollapsed('conflicts') && loading" class="git-manager__section-loading" role="status" :aria-label="t.gitManagerLoading"><LoaderCircle class="spin" :size="22" /></div>
                <div v-else-if="!sectionCollapsed('conflicts')" class="git-manager__files">
                  <template v-for="row in visibleConflictTreeRows" :key="row.key">
                    <div v-if="row.kind === 'folder'" class="git-manager__folder" :class="{ 'is-nested': row.depth > 0 }" :style="{ '--tree-depth': row.depth }">
                      <span class="git-manager__tree-spacer"></span>
                      <button class="git-manager__folder-toggle" type="button" :title="collapsedFolders.has(row.key) ? t.gitExpandFolder : t.gitCollapseFolder" @click="toggleFolder('conflicts', row.key)">
                        <ChevronRight v-if="collapsedFolders.has(row.key)" :size="13" /><ChevronDown v-else :size="13" />
                        <Folder v-if="collapsedFolders.has(row.key)" :size="15" /><FolderOpen v-else :size="15" />
                        <strong>{{ row.name }}</strong><span>{{ row.files.length }}</span>
                      </button>
                    </div>
                    <div v-else class="git-manager__file is-conflict" :class="{ 'is-nested': row.depth > 0 }" :style="{ '--tree-depth': row.depth }" @dblclick="openConflict(filteredRegularConflictFiles, fileIndex(filteredRegularConflictFiles, row.file))">
                      <AlertTriangle :size="14" />
                      <span class="git-manager__file-status is-conflicted" :title="`${statusLabel(row.file)} (${row.file.conflictStatus})`">{{ statusLabel(row.file) }}</span>
                      <button class="git-manager__file-name" type="button" :title="row.file.path" @click="openConflict(filteredRegularConflictFiles, fileIndex(filteredRegularConflictFiles, row.file))"><strong>{{ row.file.treeName }}</strong><small>{{ row.file.conflictStatus }}</small></button>
                      <span></span>
                      <div class="git-manager__file-actions">
                        <button class="git-manager__file-action" type="button" :title="t.gitResolveConflictTitle" :disabled="operationsLocked" @click="openConflict(filteredRegularConflictFiles, fileIndex(filteredRegularConflictFiles, row.file))"><Settings2 :size="13" /></button>
                      </div>
                    </div>
                  </template>
                </div>
                <p v-if="!sectionCollapsed('conflicts') && fileFilter.trim() && !filteredRegularConflictFiles.length" class="git-manager__section-empty">{{ t.gitNoMatchingFiles }}</p>
                <button v-if="!sectionCollapsed('conflicts') && remainingRowCount('conflicts', conflictTreeRows)" class="git-manager__show-more" type="button" @click="showMoreRows('conflicts')">{{ t.gitShowMoreRows.replace('{count}', Math.min(GIT_FILE_ROW_BATCH, remainingRowCount('conflicts', conflictTreeRows))) }}</button>
                <div v-if="!sectionCollapsed('conflicts') && conflictSectionReloading" class="git-manager__section-loading is-overlay" role="status" :aria-label="t.gitChangesReloading"><LoaderCircle class="spin" :size="22" /><span>{{ t.gitChangesReloading }}</span></div>
              </section>

              <section v-if="bothDeletedFiles.length" class="git-manager__file-section is-both-deleted" :aria-busy="bothDeletedSectionReloading">
                <header>
                  <div class="git-manager__section-title">
                    <button class="git-manager__section-toggle" type="button" :aria-expanded="!sectionCollapsed('both_deleted')" :title="sectionCollapsed('both_deleted') ? t.gitExpandSection : t.gitCollapseSection" @click="toggleSection('both_deleted')">
                      <ChevronRight v-if="sectionCollapsed('both_deleted')" :size="14" /><ChevronDown v-else :size="14" />
                    </button>
                    <input type="checkbox" :aria-label="t.gitSelectAll" :checked="allFilesSelected('both_deleted', bothDeletedFiles)" :indeterminate="someFilesSelected('both_deleted', bothDeletedFiles)" :disabled="agentRunning || !bothDeletedFiles.length" @change="toggleFilesSelection('both_deleted', bothDeletedFiles, $event)" />
                    <strong>{{ t.gitConflictBothDeleted }}</strong><span>{{ bothDeletedFiles.length }}</span>
                    <em v-if="selectedBothDeletedFiles.length">{{ t.gitSelectedCount.replace('{count}', selectedBothDeletedFiles.length) }}</em>
                  </div>
                  <div class="git-manager__selection-actions">
                    <button class="is-danger" type="button" :disabled="operationsLocked" @click="runBatchFileOperation('resolve_both_deleted', selectedBothDeletedFiles.length ? selectedBothDeletedFiles : bothDeletedFiles, 'both_deleted')">
                      <LoaderCircle v-if="busy === 'resolve_both_deleted:batch'" class="spin" :size="13" /><Trash2 v-else :size="13" />{{ selectedBothDeletedFiles.length ? t.gitConfirmDeletedSelected : t.gitConfirmDeletedAll }}
                    </button>
                  </div>
                </header>
                <p v-if="!sectionCollapsed('both_deleted')" class="git-manager__section-note">{{ t.gitBothDeletedHint }}</p>
                <div v-if="!sectionCollapsed('both_deleted') && loading" class="git-manager__section-loading" role="status" :aria-label="t.gitManagerLoading"><LoaderCircle class="spin" :size="22" /></div>
                <div v-else-if="!sectionCollapsed('both_deleted')" class="git-manager__files">
                  <template v-for="row in visibleBothDeletedTreeRows" :key="row.key">
                    <div v-if="row.kind === 'folder'" class="git-manager__folder" :class="{ 'is-nested': row.depth > 0 }" :style="{ '--tree-depth': row.depth }">
                      <input type="checkbox" :aria-label="`${t.gitSelectAll}: ${row.path}`" :checked="allFilesSelected('both_deleted', row.files)" :indeterminate="someFilesSelected('both_deleted', row.files)" :disabled="agentRunning" @change="toggleFilesSelection('both_deleted', row.files, $event)" />
                      <button class="git-manager__folder-toggle" type="button" :title="collapsedFolders.has(row.key) ? t.gitExpandFolder : t.gitCollapseFolder" @click="toggleFolder('both_deleted', row.key)">
                        <ChevronRight v-if="collapsedFolders.has(row.key)" :size="13" /><ChevronDown v-else :size="13" />
                        <Folder v-if="collapsedFolders.has(row.key)" :size="15" /><FolderOpen v-else :size="15" />
                        <strong>{{ row.name }}</strong><span>{{ row.files.length }}</span>
                      </button>
                    </div>
                    <div v-else class="git-manager__file" :class="{ selected: selectedBothDeletedPaths.has(row.file.path), 'is-nested': row.depth > 0 }" :style="{ '--tree-depth': row.depth }">
                      <input type="checkbox" :aria-label="`${t.gitSelectFile}: ${row.file.path}`" :checked="selectedBothDeletedPaths.has(row.file.path)" :disabled="agentRunning" @change="toggleFileSelection('both_deleted', row.file, $event)" />
                      <span class="git-manager__file-status is-conflicted" :title="`${statusLabel(row.file)} (DD)`">{{ statusLabel(row.file) }}</span>
                      <button class="git-manager__file-name" type="button" :title="row.file.path" @click="setSelection('both_deleted', [row.file.path], !selectedBothDeletedPaths.has(row.file.path))"><strong>{{ row.file.treeName }}</strong><small>DD</small></button>
                      <span></span>
                      <div class="git-manager__file-actions">
                        <button class="git-manager__file-action is-danger" type="button" :title="t.gitConfirmDeletedResult" :disabled="operationsLocked" @click="runBatchFileOperation('resolve_both_deleted', [row.file], 'both_deleted')"><Trash2 :size="13" /></button>
                      </div>
                    </div>
                  </template>
                </div>
                <p v-if="!sectionCollapsed('both_deleted') && fileFilter.trim() && !filteredBothDeletedFiles.length" class="git-manager__section-empty">{{ t.gitNoMatchingFiles }}</p>
                <button v-if="!sectionCollapsed('both_deleted') && remainingRowCount('both_deleted', bothDeletedTreeRows)" class="git-manager__show-more" type="button" @click="showMoreRows('both_deleted')">{{ t.gitShowMoreRows.replace('{count}', Math.min(GIT_FILE_ROW_BATCH, remainingRowCount('both_deleted', bothDeletedTreeRows))) }}</button>
                <div v-if="!sectionCollapsed('both_deleted') && bothDeletedSectionReloading" class="git-manager__section-loading is-overlay" role="status" :aria-label="t.gitChangesReloading"><LoaderCircle class="spin" :size="22" /><span>{{ t.gitChangesReloading }}</span></div>
              </section>

              <section v-if="repository.stashes?.length" class="git-manager__file-section is-stashes">
                <header>
                  <div class="git-manager__section-title">
                    <button class="git-manager__section-toggle" type="button" :aria-expanded="!sectionCollapsed('stashes')" :title="sectionCollapsed('stashes') ? t.gitExpandSection : t.gitCollapseSection" @click="toggleSection('stashes')">
                      <ChevronRight v-if="sectionCollapsed('stashes')" :size="14" /><ChevronDown v-else :size="14" />
                    </button>
                    <Archive :size="15" />
                    <strong>{{ t.gitStashSection }}</strong><span>{{ repository.stashes.length }}</span>
                  </div>
                </header>
                <div v-if="!sectionCollapsed('stashes')" class="git-manager__files git-manager__stash-list">
                  <article v-for="stash in repository.stashes" :key="stash.hash" class="git-manager__stash-item">
                    <Archive :size="15" />
                    <div>
                      <strong :title="stash.name">{{ stash.name }}</strong>
                      <small>{{ stash.branch || t.gitStashUnknownBranch }} · {{ formatDate(stash.timestamp) }}</small>
                    </div>
                    <code>{{ stash.ref }}</code>
                    <button type="button" :disabled="operationsLocked || !!repository.state" @click="requestRestoreStash(stash)">
                      <LoaderCircle v-if="busy === `stash_apply:${stash.hash}`" class="spin" :size="13" /><ArchiveRestore v-else :size="13" />{{ t.gitStashRestore }}
                    </button>
                    <button class="is-danger" type="button" :disabled="operationsLocked" @click="requestDeleteStash(stash)">
                      <LoaderCircle v-if="busy === `stash_drop:${stash.hash}`" class="spin" :size="13" /><Trash2 v-else :size="13" />{{ t.delete }}
                    </button>
                  </article>
                </div>
              </section>

              <section class="git-manager__file-section is-staged" :aria-busy="stagedSectionReloading">
                <header>
                  <div class="git-manager__section-title">
                    <button class="git-manager__section-toggle" type="button" :aria-expanded="!sectionCollapsed('staged')" :title="sectionCollapsed('staged') ? t.gitExpandSection : t.gitCollapseSection" @click="toggleSection('staged')">
                      <ChevronRight v-if="sectionCollapsed('staged')" :size="14" /><ChevronDown v-else :size="14" />
                    </button>
                    <input
                      type="checkbox"
                      :aria-label="t.gitSelectAll"
                      :checked="allFilesSelected('staged', stagedFiles)"
                      :indeterminate="someFilesSelected('staged', stagedFiles)"
                      :disabled="agentRunning || !stagedFiles.length"
                      @change="toggleFilesSelection('staged', stagedFiles, $event)"
                    />
                    <strong>{{ t.gitStagedChanges }}</strong><span>{{ stagedFiles.length }}</span>
                    <em v-if="selectedStagedFiles.length">{{ t.gitSelectedCount.replace('{count}', selectedStagedFiles.length) }}</em>
                  </div>
                  <div class="git-manager__selection-actions">
                    <button v-if="selectedStagedFiles.length" type="button" :disabled="operationsLocked" @click="runBatchFileOperation('unstage', selectedStagedFiles, 'staged')">
                      <LoaderCircle v-if="busy === 'unstage:batch'" class="spin" :size="13" /><X v-else :size="13" />{{ t.gitUnstageSelected }}
                    </button>
                    <button v-else type="button" :disabled="operationsLocked || !stagedFiles.length" @click="runOperation('unstage_all')">
                      <LoaderCircle v-if="busy === 'unstage_all'" class="spin" :size="13" /><X v-else :size="13" />{{ t.gitUnstageAll }}
                    </button>
                  </div>
                </header>
                <div v-if="!sectionCollapsed('staged') && loading" class="git-manager__section-loading" role="status" :aria-label="t.gitManagerLoading">
                  <LoaderCircle class="spin" :size="22" />
                </div>
                <div v-else-if="!sectionCollapsed('staged') && stagedFiles.length" class="git-manager__files">
                  <template v-for="row in visibleStagedTreeRows" :key="row.key">
                    <div v-if="row.kind === 'folder'" class="git-manager__folder" :class="{ 'is-nested': row.depth > 0 }" :style="{ '--tree-depth': row.depth }">
                      <input
                        type="checkbox"
                        :aria-label="`${t.gitSelectAll}: ${row.path}`"
                        :checked="allFilesSelected('staged', row.files)"
                        :indeterminate="someFilesSelected('staged', row.files)"
                        :disabled="agentRunning || !row.files.length"
                        @change="toggleFilesSelection('staged', row.files, $event)"
                      />
                      <button class="git-manager__folder-toggle" type="button" :title="collapsedFolders.has(row.key) ? t.gitExpandFolder : t.gitCollapseFolder" @click="toggleFolder('staged', row.key)">
                        <ChevronRight v-if="collapsedFolders.has(row.key)" :size="13" /><ChevronDown v-else :size="13" />
                        <Folder v-if="collapsedFolders.has(row.key)" :size="15" /><FolderOpen v-else :size="15" />
                        <strong>{{ row.name }}</strong><span>{{ row.files.length }}</span>
                      </button>
                      <em v-if="folderPendingIgnore(row.files)" class="git-manager__folder-state">{{ t.gitStatusPendingUntrack }}</em>
                      <button class="git-manager__more-trigger" :class="{ 'is-busy': busy === `ignore:${row.path}` }" type="button" :title="t.gitMoreActions" :aria-label="`${t.gitMoreActions}: ${row.path}`" :disabled="operationsLocked || row.files.some(file => file.conflicted)" @click.stop="openMoreMenu($event, { path: row.path, isDirectory: true, conflicted: row.files.some(file => file.conflicted), ignorePending: folderPendingIgnore(row.files), pendingCommit: row.files.some(needsUntrackCommit) })">
                        <LoaderCircle v-if="busy === `ignore:${row.path}`" class="spin" :size="14" /><Ellipsis v-else :size="16" />
                      </button>
                    </div>
                    <div v-else class="git-manager__file" :class="{ selected: selectedStagedPaths.has(row.file.path), 'is-nested': row.depth > 0, 'has-busy-action': fileOperationBusy(row.file.path) }" :style="{ '--tree-depth': row.depth }" @dblclick="openDiff(filteredStagedFiles, fileIndex(filteredStagedFiles, row.file))">
                      <input type="checkbox" :aria-label="`${t.gitSelectFile}: ${row.file.path}`" :checked="selectedStagedPaths.has(row.file.path)" :disabled="agentRunning" @change="toggleFileSelection('staged', row.file, $event)" />
                      <span class="git-manager__file-status" :class="[`is-${row.file.status}`, { 'is-pending-untrack': isPendingIgnore(row.file) }]" :title="statusLabel(row.file)">{{ statusLabel(row.file) }}</span>
                      <button class="git-manager__file-name" type="button" :title="row.file.path" @click="openDiff(filteredStagedFiles, fileIndex(filteredStagedFiles, row.file))"><strong>{{ row.file.treeName }}</strong></button>
                      <span class="git-manager__numbers"><b>+{{ row.file.added || 0 }}</b><i>-{{ row.file.deleted || 0 }}</i></span>
                      <div class="git-manager__file-actions">
                        <button class="git-manager__file-action" type="button" :title="t.gitUnstage" :disabled="operationsLocked" @click="runFileOperation('unstage', row.file)">
                          <LoaderCircle v-if="busy === `unstage:${row.file.path}`" class="spin" :size="13" /><X v-else :size="13" />
                        </button>
                        <button class="git-manager__file-action" type="button" :title="t.gitMoreActions" :aria-label="`${t.gitMoreActions}: ${row.file.path}`" :disabled="operationsLocked || row.file.conflicted" @click.stop="openMoreMenu($event, { path: row.file.path, isDirectory: false, conflicted: row.file.conflicted, ignorePending: isPendingIgnore(row.file), pendingCommit: needsUntrackCommit(row.file) })">
                          <LoaderCircle v-if="busy === `ignore:${row.file.path}`" class="spin" :size="14" /><Ellipsis v-else :size="16" />
                        </button>
                      </div>
                    </div>
                  </template>
                </div>
                <p v-if="!sectionCollapsed('staged') && fileFilter.trim() && stagedFiles.length && !filteredStagedFiles.length" class="git-manager__section-empty">{{ t.gitNoMatchingFiles }}</p>
                <button v-if="!sectionCollapsed('staged') && remainingRowCount('staged', stagedTreeRows)" class="git-manager__show-more" type="button" @click="showMoreRows('staged')">{{ t.gitShowMoreRows.replace('{count}', Math.min(GIT_FILE_ROW_BATCH, remainingRowCount('staged', stagedTreeRows))) }}</button>
                <p v-if="!sectionCollapsed('staged') && !stagedFiles.length" class="git-manager__section-empty">{{ t.gitNoStagedChanges }}</p>
                <div v-if="!sectionCollapsed('staged') && stagedSectionReloading" class="git-manager__section-loading is-overlay" role="status" :aria-label="t.gitChangesReloading">
                  <LoaderCircle class="spin" :size="22" />
                  <span>{{ t.gitChangesReloading }}</span>
                </div>
              </section>

              <section class="git-manager__file-section is-changed" :aria-busy="changedSectionReloading">
                <header>
                  <div class="git-manager__section-title">
                    <button class="git-manager__section-toggle" type="button" :aria-expanded="!sectionCollapsed('changed')" :title="sectionCollapsed('changed') ? t.gitExpandSection : t.gitCollapseSection" @click="toggleSection('changed')">
                      <ChevronRight v-if="sectionCollapsed('changed')" :size="14" /><ChevronDown v-else :size="14" />
                    </button>
                    <input
                      type="checkbox"
                      :aria-label="t.gitSelectAll"
                      :checked="allFilesSelected('changed', selectableChangedFiles)"
                      :indeterminate="someFilesSelected('changed', selectableChangedFiles)"
                      :disabled="agentRunning || !selectableChangedFiles.length"
                      @change="toggleFilesSelection('changed', selectableChangedFiles, $event)"
                    />
                    <strong>{{ t.gitUnstagedChanges }}</strong><span>{{ changedFiles.length }}</span>
                    <em v-if="selectedChangedFiles.length">{{ t.gitSelectedCount.replace('{count}', selectedChangedFiles.length) }}</em>
                  </div>
                  <div class="git-manager__selection-actions">
                    <template v-if="selectedChangedFiles.length">
                      <button class="git-manager__stage-selected" type="button" :disabled="operationsLocked" @click="runBatchFileOperation('stage', selectedChangedFiles, 'changed')">
                        <LoaderCircle v-if="busy === 'stage:batch'" class="spin" :size="13" /><Plus v-else :size="13" />{{ t.gitStageSelected }}
                      </button>
                      <button v-if="selectedTrackedChangedFiles.length" class="is-danger" type="button" :disabled="operationsLocked" @click="requestDestructiveBatchOperation('discard_tracked', selectedTrackedChangedFiles)">
                        <LoaderCircle v-if="busy === 'discard_tracked:batch'" class="spin" :size="13" /><RotateCcw v-else :size="13" />{{ t.gitRevertSelected }}
                      </button>
                      <button v-if="selectedUntrackedFiles.length" class="is-danger" type="button" :disabled="operationsLocked" @click="requestDestructiveBatchOperation('delete_untracked', selectedUntrackedFiles)">
                        <LoaderCircle v-if="busy === 'delete_untracked:batch'" class="spin" :size="13" /><Trash2 v-else :size="13" />{{ t.gitDeleteSelected }}
                      </button>
                    </template>
                    <button v-else type="button" :disabled="operationsLocked || !changedFiles.length || repository.hasConflicts" @click="runOperation('stage_all')">
                      <LoaderCircle v-if="busy === 'stage_all'" class="spin" :size="13" /><Plus v-else :size="13" />{{ t.gitStageAll }}
                    </button>
                  </div>
                </header>
                <div v-if="!sectionCollapsed('changed') && loading" class="git-manager__section-loading" role="status" :aria-label="t.gitManagerLoading">
                  <LoaderCircle class="spin" :size="22" />
                </div>
                <div v-else-if="!sectionCollapsed('changed') && changedFiles.length" class="git-manager__files">
                  <template v-for="row in visibleChangedTreeRows" :key="row.key">
                    <div v-if="row.kind === 'folder'" class="git-manager__folder" :class="{ 'is-nested': row.depth > 0 }" :style="{ '--tree-depth': row.depth }">
                      <input
                        type="checkbox"
                        :aria-label="`${t.gitSelectAll}: ${row.path}`"
                        :checked="allFilesSelected('changed', row.files)"
                        :indeterminate="someFilesSelected('changed', row.files)"
                        :disabled="agentRunning || !selectableFiles('changed', row.files).length"
                        @change="toggleFilesSelection('changed', row.files, $event)"
                      />
                      <button class="git-manager__folder-toggle" type="button" :title="collapsedFolders.has(row.key) ? t.gitExpandFolder : t.gitCollapseFolder" @click="toggleFolder('changed', row.key)">
                        <ChevronRight v-if="collapsedFolders.has(row.key)" :size="13" /><ChevronDown v-else :size="13" />
                        <Folder v-if="collapsedFolders.has(row.key)" :size="15" /><FolderOpen v-else :size="15" />
                        <strong>{{ row.name }}</strong><span>{{ row.files.length }}</span>
                      </button>
                      <em v-if="folderPendingIgnore(row.files)" class="git-manager__folder-state">{{ t.gitStatusPendingUntrack }}</em>
                      <button class="git-manager__more-trigger" :class="{ 'is-busy': busy === `ignore:${row.path}` }" type="button" :title="t.gitMoreActions" :aria-label="`${t.gitMoreActions}: ${row.path}`" :disabled="operationsLocked || row.files.some(file => file.conflicted)" @click.stop="openMoreMenu($event, { path: row.path, isDirectory: true, conflicted: row.files.some(file => file.conflicted), ignorePending: folderPendingIgnore(row.files), pendingCommit: row.files.some(needsUntrackCommit) })">
                        <LoaderCircle v-if="busy === `ignore:${row.path}`" class="spin" :size="14" /><Ellipsis v-else :size="16" />
                      </button>
                    </div>
                    <div v-else class="git-manager__file" :class="{ selected: selectedChangedPaths.has(row.file.path), 'is-nested': row.depth > 0, 'has-busy-action': fileOperationBusy(row.file.path) }" :style="{ '--tree-depth': row.depth }" @dblclick="openDiff(filteredChangedFiles, fileIndex(filteredChangedFiles, row.file))">
                      <input type="checkbox" :aria-label="`${t.gitSelectFile}: ${row.file.path}`" :checked="selectedChangedPaths.has(row.file.path)" :disabled="agentRunning || row.file.conflicted" @change="toggleFileSelection('changed', row.file, $event)" />
                      <span class="git-manager__file-status" :class="[`is-${row.file.status}`, { 'is-pending-untrack': isPendingIgnore(row.file) }]" :title="statusLabel(row.file)">{{ statusLabel(row.file) }}</span>
                      <button class="git-manager__file-name" type="button" :title="row.file.path" @click="openDiff(filteredChangedFiles, fileIndex(filteredChangedFiles, row.file))"><strong>{{ row.file.treeName }}</strong></button>
                      <span class="git-manager__numbers"><b>+{{ row.file.added || 0 }}</b><i>-{{ row.file.deleted || 0 }}</i></span>
                      <div class="git-manager__file-actions">
                        <template v-if="!row.file.conflicted">
                          <button class="git-manager__file-action" type="button" :title="t.gitStage" :aria-label="`${t.gitStage}: ${row.file.path}`" :disabled="operationsLocked" @click="runFileOperation('stage', row.file)">
                            <LoaderCircle v-if="busy === `stage:${row.file.path}`" class="spin" :size="13" /><Plus v-else :size="13" />
                          </button>
                          <button
                            class="git-manager__file-action is-danger"
                            type="button"
                            :title="row.file.untracked ? t.gitDeleteFile : t.gitRevertFile"
                            :aria-label="`${row.file.untracked ? t.gitDeleteFile : t.gitRevertFile}: ${row.file.path}`"
                            :disabled="operationsLocked"
                            @click="requestDestructiveFileOperation(row.file)"
                          >
                            <LoaderCircle v-if="busy === `${row.file.untracked ? 'delete_untracked' : 'discard_tracked'}:${row.file.path}`" class="spin" :size="13" />
                            <Trash2 v-else-if="row.file.untracked" :size="13" />
                            <RotateCcw v-else :size="13" />
                          </button>
                        </template>
                        <button class="git-manager__file-action" type="button" :title="t.gitMoreActions" :aria-label="`${t.gitMoreActions}: ${row.file.path}`" :disabled="operationsLocked || row.file.conflicted" @click.stop="openMoreMenu($event, { path: row.file.path, isDirectory: false, conflicted: row.file.conflicted, ignorePending: isPendingIgnore(row.file), pendingCommit: needsUntrackCommit(row.file) })">
                          <LoaderCircle v-if="busy === `ignore:${row.file.path}`" class="spin" :size="14" /><Ellipsis v-else :size="16" />
                        </button>
                      </div>
                    </div>
                  </template>
                </div>
                <p v-if="!sectionCollapsed('changed') && fileFilter.trim() && changedFiles.length && !filteredChangedFiles.length" class="git-manager__section-empty">{{ t.gitNoMatchingFiles }}</p>
                <button v-if="!sectionCollapsed('changed') && remainingRowCount('changed', changedTreeRows)" class="git-manager__show-more" type="button" @click="showMoreRows('changed')">{{ t.gitShowMoreRows.replace('{count}', Math.min(GIT_FILE_ROW_BATCH, remainingRowCount('changed', changedTreeRows))) }}</button>
                <p v-if="!sectionCollapsed('changed') && !changedFiles.length" class="git-manager__section-empty">{{ t.gitCleanWorktree }}</p>
                <div v-if="!sectionCollapsed('changed') && changedSectionReloading" class="git-manager__section-loading is-overlay" role="status" :aria-label="t.gitChangesReloading">
                  <LoaderCircle class="spin" :size="22" />
                  <span>{{ t.gitChangesReloading }}</span>
                </div>
              </section>
            </div>

            <aside class="git-manager__commit-pane">
              <div class="git-manager__commit-title">
                <div><GitCommitHorizontal :size="16" /><strong>{{ t.gitCreateCommit }}</strong></div>
                <span>{{ t.gitCommitOnlyStaged }}</span>
              </div>
              <div class="git-manager__commit-editor">
                <textarea v-model="commitMessage" :placeholder="t.gitCommitPlaceholder" :disabled="agentRunning || busy === 'commit' || busy === 'commit_and_push' || !stagedFiles.length" maxlength="8000"></textarea>
                <div class="git-manager__commit-tools">
                  <div class="git-manager__commit-tools-left">
                    <small v-if="!enabledCommitModels.length" class="git-manager__model-empty">{{ t.gitCommitModelUnavailable }}</small>
                  </div>
                  <div class="git-manager__commit-tools-right">
                    <div ref="commitModelWrapEl" class="git-manager__commit-model-wrap">
                      <button class="git-manager__commit-model-btn" type="button" :disabled="operationsLocked || !enabledCommitModels.length" :title="t.gitCommitModel" @click="commitModelMenuOpen = !commitModelMenuOpen">
                        <Brain :size="14" class="git-manager__commit-model-btn__icon" />
                        <span class="git-manager__commit-model-btn__label">{{ selectedCommitModelLabel }}</span>
                        <ChevronDown :size="13" :class="{ 'git-manager__commit-model-btn__arrow--open': commitModelMenuOpen }" />
                      </button>
                      <div v-if="commitModelMenuOpen" class="git-manager__commit-model-menu">
                        <div class="git-manager__commit-model-menu__pop">
                          <template v-for="group in commitModelGroups" :key="group.provider">
                            <div class="git-manager__commit-model-menu__label">{{ group.provider }}</div>
                            <button
                              v-for="option in group.options"
                              :key="option.value"
                              type="button"
                              class="git-manager__commit-model-menu__item"
                              :class="{ 'git-manager__commit-model-menu__item--active': option.value === gitCommitModel, 'git-manager__commit-model-menu__item--disabled': option.disabled }"
                              :disabled="option.disabled"
                              @click="selectCommitModel(option)"
                            >
                              <span>{{ option.model }}</span>
                              <small v-if="option.disabled">{{ option.disabledLabel }}</small>
                              <Check v-if="option.value === gitCommitModel" :size="14" />
                            </button>
                          </template>
                        </div>
                      </div>
                    </div>
                    <button class="git-manager__prompt-button" type="button" :title="t.gitEditPrompt" :aria-label="t.gitEditPrompt" :disabled="operationsLocked" @click="commitPromptOpen = true">
                      <Settings2 :size="14" />
                    </button>
                    <button class="git-manager__generate-button" type="button" :title="t.gitGenerateCommit" :aria-label="t.gitGenerateCommit" :disabled="operationsLocked || !stagedFiles.length || !gitCommitModel" @click="generateCommitMessage">
                      <LoaderCircle v-if="busy === 'generate'" class="spin" :size="14" /><Sparkles v-else :size="14" />
                    </button>
                  </div>
                </div>
              </div>
              <div class="git-manager__commit-footer">
                <button class="git-manager__commit-button" type="button" :disabled="operationsLocked || !stagedFiles.length || !commitMessage.trim() || repository.hasConflicts" @click="submitCommit">
                  <LoaderCircle v-if="busy === 'commit'" class="spin" :size="15" /><GitCommitHorizontal v-else :size="15" />{{ t.gitCommit }}
                </button>
                <button class="git-manager__commit-push-button" type="button" :title="t.gitCommitAndPushHint" :disabled="operationsLocked || !stagedFiles.length || !commitMessage.trim() || repository.hasConflicts || repository.detached || !repository.remotes?.length" @click="submitCommitAndPush">
                  <LoaderCircle v-if="busy === 'commit_and_push'" class="spin" :size="15" /><Upload v-else :size="15" />{{ t.gitCommitAndPush }}
                </button>
              </div>
              <dl class="git-manager__repo-meta">
                <div><dt>{{ t.gitCurrentBranch }}</dt><dd>{{ repository.currentBranch || 'HEAD' }}</dd></div>
                <div><dt>{{ t.gitUpstream }}</dt><dd>{{ repository.upstream || t.gitNoUpstream }}</dd></div>
                <div v-for="remote in repository.remotes" :key="remote.name"><dt>{{ remote.name }}</dt><dd :title="remote.fetchUrl">{{ remote.fetchUrl }}</dd></div>
              </dl>
            </aside>
          </div>

          <div v-else-if="activeTab === 'branches'" class="git-manager__branches">
            <div class="git-manager__branch-tools">
              <label><Search :size="14" /><input v-model="branchFilter" :placeholder="t.gitFilterBranches" /></label>
            </div>
            <div v-if="showNewBranch" class="git-manager__new-branch">
              <input v-model="newBranchName" :placeholder="t.gitNewBranchPlaceholder" :disabled="busy === 'create_branch'" @keydown.enter="createBranch" />
              <span>{{ t.gitBranchFrom }} {{ newBranchStartPoint }}<template v-if="worktreeFiles.length"> · {{ t.gitBranchAutoStashHint }}</template></span>
              <button class="git-manager__new-branch-confirm" type="button" :disabled="operationsLocked || !newBranchName.trim()" @click="createBranch">
                <LoaderCircle v-if="busy === 'create_branch'" class="spin" :size="13" /><Plus v-else :size="13" />{{ t.gitCreateBranchConfirm }}
              </button>
              <button type="button" @click="showNewBranch = false; newBranchStartPoint = 'HEAD'">{{ t.cancel }}</button>
            </div>
            <div class="git-manager__branch-columns">
              <section>
                <header><strong>{{ t.gitLocalBranches }}</strong><span>{{ localBranches.length }}</span></header>
                <div class="git-manager__branch-list">
                  <div v-for="branch in localBranches" :key="branch.fullName" class="git-manager__branch-item" :class="{ current: branch.current }">
                    <LoaderCircle v-if="busy === `checkout:${branch.fullName || branch.name}`" class="spin" :size="15" /><GitBranch v-else :size="15" />
                    <span class="git-manager__branch-item-name"><strong :title="branch.name">{{ branch.name }}</strong><small>{{ branch.subject }}</small></span>
                    <em v-if="branch.current">{{ t.gitCurrent }}</em>
                    <em v-else-if="branch.worktreePath">{{ t.gitInOtherWorktree }}</em>
                    <em v-else-if="branch.upstream && (branch.behind > 0 || branch.ahead > 0)"><span v-if="branch.behind > 0">↓{{ branch.behind }}</span><span v-if="branch.ahead > 0">↑{{ branch.ahead }}</span></em>
                    <span class="git-manager__branch-actions">
                      <button class="git-manager__branch-new" type="button" :disabled="operationsLocked" @click="openNewBranch(branch)"><Plus :size="12" />{{ t.gitNewBranch }}</button>
                      <button v-if="!branch.current" class="git-manager__branch-checkout" type="button" :disabled="operationsLocked || (branch.worktreePath && branch.worktreePath !== repository.worktreePath)" @click="requestCheckoutBranch(branch)">{{ t.gitCheckout }}</button>
                    </span>
                  </div>
                </div>
              </section>
              <section>
                <header><strong>{{ t.gitRemoteBranches }}</strong><span>{{ remoteBranches.length }}</span></header>
                <div class="git-manager__branch-list">
                  <div v-for="branch in remoteBranches" :key="branch.fullName" class="git-manager__branch-item">
                    <LoaderCircle v-if="busy === `checkout_remote:${branch.fullName || branch.name}`" class="spin" :size="15" /><GitBranch v-else :size="15" />
                    <span class="git-manager__branch-item-name"><strong :title="branch.name">{{ branch.name }}</strong><small>{{ branch.subject }}</small></span>
                    <em>{{ branch.sha }}</em>
                    <span class="git-manager__branch-actions">
                      <button class="git-manager__branch-new" type="button" :disabled="operationsLocked" @click="openNewBranch(branch)"><Plus :size="12" />{{ t.gitNewBranch }}</button>
                      <button class="git-manager__branch-checkout" type="button" :disabled="operationsLocked" @click="requestCheckoutBranch(branch, true)">{{ t.gitCheckout }}</button>
                    </span>
                  </div>
                </div>
              </section>
            </div>
          </div>

          <div v-else-if="activeTab === 'history'" class="git-manager__history">
            <header><strong>{{ t.gitRecentCommits }}</strong><span>{{ t.gitRecentCommitsHint }}</span></header>
            <div v-if="repository.commits?.length" class="git-manager__commit-list">
              <article v-for="commit in repository.commits" :key="commit.hash">
                <span class="git-manager__graph-dot"></span>
                <div><strong>{{ commit.subject }}</strong><small>{{ commit.author }} · {{ formatDate(commit.timestamp) }}</small></div>
                <span v-if="commit.decorations" class="git-manager__decorations">{{ commit.decorations }}</span>
                <code>{{ commit.shortHash }}</code>
                <button
                  class="git-manager__commit-more"
                  type="button"
                  :title="t.gitMoreActions"
                  :aria-label="`${t.gitMoreActions}: ${commit.shortHash}`"
                  aria-haspopup="menu"
                  :aria-expanded="moreMenu?.key === `commit:${commit.hash}`"
                  :disabled="operationsLocked || repository.detached || !!repository.state"
                  @click.stop="openCommitMoreMenu($event, commit)"
                >
                  <LoaderCircle v-if="busy === `reset_mixed:${commit.hash}` || busy === `reset_hard:${commit.hash}`" class="spin" :size="14" /><Ellipsis v-else :size="16" />
                </button>
              </article>
            </div>
            <p v-else class="git-manager__section-empty">{{ t.gitNoCommits }}</p>
          </div>

          <div v-else-if="activeTab === 'compare'" class="git-manager__compare">
            <div v-if="compareLoading && !compareLoaded" class="git-manager__empty">
              <LoaderCircle class="spin" :size="24" />
            </div>
            <div v-else-if="!compareData.isRepository" class="git-manager__empty">
              <GitBranch :size="30" />
              <strong>{{ t.gitNotRepository }}</strong>
              <p>{{ t.gitNotRepositoryHint }}</p>
            </div>
            <template v-else>
              <header class="git-manager__compare-header">
                <div class="git-manager__compare-identity">
                  <GitBranch :size="15" />
                  <strong>{{ t.gitManagerCompare }}</strong>
                </div>
                <div class="git-manager__compare-pickers">
                  <label v-for="side in compareSides" :key="side.key" class="git-base-picker">
                    <span class="git-base-picker__label">{{ t[side.labelKey] }}</span>
                    <div
                      :ref="el => comparePickers[side.key].root = el"
                      class="git-base-select"
                      :class="{ 'is-open': comparePickers[side.key].open, 'is-disabled': compareLoading }"
                    >
                      <button
                        type="button"
                        class="git-base-select__trigger"
                        :disabled="compareLoading"
                        :title="(comparePickers[side.key].selected || compareData[side.key]) || t.gitComparePlaceholder"
                        @click="toggleCompareSelect(side.key)"
                      >
                        <span class="git-base-select__value">{{ comparePickers[side.key].selected || compareData[side.key] || t.gitComparePlaceholder }}</span>
                        <span class="git-base-select__caret"></span>
                      </button>
                      <div v-if="comparePickers[side.key].open" class="git-base-select__pop">
                        <input
                          :ref="el => comparePickers[side.key].input = el"
                          v-model="comparePickers[side.key].filter"
                          class="git-base-select__filter"
                          type="text"
                          :placeholder="t.gitBaseFilterPlaceholder"
                          @keydown.stop="onCompareFilterKeydown(side.key, $event)"
                        />
                        <template v-if="filteredCompareOptions[side.key].local.length || filteredCompareOptions[side.key].remote.length">
                          <div v-if="filteredCompareOptions[side.key].local.length" class="git-base-select__group">{{ t.gitCompareLocal }}</div>
                          <ul v-if="filteredCompareOptions[side.key].local.length" class="git-base-select__list">
                            <li
                              v-for="branch in filteredCompareOptions[side.key].local"
                              :key="`local-${branch.name}`"
                              class="git-base-select__option"
                              :class="{ 'is-active': (comparePickers[side.key].selected || compareData[side.key]) === branch.name }"
                              :title="branch.name"
                              @click="chooseCompareBranch(side.key, branch.name)"
                            >{{ branch.name }}</li>
                          </ul>
                          <div v-if="filteredCompareOptions[side.key].remote.length" class="git-base-select__group">{{ t.gitCompareRemote }}</div>
                          <ul v-if="filteredCompareOptions[side.key].remote.length" class="git-base-select__list">
                            <li
                              v-for="branch in filteredCompareOptions[side.key].remote"
                              :key="`remote-${branch.name}`"
                              class="git-base-select__option"
                              :class="{ 'is-active': (comparePickers[side.key].selected || compareData[side.key]) === branch.name }"
                              :title="branch.name"
                              @click="chooseCompareBranch(side.key, branch.name)"
                            >{{ branch.name }}</li>
                          </ul>
                        </template>
                        <p v-else class="git-base-select__empty">{{ t.gitBaseNoMatch }}</p>
                      </div>
                    </div>
                  </label>
                  <span class="git-manager__compare-arrow" aria-hidden="true">→</span>
                  <button
                    class="git-manager__compare-action"
                    type="button"
                    :disabled="compareLoading || !hasCompareBranches || (comparePickers.left.selected && comparePickers.right.selected && comparePickers.left.selected === comparePickers.right.selected)"
                    @click="runCompare"
                  >
                    <LoaderCircle v-if="compareLoading" class="spin" :size="13" /><GitBranch v-else :size="13" />{{ t.gitCompareButton }}
                  </button>
                </div>
              </header>

              <div class="git-manager__compare-summary">
                <span v-if="compareLoaded && compareData.left">
                  <span class="git-manager__compare-arrow">{{ compareData.left }} → {{ compareData.right }}</span>
                  <span class="git-manager__compare-ahead-behind">{{ t.gitAheadBehind.replace('{ahead}', compareData.ahead || 0).replace('{behind}', compareData.behind || 0) }}</span>
                </span>
                <span v-else>{{ t.gitCompareEmpty }}</span>
                <span class="git-manager__compare-num git-manager__compare-num--added">+{{ compareData.added || 0 }}</span>
                <span class="git-manager__compare-num git-manager__compare-num--deleted">-{{ compareData.deleted || 0 }}</span>
              </div>

              <div v-if="compareLoaded && compareFiles.length" class="git-manager__compare-filters">
                <button
                  v-for="option in compareTypeFilterOptions"
                  :key="option.key"
                  type="button"
                  class="git-manager__compare-filter"
                  :class="{ 'is-active': compareTypeFilter === option.key }"
                  @click="compareTypeFilter = option.key"
                >{{ t[option.labelKey] }}<span>{{ option.count }}</span></button>
              </div>

              <div class="git-manager__compare-body">
                <p v-if="compareError" class="git-manager__compare-error">{{ compareError }}</p>
                <div v-if="visibleCompareTreeRows.length" class="git-manager__files is-compare">
                  <template v-for="row in visibleCompareTreeRows" :key="row.key">
                    <div v-if="row.kind === 'folder'" class="git-manager__folder" :class="{ 'is-nested': row.depth > 0 }" :style="{ '--tree-depth': row.depth }">
                      <button class="git-manager__folder-toggle" type="button" :title="collapsedFolders.has(row.key) ? t.gitExpandFolder : t.gitCollapseFolder" @click="toggleFolder('compare', row.key)">
                        <ChevronRight v-if="collapsedFolders.has(row.key)" :size="13" /><ChevronDown v-else :size="13" />
                        <Folder v-if="collapsedFolders.has(row.key)" :size="15" /><FolderOpen v-else :size="15" />
                        <strong>{{ row.name }}</strong><span>{{ row.files.length }}</span>
                      </button>
                    </div>
                    <div
                      v-else
                      class="git-manager__file is-compare"
                      :class="{ 'is-nested': row.depth > 0 }"
                      :style="{ '--tree-depth': row.depth }"
                      tabindex="0"
                      :title="t.gitDoubleClickCompare"
                      @dblclick="openBranchDiff(filteredCompareFiles, fileIndex(filteredCompareFiles, row.file))"
                      @keydown.enter="openBranchDiff(filteredCompareFiles, fileIndex(filteredCompareFiles, row.file))"
                    >
                      <span class="git-manager__file-status" :class="`is-${row.file.status}`" :title="statusLabel(row.file)">{{ statusLabel(row.file) }}</span>
                      <button class="git-manager__file-name" type="button" :title="row.file.path" @click="openBranchDiff(filteredCompareFiles, fileIndex(filteredCompareFiles, row.file))"><strong>{{ row.file.treeName }}</strong></button>
                      <span class="git-manager__numbers"><b>+{{ row.file.added || 0 }}</b><i>-{{ row.file.deleted || 0 }}</i></span>
                    </div>
                  </template>
                </div>
                <button v-if="visibleCompareTreeRows.length && remainingRowCount('compare', compareTreeRows)" class="git-manager__show-more" type="button" @click="showMoreRows('compare')">{{ t.gitShowMoreRows.replace('{count}', Math.min(GIT_FILE_ROW_BATCH, remainingRowCount('compare', compareTreeRows))) }}</button>
                <p v-if="!visibleCompareTreeRows.length && compareLoaded && !compareError" class="git-manager__section-empty">
                  {{ t.gitNoBranchChanges }}
                </p>
                <p v-else-if="!visibleCompareTreeRows.length && !compareLoaded" class="git-manager__section-empty">
                  {{ t.gitCompareEmpty }}
                </p>
              </div>
            </template>
          </div>
        </template>
      </section>
    </div>
  </Teleport>

  <Teleport to="body">
    <div v-if="outgoingDialogOpen" class="git-outgoing-backdrop" @click.self="closeOutgoingDialog">
      <aside
        ref="outgoingDialogRoot"
        class="git-outgoing-dialog"
        role="complementary"
        :aria-label="t.gitOutgoingTitle"
        tabindex="-1"
        @keydown="onOutgoingKeydown"
      >
        <header class="git-outgoing-dialog__header">
          <span class="git-outgoing-dialog__icon"><Upload :size="18" /></span>
          <div class="git-outgoing-dialog__title">
            <h2>{{ t.gitOutgoingTitle }}</h2>
            <p>{{ repository.currentBranch }} → {{ repository.upstream || t.gitNoUpstream }}</p>
          </div>
          <span class="git-outgoing-dialog__count">{{ t.gitPendingPush.replace('{count}', outgoingCommits.length || repository.ahead) }}</span>
          <div class="git-outgoing-dialog__actions">
            <button class="is-primary" type="button" :disabled="operationsLocked || outgoingLoading || (!!outgoingError && !outgoingCommits.length) || repository.detached || !repository.remotes?.length" @click="pushOutgoingCommits">
              <LoaderCircle v-if="busy === 'push'" class="spin" :size="14" /><Upload v-else :size="14" />{{ t.gitPush }}
            </button>
            <button class="is-close" type="button" :title="t.close" :disabled="busy === 'push'" @click="closeOutgoingDialog"><X :size="17" /></button>
          </div>
        </header>

        <div v-if="outgoingLoading" class="git-outgoing-dialog__state">
          <LoaderCircle class="spin" :size="22" />{{ t.gitOutgoingLoading }}
        </div>
        <div v-else-if="outgoingError && !outgoingCommits.length" class="git-outgoing-dialog__state is-error">{{ outgoingError }}</div>
        <div v-else-if="!outgoingCommits.length" class="git-outgoing-dialog__state">{{ t.gitOutgoingEmpty }}</div>
        <p v-if="outgoingError && outgoingCommits.length" class="git-outgoing-dialog__error" role="alert">{{ outgoingError }}</p>

        <main v-if="!outgoingLoading && outgoingCommits.length" class="git-outgoing-dialog__body">
          <aside class="git-outgoing-dialog__commits">
            <header><strong>{{ t.gitOutgoingCommits }}</strong><span>{{ outgoingCommits.length }}</span></header>
            <div>
              <button
                v-for="commit in outgoingCommits"
                :key="commit.hash"
                type="button"
                :class="{ active: selectedOutgoingCommit?.hash === commit.hash }"
                @click="selectedOutgoingHash = commit.hash"
              >
                <span class="git-manager__graph-dot"></span>
                <span>
                  <strong :title="commit.subject">{{ commit.subject }}</strong>
                  <small>{{ commit.author }} · {{ formatDate(commit.timestamp) }}</small>
                </span>
                <code>{{ commit.shortHash }}</code>
              </button>
            </div>
          </aside>

          <section v-if="selectedOutgoingCommit" class="git-outgoing-dialog__details">
            <section class="git-outgoing-dialog__files">
              <header>
                <div><strong>{{ t.gitOutgoingFiles }}</strong><span>{{ selectedOutgoingCommit.files?.length || 0 }}</span></div>
                <span class="git-manager__numbers"><b>+{{ selectedOutgoingCommit.added || 0 }}</b><i>-{{ selectedOutgoingCommit.deleted || 0 }}</i></span>
              </header>
              <div v-if="selectedOutgoingCommit.files?.length">
                <article v-for="file in selectedOutgoingCommit.files" :key="file.path" @dblclick="openOutgoingDiff(file)">
                  <span class="git-manager__file-status" :class="`is-${file.status}`">{{ statusLabel(file) }}</span>
                  <strong :title="file.path">{{ file.path }}</strong>
                  <span v-if="file.binary" class="git-outgoing-dialog__binary">{{ t.gitOutgoingBinary }}</span>
                  <span v-else class="git-manager__numbers"><b>+{{ file.added || 0 }}</b><i>-{{ file.deleted || 0 }}</i></span>
                  <button class="git-outgoing-dialog__diff" type="button" :title="t.gitViewCompare" :aria-label="`${t.gitViewCompare}: ${file.path}`" @click="openOutgoingDiff(file)"><Eye :size="14" /></button>
                </article>
              </div>
              <p v-else>{{ t.gitOutgoingNoFiles }}</p>
            </section>

            <section class="git-outgoing-dialog__info">
              <header>
                <span><GitCommitHorizontal :size="15" /><strong>{{ t.gitOutgoingCommitInfo }}</strong></span>
                <span class="git-manager__commit-actions">
                  <button type="button" :disabled="operationsLocked || repository.detached || !!repository.state" :title="t.gitResetMixedHint" @click="requestResetCommit(selectedOutgoingCommit)">
                    <LoaderCircle v-if="busy === `reset_mixed:${selectedOutgoingCommit.hash}`" class="spin" :size="13" /><RotateCcw v-else :size="13" />{{ t.gitResetMixed }}
                  </button>
                  <button class="is-danger" type="button" :disabled="operationsLocked || repository.detached || !!repository.state" :title="t.gitResetHardHint" @click="requestResetCommit(selectedOutgoingCommit, true)">
                    <LoaderCircle v-if="busy === `reset_hard:${selectedOutgoingCommit.hash}`" class="spin" :size="13" /><Trash2 v-else :size="13" />{{ t.gitResetHard }}
                  </button>
                </span>
              </header>
              <pre>{{ selectedOutgoingCommit.message || selectedOutgoingCommit.subject }}</pre>
              <dl>
                <div><dt>{{ t.gitOutgoingAuthor }}</dt><dd>{{ selectedOutgoingCommit.author }}<span v-if="selectedOutgoingCommit.authorEmail"> &lt;{{ selectedOutgoingCommit.authorEmail }}&gt;</span></dd></div>
                <div><dt>{{ t.gitOutgoingDate }}</dt><dd>{{ formatFullDate(selectedOutgoingCommit.timestamp) }}</dd></div>
                <div><dt>{{ t.gitOutgoingCommitHash }}</dt><dd><code>{{ selectedOutgoingCommit.hash }}</code></dd></div>
                <div><dt>{{ t.gitOutgoingParent }}</dt><dd><code>{{ selectedOutgoingCommit.parents?.join(', ') || '—' }}</code></dd></div>
              </dl>
            </section>
          </section>
        </main>
      </aside>
    </div>
  </Teleport>

  <Teleport to="body">
    <div
      v-if="moreMenu"
      ref="moreMenuEl"
      class="git-manager__more-menu"
      role="menu"
      :aria-label="t.gitMoreActions"
      :style="{ left: `${moreMenu.left}px`, top: `${moreMenu.top}px` }"
      @click.stop
    >
      <template v-if="moreMenu.kind === 'commit'">
        <button type="button" role="menuitem" :title="t.gitResetMixedHint" @click="resetCommitFromMoreMenu()">
          <RotateCcw :size="15" />
          <span>{{ t.gitResetMixed }}</span>
        </button>
        <button class="is-danger" type="button" role="menuitem" :title="t.gitResetHardHint" @click="resetCommitFromMoreMenu(true)">
          <Trash2 :size="15" />
          <span>{{ t.gitResetHard }}</span>
        </button>
      </template>
      <template v-else>
        <button type="button" role="menuitem" :title="t.gitAddToChat" @click="addToChatMenuTarget">
          <MessageSquarePlus :size="15" />
          <span>{{ t.gitAddToChat }}</span>
        </button>
        <button type="button" role="menuitem" :disabled="moreMenu.ignorePending" :title="moreMenu.ignorePending ? t.gitIgnorePendingHint : t.gitAddToIgnore" @click="ignoreMoreMenuTarget">
          <Check v-if="moreMenu.ignorePending" :size="15" />
          <EyeOff v-else :size="15" />
          <span>{{ moreMenu.ignorePending ? t.gitAlreadyIgnored : t.gitAddToIgnore }}</span>
        </button>
      </template>
    </div>
  </Teleport>

  <GitDiffDialog
    :open="diffDialog.open"
    :session-id="sessionId"
    :scope="diffDialog.scope"
    :files="diffDialog.files"
    :index="diffDialog.index"
    :compare-left="diffDialog.compareLeft || ''"
    :compare-right="diffDialog.compareRight || ''"
    :commit="diffDialog.commit"
    :t="t"
    :language="language"
    :model-options="modelOptions"
    :selected-model-value="selectedModelValue"
    @close="diffDialog = { ...diffDialog, open: false }"
    @update:index="diffDialog = { ...diffDialog, index: $event }"
    @add-selection-to-chat="text => emit('add-selection-to-chat', text)"
  />
  <GitConflictDialog
    :open="conflictDialog.open"
    :session-id="sessionId"
    :files="conflictDialog.files"
    :index="conflictDialog.index"
    :t="t"
    :language="language"
    :model-options="modelOptions"
    :selected-model-value="selectedModelValue"
    @close="conflictDialog = { open: false, files: [], index: 0 }"
    @resolved="onConflictResolved"
    @update:index="conflictDialog = { ...conflictDialog, index: $event }"
  />
  <GitAIPromptDialog v-model="commitPromptOpen" kind="commit" :language="language" :t="t" />
  <ConfirmDialog
    v-model="stashCreateOpen"
    class="git-manager__confirm"
    :title="t.gitStashCreateTitle"
    :description="t.gitStashCreateDesc"
    :confirm-label="t.gitStashCreateConfirm"
    :confirm-busy-label="t.gitStashCreating"
    :busy="busy === 'stash_create'"
    :confirm-disabled="!stashName.trim() || !selectedStashPaths.length"
    @confirm="createStash"
    @cancel="stashName = ''"
  >
    <div class="git-manager__stash-editor">
      <label><span>{{ t.gitStashName }}</span><input v-model="stashName" maxlength="200" :placeholder="t.gitStashNamePlaceholder" :disabled="busy === 'stash_create'" /></label>
      <div>
        <label><input v-model="stashIncludeStaged" type="checkbox" :disabled="busy === 'stash_create' || !stagedFiles.length" />{{ t.gitStagedChanges }} ({{ stagedFiles.length }})</label>
        <label><input v-model="stashIncludeChanged" type="checkbox" :disabled="busy === 'stash_create' || !changedFiles.length" />{{ t.gitUnstagedChanges }} ({{ changedFiles.length }})</label>
      </div>
      <small v-if="!selectedStashPaths.length" class="is-error">{{ t.gitStashSelectFiles }}</small>
    </div>
  </ConfirmDialog>
  <ConfirmDialog
    v-model="stashRestoreOpen"
    class="git-manager__confirm"
    :title="t.gitStashRestoreTitle"
    :description="t.gitStashRestoreDesc.replace('{name}', stashRestoreTarget?.name || '')"
    :confirm-label="t.gitStashRestore"
    :confirm-busy-label="t.gitStashRestoring"
    :busy="busy.startsWith('stash_apply:')"
    @confirm="confirmRestoreStash"
    @cancel="stashRestoreTarget = null"
  />
  <ConfirmDeleteDialog
    v-model="stashDeleteOpen"
    class="git-manager__confirm"
    :title="t.gitStashDeleteTitle"
    :description="t.gitStashDeleteDesc.replace('{name}', stashDeleteTarget?.name || '')"
    :confirm-label="t.gitStashDeleteConfirm"
    :confirm-busy-label="t.gitStashDeleting"
    :busy="busy.startsWith('stash_drop:')"
    @confirm="confirmDeleteStash"
    @cancel="stashDeleteTarget = null"
  />
  <ConfirmDeleteDialog
    v-model="confirmFileOpen"
    class="git-manager__confirm"
    :title="confirmFileOperation?.title || ''"
    :description="confirmFileOperation?.description || ''"
    :confirm-label="t.gitConfirmDiscardConfirm"
    :busy="busy.startsWith('delete_untracked:') || busy.startsWith('discard_tracked:')"
    @confirm="confirmDestructiveFileOperation"
    @cancel="confirmFileOperation = null"
  />
  <ConfirmDialog
    v-model="checkoutConfirmOpen"
    class="git-manager__confirm"
    :title="t.gitCheckoutConfirmTitle"
    :description="(worktreeFiles.length ? t.gitCheckoutConfirmDirtyDesc : t.gitCheckoutConfirmDesc).replace('{branch}', checkoutConfirmTarget?.branch?.name || '')"
    :confirm-label="t.gitCheckout"
    :busy="busy.startsWith('checkout:') || busy.startsWith('checkout_remote:')"
    @confirm="confirmCheckout"
    @cancel="checkoutConfirmTarget = null"
  />
  <ConfirmDeleteDialog
    v-model="resetConfirmOpen"
    class="git-manager__confirm"
    :title="t.gitResetHardConfirmTitle"
    :description="t.gitResetHardConfirmDesc.replace('{commit}', resetConfirmCommit?.shortHash || '')"
    :confirm-label="t.gitResetHardConfirm"
    :confirm-busy-label="t.gitResetHardRunning"
    :busy="busy.startsWith('reset_hard:')"
    @confirm="confirmHardReset"
    @cancel="resetConfirmCommit = null"
  />
</template>

<style scoped src="../../styles/chat/git-management.css"></style>
