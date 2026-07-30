<script setup>
import { computed, nextTick, onBeforeUnmount, onMounted, provide, reactive, ref, watch } from 'vue'
import {
  Archive, Blocks, Bot, Brain, Folder, LoaderCircle, Maximize2,
  Minus, Network, Plus, Settings, Sparkles,
  X
} from 'lucide-vue-next'
import { Call } from '@wailsio/runtime'
import { extensionIcon } from './extensionIcons'
import {
  abortPrompt, chooseSessionDir, chooseWorkspace, closeWindow, createSession, deleteAgent,
  getBootstrap, getSessionChanges, getSessionHistory, listSessions, minimise, onEvent,
  getExtensions, getAgentExtensions, installPi, manageExtension, restartAgent, saveConfig,
  saveFigmaConfig, sendAgentCommand, startPrompt, testModel, toggleMaximise,
  saveBrowserProfile,
  readAgentFile, writeAgentFile,
  listSkills, installSkills, previewSkillArchive, previewSkillUrl, editSkill, deleteSkill, updateSkill,
  installGlobalPackage as beInstallGlobalPackage,
  removeGlobalPackage as beRemoveGlobalPackage,
  installAgentMcp as beInstallAgentMcp,
  installAgentExtension as beInstallAgentExtension,
  uninstallAgentExtension as beUninstallAgentExtension
} from './backend'
import { buildT } from './i18n'
import ChatView from './ChatView.vue'
import logo from './assets/logo.png'
import AppDialogs from './components/AppDialogs.vue'
import InstallLogModal from './components/InstallLogModal.vue'
import PiInstallGate from './components/PiInstallGate.vue'
import AgentsPage from './components/pages/AgentsPage.vue'
import AgentConfigPage from './components/pages/AgentConfigPage.vue'
import DocsPage from './components/pages/DocsPage.vue'
import EnvironmentPage from './components/pages/EnvironmentPage.vue'
import McpPage from './components/pages/McpPage.vue'
import ModelsPage from './components/pages/ModelsPage.vue'
import PluginsPage from './components/pages/PluginsPage.vue'
import SettingsPage from './components/pages/SettingsPage.vue'
import SkillsPage from './components/pages/SkillsPage.vue'
import TasksPage from './components/pages/TasksPage.vue'
import { shouldAbortAfterExtensionResponse, isPlanConfirmationDialog, BROWSER_IDENTITY_DIALOG_TITLE } from './components/chat/extensionDialog'
import { mergeSubagentRuntime } from './components/chat/subagentRuntime'
import { completeCompactionMessage, createCompactionMessage } from './components/chat/compactionMessages'
import { appContextKey } from './composables/appContext'
import { defaultThinkingLevelForModel } from './modelThinking'

// 主页面固定为对话框；菜单点击后以模态浮层（新页面）打开，而非切换主内容区。
const activePage = ref('chat')
const pageComponents = {
  agents: AgentsPage,
  'agent-config': AgentConfigPage,
  workspace: EnvironmentPage,
  skills: SkillsPage,
  mcp: McpPage,
  plugins: PluginsPage,
  models: ModelsPage,
  docs: DocsPage,
  tasks: TasksPage,
  settings: SettingsPage
}
const pageComponent = computed(() => pageComponents[activePage.value] || null)
function goHome() { activePage.value = 'chat' }
const environmentTab = ref('workspace')
const sidebarOpen = ref(true)
const bootstrap = ref(null)
// 客户端自身更新：启动后后台检查一次，有新版本时为 true，用于设置菜单红点。
const appUpdateAvailable = ref(false)
const config = reactive({
  preferences: { theme: 'system', language: 'zh-CN', accentColor: '#d9a441', chatLayout: 'left', showIdentity: true, diffMode: 'unified' },
  userProfile: { name: '', avatar: '' },
  providers: [],
  defaultProvider: '',
  defaultModel: '',
  lastEnvironment: '',
  activeAgentId: '',
  agents: [],
  environments: [],
  activeEnvId: '',
  sshConfigs: []
})
const draft = ref('')
// 未发送内容（输入框草稿）按工作空间分别持久化：每个工作空间各自保存一份，
// 刷新页面后按当前激活的工作空间恢复，切换工作空间时自动保存/载入对应草稿。
const DRAFT_PREFIX = 'codingto:draft:'
function draftKeyForEnv(envId) {
  return DRAFT_PREFIX + (envId || '_none')
}
function loadDraftForEnv(envId) {
  try {
    return localStorage.getItem(draftKeyForEnv(envId)) || ''
  } catch {
    return ''
  }
}
function persistDraftForEnv(envId, text) {
  try {
    const key = draftKeyForEnv(envId)
    if (text && String(text).length) localStorage.setItem(key, text)
    else localStorage.removeItem(key)
  } catch {}
}
// 输入框内容变化时，实时保存当前工作空间的草稿。
// 仅在「新建对话 / 首页」模式下把草稿写入存储；查看历史对话时输入框用于
// 续写该会话，不应读取或覆盖工作空间的未发送草稿。
watch(draft, value => {
  if (isHomeMode.value) persistDraftForEnv(config.activeEnvId, value)
})
const pendingPromptsByTask = reactive(new Map())
const mode = ref('execute')
const thinkingLevel = ref('off')
const selectedSkill = ref(null)
const promptImages = ref([])
const attachmentReadsPending = ref(0)
const selectedPreset = ref('')
const providerEditorOpen = ref(false)
const providerDraft = ref(null)
const selectedModelsProviderName = ref('')
const pendingDeleteProvider = ref(null)
const showProviderApiKey = ref(false)
const running = ref(false)
const runningTaskIds = reactive(new Set())
const runtimeTaskIds = reactive(new Set())
const stoppingTaskIds = reactive(new Set())
const connected = ref(false)
// 当前会话累计的 token/上下文用量。运行时来自 Pi 的 get_session_stats 响应
// （其 production 响应不带 command 字段，已在 handleAgentEvent 中按 data 形状路由），
// 历史加载则来自后端对会话文件的聚合。
const tokenStats = ref({ input: 0, cached: 0, cacheWrite: 0, output: 0, total: 0 })
const contextUsage = ref({ tokens: 0, contextWindow: 0, percent: 0 })
const compactionByTask = reactive(new Map())
const extensionDialog = ref(null)
const extensionStatuses = reactive({})
const skills = ref([])
const skillsLoading = ref(false)
const extensionWidgets = reactive({})
const planItems = ref([])
const executionPlan = ref([])
// 执行计划在本地点持久化，刷新页面后按当前任务恢复（后端暂无对应持久化接口）。
const EXEC_PLAN_KEY = 'codingto:exec-plan:'
const PLAN_ITEMS_KEY = 'codingto:plan-items:'
function persistExecPlan() {
  try {
    const id = activeTaskId.value
    if (!id) return
    if (executionPlan.value.length) localStorage.setItem(EXEC_PLAN_KEY + id, JSON.stringify(executionPlan.value))
    else localStorage.removeItem(EXEC_PLAN_KEY + id)
  } catch {}
}
function restoreExecPlan(taskId) {
  if (!taskId) { executionPlan.value = []; return }
  try {
    const raw = localStorage.getItem(EXEC_PLAN_KEY + taskId)
    executionPlan.value = raw ? JSON.parse(raw) : []
  } catch {
    executionPlan.value = []
  }
}
// planItems（待确认计划）不会由后端持久化，仅存在于前端内存，刷新后会丢失。
// 因此在本地也做一份持久化，刷新后能恢复计划面板，而不是一直“执行中转圈”。
function persistPlanItems() {
  try {
    const id = activeTaskId.value
    if (!id) return
    if (planItems.value.length) localStorage.setItem(PLAN_ITEMS_KEY + id, JSON.stringify(planItems.value))
    else localStorage.removeItem(PLAN_ITEMS_KEY + id)
  } catch {}
}
function restorePlanItems(taskId) {
  if (!taskId) { planItems.value = []; return }
  try {
    const raw = localStorage.getItem(PLAN_ITEMS_KEY + taskId)
    planItems.value = raw ? JSON.parse(raw) : []
  } catch {
    planItems.value = []
  }
}
watch(executionPlan, persistExecPlan, { deep: true })
watch(planItems, persistPlanItems, { deep: true })
// 扩展交互对话框（如计划确认）在本地点持久化，刷新页面后按当前任务恢复，
// 避免刷新后 agent 仍在等待确认却没有任何可操作的对话框（一直“转圈”）。
const EXT_DIALOG_KEY = 'codingto:ext-dialog:'
function persistExtDialog() {
  try {
    const id = activeTaskId.value
    if (!id) return
    const d = extensionDialog.value
    if (d) localStorage.setItem(EXT_DIALOG_KEY + id, JSON.stringify(d))
    else localStorage.removeItem(EXT_DIALOG_KEY + id)
  } catch {}
}
function persistExtDialogForTask(taskId, dialog) {
  try {
    if (!taskId) return
    if (dialog) localStorage.setItem(EXT_DIALOG_KEY + taskId, JSON.stringify(dialog))
    else localStorage.removeItem(EXT_DIALOG_KEY + taskId)
  } catch {}
}
function clearPersistedExtDialog(taskId) {
  try { localStorage.removeItem(EXT_DIALOG_KEY + (taskId || activeTaskId.value || '')) } catch {}
}
function restoreExtDialog(taskId) {
  try {
    const raw = taskId ? localStorage.getItem(EXT_DIALOG_KEY + taskId) : null
    if (raw) extensionDialog.value = JSON.parse(raw)
  } catch {}
}
// 仅「待确认的执行计划」与「待选择的浏览器身份」两种对话框需要显示渐变圆点，
// 其它扩展对话框（通用 confirm/select/prompt）不视为待确认内容。
function isBrowserIdentityDialog(dialog) {
  return dialog?.method === 'select' && String(dialog?.title || '').startsWith(BROWSER_IDENTITY_DIALOG_TITLE)
}
function isPendingConfirmDialog(dialog) {
  return isPlanConfirmationDialog(dialog) || isBrowserIdentityDialog(dialog)
}
function readTaskDialog(id) {
  if (!id) return null
  if (String(id) === String(activeTaskId.value)) return extensionDialog.value
  try {
    const raw = localStorage.getItem(EXT_DIALOG_KEY + String(id))
    return raw ? JSON.parse(raw) : null
  } catch {
    return null
  }
}
function taskNeedsConfirm(id) {
  return isPendingConfirmDialog(readTaskDialog(id))
}
const contextWindow = computed(() => selectedModel.value?.contextWindow || 0)
// 顶部条错误（兼容旧 UI，提示最新一次失败）。重试/失败的历史以
// role:'error' 消息形式插入 messagesList，随对话流按时间顺序保留，
// 不再用单条字符串覆盖，最终失败时用户可回看每次重试的完整错误。
const error = ref('')
function pushErrorMessage(text) {
  const value = String(text || '').trim()
  if (!value) return
  messagesList.value.push({
    id: crypto.randomUUID(),
    role: 'error',
    content: value,
    createdAt: Date.now()
  })
}

function pushChangeMessage(summary, recordedAt = Date.now()) {
  if (!summary?.nodeId) return
  // 没有任何改动（无文件、无增删行）时不插入「本次问题」变更消息，
  // 避免出现「本次问题未改动文件」这类空提示。
  const hasChanges = Boolean(summary.files?.length) || Boolean(summary.added) || Boolean(summary.deleted)
  if (!hasChanges) return
  const id = `changes-${summary.nodeId}`
  if (messagesList.value.some(message => message.id === id)) return
  messagesList.value.push({
    id,
    role: 'changes',
    changes: summary,
    createdAt: Number(recordedAt) || Date.now()
  })
}
const saving = ref(false)
const saved = ref(false)
const agentDeleteBusy = ref(false)
const agentNotice = ref(null)
const pendingDeleteAgent = ref(null)
const newAgentId = ref('')
const newAgentDraft = ref(null)
const previousAgentId = ref('')
const previousDefaultAgentId = ref('')
const currentAgentId = ref('')
const agentEditorOpen = ref(false)
const editingNewAgent = ref(false)
// 独立配置页面当前正在编辑的 agent id；返回列表时清空并刷新。
const editingAgentId = ref('')
const toasts = ref([])
let toastSeq = 0
function pushToast(type, text, timeout = 2800) {
  const id = ++toastSeq
  toasts.value.push({ id, type, text })
  window.setTimeout(() => {
    toasts.value = toasts.value.filter(item => item.id !== id)
  }, timeout)
}
const piInstallBusy = ref(false)
const piInstallError = ref('')
const messagesList = ref([])
const loadingHistory = ref(false)
const tasks = ref([])
const activeTaskId = ref('')
// 首页 / 新建对话 模式：未绑定任何历史会话。草稿（未发送内容）只在此模式下
// 显示并持久化；查看历史对话时输入框用于续写该对话，不显示也不覆盖工作空间草稿。
const isHomeMode = computed(() => activeTaskId.value === '')
const compaction = computed(() => {
  const key = String(activeTaskId.value || '')
  const state = compactionByTask.get(key) || { running: false, reason: '', error: '', messageId: '' }
  return { ...state, available: Boolean(key) && runtimeTaskIds.has(key) }
})
function pendingPromptList(taskId = activeTaskId.value) {
  const key = String(taskId || 'new')
  if (!pendingPromptsByTask.has(key)) pendingPromptsByTask.set(key, reactive([]))
  return pendingPromptsByTask.get(key)
}
const pendingPrompts = computed({
  get: () => pendingPromptList(),
  set: value => pendingPromptsByTask.set(String(activeTaskId.value || 'new'), reactive(value || []))
})
const activeSessionPath = ref('')
const sessionChanges = ref({ root: '', nodes: [], files: [], added: 0, deleted: 0 })
const sessionChangesLoading = ref(false)
const documentPreviewRequest = ref(null)
const documentArtifactFocus = ref(null)
const executionElapsedMs = ref(0)
const executionRunning = ref(false)
const extensionSnapshot = ref({
  tools: [],
  figma: { installed: false, enabled: false, running: false, pid: 0, hasToken: false, version: '' },
  globalMcp: [],
  globalPlugins: [],
  builtinCatalog: [],
  builtins: {},
  recommended: {},
  packages: {},
  mcp: {},
})
const extensionBusy = ref('')
const extensionDeleteBusy = ref(false)
const extensionLoading = ref(false)
const extensionNotice = ref(null)
const extensionRestartPending = ref('')
const figmaAuthorizationsDraft = ref([])
const figmaActiveAuthorizationIdDraft = ref('')
const showFigmaConfig = ref(false)
const figma = computed(() => extensionSnapshot.value.figma || { installed: false, enabled: false, running: false, hasToken: false, version: '' })
let activeAssistant = null
let offEvent
let offState
let offDocumentPreview
let offAttachmentDrop
let offSubagentEvent
let offExtensionsChanged
let changeRefreshTimer
let changeRefreshRequest = 0

const t = computed(() => buildT(config.preferences.language || 'zh-CN'))
const activeTaskRunning = computed(() => (
  activeTaskId.value !== ''
  && (runningTaskIds.has(String(activeTaskId.value)) || !!extensionDialog.value)
))
const activeTaskStopping = computed(() => stoppingTaskIds.has(String(activeTaskId.value)))

function setTaskRunning(id, live) {
  const key = String(id ?? '')
  if (!key) return
  if (live) runningTaskIds.add(key)
  else {
    runningTaskIds.delete(key)
    stoppingTaskIds.delete(key)
  }
  running.value = runningTaskIds.size > 0
  setTaskRuntimeStatus(id, live ? 'running' : 'active')
}

function setTaskRuntimeAvailable(id, available) {
  const key = String(id ?? '')
  if (!key) return
  if (available) runtimeTaskIds.add(key)
  else runtimeTaskIds.delete(key)
}

function setTaskCompaction(id, state) {
  const key = String(id ?? '')
  if (!key) return
  compactionByTask.set(key, {
    running: false,
    reason: '',
    error: '',
    messageId: '',
    ...(compactionByTask.get(key) || {}),
    ...state
  })
}
const apiKeyVisibilityLabel = computed(() => {
  const chinese = config.preferences.language === 'zh-CN'
  if (showProviderApiKey.value) return chinese ? '隐藏 API Key' : 'Hide API Key'
  return chinese ? '显示 API Key' : 'Show API Key'
})
const nav = computed(() => [
  { id: 'agents', label: t.value.agents, icon: Bot },
  { id: 'workspace', label: t.value.workspaceMenu, icon: Folder },
  { id: 'skills', label: t.value.skillsMenu, icon: Sparkles },
  { id: 'mcp', label: t.value.mcpMenu, icon: Network },
  { id: 'plugins', label: t.value.plugins, icon: Blocks },
  { id: 'models', label: t.value.modelsMenu, icon: Brain }
  // { id: 'docs', label: t.value.docs, icon: BookOpen }
])
const selectedAgent = computed(() => {
  // 配置页内：始终以 editingAgentId 为准，避免被 newAgentDraft 分支劫持，
  // 否则顶部切换 agent 后整个配置页仍停留在上一个智能体。
  if (activePage.value === 'agent-config') {
    if (editingAgentId.value) {
      const found = config.agents.find(agent => agent.id === editingAgentId.value)
      if (found) return found
    }
    return config.agents[0] || null
  }
  if (newAgentDraft.value?.id === currentAgentId.value) return newAgentDraft.value
  return config.agents.find(agent => agent.id === currentAgentId.value) || config.agents[0] || null
})
const activeAgentId = computed(() => currentAgentId.value)
const defaultAgentId = computed(() => config.activeAgentId)
const agentList = computed(() => newAgentDraft.value ? [...config.agents, newAgentDraft.value] : config.agents)
const selectedProvider = computed(() => config.providers.find(p => p.name === config.defaultProvider) || config.providers[0])
const selectedModelsProvider = computed(() => config.providers.find(p => p.name === selectedModelsProviderName.value) || config.providers[0] || null)
const availableModels = computed(() => selectedProvider.value?.models || [])
const selectedModel = computed(() => availableModels.value.find(model => model.id === config.defaultModel) || availableModels.value[0])
const modelOptions = computed(() => config.providers.filter(p => p.enabled !== false).flatMap(p => (p.models || []).map(m => ({
  value: `${p.name}/${m.id}`, provider: p.name, model: m.id, label: `${p.label || p.name} · ${m.name || m.id}`
}))))
const supportsImages = computed(() => selectedModel.value?.input?.includes('image'))
const supportsTools = computed(() => selectedModel.value?.capabilities?.toolCall !== false)
const thinkingLevels = computed(() => {
  const known = ['off', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max']
  const mapping = selectedModel.value?.thinkingLevelMap
  return mapping ? known.filter(level => level === 'off' || mapping[level] !== null) : known
})
const selectedModelValue = computed({
  get: () => `${selectedAgent.value?.defaultProvider || ''}/${selectedAgent.value?.defaultModel || ''}`,
  set: value => {
    const index = value.indexOf('/')
    if (!selectedAgent.value) return
    selectedAgent.value.defaultProvider = value.slice(0, index)
    selectedAgent.value.defaultModel = value.slice(index + 1)
    // 保留 config 上的镜像字段，仅用于向后兼容（配置文件/持久化）。
    config.defaultProvider = selectedAgent.value.defaultProvider
    config.defaultModel = selectedAgent.value.defaultModel
    persist()
  }
})

function applyTheme() {
  const pref = config.preferences.theme
  const dark = pref === 'dark' || (pref === 'system' && matchMedia('(prefers-color-scheme: dark)').matches)
  document.documentElement.dataset.theme = dark ? 'dark' : 'light'
  document.documentElement.lang = config.preferences.language
  document.documentElement.style.setProperty('--amber', config.preferences.accentColor || '#d9a441')
}

watch(() => [config.preferences.theme, config.preferences.language, config.preferences.accentColor], applyTheme)
watch(() => config.defaultProvider, () => {
  if (!availableModels.value.some(model => model.id === config.defaultModel)) {
    config.defaultModel = availableModels.value[0]?.id || ''
  }
})
watch(() => config.providers.map(provider => provider.name).join('\u0000'), () => {
  if (!config.providers.some(provider => provider.name === selectedModelsProviderName.value)) {
    selectedModelsProviderName.value = config.providers[0]?.name || ''
  }
})
watch(selectedModel, model => {
  thinkingLevel.value = defaultThinkingLevelForModel(model)
  if (!model?.input?.includes('image')) promptImages.value = []
})

watch(showFigmaConfig, open => {
  if (!open) return
  const figmaConfig = config.extensions?.figma || {}
  figmaAuthorizationsDraft.value = safeClone(figmaConfig.authorizations || [])
  figmaActiveAuthorizationIdDraft.value = figmaConfig.activeAuthorizationId || figmaAuthorizationsDraft.value[0]?.id || ''
})


async function load() {
  bootstrap.value = await getBootstrap()
  const rawConfig = JSON.parse(JSON.stringify(bootstrap.value?.config ?? {}))
  Object.assign(config, rawConfig)
  if (!config.preferences) config.preferences = {}
  config.preferences.accentColor ||= '#d9a441'
  config.providers ||= []
  config.configVersion ||= 4
  config.providers.forEach(normalizeProvider)
  selectedModelsProviderName.value = config.providers.find(provider => provider.name === config.defaultProvider)?.name || config.providers[0]?.name || ''
  config.extensions ||= { figma: { enabled: false, activeAuthorizationId: '', authorizations: [] } }
  config.extensions.figma ||= { enabled: false, activeAuthorizationId: '', authorizations: [] }
  config.extensions.figma.authorizations ||= []
  config.extensions.globalMcp ||= []
  config.extensions.globalPlugins ||= []
  config.agents ||= []
  config.agents.forEach(normalizeAgent)
  if (config.agents.length && !config.agents.some(agent => agent.id === config.activeAgentId)) config.activeAgentId = config.agents[0].id
  if (config.agents.length && !config.agents.some(agent => agent.id === currentAgentId.value)) currentAgentId.value = config.activeAgentId
  config.environments ||= []
  config.sshConfigs ||= []
  config.sshConfigs.forEach(normalizeSsh)
  config.environments.forEach(normalizeWorkspace)
  if (config.environments.length && !config.environments.some(ws => ws.id === config.activeEnvId)) config.activeEnvId = config.environments[0].id
  selectedWorkspaceId.value = config.activeEnvId || config.environments[0]?.id || ''
  // 恢复当前工作空间的未发送草稿：刷新页面后重新把这句话显示出来。
  draft.value = loadDraftForEnv(config.activeEnvId)
  // 清理已不存在的工作空间缓存（折叠/排序），避免残留幽灵项。
  const envIds = new Set(config.environments.map(ws => ws.id))
  for (const id of [...collapsedWorkspaceIds]) if (!envIds.has(id)) collapsedWorkspaceIds.delete(id)
  workspaceOrder.value = workspaceOrder.value.filter(id => envIds.has(id))
  tasks.value = (await listSessions()) || []
  // 仅首次加载时做一次全量扩展扫描；之后返回列表/编辑扩展都只静默刷新单个 agent。
  await refreshExtensions()
  await refreshSkills()
  applyTheme()
  connected.value = true
}

// 启动后静默检查一次新版本，用于设置菜单红点。Wails 桥接在 onMounted 时可能
// 尚未就绪，故失败重试几次（每次间隔 800ms），全部失败则保持无红点。
async function checkUpdateOnStartup(retries = 4) {
  for (let i = 0; i < retries; i++) {
    try {
      const res = await Call.ByName('codingto/internal/app.App.CheckAppUpdate')
      if (res && res.available) appUpdateAvailable.value = true
      return
    } catch {
      if (i === retries - 1) return
      await new Promise(r => setTimeout(r, 800))
    }
  }
}

async function installPiNow() {
  if (piInstallBusy.value) return
  piInstallBusy.value = true
  piInstallError.value = ''
  try {
    await installPi()
    await load()
  } catch (err) {
    piInstallError.value = String(err)
  } finally {
    piInstallBusy.value = false
  }
}

async function refreshSkills() {
  skillsLoading.value = true
  try {
    skills.value = (await listSkills()) || []
  } catch (err) {
    pushToast('error', String(err))
  } finally {
    skillsLoading.value = false
  }
}


async function refreshExtensions({ showLoading = false } = {}) {
  if (showLoading) extensionLoading.value = true
  try {
    const snapshot = await getExtensions()
    snapshot.tools ||= []
    snapshot.figma ||= { installed: false, enabled: false, running: false, pid: 0, hasToken: false, version: '' }
    snapshot.builtinCatalog ||= []
    snapshot.globalMcp ||= []
    snapshot.globalPlugins ||= []
    snapshot.builtins ||= {}
    snapshot.recommended ||= {}
    snapshot.packages ||= {}
    snapshot.mcp ||= {}
    // Keep the current extension UI mounted during install/enable/remove
    // operations and replace only its backing snapshot when fresh data arrives.
    extensionSnapshot.value = snapshot
  } catch (err) {
    extensionNotice.value = { error: true, text: String(err) }
  } finally {
    if (showLoading) extensionLoading.value = false
  }
}

// 仅刷新单个智能体的扩展状态，不动其它智能体，避免每次返回列表都全量重扫。
async function refreshAgentExtensions(agentId) {
  if (!agentId) return
  try {
    const status = await getAgentExtensions(agentId)
    const current = extensionSnapshot.value || {}
    current.builtins = { ...(current.builtins || {}), [agentId]: status.builtins || [] }
    current.recommended = { ...(current.recommended || {}), [agentId]: status.recommended || [] }
    current.packages = { ...(current.packages || {}), [agentId]: status.packages || [] }
    current.mcp = { ...(current.mcp || {}), [agentId]: status.mcp || [] }
    extensionSnapshot.value = { ...current }
  } catch (err) {
    extensionNotice.value = { error: true, text: String(err) }
  }
}

function defaultBrowserProfilePolicy() {
  return {
    existingProfileMode: 'headless',
    interactiveLoginMode: 'headed',
    authenticatedTaskMode: 'headless',
  }
}

function builtinCatalog() {
  const catalog = extensionSnapshot.value?.builtinCatalog || []
  if (catalog.length) return catalog
  return Object.values(extensionSnapshot.value?.builtins || {}).find(items => items?.length) || []
}

function defaultBuiltinSelection() {
  return Object.fromEntries(builtinCatalog().map(tool => [tool.key, true]))
}

function defaultAgent() {
  const id = `agent-${crypto.randomUUID().slice(0, 8)}`
  return {
    id,
    name: `Agent ${config.agents.length + 1}`,
    description: '',
    dataDir: '',
    builtin: defaultBuiltinSelection(),
    recommended: {},
    subagents: [],
    piTools: { read: true, bash: true, edit: true, write: true },
    defaultProvider: config.defaultProvider,
    defaultModel: config.defaultModel,
    browserProfilePolicy: defaultBrowserProfilePolicy(),
  }
}

function normalizeAgent(agent) {
  agent.id ||= `agent-${crypto.randomUUID().slice(0, 8)}`
  agent.name ||= 'Agent'
  agent.builtin ||= {}
  for (const tool of builtinCatalog()) {
    if (tool.required) agent.builtin[tool.key] = true
  }
  agent.recommended ||= {}
  agent.subagents ||= []
  agent.piTools = { read: true, bash: true, edit: true, write: true, ...(agent.piTools || {}) }
  agent.defaultProvider ||= config.defaultProvider
  agent.defaultModel ||= config.defaultModel
  agent.browserProfilePolicy ||= defaultBrowserProfilePolicy()
  return agent
}

function normalizeWorkspace(ws) {
  ws.id ||= `env-${crypto.randomUUID().slice(0, 8)}`
  ws.name ||= ''
  ws.path ||= ''
  ws.description ||= ''
  if (!ws.remotes || !ws.remotes.length) {
    ws.remotes = [defaultRemote()]
  }
  // 规范化已有的所有 remote，保留完整列表
  for (const remote of ws.remotes) {
    remote.id ||= `remote-${crypto.randomUUID().slice(0, 8)}`
    remote.remotePath ||= ''
    remote.sshConfigId ||= ''
  }
  return ws
}

// --- SSH config ---

const sshDraft = ref(null)
const editingNewSsh = ref(false)
const newSshId = ref('')
const sshEditorOpen = ref(false)
const pendingDeleteSsh = ref(null)
const sshBusy = ref(false)
const pendingExtensionDelete = ref(null)

function defaultSsh() {
  return { id: `ssh-${crypto.randomUUID().slice(0, 8)}`, name: `SSH ${config.sshConfigs.length + 1}`, address: '', port: 22, username: '', password: '', remark: '' }
}
function normalizeSsh(ssh) {
  ssh.id ||= `ssh-${crypto.randomUUID().slice(0, 8)}`
  ssh.name ||= ''
  ssh.address ||= ''
  ssh.port = Number(ssh.port) || 22
  ssh.username ||= ''
  ssh.password ||= ''
  ssh.remark ||= ''
  return ssh
}
function openSshEditor(ssh) {
  sshDraft.value = ssh ? normalizeSsh(ssh) : defaultSsh()
  editingNewSsh.value = !ssh
  newSshId.value = ssh ? '' : sshDraft.value.id
  sshEditorOpen.value = true
}
function closeSshEditor() {
  if (editingNewSsh.value) {
    sshDraft.value = null
    newSshId.value = ''
    editingNewSsh.value = false
  }
  sshEditorOpen.value = false
}
function persistSshChange() { if (!newSshId.value) persist() }
async function saveNewSsh() {
  const ssh = sshDraft.value
  if (!ssh || sshBusy.value) return
  if (!ssh.address.trim()) { pushToast('error', t.value.sshAddressRequired); return }
  if (!Number.isInteger(Number(ssh.port)) || Number(ssh.port) < 1 || Number(ssh.port) > 65535) { pushToast('error', t.value.sshPortRequired); return }
  ssh.port = Number(ssh.port)
  if (!ssh.username.trim()) { pushToast('error', t.value.sshUsernameRequired); return }
  if (!ssh.password) { pushToast('error', t.value.sshPasswordRequired); return }
  sshBusy.value = true
  config.sshConfigs.push(ssh)
  const ok = await persist()
  if (ok) {
    newSshId.value = ''
    sshDraft.value = null
    editingNewSsh.value = false
    sshEditorOpen.value = false
    pushToast('success', t.value.sshCreated)
  } else {
    config.sshConfigs = config.sshConfigs.filter(item => item.id !== ssh.id)
    pushToast('error', t.value.sshCreateFailed)
  }
  sshBusy.value = false
}
function requestDeleteSsh(ssh) { if (sshBusy.value) return; pendingDeleteSsh.value = ssh }
async function confirmDeleteSsh() {
  const ssh = pendingDeleteSsh.value
  if (!ssh || sshBusy.value) return
  sshBusy.value = true
  const index = config.sshConfigs.indexOf(ssh)
  config.sshConfigs.splice(index, 1)
  const ok = await persist()
  if (ok) {
    pendingDeleteSsh.value = null
    pushToast('success', t.value.sshCreated)
  } else {
    config.sshConfigs.splice(index, 0, ssh)
    pushToast('error', t.value.sshCreateFailed)
  }
  sshBusy.value = false
}

// --- Agent extension removal confirmation ---
function requestDeleteExtension(payload) {
  if (extensionDeleteBusy.value) return
  pendingExtensionDelete.value = payload
}
async function confirmDeleteExtension() {
  const payload = pendingExtensionDelete.value
  if (!payload || extensionDeleteBusy.value) return
  extensionDeleteBusy.value = true
  try {
    if (payload.type === 'browser') {
      if (selectedAgent.value) await uninstallAgentExtension(selectedAgent.value.id, 'browser-native')
    } else if (payload.type === 'recommended-package') {
      if (selectedAgent.value) {
        const result = await uninstallAgentExtension(selectedAgent.value.id, payload.packageKey, { name: payload.name })
        if (result?.success === true) await toggleAgentExtension(payload.group, payload.key, false)
      }
    } else if (payload.type === 'package') {
      if (selectedAgent.value) await uninstallAgentExtension(selectedAgent.value.id, payload.key)
    } else {
      await toggleAgentExtension(payload.group, payload.key)
    }
  } finally {
    extensionDeleteBusy.value = false
    pendingExtensionDelete.value = null
  }
}

// --- Workspace ---

const wsDraft = ref(null)
const editingNewWs = ref(false)
const newWsId = ref('')
const wsEditorOpen = ref(false)
const pendingDeleteWs = ref(null)
const wsBusy = ref(false)
const selectedWorkspaceId = ref('')
const selectedWorkspace = computed(() => {
  if (wsDraft.value?.id === selectedWorkspaceId.value) return wsDraft.value
  return config.environments.find(ws => ws.id === selectedWorkspaceId.value)
    || config.environments.find(ws => ws.id === config.activeEnvId)
    || config.environments[0]
})

function defaultWorkspace() {
  return {
    id: `env-${crypto.randomUUID().slice(0, 8)}`,
    name: '',
    path: '',
    description: '',
    remotes: [defaultRemote()],
    active: false
  }
}
function openWsEditor(ws) {
  wsDraft.value = ws ? safeClone(ws) : defaultWorkspace()
  normalizeWorkspace(wsDraft.value)
  editingNewWs.value = !ws
  newWsId.value = ws ? '' : wsDraft.value.id
  wsEditorOpen.value = true
}
function closeWsEditor() {
  if (editingNewWs.value) {
    wsDraft.value = null
    newWsId.value = ''
    editingNewWs.value = false
  }
  wsEditorOpen.value = false
}
function persistWsChange() {
  if (newWsId.value) return
  // 编辑已有 workspace 时，将 wsDraft 的修改同步回 config.environments
  if (wsDraft.value) {
    const idx = config.environments.findIndex(env => env.id === wsDraft.value.id)
    if (idx >= 0) {
      config.environments[idx] = safeClone(wsDraft.value)
      normalizeWorkspace(config.environments[idx])
    }
  }
  persist()
}
function handleWorkspaceSshChange() {
  const remote = wsDraft.value?.remotes?.[0]
  if (remote && !remote.sshConfigId) remote.remotePath = ''
  persistWsChange()
}
async function saveNewWs() {
  const ws = wsDraft.value
  if (!ws || wsBusy.value) return
  const remote = ws.remotes?.[0]
  if (!ws.path) { pushToast('error', t.value.wsLocalRequired); return }
  if (remote?.sshConfigId && !remote.remotePath?.trim()) { pushToast('error', t.value.wsRemoteRequired); return }
  if (remote && !remote.sshConfigId) remote.remotePath = ''
  const previousActiveId = config.activeEnvId
  const previousEnvironment = config.lastEnvironment
  config.environments.push(ws)
  selectedWorkspaceId.value = ws.id
  if (!previousActiveId) {
    config.activeEnvId = ws.id
    config.lastEnvironment = ws.path
  }
  const ok = await persist()
  if (ok) {
    newWsId.value = ''
    wsDraft.value = null
    editingNewWs.value = false
    wsEditorOpen.value = false
    pushToast('success', t.value.wsCreated)
  } else {
    config.environments = config.environments.filter(item => item.id !== ws.id)
    selectedWorkspaceId.value = ''
    config.activeEnvId = previousActiveId
    config.lastEnvironment = previousEnvironment
    pushToast('error', t.value.wsCreateFailed)
  }
}
async function setActiveWorkspace(ws) {
  if (wsBusy.value || ws.id === config.activeEnvId) return
  // 切换工作空间时，仅当处于首页模式才保存/载入草稿；历史会话视图下
  // 输入框为空，不覆盖目标工作空间已保存的未发送草稿。
  if (isHomeMode.value) persistDraftForEnv(config.activeEnvId, draft.value)
  config.activeEnvId = ws.id
  config.lastEnvironment = ws.path
  draft.value = isHomeMode.value ? loadDraftForEnv(ws.id) : ''
  await persist()
  pushToast('success', t.value.wsChanged)
}
function requestDeleteWs(ws) { if (config.environments.length <= 1 || wsBusy.value) return; pendingDeleteWs.value = ws }
async function confirmDeleteWs() {
  const ws = pendingDeleteWs.value
  if (!ws || config.environments.length <= 1 || wsBusy.value) return
  wsBusy.value = true
  const index = config.environments.indexOf(ws)
  config.environments.splice(index, 1)
  if (config.activeEnvId === ws.id) {
    config.activeEnvId = config.environments[Math.min(index, config.environments.length - 1)]?.id || ''
    config.lastEnvironment = config.environments.find(w => w.id === config.activeEnvId)?.path || ''
  }
  if (selectedWorkspaceId.value === ws.id) {
    selectedWorkspaceId.value = config.environments[Math.min(index, config.environments.length - 1)]?.id || ''
  }
  const ok = await persist()
  if (ok) {
    pendingDeleteWs.value = null
    pushToast('success', t.value.wsCreated)
  } else {
    config.environments.splice(index, 0, ws)
    pendingDeleteWs.value = null
    pushToast('error', t.value.wsCreateFailed)
  }
  wsBusy.value = false
}
function extractDirName(p) {
  const segments = p.replace(/\\/g, '/').split('/').filter(Boolean)
  return segments[segments.length - 1] || p
}
function handleWsPathChange() {
  nextTick(() => {
    const ws = wsDraft.value
    if (ws && !ws.name && ws.path) {
      ws.name = extractDirName(ws.path)
    }
  })
  persistWsChange()
}
async function pickWorkspacePath() {
  const path = await chooseWorkspace()
  if (!path) return
  if (wsDraft.value) {
    wsDraft.value.path = path
    if (!wsDraft.value.name) {
      wsDraft.value.name = extractDirName(path)
    }
  } else {
    config.lastEnvironment = path
  }
}

// A workspace always owns one remote server directory and references one SSH profile.
function defaultRemote() {
  return { id: `remote-${crypto.randomUUID().slice(0, 8)}`, remotePath: '', sshConfigId: '' }
}
function workspaceRemote(ws) {
  return ws?.remotes?.[0] || null
}
function workspaceSsh(ws) {
  const sshId = workspaceRemote(ws)?.sshConfigId
  return config.sshConfigs.find(item => item.id === sshId)
}

function showAgentNotice(type, text) {
  agentNotice.value = { type, text }
  window.setTimeout(() => {
    if (agentNotice.value?.text === text) agentNotice.value = null
  }, 2600)
}

async function createAgent() {
  if (!bootstrap.value?.piInstalled) return
  if (newAgentId.value) {
    agentEditorOpen.value = true
    editingNewAgent.value = true
    return
  }
  previousAgentId.value = currentAgentId.value
  previousDefaultAgentId.value = config.activeAgentId
  if (!builtinCatalog().length) await refreshExtensions()
  const agent = defaultAgent()
  newAgentDraft.value = agent
  currentAgentId.value = agent.id
  newAgentId.value = agent.id
  agentNotice.value = null
  agentEditorOpen.value = true
  editingNewAgent.value = true
  await nextTick()
  document.querySelector('.agent-editor-dialog input')?.focus()
}

async function saveNewAgent() {
  const agent = newAgentDraft.value
  if (!agent || saving.value) return
  config.agents.push(agent)
  const persisted = await persist()
  if (persisted) {
    newAgentId.value = ''
    newAgentDraft.value = null
    previousAgentId.value = ''
    previousDefaultAgentId.value = ''
    editingNewAgent.value = false
    agentEditorOpen.value = false
    showAgentNotice('success', t.value.agentCreated.replace('{name}', agent.name))
    pushToast('success', t.value.agentCreated.replace('{name}', agent.name))
  } else {
    const index = config.agents.findIndex(item => item.id === agent.id)
    if (index >= 0) config.agents.splice(index, 1)
    currentAgentId.value = agent.id
    showAgentNotice('error', t.value.agentCreateFailed)
    pushToast('error', t.value.agentCreateFailed)
  }
}

function cancelNewAgent() {
  currentAgentId.value = config.agents.some(item => item.id === previousAgentId.value)
    ? previousAgentId.value
    : config.agents[0]?.id || ''
  if (config.activeAgentId === newAgentId.value) {
    config.activeAgentId = config.agents.some(item => item.id === previousDefaultAgentId.value)
      ? previousDefaultAgentId.value
      : config.agents[0]?.id || ''
  }
  newAgentId.value = ''
  newAgentDraft.value = null
  previousAgentId.value = ''
  previousDefaultAgentId.value = ''
  editingNewAgent.value = false
  agentEditorOpen.value = false
  agentNotice.value = null
}

function openAgentEditor() {
  agentEditorOpen.value = true
  editingNewAgent.value = false
}

// 点击列表“配置”按钮：editingAgentId 是配置页唯一目标。不要修改
// activeAgentId，否则给另一个 Agent 安装扩展会意外切换当前聊天 Agent。
function openAgentConfig(agent) {
  if (!agent) return
  editingAgentId.value = agent.id
  agentNotice.value = null
  activePage.value = 'agent-config'
}

// 配置页面返回：不整页 reload，只静默刷新刚编辑的智能体的扩展状态。
async function backToAgentList() {
  const editedId = editingAgentId.value
  editingAgentId.value = ''
  activePage.value = 'agents'
  if (editedId) await refreshAgentExtensions(editedId)
}

function closeAgentEditor() {
  if (editingNewAgent.value) {
    cancelNewAgent()
  } else {
    agentEditorOpen.value = false
  }
}

function persistAgentChange(agent) {
  if (agent?.id !== newAgentId.value) return persist()
  return Promise.resolve(false)
}

async function setDefaultAgent(agent, enabled = true) {
  if (!agent) return
  if (!enabled) {
    if (agent.id === newAgentId.value && config.activeAgentId === agent.id) {
      config.activeAgentId = config.agents.some(item => item.id === previousDefaultAgentId.value)
        ? previousDefaultAgentId.value
        : config.agents[0]?.id || ''
    }
    return
  }
  if (config.activeAgentId === agent.id) return
  const previousDefault = config.activeAgentId
  config.activeAgentId = agent.id
  if (agent.id !== newAgentId.value) {
    const savedDefault = await persist()
    if (savedDefault) {
      pushToast('success', t.value.agentDefaultChanged.replace('{name}', agent.name))
    } else {
      config.activeAgentId = previousDefault
    }
  }
}

function requestDeleteAgent(agent) {
  if (agent?.id === config.activeAgentId || config.agents.length <= 1 || agentDeleteBusy.value) return
  pendingDeleteAgent.value = agent
}

async function confirmDeleteAgent() {
  const agent = pendingDeleteAgent.value
  if (!agent || agent.id === config.activeAgentId || config.agents.length <= 1 || agentDeleteBusy.value) return
  agentDeleteBusy.value = true
  try {
    const result = await deleteAgent(agent.id)
    Object.assign(config, result)
    if (!config.agents.some(item => item.id === currentAgentId.value)) currentAgentId.value = config.activeAgentId
    pendingDeleteAgent.value = null
    showAgentNotice('success', t.value.agentDeleted.replace('{name}', agent.name))
    pushToast('success', t.value.agentDeleted.replace('{name}', agent.name))
  } catch (err) {
    error.value = localizeError(String(err))
    showAgentNotice('error', t.value.agentDeleteFailed)
    pushToast('error', t.value.agentDeleteFailed)
  } finally {
    agentDeleteBusy.value = false
  }
}

async function pickAgentDataDir() {
  const path = await chooseWorkspace()
  if (!path || !selectedAgent.value) return
  selectedAgent.value.dataDir = path
  if (selectedAgent.value.id !== newAgentId.value) await persist()
}

async function toggleAgentExtension(group, key, desiredState) {
  const agent = selectedAgent.value
  if (!agent) return
  agent[group] ||= {}
  const previous = !!agent[group][key]
  const next = typeof desiredState === 'boolean' ? desiredState : !previous
  // RTK needs its shared binary on PATH before it can be materialized per agent.
  if (group === 'recommended' && key === 'rtk' && next) {
    const rtk = (extensionSnapshot.value?.recommended?.[agent.id] || []).find(tool => tool.key === 'rtk')
    if (rtk && !rtk.installed) {
      pushToast('error', t.value.rtkNotInstalledHint)
      return
    }
  }
  agent[group][key] = next
  const name = key === 'rtk'
    ? 'RTK'
    : (key === 'figma' ? t.value.piFigma : (key === 'pi-plugins' ? t.value.piPlugins : key.toUpperCase()))
  if (agent.id !== newAgentId.value) {
    const ok = await persist()
    if (ok) {
      if (group === 'builtin' || key === 'rtk' || key === 'figma' || key === 'pi-plugins') {
        // These extensions are discovered by Pi at process startup. Reload the
        // current agent after its isolated extension state changes.
        extensionRestartPending.value = name
        pushToast('info', t.value.extensionRestartingAgent.replace('{name}', name))
        try {
          await restartAgent()
        } catch (err) {
          // performRestart also emits restart_done on failure. Clear the local
          // guard here as a fallback in case the bridge rejects first.
          if (extensionRestartPending.value) {
            extensionRestartPending.value = ''
            pushToast('error', t.value.toastExtensionError.replace('{error}', String(err)))
          }
        }
        await refreshAgentExtensions(agent.id)
      }
      if (next) pushToast('success', t.value.toastInstalledForAgent.replace('{agent}', agent.name).replace('{name}', name))
      else pushToast('info', t.value.toastUninstalledForAgent.replace('{agent}', agent.name).replace('{name}', name))
    } else {
      agent[group][key] = previous
      pushToast('error', t.value.toastConfigFailed.replace('{error}', t.value.agentCreateFailed))
    }
  } else {
    pushToast('success', next
      ? t.value.toastInstalledForAgent.replace('{agent}', agent.name).replace('{name}', name)
      : t.value.toastUninstalledForAgent.replace('{agent}', agent.name).replace('{name}', name))
  }
}

async function extensionAction(tool, action) {
  extensionBusy.value = tool.key
  extensionNotice.value = null
  try {
    const result = await manageExtension({ key: tool.key, action })
    extensionNotice.value = {
      text: result?.message || '',
      command: result?.command || '',
      output: result?.output || ''
    }
    await refreshAgentExtensions(selectedAgent.value?.id)
    if (action === 'install') pushToast('success', t.value.toastInstalled.replace('{name}', tool.name))
    else if (action === 'enable') pushToast('success', t.value.toastEnabled.replace('{name}', tool.name))
    else if (action === 'disable') pushToast('info', t.value.toastDisabled.replace('{name}', tool.name))
    else if (action === 'start') pushToast('success', t.value.toastStarted.replace('{name}', tool.name))
    else if (action === 'stop') pushToast('info', t.value.toastStopped.replace('{name}', tool.name))
  } catch (err) {
    extensionNotice.value = { error: true, text: String(err) }
    pushToast('error', t.value.toastExtensionError.replace('{error}', String(err)))
  } finally {
    extensionBusy.value = ''
  }
}

async function installAgentExtension(agentId, command, options = {}) {
  const busyKey = options.busyKey || 'browser-install'
  const displayName = options.name || t.value.browserNative
  extensionBusy.value = busyKey
  try {
    const result = await beInstallAgentExtension(agentId, command)
    if (result?.success === false) {
      pushToast('error', t.value.toastExtensionError.replace('{error}', result?.message || ''))
    } else {
      pushToast('success', result?.message || t.value.toastInstalled.replace('{name}', displayName))
    }
    await refreshAgentExtensions(agentId)
    return result
  } catch (err) {
    pushToast('error', t.value.toastExtensionError.replace('{error}', String(err)))
  } finally {
    extensionBusy.value = ''
  }
}

async function installGlobalPackage(scope, packageName) {
  const name = String(packageName || '').trim()
  if (!name || extensionBusy.value) return
  extensionBusy.value = `global-${scope}-install`
  try {
    const result = await beInstallGlobalPackage(scope, name)
    const latest = await getBootstrap()
    config.extensions = latest?.config?.extensions || config.extensions
    config.extensions.globalMcp ||= []
    config.extensions.globalPlugins ||= []
    await refreshExtensions()
    pushToast('success', result?.message || t.value.toastInstalled.replace('{name}', name))
    return result
  } catch (err) {
    pushToast('error', t.value.toastExtensionError.replace('{error}', String(err)))
    throw err
  } finally {
    extensionBusy.value = ''
  }
}

async function removeGlobalPackage(scope, packageName) {
  const name = String(packageName || '').trim()
  if (!name || extensionBusy.value) return
  extensionBusy.value = `global-${scope}-remove`
  try {
    const result = await beRemoveGlobalPackage(scope, name)
    const latest = await getBootstrap()
    config.extensions = latest?.config?.extensions || config.extensions
    config.extensions.globalMcp ||= []
    config.extensions.globalPlugins ||= []
    await refreshExtensions()
    pushToast('success', result?.message || t.value.toastRemoved.replace('{name}', name))
    return result
  } catch (err) {
    pushToast('error', t.value.toastExtensionError.replace('{error}', String(err)))
    throw err
  } finally {
    extensionBusy.value = ''
  }
}

async function installAgentMcp(agentId, packageName) {
  const name = String(packageName || '').trim()
  if (!agentId || !name || extensionBusy.value) return
  extensionBusy.value = 'agent-mcp-install'
  try {
    const result = await beInstallAgentMcp(agentId, name)
    await refreshExtensions()
    pushToast('success', result?.message || t.value.toastInstalled.replace('{name}', name))
    return result
  } catch (err) {
    pushToast('error', t.value.toastExtensionError.replace('{error}', String(err)))
    throw err
  } finally {
    extensionBusy.value = ''
  }
}

async function uninstallAgentExtension(agentId, key, options = {}) {
  extensionBusy.value = options.busyKey || 'browser-install'
  const displayName = options.name || t.value.browserNative
  try {
    const result = await beUninstallAgentExtension(agentId, key)
    if (result?.success === false) {
      pushToast('error', t.value.toastExtensionError.replace('{error}', result?.message || ''))
    } else {
      pushToast('success', result?.message || t.value.toastUninstalled.replace('{name}', displayName))
    }
    await refreshAgentExtensions(agentId)
    return result
  } catch (err) {
    pushToast('error', t.value.toastExtensionError.replace('{error}', String(err)))
  } finally {
    extensionBusy.value = ''
  }
}

async function figmaAction(action) {
  extensionBusy.value = 'figma-' + action
  try {
    await manageExtension({ key: 'figma', action })
    await refreshAgentExtensions(selectedAgent.value?.id)
    if (action === 'install') pushToast('success', t.value.toastInstalled.replace('{name}', t.value.figma))
  } catch (err) {
    pushToast('error', t.value.toastExtensionError.replace('{error}', String(err)))
  } finally {
    extensionBusy.value = ''
  }
}

async function persistFigma() {
  extensionBusy.value = 'figma-config'
  try {
    const authorizations = figmaAuthorizationsDraft.value
      .map((item, index) => ({ ...item, name: item.name.trim() || `Figma ${index + 1}`, token: item.token.trim(), tokenType: item.tokenType === 'oauth' ? 'oauth' : 'pat' }))
      .filter(item => item.token)
    const activeAuthorizationId = authorizations.some(item => item.id === figmaActiveAuthorizationIdDraft.value)
      ? figmaActiveAuthorizationIdDraft.value
      : authorizations[0]?.id || ''
    const payload = {
      enabled: authorizations.length > 0,
      activeAuthorizationId,
      authorizations
    }
    const snapshot = await saveFigmaConfig(payload)
    extensionSnapshot.value = snapshot
    config.extensions.figma = safeClone(payload)
    pushToast('success', authorizations.length ? t.value.figmaAuthorizationVerified : t.value.toastConfigSaved)
    showFigmaConfig.value = false
    await refreshAgentExtensions(selectedAgent.value?.id)
    if (running.value && selectedAgent.value?.recommended?.figma) {
      extensionRestartPending.value = t.value.piFigma
      pushToast('info', t.value.extensionRestartingAgent.replace('{name}', t.value.piFigma))
      try {
        await restartAgent()
      } catch (err) {
        extensionRestartPending.value = ''
        pushToast('error', t.value.toastExtensionError.replace('{error}', String(err)))
      }
    }
  } catch (err) {
    extensionNotice.value = { error: true, text: String(err) }
    pushToast('error', t.value.toastConfigFailed.replace('{error}', String(err)))
  } finally {
    extensionBusy.value = ''
  }
}

function addFigmaAuthorization() {
  const id = `figma-${crypto.randomUUID().slice(0, 8)}`
  figmaAuthorizationsDraft.value.push({ id, name: '', token: '', tokenType: 'pat' })
  figmaActiveAuthorizationIdDraft.value ||= id
}

function removeFigmaAuthorization(id) {
  figmaAuthorizationsDraft.value = figmaAuthorizationsDraft.value.filter(item => item.id !== id)
  if (figmaActiveAuthorizationIdDraft.value === id) {
    figmaActiveAuthorizationIdDraft.value = figmaAuthorizationsDraft.value[0]?.id || ''
  }
}

function safeClone(value) {
  return JSON.parse(JSON.stringify(value))
}

// 思考区的默认展开状态按“最后一轮问题”计算：历史记录中，最后一条
// 用户消息之前的思考全部折叠，最后一条用户消息之后的思考保持展开。
function initializeThinkingVisibility(messages) {
  let lastUserIndex = -1
  for (let index = messages.length - 1; index >= 0; index--) {
    if (messages[index]?.role === 'user') {
      lastUserIndex = index
      break
    }
  }
  messages.forEach((message, index) => {
    if (message?.role === 'assistant' && message.thinkingContent) {
      message.thinkingOpen = index > lastUserIndex
    }
  })
  return messages
}

function collapsePreviousThinking() {
  messagesList.value.forEach(message => {
    if (message?.role === 'assistant' && message.thinkingContent) message.thinkingOpen = false
  })
}

function updateThinkingOpen({ id, open }) {
  const message = messagesList.value.find(item => item.id === id)
  if (message) message.thinkingOpen = Boolean(open)
}

function currentTask() {
  return tasks.value.find(task => task.id === activeTaskId.value)
}

function taskById(id) {
  return tasks.value.find(task => String(task.id) === String(id))
}

function eventTaskId(event) {
  return event?.codingToSessionId ?? event?.sessionId ?? ''
}

function setTaskRuntimeStatus(id, status) {
  const task = taskById(id)
  if (task) task.status = status
}

// The backend archives uploaded files and exposes each prompt node's input
// artifacts (with absolute paths) via session changes. The live user message
// we pushed only knows the file name/size, so back-fill the absolute path here
// by matching artifact names. Once an attachment has an absPath the message
// bubble renders it as a clickable chip that opens via openSessionArtifact.
function hydrateMessageAttachments() {
  const pending = new Map()
  for (const node of sessionChanges.value.nodes || []) {
    for (const art of node.inputArtifacts || []) {
      if (!art || !art.name) continue
      const bucket = pending.get(art.name) || []
      bucket.push(art.absPath)
      pending.set(art.name, bucket)
    }
  }
  if (!pending.size) return
  for (const msg of messagesList.value) {
    if (msg.role !== 'user' || !msg.attachments?.length) continue
    for (const att of msg.attachments) {
      if (att.absPath) continue
      const bucket = pending.get(att.name)
      if (bucket && bucket.length) att.absPath = bucket.shift()
    }
  }
}

async function refreshSessionChanges() {
  const taskId = activeTaskId.value
  const requestId = ++changeRefreshRequest
  if (!taskId) {
    sessionChanges.value = { root: '', nodes: [], files: [], added: 0, deleted: 0 }
    sessionChangesLoading.value = false
    return
  }
  sessionChangesLoading.value = true
  try {
    const changes = await getSessionChanges(Number(taskId))
    if (requestId === changeRefreshRequest && String(activeTaskId.value) === String(taskId)) {
      sessionChanges.value = { root: '', nodes: [], files: [], added: 0, deleted: 0, ...(changes || {}) }
      hydrateMessageAttachments()
    }
  } catch {
    // A missing baseline is expected before the first prompt in a new conversation.
  } finally {
    if (requestId === changeRefreshRequest && String(activeTaskId.value) === String(taskId)) {
      sessionChangesLoading.value = false
    }
  }
}

function scheduleSessionChangesRefresh(delay = 180) {
  window.clearTimeout(changeRefreshTimer)
  changeRefreshTimer = window.setTimeout(() => refreshSessionChanges(), delay)
}

// document 工具产出文件（create 生成、download_list 分发）后，通知聊天视图
// 打开右侧节点产物并展开文档所属的节点，方便用户立即看到生成产物。
function maybeFocusDocumentArtifacts(tool, event) {
  if (!tool || event?.isError) return
  const toolName = String(event?.toolName || tool.detail?.toolName || tool.detail?.name || tool.content || '')
  if (toolName !== 'codingto_document') return
  const input = tool.detail?.input ?? event?.args ?? event?.input
  let action = ''
  if (typeof input === 'string') {
    try { action = JSON.parse(input)?.action || '' } catch { /* 非 JSON 参数忽略 */ }
  } else if (input && typeof input === 'object') {
    action = input.action || ''
  }
  if (action !== 'download_list' && action !== 'create') return
  const nodeId = String(event?.changeNodeId || tool.detail?.changeNodeId || '')
  if (!nodeId) return
  documentArtifactFocus.value = { nodeId, nonce: Date.now() }
}

function syncCurrentTask() {
  const task = currentTask()
  if (!task) return
  task.tokenStats = { ...tokenStats.value }
  task.contextUsage = { ...contextUsage.value }
  task.planItems = safeClone(planItems.value)
  task.execDurationMs = executionElapsedMs.value
}

async function ensureConversation(title) {
  let task = currentTask()
  if (task) return task
  const agent = selectedAgent.value
  task = await createSession({
    agentId: agent?.id || config.activeAgentId,
    environmentId: config.activeEnvId,
    title: Array.from(title.trim()).slice(0, 50).join('') || t.value.chatNewSession,
    provider: agent?.defaultProvider || config.defaultProvider,
    model: agent?.defaultModel || config.defaultModel
  })
  // 若 agent 未单独设置默认模型，用首个启用服务商的首个模型兜底，
  // 防止 createSession 落到一个不存在的全局默认模型（如 openai/gpt-5.6-terra）。
  if (!agent?.defaultProvider || !agent?.defaultModel) {
    const first = config.providers.find(p => p.enabled !== false && (p.models || []).length)
    if (first && !task.provider) {
      task = { ...task, provider: first.name, model: first.models[0].id }
    }
  }
  tasks.value.unshift(task)
  activeTaskId.value = task.id
  return task
}

async function refreshSessions() {
  const latest = (await listSessions()) || []
  tasks.value = latest
  const active = latest.find(item => item.id === activeTaskId.value)
  if (active) executionElapsedMs.value = Number(active.execDurationMs) || 0
}

function firstNumber(...values) {
  for (const value of values) {
    const number = Number(value)
    if (Number.isFinite(number)) return number
  }
  return 0
}

const SESSION_PAGE_SIZE = 5
const ARCHIVED_KEY = 'codingto:archived-tasks'
const archivedTaskIds = reactive(new Set())
try {
  const raw = localStorage.getItem(ARCHIVED_KEY)
  if (raw) JSON.parse(raw).forEach(id => archivedTaskIds.add(id))
} catch {}
function persistArchivedTasks() {
  try { localStorage.setItem(ARCHIVED_KEY, JSON.stringify([...archivedTaskIds])) } catch {}
}
const visibleSessionCounts = reactive({})

// Conversations grouped by workspace, each showing the latest few with a
// "show more" button that reveals another page of five.
// 工作空间折叠状态：前端缓存，刷新后维持 collapse/expand。
const WS_COLLAPSED_KEY = 'codingto:ws-collapsed'
const collapsedWorkspaceIds = reactive(new Set())
try {
  const raw = localStorage.getItem(WS_COLLAPSED_KEY)
  if (raw) JSON.parse(raw).forEach(id => collapsedWorkspaceIds.add(id))
} catch {}
function persistCollapsedWorkspaces() {
  try { localStorage.setItem(WS_COLLAPSED_KEY, JSON.stringify([...collapsedWorkspaceIds])) } catch {}
}
// 工作空间排序（被置顶者优先）：前端缓存，重新进入时恢复。
const WS_ORDER_KEY = 'codingto:ws-order'
const workspaceOrder = ref([])
try {
  const raw = localStorage.getItem(WS_ORDER_KEY)
  if (raw) workspaceOrder.value = JSON.parse(raw).filter(Boolean)
} catch {}
function persistWorkspaceOrder() {
  try { localStorage.setItem(WS_ORDER_KEY, JSON.stringify(workspaceOrder.value)) } catch {}
}
// 当在工作空间执行对话或在历史对话中发消息时，把该工作空间置为第一位。
function bumpWorkspaceToTop(envId) {
  if (!envId || !config.environments.some(ws => ws.id === envId)) return
  workspaceOrder.value = [envId, ...workspaceOrder.value.filter(id => id !== envId)]
  persistWorkspaceOrder()
}
function toggleWorkspaceCollapse(envId) {
  if (!envId) return
  if (collapsedWorkspaceIds.has(envId)) collapsedWorkspaceIds.delete(envId)
  else collapsedWorkspaceIds.add(envId)
  persistCollapsedWorkspaces()
}

const sessionGroups = computed(() => {
  const groups = config.environments.map(ws => ({
    id: ws.id,
    name: ws.name || ws.path,
    env: ws,
    all: [],
    visible: [],
    remaining: 0,
  }))
  const orphan = { id: '', name: t.value.ungrouped || '未分类', env: null, all: [], visible: [], remaining: 0 }
  for (const task of tasks.value) {
    if (archivedTaskIds.has(task.id)) continue
    const group = groups.find(g => g.id === task.environmentId) || orphan
    group.all.push(task)
  }
  const ordered = [...groups]
  if (orphan.all.length) ordered.push(orphan)
  // 依 workspaceOrder（被置顶者优先）重排工作空间分组，其余保持原顺序。
  if (workspaceOrder.value.length) {
    const byId = new Map(ordered.map(g => [g.id, g]))
    const top = []
    for (const id of workspaceOrder.value) {
      const g = byId.get(id)
      if (g) { top.push(g); byId.delete(id) }
    }
    ordered.length = 0
    ordered.push(...top, ...byId.values())
  }
  for (const group of ordered) {
    group.all.sort((a, b) => (Number(b.updatedAt) || Number(b.createdAt) || 0) - (Number(a.updatedAt) || Number(a.createdAt) || 0))
    const limit = visibleSessionCounts[group.id] || SESSION_PAGE_SIZE
    group.visible = group.all.slice(0, limit)
    group.remaining = group.all.length - group.visible.length
  }
  return ordered
})

function showMoreSessions(envId) {
  const current = visibleSessionCounts[envId] || SESSION_PAGE_SIZE
  visibleSessionCounts[envId] = current + SESSION_PAGE_SIZE
}

// Fake delete: hide the conversation from the list without touching the backend.
const pendingArchiveTask = ref(null)
const archivePopPos = reactive({ top: 0, left: 0 })
function requestArchive(event, task) {
  if (task?.id == null) return
  const rect = event.currentTarget.getBoundingClientRect()
  archivePopPos.top = rect.bottom + 6
  archivePopPos.left = Math.max(8, rect.right - 220)
  pendingArchiveTask.value = task
}
function cancelArchive() {
  pendingArchiveTask.value = null
}
function archiveTask(task) {
  if (!task?.id) return
  archivedTaskIds.add(task.id)
  persistArchivedTasks()
  pendingArchiveTask.value = null
}
function closeArchivePop() {
  pendingArchiveTask.value = null
}

function selectWorkspaceById(id) {
  const ws = config.environments.find(env => env.id === id)
  if (ws) chatSelectEnvironment(ws)
  goHome()
}

function chatNewSessionFor(group) {
  if (group?.id) selectWorkspaceById(group.id)
  chatNewSession()
}

function applySessionTokenTotals(usage) {
  if (!usage) return
  const input = firstNumber(usage.input, usage.input_tokens, usage.inputTokens)
  const cached = firstNumber(usage.cached, usage.cacheRead, usage.cache_read_input_tokens, usage.cached_input_tokens)
  const cacheWrite = firstNumber(usage.cacheWrite, usage.cache_write_input_tokens)
  const output = firstNumber(usage.output, usage.output_tokens, usage.outputTokens)
  tokenStats.value = {
    input,
    cached,
    cacheWrite,
    output,
    total: firstNumber(usage.total, input + cached + cacheWrite + output)
  }
}

function applySessionStats(data) {
  if (!data) return
  applySessionTokenTotals(data.tokens)
  const usage = data.contextUsage || {}
  contextUsage.value = {
    tokens: firstNumber(usage.tokens),
    contextWindow: firstNumber(usage.contextWindow, contextWindow.value),
    percent: firstNumber(usage.percent)
  }
}

function messageText(message) {
  if (!message) return ''
  if (typeof message.content === 'string') return message.content
  if (!Array.isArray(message.content)) return ''
  return message.content
    .filter(block => block?.type === 'text')
    .map(block => block.text || '')
    .join('\n')
}

function parsePlanItems(text) {
  if (!text) return []
  const lines = String(text).split(/\r?\n/)
  const items = []
  let inPlan = false
  for (const rawLine of lines) {
    const line = rawLine.replace(/\*\*/g, '').trim()
    if (/^(plan|计划(?:步骤)?)\s*[:：]/i.test(line)) {
      inPlan = true
      continue
    }
    const match = line.match(/^(\d+)[.)、]\s*(?:[☐☑✓○]\s*)?(.+)$/)
    if (match && (inPlan || /[☐☑✓○]/.test(line))) {
      items.push({
        step: Number(match[1]),
        text: match[2].replace(/^~~|~~$/g, '').trim(),
        completed: /[☑✓]/.test(line) || /~~.+~~/.test(line)
      })
    } else if (inPlan && items.length && line && !/^[-*]\s/.test(line)) {
      break
    }
  }
  return items
}

function parsePlanLines(lines) {
  if (!Array.isArray(lines)) return []
  return lines.map((line, index) => ({
    step: index + 1,
    text: String(line).replace(/^[☐☑✓○]\s*/, '').replace(/^~~|~~$/g, '').trim(),
    completed: /^[☑✓]/.test(String(line)) || /~~.+~~/.test(String(line))
  }))
}
function updatePlanFromWidget(lines) {
  planItems.value = parsePlanLines(lines)
}

async function requestSessionState() {
  try {
    await sendAgentCommand(activeTaskId.value, { id: 'codingto-session-state', type: 'get_state' })
  } catch {
    // The first prompt error is already surfaced by StartPrompt.
  }
}

async function requestSessionStats() {
  try {
    await sendAgentCommand(activeTaskId.value, { id: 'codingto-session-stats-ui', type: 'get_session_stats' })
  } catch {
    // Stats are supplementary and should never hide the actual response.
  }
}

async function respondExtensionDialog(payload) {
  const dialog = extensionDialog.value
  if (!dialog) return

  if (payload?.browserProfile) {
    extensionDialog.value = { ...dialog, saving: true, error: '' }
    const form = payload.browserProfile
    let saved = false
    try {
      const profile = await saveBrowserProfile({
        key: form.key,
        targetUrl: form.targetUrl,
        loginUrl: form.targetUrl,
        authMode: 'manual'
      })
      saved = true
      extensionDialog.value = null
      clearPersistedExtDialog()
      await sendAgentCommand(activeTaskId.value, { type: 'extension_ui_response', id: dialog.id, value: profile.id })
    } catch (err) {
      const message = localizeError(String(err))
      if (saved) {
        extensionDialog.value = null
        clearPersistedExtDialog()
        error.value = message
        pushErrorMessage(message)
        setTaskRunning(activeTaskId.value, false)
      } else {
        extensionDialog.value = { ...dialog, saving: false, error: message }
      }
    }
    return
  }

  extensionDialog.value = null
  clearPersistedExtDialog()
  const command = { type: 'extension_ui_response', id: dialog.id, ...payload }
  const abortCurrentTurn = shouldAbortAfterExtensionResponse(dialog, payload, {
    hasPendingPlan: planItems.value.length > 0
  })
  try {
    await sendAgentCommand(activeTaskId.value, command)
    if (abortCurrentTurn) {
      await stop()
      return
    }
    const value = String(payload?.value || '')
    if (payload?.cancelled || payload?.confirmed === false || value.startsWith('Stay')) {
      setTaskRunning(activeTaskId.value, false)
    }
  } catch (err) {
    const msg = localizeError(String(err))
    error.value = msg
    pushErrorMessage(msg)
    setTaskRunning(activeTaskId.value, false)
  }
}

async function compactContext() {
  if (!compaction.value.available || compaction.value.running || !messagesList.value.length) return
  setTaskCompaction(activeTaskId.value, { running: true, reason: 'manual', error: '', messageId: '' })
  try {
    await sendAgentCommand(activeTaskId.value, { id: 'codingto-compact', type: 'compact' })
  } catch (err) {
    const message = localizeError(String(err))
    handleCompactionEvent({
      type: 'compaction_end',
      reason: 'manual',
      errorMessage: message,
      _recordedAt: Date.now()
    }, activeTaskId.value, true)
  }
}

function latestRunningCompactionMessage() {
  return [...messagesList.value].reverse().find(message => (
    message.role === 'compaction' && message.status === 'running'
  ))
}

function handleCompactionEvent(event, taskId, sourceIsActive) {
  const key = String(taskId || activeTaskId.value || '')
  if (!key) return
  if (event.type === 'compaction_start') {
    let messageId = ''
    if (sourceIsActive) {
      const existing = latestRunningCompactionMessage()
      const message = existing || createCompactionMessage(event, crypto.randomUUID())
      if (!existing) messagesList.value.push(message)
      messageId = message.id
    }
    setTaskCompaction(key, {
      running: true,
      reason: event.reason || 'manual',
      error: '',
      messageId
    })
    return
  }

  let message = null
  if (sourceIsActive) {
    const state = compactionByTask.get(key)
    message = state?.messageId
      ? messagesList.value.find(item => item.id === state.messageId)
      : null
    message ||= latestRunningCompactionMessage()
    if (!message) {
      message = createCompactionMessage(event, crypto.randomUUID())
      messagesList.value.push(message)
    }
    completeCompactionMessage(message, event)
  }
  setTaskCompaction(key, {
    running: false,
    reason: event.reason || '',
    error: event.errorMessage || '',
    messageId: ''
  })
  if (sourceIsActive) requestSessionStats()
}

function handleSubagentEvent(event) {
  const sourceTaskId = eventTaskId(event)
  if (
    sourceTaskId !== ''
    && String(sourceTaskId) !== String(activeTaskId.value)
  ) return

  const toolCallId = String(event?.toolCallId || '')
  if (!toolCallId) return
  const tool = [...messagesList.value].reverse().find(item => (
    item.role === 'tool'
    && String(item.detail?.toolCallId || item.detail?.id || '') === toolCallId
  ))
  if (!tool) return

  const agentName = config.agents.find(agent => agent.id === event.agentKey)?.name || ''
  tool.detail = mergeSubagentRuntime(tool.detail, { ...event, agentName })
}

function handleAgentEvent(event) {
  const type = event?.type
  const sourceTaskId = eventTaskId(event)
  const hasSourceTask = sourceTaskId !== '' && sourceTaskId != null
  const sourceIsActive = !hasSourceTask || String(sourceTaskId) === String(activeTaskId.value)

  // Runtime ownership and rendering are both scoped by conversation. Always
  // consume lifecycle/progress events so background turns settle independently
  // even when another conversation is currently on screen.
  if (hasSourceTask && (type === 'agent_start' || type === 'auto_retry_start')) {
    setTaskRunning(sourceTaskId, true)
  }
  if (type === 'compaction_start' || type === 'compaction_end') {
    handleCompactionEvent(event, sourceTaskId, sourceIsActive)
    return
  }
  if (!sourceIsActive) {
    if (type === 'exec_progress') {
      const task = taskById(sourceTaskId)
      if (task) task.execDurationMs = firstNumber(event.totalMs, event.total_ms)
    } else if (type === 'extension_ui_request') {
      if (['select', 'confirm', 'input', 'editor'].includes(event.method)) {
        persistExtDialogForTask(sourceTaskId, event)
      } else if (event.method === 'setWidget' && event.widgetKey === 'plan-todos') {
        try {
          localStorage.setItem(PLAN_ITEMS_KEY + sourceTaskId, JSON.stringify(parsePlanLines(event.widgetLines)))
        } catch {}
      } else if (event.method === 'setWidget' && event.widgetKey === 'plan-execution') {
        try {
          localStorage.setItem(EXEC_PLAN_KEY + sourceTaskId, JSON.stringify(parsePlanLines(event.widgetLines)))
        } catch {}
      }
    } else if (type === 'agent_settled') {
      setTaskRunning(sourceTaskId, false)
      persistExtDialogForTask(sourceTaskId, null)
      refreshSessions().catch(() => {})
      void sendNextPendingPrompt(sourceTaskId)
    }
    return
  }

  if (type === 'response') {
    if (event.success === false) {
      const msg = localizeError(event.error || `${event.command || 'Agent command'} failed`)
      error.value = msg
      pushErrorMessage(msg)
      if (event.command === 'prompt') setTaskRunning(sourceTaskId || activeTaskId.value, false)
      if (event.command === 'compact') {
        const taskId = sourceTaskId || activeTaskId.value
        const failedEvent = { ...event, type: 'compaction_end', errorMessage: msg, reason: 'manual' }
        handleCompactionEvent(failedEvent, taskId, true)
      }
    // Pi's production response events omit the top-level `command` field (the
    // mock backend includes it, the real agent does not), so route by payload
    // shape as well. get_session_stats responses carry `data.tokens`.
    } else if (event.command === 'get_session_stats' || (event.data && event.data.tokens)) {
      applySessionStats(event.data)
    } else if (event.command === 'get_state') {
      const task = currentTask()
      if (event.data?.sessionFile) {
        activeSessionPath.value = event.data.sessionFile
        if (task) task.sessionPath = event.data.sessionFile
      }
    }
  } else if (type === 'agent_start') {
    setTaskRunning(sourceTaskId || activeTaskId.value, true)
    executionRunning.value = true
    // 新一轮任务开始：上一轮的执行计划条已过期，先清空，避免遮挡新一轮的计划面板
    executionPlan.value = []
    ensureAssistant()
  } else if (type === 'message_update') {
    const update = event.assistantMessageEvent || event.messageEvent || {}
    if (update.type === 'text_delta' || update.type === 'thinking_delta') {
      ensureAssistant(update.type === 'thinking_delta')
      if (update.type === 'thinking_delta') {
        activeAssistant.thinkingStartedAt ||= Date.now()
        activeAssistant.thinkingContent = (activeAssistant.thinkingContent || '') + (update.delta || '')
      }
      else activeAssistant.content += update.delta || ''
    } else if (update.type === 'thinking_start') {
      ensureAssistant(true)
      activeAssistant.thinkingStartedAt ||= Date.now()
    } else if (update.type === 'thinking_end' && activeAssistant?.thinkingStartedAt) {
      activeAssistant.thinkingDurationMs = Date.now() - activeAssistant.thinkingStartedAt
    } else if (['toolcall_start', 'tool_call_start', 'toolcall_delta', 'tool_call_delta', 'toolcall_end', 'tool_call_end'].includes(update.type)) {
      upsertToolMessage(update)
    } else if (update.type === 'error') {
      const msg = localizeError(update.error || update.content || update.message || t.modelError)
      error.value = msg
      pushErrorMessage(msg)
      setTaskRunning(sourceTaskId || activeTaskId.value, false)
    }
  } else if (type === 'message_end') {
    reconcileAssistantMessage(event.message)
  } else if (type === 'tool_execution_start') {
    upsertToolMessage(event)
  } else if (type === 'tool_execution_update' || type === 'tool_execution_end') {
    const toolId = toolEventId(event)
    const tool = [...messagesList.value].reverse().find(item => item.role === 'tool' && (item.detail?.toolCallId || item.detail?.id) === toolId)
    if (tool) {
      const startedAt = tool.detail?.startedAt || Date.now()
      tool.detail = {
        ...tool.detail,
        ...event,
        status: type === 'tool_execution_end' ? 'done' : 'running',
        startedAt,
        durationMs: type === 'tool_execution_end' ? Date.now() - startedAt : tool.detail?.durationMs
      }
    }
    if (type === 'tool_execution_end') {
      scheduleSessionChangesRefresh()
      maybeFocusDocumentArtifacts(tool, event)
    }
  } else if (type === 'auto_retry_start') {
    setTaskRunning(sourceTaskId || activeTaskId.value, true)
    executionRunning.value = true
    const seconds = Math.max(0, Math.ceil(Number(event.delayMs || 0) / 1000))
    const msg = `${localizeError(event.errorMessage || 'Connection error.')} · ${event.attempt || 1}/${event.maxAttempts || 1} · ${seconds}s`
    error.value = msg
    pushErrorMessage(msg)
  } else if (type === 'exec_progress') {
    executionElapsedMs.value = firstNumber(event.totalMs, event.total_ms)
    executionRunning.value = event.running !== false
    const task = tasks.value.find(item => item.id === firstNumber(event.sessionId, activeTaskId.value))
    if (task) task.execDurationMs = executionElapsedMs.value
  } else if (type === 'agent_end') {
    if (event.willRetry) {
      setTaskRunning(sourceTaskId || activeTaskId.value, true)
      executionRunning.value = true
      if (activeAssistant && !activeAssistant.content && !activeAssistant.thinkingContent) {
        messagesList.value = messagesList.value.filter(message => message !== activeAssistant)
        activeAssistant = null
      }
      syncCurrentTask()
      return
    }
    const lastAssistant = [...(event.messages || [])].reverse().find(message => message?.role === 'assistant')
    const extracted = parsePlanItems(messageText(lastAssistant) || activeAssistant?.content)
    if (extracted.length) planItems.value = extracted
    if (activeAssistant) {
      activeAssistant.live = false
      if (activeAssistant.thinkingStartedAt && !activeAssistant.thinkingDurationMs) {
        activeAssistant.thinkingDurationMs = Date.now() - activeAssistant.thinkingStartedAt
      }
    }
    for (const tool of messagesList.value.filter(message => message.role === 'tool' && message.detail?.status !== 'done')) {
      tool.detail = { ...tool.detail, status: 'done', endedAt: Date.now(), durationMs: Date.now() - (tool.detail?.startedAt || Date.now()) }
    }
    // 本轮是否产出了有效回复：有则视为成功；成功时清掉消息流中本轮产生的
    // 重试报错（避免红字干扰结果），最终失败则保留所有报错供用户回看。
    const succeeded = Boolean(messageText(lastAssistant)) || Boolean(activeAssistant?.content) || Boolean(activeAssistant?.thinkingContent)
    activeAssistant = null
    if (succeeded) {
      error.value = ''
      messagesList.value = messagesList.value.filter(message => message.role !== 'error')
    }
    pushChangeMessage(event.changeSummary, event._recordedAt)
    clearPersistedExtDialog(currentTask()?.id)
    requestSessionState()
    refreshSessions().catch(() => {})
    scheduleSessionChangesRefresh(60)
  } else if (type === 'agent_settled') {
    setTaskRunning(sourceTaskId || activeTaskId.value, false)
    executionRunning.value = false
    requestSessionStats()
    refreshSessions().catch(() => {})
    void sendNextPendingPrompt(sourceTaskId || activeTaskId.value)
  } else if (type === 'extension_ui_request') {
    if (type && event.method === 'notify') {
      pushToast(event.notifyType === 'error' ? 'error' : event.notifyType === 'warning' ? 'info' : 'success', event.message || '')
    } else if (event.method === 'setStatus') {
      if (event.statusText) extensionStatuses[event.statusKey] = event.statusText
      else delete extensionStatuses[event.statusKey]
    } else if (event.method === 'setWidget') {
      const key = event.widgetKey
      const lines = event.widgetLines
      if (key === 'plan-todos') {
        planItems.value = parsePlanLines(lines)
        // 出现新的计划提案时，清空上一轮残留的执行计划，否则计划面板会被执行计划条遮挡
        if (lines && lines.length) executionPlan.value = []
      } else if (key === 'plan-execution') {
        executionPlan.value = parsePlanLines(lines)
      } else if (key) {
        if (lines) extensionWidgets[key] = lines
        else delete extensionWidgets[key]
      }
    } else if (['select', 'confirm', 'input', 'editor'].includes(event.method)) {
      extensionDialog.value = event
      setTaskRunning(sourceTaskId || activeTaskId.value, true)
      persistExtDialog()
    }
  } else if (type === 'error') {
    const msg = event.code === 'model_first_response_timeout'
      ? t.value.modelFirstResponseTimeout
      : localizeError(typeof event.error === 'string' ? event.error : event.error?.message || 'Agent error')
    error.value = msg
    pushErrorMessage(msg)
    setTaskRunning(sourceTaskId || activeTaskId.value, false)
  }
  syncCurrentTask()
}

function ensureAssistant(thinking = false) {
  if (!activeAssistant) {
    activeAssistant = reactive({
      id: crypto.randomUUID(), role: 'assistant', content: '', thinking,
      thinkingOpen: true, live: true, createdAt: Date.now()
    })
    messagesList.value.push(activeAssistant)
  }
}

function messageThinking(message) {
  if (!Array.isArray(message?.content)) return ''
  return message.content
    .filter(block => block?.type === 'thinking')
    .map(block => block.thinking || '')
    .join('')
}

function messageToolCalls(message) {
  if (!Array.isArray(message?.content)) return []
  return message.content.filter(block => block?.type === 'toolCall')
}

function reconcileAssistantMessage(message) {
  if (!message || message.role !== 'assistant') return
  const text = messageText(message)
  const thinking = messageThinking(message)
  const toolCalls = messageToolCalls(message)
  if (text || thinking) {
    ensureAssistant(Boolean(thinking))
    if (text) activeAssistant.content = text
    if (thinking) activeAssistant.thinkingContent = thinking
    activeAssistant.live = false
    if (activeAssistant.thinkingStartedAt && !activeAssistant.thinkingDurationMs) {
      activeAssistant.thinkingDurationMs = Date.now() - activeAssistant.thinkingStartedAt
    }
  } else if (activeAssistant && !activeAssistant.content && !activeAssistant.thinkingContent) {
    messagesList.value = messagesList.value.filter(messageItem => messageItem !== activeAssistant)
  }
  for (const toolCall of toolCalls) upsertToolMessage({ toolCall })
  activeAssistant = null
}

function toolCallFromEvent(event) {
  if (event?.toolCall?.id) return event.toolCall
  const content = event?.partial?.content
  if (!Array.isArray(content)) return {}
  const indexed = content[Number(event.contentIndex)]
  if (indexed?.type === 'toolCall' && indexed.id) return indexed
  return [...content].reverse().find(block => block?.type === 'toolCall' && block.id) || {}
}

function toolEventId(event) {
  const call = toolCallFromEvent(event)
  return event?.toolCallId || event?.id || call.id || ''
}

function upsertToolMessage(event) {
  const call = toolCallFromEvent(event)
  const toolId = toolEventId(event)
  if (!toolId) return null
  let tool = [...messagesList.value].reverse().find(item => item.role === 'tool' && (item.detail?.toolCallId || item.detail?.id) === toolId)
  const input = call.arguments ?? call.partialArgs ?? event.args ?? event.input ?? event.arguments
  if (!tool) {
    tool = reactive({
      id: `tool-${toolId}`,
      role: 'tool',
      content: event.toolName || event.name || call.name || 'Tool',
      detail: { ...event, ...call, toolCallId: toolId, input, status: 'running', startedAt: Date.now() },
      createdAt: Date.now()
    })
    messagesList.value.push(tool)
  } else {
    tool.content = event.toolName || event.name || call.name || tool.content
    tool.detail = { ...tool.detail, ...event, ...call, toolCallId: toolId, input: input ?? tool.detail?.input }
  }
  return tool
}

async function runPrompt({ message, images, attachments: promptAttachments, skillPath, agentId, provider, model, workDir, mode: promptMode, thinkingLevel: promptThinking }) {
  error.value = ''
  const task = await ensureConversation(message)
  const promptAgent = config.agents.find(agent => agent.id === agentId) || selectedAgent.value
  // 新问题真正进入消息流时，折叠此前所有轮次的思考；本轮新产生的
  // assistant 消息会由 ensureAssistant 以展开状态创建。
  collapsePreviousThinking()
  const attachmentMeta = (promptAttachments || []).map((a) => ({ name: a.name, kind: a.kind, size: a.size, path: a.path || '' }))
  messagesList.value.push({
    id: crypto.randomUUID(),
    role: 'user',
    content: message || (promptAttachments?.length ? '' : `[${images?.length || 0} images]`),
    images: images || [],
    attachments: attachmentMeta
  })
  draft.value = ''
  setTaskRunning(task.id, true)
  activeAssistant = null
  try {
    await startPrompt({
      agentId: promptAgent?.id,
      message,
      provider: task.provider || provider || promptAgent?.defaultProvider || config.defaultProvider,
      model: task.model || model || promptAgent?.defaultModel || config.defaultModel,
      // 当 agent 未单独设置默认模型，且前端也没显式指定时，回退到首个启用服务商，
      // 避免把空 provider/model 发到后端去触发全局 openai 默认。
      fallbackProvider: selectedAgent.value?.defaultProvider || config.defaultProvider,
      fallbackModel: selectedAgent.value?.defaultModel || config.defaultModel,
      workDir: workDir || config.lastEnvironment,
      mode: promptMode || mode.value,
      thinkingLevel: promptThinking || thinkingLevel.value,
      skillPath: skillPath || '',
      images,
      attachments: toAttachmentInputs(promptAttachments || []),
      sessionId: task.id,
      sessionPath: task.sessionPath || ''
    })
    // StartPrompt has persisted the prompt node before returning. Refresh now
    // so the right sidebar shows the running node even before the first edit.
    scheduleSessionChangesRefresh(0)
    // 在当前工作空间发消息：将该工作空间置顶。
    bumpWorkspaceToTop(config.activeEnvId)
    requestSessionState()
  } catch (err) {
    setTaskRunning(task.id, false)
    const raw = String(err)
    if (raw.toLowerCase().includes('prompt canceled')) {
      error.value = ''
      return
    }
    const msg = localizeError(raw)
    error.value = msg
    pushErrorMessage(msg)
  }
}

const pendingPromptDispatching = reactive(new Set())

async function runBackgroundPrompt(taskId, prompt) {
  const task = taskById(taskId)
  if (!task) return
  const environment = config.environments.find(item => item.id === task.environmentId)
  setTaskRunning(taskId, true)
  try {
    await startPrompt({
      agentId: task.agentId || prompt.agentId,
      message: prompt.message,
      provider: task.provider || prompt.provider,
      model: task.model || prompt.model,
      workDir: environment?.path || prompt.workDir,
      mode: prompt.mode,
      thinkingLevel: prompt.thinkingLevel,
      skillPath: prompt.skillPath || '',
      images: prompt.images,
      attachments: toAttachmentInputs(prompt.attachments || []),
      sessionId: task.id,
      sessionPath: task.sessionPath || prompt.sessionPath || ''
    })
  } catch (err) {
    setTaskRunning(taskId, false)
    pushToast('error', localizeError(String(err)))
  }
}

async function sendNextPendingPrompt(taskId = activeTaskId.value) {
  const key = String(taskId)
  const queue = pendingPromptList(taskId)
  if (!taskId || runningTaskIds.has(key) || pendingPromptDispatching.has(key) || !queue.length) return
  const next = queue.shift()
  pendingPromptDispatching.add(key)
  try {
    if (String(activeTaskId.value) === key) await runPrompt(next)
    else await runBackgroundPrompt(taskId, next)
  } finally {
    pendingPromptDispatching.delete(key)
    // A prompt can fail before an agent run starts. In that case continue with
    // the remaining queue; successful starts stay gated by this conversation.
    if (!runningTaskIds.has(key) && queue.length) queueMicrotask(() => sendNextPendingPrompt(taskId))
  }
}

async function sendPrompt() {
  const message = draft.value.trim()
  if (attachmentReadsPending.value > 0 || (!message && !promptImages.value.length && !attachments.value.length) || !selectedModelValue.value) return
  const prompt = {
    id: crypto.randomUUID(),
    message,
    images: safeClone(promptImages.value),
    attachments: safeClone(attachments.value),
    createdAt: Date.now(),
    agentId: selectedAgent.value?.id,
    provider: selectedAgent.value?.defaultProvider || config.defaultProvider,
    model: selectedAgent.value?.defaultModel || config.defaultModel,
    workDir: config.lastEnvironment,
    mode: mode.value,
    thinkingLevel: thinkingLevel.value,
    skillPath: selectedSkill.value?.agents?.find(agent => agent.id === selectedAgent.value?.id)?.path || '',
    sessionPath: currentTask()?.sessionPath || ''
  }
  draft.value = ''
  // 在首页（新建对话）发送后，草稿已消费，清除已保存的未发送内容；
  // 历史会话里的续写发送不在此列，不触碰工作空间草稿。
  if (isHomeMode.value) persistDraftForEnv(config.activeEnvId, '')
  promptImages.value = []
  attachments.value = []
  selectedSkill.value = null
  if (activeTaskRunning.value) {
    pendingPrompts.value.push(prompt)
    return
  }
  await runPrompt(prompt)
}

function editPendingPrompt(id) {
  const index = pendingPrompts.value.findIndex(item => item.id === id)
  if (index < 0) return
  const [prompt] = pendingPrompts.value.splice(index, 1)
  draft.value = prompt.message
  promptImages.value = safeClone(prompt.images || [])
  attachments.value = safeClone(prompt.attachments || [])
  selectedSkill.value = skills.value.find(skill => skill.agents?.some(agent => agent.path === prompt.skillPath && agent.id === prompt.agentId)) || null
}

function deletePendingPrompt(id) {
  const index = pendingPrompts.value.findIndex(item => item.id === id)
  if (index >= 0) pendingPrompts.value.splice(index, 1)
}

async function stop() {
  const taskId = activeTaskId.value
  const key = String(taskId)
  if (!activeTaskRunning.value || activeTaskStopping.value) return
  stoppingTaskIds.add(key)
  try {
    await abortPrompt(taskId)
  } catch (err) {
    stoppingTaskIds.delete(key)
    error.value = localizeError(String(err))
  }
}

async function pickWorkspace() {
  const value = await chooseWorkspace()
  if (value) config.lastEnvironment = value
}

async function pickSessionDirectory() {
  const value = await chooseSessionDir()
  if (value) config.sessionDir = value
}

// --- ChatView 交互回调 ---

async function chatNewSession() {
  syncCurrentTask()
  activeTaskId.value = ''
  currentAgentId.value = config.activeAgentId
  const defaultAgent = config.agents.find(agent => agent.id === config.activeAgentId)
  if (defaultAgent?.defaultProvider && defaultAgent?.defaultModel) {
    config.defaultProvider = defaultAgent.defaultProvider
    config.defaultModel = defaultAgent.defaultModel
  } else {
    // agent 未单独设置默认模型时，回退到首个启用服务商的首个模型，
    // 避免出现空默认模型导致后端校验拦截（config 字段仅作镜像）。
    const first = config.providers.find(p => p.enabled !== false && (p.models || []).length)
    config.defaultProvider = first?.name || ''
    config.defaultModel = first?.models?.[0]?.id || ''
  }
  thinkingLevel.value = defaultThinkingLevelForModel(selectedModel.value)
  changeRefreshRequest += 1
  messagesList.value = []
  sessionChanges.value = { root: '', nodes: [], files: [], added: 0, deleted: 0 }
  sessionChangesLoading.value = false
  activeAssistant = null
  error.value = ''
  // 新建对话时恢复该工作空间已保存的未发送草稿，而不是清空，
  // 这样「输入内容 → 切走 → 再点新建对话」仍能把内容带回。
  draft.value = loadDraftForEnv(config.activeEnvId)
  pendingPrompts.value = []
  selectedSkill.value = null
  promptImages.value = []
  attachments.value = []
  tokenStats.value = { input: 0, cached: 0, cacheWrite: 0, output: 0, total: 0 }
  contextUsage.value = { tokens: 0, contextWindow: contextWindow.value, percent: 0 }
  executionElapsedMs.value = 0
  executionRunning.value = false
  planItems.value = []
  executionPlan.value = []
  extensionDialog.value = null
  goHome()
}

async function selectTask(task) {
  if (!task) return
  if (String(task.id) === String(activeTaskId.value)) {
    goHome()
    return
  }
  // 离开首页（新建对话）进入历史会话前，先把未发送草稿存好；
  // 历史会话的输入框不显示该草稿。
  if (isHomeMode.value) persistDraftForEnv(config.activeEnvId, draft.value)
  syncCurrentTask()
  // 先切到对话框并展示加载动画，再异步拉取历史，避免点击后长时间无反应
  activeTaskId.value = task.id
  draft.value = ''
  sessionChanges.value = { root: '', nodes: [], files: [], added: 0, deleted: 0 }
  refreshSessionChanges()
  goHome()
  loadingHistory.value = true
  messagesList.value = []
  try {
    const history = await getSessionHistory(task.id)
    messagesList.value = initializeThinkingVisibility(safeClone(history?.messages || []))
    // Token stats come from the backend aggregation (authoritative for history,
    // including sessions recorded before this fix). Fall back to the snapshot
    // saved on the task when no fresh aggregation is available.
    tokenStats.value = { input: 0, cached: 0, cacheWrite: 0, output: 0, total: 0 }
    if (history?.tokenStats) {
      applySessionStats({ tokens: history.tokenStats })
    } else if (task.tokenStats) {
      tokenStats.value = { ...tokenStats.value, ...task.tokenStats }
    }
    contextUsage.value = { tokens: 0, contextWindow: contextWindow.value, percent: 0, ...(task.contextUsage || {}) }
    activeSessionPath.value = task.sessionPath || ''
    planItems.value = safeClone(task.planItems || [])
    restorePlanItems(task.id)
    executionPlan.value = []
    restoreExecPlan(task.id)
    // 若存在待确认的计划，则隐藏上一轮残留的执行计划条，确保计划面板正常显示
    if (planItems.value.length) executionPlan.value = []
    executionElapsedMs.value = Number(task.execDurationMs) || 0
    executionRunning.value = activeTaskRunning.value
    if (task.agentId && config.agents.some(agent => agent.id === task.agentId)) currentAgentId.value = task.agentId
    if (task.environmentId && config.environments.some(env => env.id === task.environmentId)) {
      config.activeEnvId = task.environmentId
      config.lastEnvironment = config.environments.find(env => env.id === task.environmentId)?.path || config.lastEnvironment
    }
    if (task.provider && config.providers.some(provider => provider.name === task.provider)) {
      config.defaultProvider = task.provider
      config.defaultModel = task.model || config.defaultModel
    }
    activeAssistant = executionRunning.value
      ? [...messagesList.value].reverse().find(message => message.role === 'assistant' && message.live) || null
      : null
    extensionDialog.value = null
    // 任务仍在进行（如等待用户确认计划）时，恢复持久化的扩展对话框，避免刷新后无对话框可操作
    if (task.status === 'running') restoreExtDialog(task.id)
    error.value = ''
    // Browsing history must not mutate the shared Pi runtime. StartPrompt loads
    // this session path atomically when the user actually sends the next prompt.
  } finally {
    loadingHistory.value = false
  }
  if (
    String(activeTaskId.value) === String(task.id)
    && !activeTaskRunning.value
    && pendingPromptList(task.id).length
  ) queueMicrotask(sendNextPendingPrompt)
}

function chatSelectEnvironment(env) {
  if (env?.id === config.activeEnvId) return
  // 切换工作空间时，仅当处于首页模式才保存/载入草稿；历史会话视图下
  // 输入框为空，不覆盖目标工作空间已保存的未发送草稿。
  if (isHomeMode.value) persistDraftForEnv(config.activeEnvId, draft.value)
  config.activeEnvId = env.id
  config.lastEnvironment = env.path
  draft.value = isHomeMode.value ? loadDraftForEnv(env.id) : ''
  persist()
}

function chatSelectAgent(agent) {
  if (!agent) return
  currentAgentId.value = agent.id
  selectedSkill.value = null
  if (agent.defaultProvider && agent.defaultModel) {
    config.defaultProvider = agent.defaultProvider
    config.defaultModel = agent.defaultModel
  } else {
    const first = config.providers.find(p => p.enabled !== false && (p.models || []).length)
    config.defaultProvider = first?.name || ''
    config.defaultModel = first?.models?.[0]?.id || ''
  }
  persist()
}

function chatNewEnvironment() {
  openWsEditor(null)
}

function chatOpenAgentConfig() {
  activePage.value = 'agents'
}

function onModelChange(value) {
  const index = value.indexOf('/')
  if (index < 0) return
  config.defaultProvider = value.slice(0, index)
  config.defaultModel = value.slice(index + 1)
  if (selectedAgent.value) {
    selectedAgent.value.defaultProvider = config.defaultProvider
    selectedAgent.value.defaultModel = config.defaultModel
  }
  persist()
}

function onRemoveImage(index) {
  if (index >= 0 && index < promptImages.value.length) promptImages.value.splice(index, 1)
}

function onAddImages(images) {
  if (!images || !images.length) return
  for (const image of images) promptImages.value.push(image)
  if (promptImages.value.length > 10) promptImages.value = promptImages.value.slice(-10)
}

async function persist() {
  saving.value = true
  saved.value = false
  try {
    // 若已没有任何服务商（或没有任何模型），清空默认模型指向，避免后端校验拦截。
    const hasAnyProvider = (config.providers || []).length > 0
    const hasAnyModel = config.providers.some(p => (p.models || []).length > 0)
    if (!hasAnyProvider || !hasAnyModel) {
      config.defaultProvider = ''
      config.defaultModel = ''
    }
    const result = await saveConfig(JSON.parse(JSON.stringify(config)))
    // 防御：若后端返回的 environments 异常（null/undefined），保留本地数据
    if (!Array.isArray(result.environments)) {
      result.environments = config.environments
    }
    Object.assign(config, result)
    saved.value = true
    setTimeout(() => { saved.value = false }, 1800)
    pushToast('success', t.value.toastConfigSaved)
    return true
  } catch (err) {
    const message = localizeError(String(err))
    error.value = message
    pushToast('error', t.value.toastConfigFailed.replace('{error}', message))
    return false
  } finally {
    saving.value = false
  }
}

// 后端返回的错误可能仍是英文，按关键词做最小化中文化兜底。
function localizeError(raw) {
  if (!raw) return raw
  const map = [
    [/duplicate agent id/i, 'Agent ID 重复'],
    [/no enabled model providers/i, '没有启用的服务商'],
    [/invalid base URL/i, '基础域名无效'],
    [/requires a base URL/i, '需要填写基础域名'],
    [/unsupported API protocol/i, '不支持的 API 协议'],
    [/does not exist or its provider is disabled/i, '默认模型不存在或所属服务商已停用'],
    [/maximum concurrent task limit reached/i, '并发任务已达到上限（4）'],
    [/empty (ID|key)/i, '存在空的名称或标识'],
    [/API key environment variable (\w+) is not set for provider (.+)/i, '所选模型缺少 API Key 环境变量，请先在模型设置中配置'],
  ]
  for (const [re, text] of map) {
    if (re.test(raw)) return text
  }
  return raw
}

const piCompatBooleanFields = [
  { key: 'supportsStore', hint: 'compatSupportsStore' },
  { key: 'supportsDeveloperRole', hint: 'compatSupportsDeveloperRole' },
  { key: 'supportsReasoningEffort', hint: 'compatSupportsReasoningEffort' },
  { key: 'supportsUsageInStreaming', hint: 'compatSupportsUsageInStreaming' },
  { key: 'requiresToolResultName', hint: 'compatRequiresToolResultName' },
  { key: 'requiresAssistantAfterToolResult', hint: 'compatRequiresAssistantAfterToolResult' },
  { key: 'requiresThinkingAsText', hint: 'compatRequiresThinkingAsText' },
  { key: 'requiresReasoningContentOnAssistantMessages', hint: 'compatRequiresReasoningContent' },
  { key: 'zaiToolStream', hint: 'compatZaiToolStream' },
  { key: 'supportsStrictMode', hint: 'compatSupportsStrictMode' },
  { key: 'sendSessionAffinityHeaders', hint: 'compatSendSessionAffinityHeaders' },
  { key: 'sendSessionIdHeader', hint: 'compatSendSessionIdHeader' },
  { key: 'supportsLongCacheRetention', hint: 'compatSupportsLongCacheRetention' }
]

const piThinkingFormats = [
  'openai', 'openrouter', 'deepseek', 'together', 'zai',
  'qwen', 'chat-template', 'qwen-chat-template', 'string-thinking', 'ant-ling'
]

const piThinkingLevels = ['off', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max']

function ensureCompat(target) {
  if (!target.compat || Array.isArray(target.compat) || typeof target.compat !== 'object') target.compat = {}
  return target.compat
}

function compatBooleanValue(target, key) {
  const value = target?.compat?.[key]
  return typeof value === 'boolean' ? String(value) : ''
}

function setCompatBoolean(target, key, value) {
  const compat = ensureCompat(target)
  if (value === '') delete compat[key]
  else compat[key] = value === 'true'
}

function compatStringValue(target, key) {
  const value = target?.compat?.[key]
  return typeof value === 'string' ? value : ''
}

function setCompatString(target, key, value) {
  const compat = ensureCompat(target)
  if (value === '') delete compat[key]
  else compat[key] = value
}

function formatCompat(target) {
  return JSON.stringify(target?.compat || {}, null, 2)
}

function updateCompatJson(target, event) {
  const raw = String(event?.target?.value || '').trim()
  try {
    const parsed = raw ? JSON.parse(raw) : {}
    if (!parsed || Array.isArray(parsed) || typeof parsed !== 'object') throw new Error('compat must be an object')
    target.compat = parsed
    event.target.value = formatCompat(target)
  } catch {
    if (event?.target) event.target.value = formatCompat(target)
    pushToast('error', t.value.piCompatInvalidJson)
  }
}

function protocolEndpoint(api) {
  if (api === 'openai-responses') return 'responses'
  if (api === 'anthropic-messages') return 'messages'
  if (api === 'google-generative-ai') return 'models/{model}:streamGenerateContent'
  return 'chat/completions'
}

function effectiveModelBaseUrl(provider, model) {
  const providerBase = String(provider?.baseUrl || '').trim().replace(/\/+$/, '')
  const modelBase = String(model?.baseUrl || '').trim()
  if (!modelBase) return providerBase
  if (/^https?:\/\//i.test(modelBase)) return modelBase.replace(/\/+$/, '')
  if (!providerBase) return modelBase
  return `${providerBase}/${modelBase.replace(/^\/+/, '')}`.replace(/\/+$/, '')
}

function modelRequestRoute(provider, model) {
  const base = effectiveModelBaseUrl(provider, model) || '—'
  return base === '—' ? base : `${base}/${protocolEndpoint(model?.api)}`
}

function normalizeProvider(provider) {
  provider.enabled ??= true
  const legacyApi = provider.api || (provider.type === 'anthropic' ? 'anthropic-messages' : provider.type === 'google' ? 'google-generative-ai' : provider.type === 'openai-responses' ? 'openai-responses' : 'openai-completions')
  ensureCompat(provider)
  provider.models ||= []
  provider.models.forEach(model => {
    model.api ||= legacyApi
    model.baseUrl ||= ''
    model.input ||= ['text']
    model.maxTokens ||= 16384
    model.capabilities ||= { toolCall: true }
    ensureCompat(model)
  })
  provider.api = ''
  return provider
}

// formatTokens 把 token 数压缩为带 K / M 的简短形式，例如 128000 → 128K。
function formatTokens(value) {
  const n = Number(value) || 0
  if (n >= 1_000_000) return `${(n / 1_000_000).toLocaleString(undefined, { maximumFractionDigits: 1 })}M`
  if (n >= 1000) return `${(n / 1000).toLocaleString(undefined, { maximumFractionDigits: 1 })}K`
  return String(n)
}

function buildProviderDraft() {
  if (selectedPreset.value) {
    const preset = bootstrap.value?.providerPresets?.find(item => item.name === selectedPreset.value)
    if (preset) return normalizeProvider(safeClone(preset))
  }
  const id = `provider-${config.providers.length + 1}`
  return normalizeProvider({ name: id, baseUrl: 'http://127.0.0.1:8080', apiKey: '', enabled: true, models: [] })
}

const editingNewProvider = ref(false)

function openProviderEditor() {
  selectedPreset.value = ''
  showProviderApiKey.value = false
  providerDraft.value = buildProviderDraft()
  editingNewProvider.value = true
  providerEditorOpen.value = true
}

function openProviderEdit(provider) {
  // 先打开弹窗，避免后续任何异常阻断渲染；克隆用 JSON 方式以兼容 Vue reactive 代理。
  providerEditorOpen.value = true
  editingNewProvider.value = false
  selectedPreset.value = ''
  try {
    providerDraft.value = normalizeProvider(JSON.parse(JSON.stringify(provider)))
  } catch (err) {
    providerDraft.value = {}
  }
  providerDraft.value.__name = provider.name
}

function onProviderPresetChange() {
  providerDraft.value = buildProviderDraft()
}

function closeProviderEditor() {
  providerEditorOpen.value = false
  providerDraft.value = null
  selectedPreset.value = ''
  editingNewProvider.value = false
}

function confirmAddProvider() {
  const copy = providerDraft.value
  if (!copy) return
  const base = copy.name || `provider-${config.providers.length + 1}`
  copy.name = base
  let suffix = 2
  while (config.providers.some(item => item.name === copy.name)) copy.name = `${base}-${suffix++}`
  // providerDraft 是响应式代理，避免把 Proxy 推入配置；改用 JSON 深拷贝。
  const plain = JSON.parse(JSON.stringify(copy))
  delete plain.__name
  config.providers.push(plain)
  selectedModelsProviderName.value = plain.name
  if (!config.defaultProvider && plain.models?.length) {
    config.defaultProvider = plain.name
    config.defaultModel = plain.models[0].id
  }
  closeProviderEditor()
  persist()
}

function confirmSaveProvider() {
  const draft = providerDraft.value
  if (!draft) return
  const target = config.providers.find(item => item.name === draft.__name)
  if (!target) return
  const base = draft.name || `provider-${config.providers.length + 1}`
  draft.name = base
  let suffix = 2
  while (config.providers.some(item => item.name === draft.name && item !== target)) draft.name = `${base}-${suffix++}`
  // 若改了标识（name），且该项正是当前默认服务商，需要同步默认指向，
  // 否则后端会判定默认模型已失效而拒绝保存。
  if (draft.__name !== draft.name && config.defaultProvider === draft.__name) {
    config.defaultProvider = draft.name
    const migrated = config.providers.some(p => p.name === draft.name && (p.models || []).some(m => m.id === config.defaultModel))
    if (!migrated) config.defaultModel = ''
  }
  // providerDraft 是 Vue 响应式代理，structuredClone 无法克隆 Proxy，
  // 改用 JSON 深拷贝；随后剔除内部标记字段 __name。
  const copy = JSON.parse(JSON.stringify(draft))
  delete copy.__name
  Object.assign(target, copy)
  persist()
  closeProviderEditor()
}

function addModel(provider) {
  provider.models ||= []
  provider.models.push({ id: '', name: '', api: 'openai-completions', baseUrl: '', contextWindow: 128000, maxTokens: 16384, reasoning: false, defaultThinkingLevel: 'off', input: ['text'], capabilities: { toolCall: true }, compat: {} })
}

function toggleImageInput(model, enabled) {
  model.input = enabled ? ['text', 'image'] : ['text']
}

function selectModelsProvider(provider) {
  selectedModelsProviderName.value = provider?.name || ''
  showProviderApiKey.value = false
}

function renameModelsProvider(event) {
  const provider = selectedModelsProvider.value
  if (!provider) return
  const previousName = provider.name
  const nextName = String(event.target.value || '').trim()
  if (!nextName || config.providers.some(item => item !== provider && item.name === nextName)) {
    event.target.value = previousName
    return
  }
  provider.name = nextName
  selectedModelsProviderName.value = nextName
  if (config.defaultProvider === previousName) config.defaultProvider = nextName
}

function requestDeleteProvider(provider) {
  pendingDeleteProvider.value = provider
}

async function confirmDeleteProvider() {
  const provider = pendingDeleteProvider.value
  if (!provider) return
  const snapshot = safeClone(config.providers)
  const previousDefaultProvider = config.defaultProvider
  const previousDefaultModel = config.defaultModel
  const index = config.providers.findIndex(item => item === provider || item.name === provider.name)
  if (index < 0) return
  config.providers.splice(index, 1)
  const fallback = config.providers[Math.min(index, config.providers.length - 1)] || config.providers[0]
  selectedModelsProviderName.value = fallback?.name || ''
  if (config.defaultProvider === provider.name) {
    const fallbackWithModel = config.providers.find(item => item.enabled !== false && item.models?.length)
    config.defaultProvider = fallbackWithModel?.name || ''
    config.defaultModel = fallbackWithModel?.models?.[0]?.id || ''
  }
  pendingDeleteProvider.value = null
  if (!await persist()) {
    config.providers.splice(0, config.providers.length, ...snapshot)
    config.defaultProvider = previousDefaultProvider
    config.defaultModel = previousDefaultModel
    selectedModelsProviderName.value = provider.name
  }
}

// ---- Attachment upload (design §A2) ----
// Each attachment is { path?, name, mimeType, data?, kind, size, imagePreview? }.
// Picker items carry a real OS path; drag-drop items carry base64 data.
const attachments = ref([])

function attachmentKindFromMime(mime) {
  if (!mime) return 'other'
  if (mime.startsWith('image/')) return 'image'
  if (mime.startsWith('audio/')) return 'audio'
  if (mime.startsWith('video/')) return 'video'
  if (mime.startsWith('text/') || /(pdf|word|excel|powerpoint|spreadsheet|presentation|officedocument|opendocument)/i.test(mime)) return 'document'
  return 'other'
}

function attachmentMime(file) {
  const supplied = String(file?.type || file?.mimeType || '').toLowerCase()
  if (supplied) return supplied
  const ext = String(file?.name || '').split('.').pop()?.toLowerCase()
  const known = {
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
  return known[ext] || 'application/octet-stream'
}

function isNativePathAttachment(file) {
  return typeof file?.path === 'string' && file.path.trim() !== ''
}

async function onAddAttachments(files) {
  if (!files || !files.length) return
  const acceptedFiles = []
  const maxCount = 10
  const maxBytes = 50 * 1024 * 1024
  const maxTotal = 100 * 1024 * 1024
  let total = attachments.value.reduce((sum, item) => sum + (Number(item.size) || 0), 0)
  for (const file of Array.from(files)) {
    if (attachments.value.length + acceptedFiles.length >= maxCount) {
      pushToast('error', t.value.attachmentErrorCount?.replace('{count}', String(maxCount)) || `最多 ${maxCount} 个附件`)
      break
    }
    if (file.size > maxBytes) {
      pushToast('error', t.value.attachmentErrorSize?.replace('{name}', file.name) || `${file.name} 超过大小限制`)
      continue
    }
    if (total + file.size > maxTotal) {
      pushToast('error', t.value.attachmentErrorTotal || '附件总大小超过限制')
      break
    }
    acceptedFiles.push(file)
    total += file.size
  }
  if (!acceptedFiles.length) return

  // Materialize lightweight preview entries before reading the full payload so
  // the composer responds immediately, even for several large files.
  const pendingItems = acceptedFiles.map((file) => {
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
    reads.push(new Promise((resolve) => {
      const reader = new FileReader()
      reader.onload = () => {
        const dataUrl = String(reader.result || '')
        const comma = dataUrl.indexOf(',')
        const data = comma >= 0 ? dataUrl.slice(comma + 1) : dataUrl
        resolve({
          id: pending.id,
          data,
          reading: false,
          imagePreview: pending.kind === 'image' ? `data:${pending.mimeType};base64,${data}` : ''
        })
      }
      reader.onerror = () => resolve({ id: pending.id, error: true })
      reader.readAsDataURL(file)
    }))
  }
  if (!reads.length) return

  attachmentReadsPending.value += 1
  try {
    const results = await Promise.all(reads)
    for (const pending of pendingItems) {
      if (pending.imagePreview.startsWith('blob:')) URL.revokeObjectURL(pending.imagePreview)
    }
    const byID = new Map(results.map((item) => [item.id, item]))
    attachments.value = attachments.value.flatMap((item) => {
      const result = byID.get(item.id)
      if (!result) return [item]
      if (result.error) return []
      return [{ ...item, ...result }]
    })
    if (results.some((item) => item.error)) {
      pushToast('error', t.value.attachmentReadError || '无法读取部分附件')
    }
  } finally {
    attachmentReadsPending.value = Math.max(0, attachmentReadsPending.value - 1)
  }
}

function onRemoveAttachment(index) {
  if (index >= 0 && index < attachments.value.length) {
    const preview = attachments.value[index]?.imagePreview
    if (String(preview || '').startsWith('blob:')) URL.revokeObjectURL(preview)
    attachments.value.splice(index, 1)
    attachments.value = attachments.value.slice()
  }
}

function toAttachmentInputs(list) {
  return list.map((a) => ({
    path: a.path || '',
    name: a.name,
    mimeType: a.mimeType,
    data: a.data || ''
  }))
}

function removeProvider(index) {
  config.providers.splice(index, 1)
}

const testingModel = ref('') // 兼容旧逻辑：非空表示有模型正在测试
const testingModels = reactive({}) // 按模型独立记录，避免一个卡住全部禁用
const testResult = reactive({})
function testModelKey(provider, model) { return `${provider.name}/${model.id}` }
async function runModelTest(provider, model) {
  const key = testModelKey(provider, model)
  if (testingModels[key]) return
  testingModels[key] = true
  testingModel.value = key
  delete testResult[key]
  try {
    const result = await testModel({ provider: provider.name, model: model.id })
    testResult[key] = { ok: !!result?.ok, error: result?.error || '', latency: result?.latencyMs || 0 }
    if (result?.ok) pushToast('success', t.value.testPassed)
    else pushToast('error', t.value.testFailed)
  } catch (err) {
    testResult[key] = { ok: false, error: String(err), latency: 0 }
    pushToast('error', t.value.testFailed)
  } finally {
    delete testingModels[key]
    if (testingModel.value === key) testingModel.value = ''
  }
}

function onKeydown(event) {
  if ((event.metaKey || event.ctrlKey) && event.key === 'Enter') sendPrompt()
}

provide(appContextKey, {
  t,
  bootstrap,
  config,
  environmentTab,
  agentNotice,
  piInstallBusy,
  piInstallError,
  agentList,
  modelOptions,
  selectedAgent,
  activeAgentId,
  defaultAgentId,
  newAgentId,
  agentDeleteBusy,
  skills,
  skillsLoading,
  extensionSnapshot,
  extensionBusy,
  extensionDeleteBusy,
  extensionLoading,
  extensionNotice,
  figma,
  selectedModelsProvider,
  showProviderApiKey,
  apiKeyVisibilityLabel,
  piCompatBooleanFields,
  piThinkingFormats,
  piThinkingLevels,
  testingModels,
  testResult,
  tasks,
  saving,
  saved,
  selectedWorkspace,
  selectedWorkspaceId,
  wsDraft,
  newWsId,
  wsBusy,
  pendingDeleteAgent,
  pendingDeleteProvider,
  agentEditorOpen,
  editingNewAgent,
  editingAgentId,
  openAgentConfig,
  backToAgentList,
  providerEditorOpen,
  providerDraft,
  editingNewProvider,
  pendingDeleteSsh,
  sshBusy,
  pendingExtensionDelete,
  requestDeleteExtension,
  confirmDeleteExtension,
  pendingDeleteWs,
  sshEditorOpen,
  editingNewSsh,
  sshDraft,
  wsEditorOpen,
  editingNewWs,
  toasts,
  showFigmaConfig,
  figmaAuthorizationsDraft,
  figmaActiveAuthorizationIdDraft,
  installPiNow,
  createAgent,
  openAgentEditor,
  requestDeleteAgent,
  setDefaultAgent,
  refreshExtensions,
  refreshAgentExtensions,
  refreshSkills,
  installSkills,
  previewSkillArchive,
  previewSkillUrl,
  editSkill,
  deleteSkill,
  updateSkill,

  extensionIcon,
  extensionAction,
  figmaAction,
  toggleAgentExtension,
  installGlobalPackage,
  removeGlobalPackage,
  installAgentMcp,
  installAgentExtension,
  uninstallAgentExtension,
  openWsEditor,
  openSshEditor,
  requestDeleteWs,
  workspaceSsh,
  workspaceRemote,
  setActiveWorkspace,
  requestDeleteSsh,
  selectModelsProvider,
  openProviderEditor,
  openProviderEdit,
  requestDeleteProvider,
  persist,
  compatBooleanValue,
  setCompatBoolean,
  compatStringValue,
  setCompatString,
  formatCompat,
  updateCompatJson,
  addModel,
  testModelKey,
  runModelTest,
  modelRequestRoute,
  toggleImageInput,
  renameModelsProvider,
  restartAgent,
  pickSessionDirectory,
  confirmDeleteAgent,
  closeAgentEditor,
  persistAgentChange,
  pickAgentDataDir,
  cancelNewAgent,
  saveNewAgent,
  closeProviderEditor,
  confirmDeleteProvider,
  confirmAddProvider,
  confirmSaveProvider,
  confirmDeleteSsh,
  confirmDeleteWs,
  closeSshEditor,
  persistSshChange,
  saveNewSsh,
  closeWsEditor,
  persistWsChange,
  handleWsPathChange,
  pickWorkspacePath,
  handleWorkspaceSshChange,
  saveNewWs,
  persistFigma,
  addFigmaAuthorization,
  removeFigmaAuthorization,
  readAgentFile,
  writeAgentFile,
  pushToast,
  appUpdateAvailable
})

onMounted(async () => {
  offAttachmentDrop = onEvent('attachments:dropped', payload => {
    const files = Array.isArray(payload?.files) ? payload.files : []
    if (files.length) void onAddAttachments(files)
  })
  await load()
  // 启动后后台静默检查一次客户端新版本（不弹提示，仅用于设置菜单红点）。
  // Wails 桥接在 onMounted 时可能尚未就绪，因此失败重试几次。
  void checkUpdateOnStartup()
  offEvent = onEvent('agent:event', handleAgentEvent)
  offSubagentEvent = onEvent('subagent:event', handleSubagentEvent)
  offDocumentPreview = onEvent('document:preview', async request => {
    const task = tasks.value.find(item => String(item.id) === String(request?.codingToSessionId))
    if (task && String(activeTaskId.value) !== String(task.id)) {
      await selectTask(task)
    }
    documentPreviewRequest.value = { ...request, nonce: Date.now() }
    await refreshSessionChanges()
  })
  offState = onEvent('agent:state', state => {
    const sourceTaskId = eventTaskId(state)
    const targetTaskId = sourceTaskId || activeTaskId.value
    if (targetTaskId !== '') setTaskRunning(targetTaskId, !!state?.running)
    if (targetTaskId !== '' && typeof state?.processRunning === 'boolean') {
      setTaskRuntimeAvailable(targetTaskId, state.processRunning)
    }
    if (
      state?.error
      && (sourceTaskId === '' || String(sourceTaskId) === String(activeTaskId.value))
    ) error.value = state.error
    if (
      sourceTaskId !== ''
      && String(sourceTaskId) === String(activeTaskId.value)
    ) executionRunning.value = !!state?.running
  })
  onEvent('agent:restart_deferred', () => {
    if (extensionRestartPending.value) pushToast('info', t.value.extensionRestartDeferred.replace('{name}', extensionRestartPending.value))
  })
  onEvent('agent:restart_done', result => {
    if (extensionRestartPending.value) {
      const name = extensionRestartPending.value
      extensionRestartPending.value = ''
      if (result?.success === false) {
        pushToast('error', t.value.toastExtensionError.replace('{error}', result?.error || 'restart failed'))
      } else {
        pushToast('success', t.value.extensionRestartDone.replace('{name}', name))
      }
    }
  })
  // 全局插件(Plugins 页)安装/启用后，后端会广播此事件，重新拉取快照刷新列表，
  // 否则 UI 会停在安装前的旧状态（例如 Playwright 仍显示"未安装"）。
  offExtensionsChanged = onEvent('extensions:changed', () => {
    void refreshExtensions()
  })
  matchMedia('(prefers-color-scheme: dark)').addEventListener('change', applyTheme)
})

// 点击归档浮层以外的区域时自动关闭它。
document.addEventListener('click', closeArchivePop)

onBeforeUnmount(() => {
  window.clearTimeout(changeRefreshTimer)
  offEvent?.()
  offSubagentEvent?.()
  offState?.()
  offDocumentPreview?.()
  offAttachmentDrop?.()
  offExtensionsChanged?.()
  document.removeEventListener('click', closeArchivePop)
})
</script>

<template>
  <div class="app-shell">
    <header class="titlebar">
      <div class="titlebar__drag">
        <img class="brand-mark" :src="logo" alt="CodingTo" />
        <span class="brand-name">CodingTo</span>
        <span v-if="running" class="run-pill"><span></span>{{ t.statusRunning }}</span>
      </div>
      <div class="window-controls">
        <button @click="minimise" aria-label="Minimize"><Minus :size="15" /></button>
        <button @click="toggleMaximise" aria-label="Maximize"><Maximize2 :size="13" /></button>
        <button class="window-close" @click="closeWindow" aria-label="Close"><X :size="16" /></button>
      </div>
    </header>

    <div class="workspace-shell">
      <aside class="sidebar" :class="{ 'sidebar--closed': !sidebarOpen }">
        <nav class="primary-nav">
          <button v-for="item in nav" :key="item.id" :class="{ active: activePage === item.id }" @click="activePage = item.id">
            <component :is="item.icon" :size="14" />
            <span v-if="sidebarOpen">{{ item.label }}</span>
            <span v-if="sidebarOpen && item.dev" class="nav-dev">{{ t.devBadge }}</span>
          </button>
        </nav>

        <section v-if="sidebarOpen" class="sidebar-browser">
          <div class="sidebar-browser__heading">
            <span>{{ t.chatWorkspaces }}</span>
            <button :title="t.addWs" @click="chatNewEnvironment"><Plus :size="17" :stroke-width="2.5" /></button>
          </div>

          <div class="sidebar-browser__groups">
            <template v-if="sessionGroups.length">
              <div v-for="group in sessionGroups" :key="group.id || 'orphan'" class="sidebar-group">
                <div
                  class="sidebar-group__head"
                  :class="{ active: group.id && group.id === config.activeEnvId }"
                >
                  <button class="sidebar-group__title" @click="group.id && toggleWorkspaceCollapse(group.id)" :title="group.id ? (collapsedWorkspaceIds.has(group.id) ? t.expand : t.collapse) : ''">
                    <Folder :size="14" />
                    <span :title="group.env?.path || group.name">{{ group.name }}</span>
                  </button>
                  <div class="sidebar-group__actions">
                    <button class="sidebar-group__add" :title="t.chatNewSession" @click.stop="chatNewSessionFor(group)">
                      <Plus :size="15" :stroke-width="2.5" />
                    </button>
                  </div>
                </div>
                <div v-show="!group.id || !collapsedWorkspaceIds.has(group.id)" class="sidebar-browser__list sidebar-browser__list--sessions">
                  <div
                    v-for="task in group.visible"
                    :key="task.id"
                    class="session-item"
                    :class="{ active: task.id === activeTaskId, 'session-item--running': task.status === 'running' }"
                    @click="selectTask(task)"
                  >
                    <span v-if="taskNeedsConfirm(task.id)" class="confirm-dot confirm-dot--session"></span>
                    <LoaderCircle v-if="task.status === 'running'" class="spin" :size="14" />
                    <span class="session-item__name">{{ task.title }}</span>
                    <button class="session-archive" :title="t.archive" @click.stop="requestArchive($event, task)">
                      <Archive :size="13" />
                    </button>
                  </div>
                  <button v-if="group.remaining > 0" class="sidebar-show-more" @click="showMoreSessions(group.id)">
                    {{ t.showMore }} ({{ group.remaining }})
                  </button>
                </div>
              </div>
            </template>
            <p v-else class="sidebar-browser__empty">{{ t.noTasks }}</p>
          </div>
        </section>

        <Teleport to="body">
          <div v-if="pendingArchiveTask" class="archive-pop" :style="{ top: archivePopPos.top + 'px', left: archivePopPos.left + 'px' }" @click.stop>
            <p>{{ t.archiveConfirm }}</p>
            <div class="archive-pop__actions">
              <button class="archive-pop__cancel" @click="cancelArchive">{{ t.cancel }}</button>
              <button class="archive-pop__ok" @click="archiveTask(pendingArchiveTask)">{{ t.confirmOk }}</button>
            </div>
          </div>
        </Teleport>

        <div class="sidebar__bottom">
          <button :class="{ active: activePage === 'settings' }" @click="activePage = 'settings'">
            <span class="sidebar__icon-wrap">
              <Settings :size="17" />
              <span v-if="appUpdateAvailable" class="sidebar__badge" :title="t.new_version_dot"></span>
            </span>
            <span v-if="sidebarOpen">{{ t.settings }}</span>
          </button>
        </div>
      </aside>

      <main class="main-content">
        <PiInstallGate
          v-if="bootstrap && !bootstrap.piInstalled"
          :on-install="installPiNow"
          :busy="piInstallBusy"
          :error="piInstallError"
        />
        <div v-else-if="!bootstrap" class="pi-gate-loading">正在加载…</div>
        <template v-else>
        <section v-if="activePage === 'chat'" class="command-page">
          <ChatView
            :config="config"
            :agents="config.agents"
            :messages-list="messagesList"
            :loading-history="loadingHistory"
            :session-id="Number(activeTaskId) || 0"
            :running="activeTaskRunning"
            :stopping="activeTaskStopping"
            :connected="connected"
            :selected-agent="selectedAgent"
            :tasks="tasks"
            :draft="draft"
            :pending-prompts="pendingPrompts"
            :mode="mode"
            :model-options="modelOptions"
            :selected-model-value="selectedModelValue"
            :supports-images="supportsImages"
            :prompt-images="promptImages"
            :attachments="attachments"
            :attachments-busy="attachmentReadsPending > 0"
            :thinking-levels="thinkingLevels"
            :thinking-level="thinkingLevel"
            :skills="skills"
            :selected-skill="selectedSkill"
            :token-stats="tokenStats"
            :context-window="contextWindow"
            :context-usage="contextUsage"
            :active-title="currentTask()?.title || ''"
            :active-created-at="currentTask()?.createdAt || 0"
            :execution-elapsed-ms="executionElapsedMs"
            :execution-running="executionRunning"
            :session-changes="sessionChanges"
            :session-changes-loading="sessionChangesLoading"
            :document-preview-request="documentPreviewRequest"
            :document-artifact-focus="documentArtifactFocus"
            :plan-items="planItems"
            :execution-plan="executionPlan"
            :extension-dialog="extensionDialog"
            :compaction="compaction"
            :error="error"
            @update:draft="draft = $event"
            @send="sendPrompt"
            @edit-pending="editPendingPrompt"
            @delete-pending="deletePendingPrompt"
            @stop="stop"
            @select-agent="chatSelectAgent"
            @open-agent-config="chatOpenAgentConfig"
            @update:mode="mode = $event"
            @update:model="onModelChange"
            @add-images="onAddImages"
            @remove-image="onRemoveImage"
            @add-attachments="onAddAttachments"
            @remove-attachment="onRemoveAttachment"
            @update:thinking="thinkingLevel = $event"
            @update:skill="selectedSkill = $event"
            @update-thinking-open="updateThinkingOpen"
            @compact="compactContext"
            @respond-extension="respondExtensionDialog"
            @refresh-session-changes="refreshSessionChanges"
            @artifact-error="pushToast('error', localizeError(String($event)))"
          />
        </section>
        <section v-else class="page-view">
        <component :is="pageComponent" />
        </section>
        </template>
      </main>
    </div>

    <AppDialogs />
    <InstallLogModal />
  </div>
</template>
