<script setup>
import { computed, defineAsyncComponent, nextTick, onBeforeUnmount, onMounted, provide, reactive, ref, watch } from 'vue'
import {
  Archive, Blocks, Bot, Brain, Folder, LoaderCircle,
  Network, Plus, Settings, Smartphone, Sparkles,
  X
} from 'lucide-vue-next'
import { Call } from '@wailsio/runtime'
import { extensionIcon } from './extensionIcons'
import {
  abortPrompt, activateWorkspace, chooseSessionDir, chooseWorkspace, createSession, deleteAgent,
  chatgptAccount,
  getBootstrap, getSessionChanges, getSessionHistory, getSessionRuntimeState, getStewardProfile, listSessions, onEvent,
  getExtensions, getAgentExtensions, installPi, manageExtension, restartAgent, saveConfig,
  saveFigmaConfig, sendAgentCommand, startPrompt, testModel,
  saveBrowserProfile,
  respondSubagentUI, ackSubagentUI,
  setSessionDcgDisabled, getSessionDcgDisabled,
  updateSessionModel,
  readAgentFile, writeAgentFile,
  listSkills, installSkills, previewSkillArchive, previewSkillUrl, editSkill, deleteSkill, updateSkill,
  installGlobalPackage as beInstallGlobalPackage,
  removeGlobalPackage as beRemoveGlobalPackage,
  installAgentMcp as beInstallAgentMcp,
  addManualMCP as beAddManualMCP,
  removeAgentMcpServer as beRemoveAgentMcpServer,
  installAgentExtension as beInstallAgentExtension,
  uninstallAgentExtension as beUninstallAgentExtension,
  deleteAgentExtensionDir as beDeleteAgentExtensionDir,
  listBotChannels, getSessionGitAvailability
} from './backend'
import { buildT } from './i18n'
import ChatView from './ChatView.vue'
import AppDialogs from './components/AppDialogs.vue'
import AppTitleBar from './components/app/AppTitleBar.vue'
import PiInstallGate from './components/PiInstallGate.vue'
// 页面组件按需懒加载：每个菜单页独立 chunk，仅首次打开时加载，减小首屏主包体积。
// 菜单页以模态浮层打开，本地文件加载开销可忽略，切换无感知。
const AgentsPage = defineAsyncComponent(() => import('./components/pages/AgentsPage.vue'))
const AgentConfigPage = defineAsyncComponent(() => import('./components/pages/AgentConfigPage.vue'))
const DocsPage = defineAsyncComponent(() => import('./components/pages/DocsPage.vue'))
const EnvironmentPage = defineAsyncComponent(() => import('./components/pages/EnvironmentPage.vue'))
const McpPage = defineAsyncComponent(() => import('./components/pages/McpPage.vue'))
const ModelsPage = defineAsyncComponent(() => import('./components/pages/ModelsPage.vue'))
const PluginsPage = defineAsyncComponent(() => import('./components/pages/PluginsPage.vue'))
const MemoryConfigDialog = defineAsyncComponent(() => import('./components/MemoryConfigDialog.vue'))
const PlanConfigDialog = defineAsyncComponent(() => import('./components/PlanConfigDialog.vue'))
const GlobalPromptConfigDialog = defineAsyncComponent(() => import('./components/GlobalPromptConfigDialog.vue'))
const GitManagementDialog = defineAsyncComponent(() => import('./components/chat/GitManagementDialog.vue'))
const SettingsPage = defineAsyncComponent(() => import('./components/pages/SettingsPage.vue'))
const SkillsPage = defineAsyncComponent(() => import('./components/pages/SkillsPage.vue'))
const StewardPage = defineAsyncComponent(() => import('./components/pages/StewardPage.vue'))
const TasksPage = defineAsyncComponent(() => import('./components/pages/TasksPage.vue'))
import { BROWSER_IDENTITY_DIALOG_TITLE, isPlanConfirmationDialog, shouldAbortAfterExtensionResponse } from './components/chat/extensionDialog'
import {
  toAttachmentInputs
} from './components/chat/attachmentTypes'
import { mergeSubagentRuntime, parseSubagentEvent } from './components/chat/subagentRuntime'
import { completeCompactionMessage, createCompactionMessage } from './components/chat/compactionMessages'
import {
  messageText,
  parsePlanItems,
  parsePlanLines,
  parsePlanStepsFromMessage,
  stripPlanStepsMarker
} from './components/chat/planMessages'
import { buildSessionGroups } from './components/chat/sessionGroups'
import { appContextKey } from './composables/appContext'
import { useSidebarLayout } from './composables/app/useSidebarLayout'
import { useToasts } from './composables/app/useToasts'
import { useChatAttachments } from './composables/chat/useChatAttachments'
import { useCurrentProviderQuota } from './composables/models/useCurrentProviderQuota'
import { useDatabaseConnections } from './composables/environment/useDatabaseConnections'
import { useSshConnections } from './composables/environment/useSshConnections'
import { requestConciseToggleFocus } from './utils/settingsNav'
import { localizeError, safeClone } from './utils/appHelpers'
import {
  compatBooleanValue,
  compatStringValue,
  formatCompat,
  modelRequestRoute,
  normalizeProvider,
  piCompatBooleanFields,
  piThinkingFormats,
  piThinkingLevels,
  setCompatBoolean,
  setCompatString
} from './utils/modelConfig'
import { defaultThinkingLevelForModel } from './modelThinking'
import { isResolvedStewardPermission, stewardPermissionMatchesDialog } from './stewardState.js'
import { sendSystemNotification } from './systemNotifications'
import {
  loadDraftForEnv, persistDraftForEnv,
  persistExecPlan as persistExecPlanStorage, restoreExecPlan as restoreExecPlanStorage,
  persistPlanItems as persistPlanItemsStorage, restorePlanItems as restorePlanItemsStorage,
  persistExtDialogForTask as persistExtDialogStorage, clearPersistedExtDialog as clearExtDialogStorage,
  readPersistedExtDialog
} from './utils/localStorage'

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
  steward: StewardPage,
  tasks: TasksPage,
  settings: SettingsPage
}
const pageComponent = computed(() => pageComponents[activePage.value] || null)
function goHome() { activePage.value = 'chat' }
// 聊天区「精简模式」提示 → 打开设置页并定位到「精简对话」开关。
function openSettingsToConcise() {
  activePage.value = 'settings'
  requestConciseToggleFocus()
}
const environmentTab = ref('workspace')
const showMemoryConfig = ref(false)
const showPlanConfig = ref(false)
const showGlobalPromptConfig = ref(false)
function openMemoryConfig() { showMemoryConfig.value = true }
function openPlanConfig() { showPlanConfig.value = true }
function openGlobalPromptConfig() { showGlobalPromptConfig.value = true }
const { sidebarOpen, sidebarWidth, sidebarResizing, startSidebarResize } = useSidebarLayout()
// 后端发出 app:shutting-down 后置为 true：覆盖全屏"正在关闭中"蒙层，
// 让后台清理（关闭 agent 进程、steward 渠道等，最多数秒）期间有即时反馈，
// 避免界面看起来像卡死。
const shuttingDown = ref(false)
// 窗口最大化状态：用于切换 border-radius 等样式，避免最大化时出现边角空隙。
const isMaximised = ref(false)
// 管家是否已连接任意渠道：用于底部按钮的手机图标颜色（绿色=已连接 / 灰色=未连接）。
const stewardConnected = ref(false)
async function refreshStewardConnected() {
  try {
    const channels = await listBotChannels()
    stewardConnected.value = Array.isArray(channels) && channels.some(c => c.status === 'connected')
  } catch {
    stewardConnected.value = false
  }
}
// 常驻管家会话 id：用于在其自主运行时跳过前端计划确认弹窗（消息 Tab 仅展示会话详情）。
const residentSessionId = ref(0)
async function refreshResidentSessionId() {
  try {
    const profile = await getStewardProfile()
    residentSessionId.value = Number(profile?.residentSessionId) || 0
  } catch {}
}
// 打开“管家设置-消息”时刷新一次常驻会话 id，确保网关判断准确。
watch(activePage, (page) => {
  if (page === 'steward') void refreshResidentSessionId()
})
const bootstrap = ref(null)
// 客户端自身更新：启动后后台检查一次，有新版本时为 true，用于设置菜单红点。
const appUpdateAvailable = ref(false)
const config = reactive({
  preferences: { theme: 'system', language: 'zh-CN', accentColor: '#d9a441', chatLayout: 'left', showIdentity: true, diffMode: 'unified', fontSize: 'small', conciseChat: false },
  userProfile: { name: '', avatar: '' },
  providers: [],
  defaultProvider: '',
  defaultModel: '',
  lastEnvironment: '',
  activeAgentId: '',
  agents: [],
  environments: [],
  activeEnvId: '',
  sshConfigs: [],
  subagentConcurrency: 4,
  systemNotificationEnabled: true
})
const draft = ref('')
// The active working directory is process-local. It starts at
// ~/.codingto/tempwork and is never saved as a default workspace.
const currentWorkDir = ref('')
// 「添加到对话」功能最近一次新建并仍在使用的对话 id。当用户从 Git 管理
// 文件列表反复添加时，只要当前仍停留在该对话就复用而非重复新建。
const addToChatSessionId = ref('')
// 未发送内容（输入框草稿）按工作空间分别持久化：每个工作空间各自保存一份，
// 刷新页面后按当前激活的工作空间恢复，切换工作空间时自动保存/载入对应草稿。
// 输入框内容变化时，实时保存当前工作空间的草稿。
// 仅在「新建对话 / 首页」模式下把草稿写入存储；查看历史对话时输入框用于
// 续写该会话，不应读取或覆盖工作空间的未发送草稿。
watch(draft, value => {
  if (isHomeMode.value) persistDraftForEnv(config.activeEnvId, value)
})
const pendingPromptsByTask = reactive(new Map())
const mode = ref('execute')
const thinkingLevel = ref('off')
// 本次对话是否关闭命令拦截（会话级）。仅影响当前对话的 DCG 拦截，
// 不修改智能体 recommended.dcg 配置。切换对话时依据会话标记恢复。
const sessionDcgDisabled = ref(false)
const selectedSkill = ref(null)
const promptImages = ref([])
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
// Per-conversation lifecycle generation. Session-list requests snapshot these
// values so a response that started before a newer local event cannot overwrite
// that event with an older backend snapshot.
const taskRuntimeVersions = new Map()
let sessionRefreshRequest = 0
const connected = ref(false)
// 当前会话累计的 token/上下文用量。运行时来自 Pi 的 get_session_stats 响应
// （其 production 响应不带 command 字段，已在 handleAgentEvent 中按 data 形状路由），
// 历史加载则来自后端对会话文件的聚合。
const tokenStats = ref({ input: 0, cached: 0, cacheWrite: 0, output: 0, total: 0 })
const contextUsage = ref({ tokens: 0, contextWindow: 0, percent: 0 })
const compactionByTask = reactive(new Map())
const extensionDialog = ref(null)
// 计划确认弹窗等待 plan-todos 的缓存：confirm 事件可能先于 setWidget 到达
// （Wails/WebView 事件队列乱序），此时 planItems 为空，直接弹窗会渲染出
// “只有标题和按钮、没有计划内容”的空壳确认框，用户可能在计划数据到达前
// 就点击批准。缓存该弹窗，等 setWidget('plan-todos') 到达后再提升为正式
// 弹窗；等待超时后仍显示（标记计划缺失、禁用批准），避免无限等待且保证
// ack 照常发出、后端看门狗不会误判为“弹窗未渲染”而自动取消。
const pendingPlanDialog = ref(null)
let pendingPlanTimer = null
const PLAN_WIDGET_WAIT_MS = 2500
const extensionStatuses = reactive({})
const skills = ref([])
const skillsLoading = ref(false)
const extensionWidgets = reactive({})
const planItems = ref([])
const executionPlan = ref([])
// 执行计划在本地点持久化，刷新页面后按当前任务恢复（后端暂无对应持久化接口）。
function persistExecPlan() { persistExecPlanStorage(activeTaskId.value, executionPlan.value) }
function restoreExecPlan(taskId) { executionPlan.value = restoreExecPlanStorage(taskId) }
// planItems（待确认计划）不会由后端持久化，仅存在于前端内存，刷新后会丢失。
// 因此在本地也做一份持久化，刷新后能恢复计划面板，而不是一直"执行中转圈"。
function persistPlanItems() { persistPlanItemsStorage(activeTaskId.value, planItems.value) }
function restorePlanItems(taskId) { planItems.value = restorePlanItemsStorage(taskId) }
watch(executionPlan, persistExecPlan, { deep: true })
watch(planItems, persistPlanItems, { deep: true })
// 扩展交互对话框（如计划确认）在本地点持久化，刷新页面后按当前任务恢复，
// 避免刷新后 agent 仍在等待确认却没有任何可操作的对话框（一直"转圈"）。
function persistExtDialog() { persistExtDialogForTask(activeTaskId.value, extensionDialog.value) }
function persistExtDialogForTask(taskId, dialog) {
  persistExtDialogStorage(taskId, dialog)
  syncTaskPendingAttention(taskId, dialog)
}
function clearPersistedExtDialog(taskId) {
  const targetTaskId = taskId || activeTaskId.value || ''
  clearExtDialogStorage(targetTaskId)
  clearTaskPendingAttention(targetTaskId, 'main')
}
function restoreExtDialog(taskId) {
  const restored = readPersistedExtDialog(taskId)
  if (restored) {
    // 刷新/切回恢复弹窗时的竞态兜底：若恢复的是计划确认但本地无计划数据，
    // 优先解析消息内嵌步骤；仍没有则标记计划缺失，避免空壳确认框。
    let dialog = restored
    if (isPlanConfirmationDialog(restored) && planItems.value.length === 0) {
      const embeddedSteps = parsePlanStepsFromMessage(restored.message)
      if (embeddedSteps) {
        dialog = { ...restored, message: stripPlanStepsMarker(restored.message) }
        planItems.value = embeddedSteps
      } else {
        dialog = { ...restored, planMissing: true }
      }
    }
    extensionDialog.value = dialog
    syncTaskPendingAttention(taskId, dialog)
  }
}
const sidebarDotTaskIds = reactive(new Set())
const pendingAttentionByTask = reactive(new Map())
function isBrowserIdentityDialog(dialog) {
  return dialog?.method === 'select' && String(dialog?.title || '').startsWith(BROWSER_IDENTITY_DIALOG_TITLE)
}
function isSidebarAttentionDialog(dialog) {
  return isPlanConfirmationDialog(dialog) || isBrowserIdentityDialog(dialog)
}
function syncTaskPendingAttention(id, dialog) {
  if (isSidebarAttentionDialog(dialog)) markTaskPendingAttention(id, 'main', dialog)
  else clearTaskPendingAttention(id, 'main')
}
function systemNotificationTypeLabel(type) {
  switch (type) {
    case 'plan-request': return t.value.systemTypePlanRequest
    case 'browser-identity': return t.value.systemTypeBrowserIdentity
    case 'conversation-complete': return t.value.systemTypeCompletion
    default: return t.value.systemTypeAttention
  }
}
// 系统通知正文的占位文本（任务标题/操作描述）可能含英文双引号等字符：
// Windows toast XML 文本节点不转义引号会显示杂乱，统一替换为中文引号并压缩空白。
function cleanNotificationText(text) {
  let open = true
  return String(text || '')
    .replace(/"/g, () => {
      const q = open ? '“' : '”'
      open = !open
      return q
    })
    .replace(/\s+/g, ' ')
    .trim()
}
function notifyTaskPendingAttention(id, requestKey, dialog) {
  const task = taskById(id)
  const taskTitle = cleanNotificationText(task?.title || t.value.chatNewSession)
  const operation = cleanNotificationText(dialog?.title || t.value.systemAttentionOperation)
  const type = isPlanConfirmationDialog(dialog) ? 'plan-request'
    : isBrowserIdentityDialog(dialog) ? 'browser-identity'
    : 'attention'
  // 设置（Agent 运行设置 → 系统通知）关闭后，计划审批等场景不再发送系统通知（红点保留）。
  if (type === 'plan-request' && config.systemNotificationEnabled === false) return
  // 正文以【类型】开头，即使部分平台不渲染标题也能看出通知类型。
  const body = `【${systemNotificationTypeLabel(type)}】${t.value.systemAttentionBody
    .replace('{task}', taskTitle)
    .replace('{operation}', operation)}`
  void sendSystemNotification({
    id: `codingto-attention-${id}-${requestKey}-${Date.now()}`,
    taskId: id,
    type,
    title: `CodingTo · ${systemNotificationTypeLabel(type)}`,
    body
  }).catch(() => {})
}
function markTaskPendingAttention(id, requestKey = 'main', dialog = null, options = {}) {
  const key = String(id ?? '')
  if (!key) return
  let requests = pendingAttentionByTask.get(key)
  if (!requests) {
    requests = reactive(new Set())
    pendingAttentionByTask.set(key, requests)
  }
  const normalizedRequestKey = String(requestKey || 'main')
  if (requests.has(normalizedRequestKey)) return
  requests.add(normalizedRequestKey)
  if (options.announce !== false) {
    // 红点只提示一次：当前正在查看的对话无需点亮；后台对话的新请求点亮后，
    // 用户点击清除，待确认状态本身继续保留但不会在切走后重新生成红点。
    if (key !== String(activeTaskId.value ?? '')) sidebarDotTaskIds.add(key)
    notifyTaskPendingAttention(key, normalizedRequestKey, dialog)
  }
}
function clearTaskPendingAttention(id, requestKey) {
  const key = String(id ?? '')
  if (!key) return
  if (requestKey == null) {
    pendingAttentionByTask.delete(key)
    return
  }
  const requests = pendingAttentionByTask.get(key)
  if (!requests) return
  requests.delete(String(requestKey))
  if (!requests.size) pendingAttentionByTask.delete(key)
}
function restorePendingAttentionState(taskList) {
  for (const task of taskList || []) {
    const dialog = readPersistedExtDialog(task.id)
    // 刷新后只恢复仍需处理的弹窗状态，不恢复也不重新通知上个页面生命周期的红点。
    if (isSidebarAttentionDialog(dialog)) markTaskPendingAttention(task.id, 'main', dialog, { announce: false })
  }
}
function markTaskForSidebar(id) {
  const key = String(id ?? '')
  if (!key || key === String(activeTaskId.value ?? '') || sidebarDotTaskIds.has(key)) return
  sidebarDotTaskIds.add(key)
  // 设置（Agent 运行设置 → 系统通知）关闭后，任务完成不再发送系统通知（侧边栏红点保留）。
  if (config.systemNotificationEnabled === false) return
  const taskTitle = cleanNotificationText(taskById(key)?.title || t.value.chatNewSession)
  void sendSystemNotification({
    id: `codingto-completed-${key}-${Date.now()}`,
    taskId: key,
    type: 'conversation-complete',
    title: `CodingTo · ${systemNotificationTypeLabel('conversation-complete')}`,
    body: `【${systemNotificationTypeLabel('conversation-complete')}】${t.value.systemCompletionBody.replace('{task}', taskTitle)}`
  }).catch(() => {})
}
function clearTaskSidebarDot(id) {
  const key = String(id ?? '')
  if (!key) return
  sidebarDotTaskIds.delete(key)
}
function taskHasSidebarDot(id) {
  const key = String(id ?? '')
  return key !== String(activeTaskId.value ?? '') && sidebarDotTaskIds.has(key)
}
const contextWindow = computed(() => selectedModel.value?.contextWindow || 0)
// 顶部条错误（兼容旧 UI，提示最新一次失败）。重试/失败的历史以
// role:'error' 消息形式插入 messagesList，随对话流按时间顺序保留，
// 不再用单条字符串覆盖，最终失败时用户可回看每次重试的完整错误。
const error = ref('')
// Pi 会把同一次模型失败重复附在 message_end、turn_end、agent_end 上。
// 按会话、按底层运行回合记录已展示的终止错误，避免同一错误连续出现三次；
// agent_start/auto_retry_start 会重置集合，因此后续回合或下一次重试仍会展示。
const terminalErrorsByTask = new Map()
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
function resetTerminalErrors(taskId) {
  const key = String(taskId ?? '')
  if (key) terminalErrorsByTask.delete(key)
}
function pushTerminalErrorMessage(taskId, text) {
  const value = String(text || '').trim()
  const key = String(taskId ?? '')
  if (!value || !key) return
  let errors = terminalErrorsByTask.get(key)
  if (!errors) {
    errors = new Set()
    terminalErrorsByTask.set(key, errors)
  }
  if (errors.has(value)) return
  errors.add(value)
  pushErrorMessage(value)
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
const currentAgentId = ref('')
const agentEditorOpen = ref(false)
const editingNewAgent = ref(false)
// 独立配置页面当前正在编辑的 agent id；返回列表时清空并刷新。
const editingAgentId = ref('')
const { toasts, pushToast } = useToasts()
// 启动时自动清理过期会话数据的提示横幅：后端异步清理完成后展示，叉掉即关闭。
// 仅在实际清理掉会话或失败时展示；没有可清理的会话时静默，不打扰用户。
// 既监听后端广播事件，也在启动后主动拉取一次，避免清理先于前端监听完成时丢失提示。
const sessionCleanupNotice = ref(null)
function applySessionCleanupResult(res) {
  if (!res || res.skipped) return
  if (res.error) {
    sessionCleanupNotice.value = { type: 'error', text: t.value.sessionCleanupFailed.replace('{error}', res.error) }
  } else if (res.cleaned > 0) {
    sessionCleanupNotice.value = { type: 'success', text: t.value.sessionCleanupDone.replace('{cleaned}', res.cleaned).replace('{days}', res.days) }
  }
}
async function fetchSessionCleanupNotice() {
  try {
    const res = await Call.ByName('codingto/internal/app.App.GetSessionCleanupResult')
    applySessionCleanupResult(res)
  } catch (e) {
    // 忽略桥接未就绪等临时失败；横幅不显示也不影响功能。
  }
}
const piInstallBusy = ref(false)
const piInstallError = ref('')
const messagesList = ref([])
const loadingHistory = ref(false)
const tasks = ref([])
const activeTaskId = ref('')
const gitDialogOpen = ref(false)
const gitAvailability = ref({ isRepository: false, root: '', currentBranch: '', changeCount: 0, ahead: 0, hasConflicts: false })
const gitWorkspaceRevision = ref(0)
const runtimeWorkspaceRevision = ref(0)
const workspaceActivating = ref(false)
let gitAvailabilityRequest = 0
let workspaceActivationRequest = 0
let workspaceActivationQueue = Promise.resolve()

function environmentWorkDir(environmentId = config.activeEnvId) {
  return config.environments.find(environment => environment.id === environmentId)?.path || ''
}

function normalizedWorkDir(value) {
  const normalized = String(value || '').replaceAll('\\', '/').replace(/\/$/, '')
  return bootstrap.value?.os === 'windows' ? normalized.toLowerCase() : normalized
}

async function initializeWorkspace(environmentId = '') {
  const requestedId = String(environmentId || '')
  const request = ++workspaceActivationRequest
  const previousId = String(config.activeEnvId || '')
  const previousRoot = currentWorkDir.value
  config.activeEnvId = requestedId
  currentWorkDir.value = environmentWorkDir(requestedId) || bootstrap.value?.defaultWorkDir || config.lastEnvironment || ''
  workspaceActivating.value = true

  const activation = workspaceActivationQueue
    .catch(() => {})
    .then(() => activateWorkspace(requestedId))
  workspaceActivationQueue = activation
  try {
    const result = await activation
    if (request !== workspaceActivationRequest || String(config.activeEnvId || '') !== requestedId) return false
    currentWorkDir.value = result?.root || currentWorkDir.value
    config.lastEnvironment = currentWorkDir.value
    workspaceActivating.value = false
    runtimeWorkspaceRevision.value += 1
    void refreshGitAvailability()
    return true
  } catch (cause) {
    if (request !== workspaceActivationRequest || String(config.activeEnvId || '') !== requestedId) return false
    config.activeEnvId = previousId
    currentWorkDir.value = previousRoot || bootstrap.value?.defaultWorkDir || ''
    workspaceActivating.value = false
    runtimeWorkspaceRevision.value += 1
    pushToast('error', localizeError(String(cause)))
    return false
  }
}

function applyGitWorkspaceUpdate(payload) {
  if (!payload?.availability) return
  if (normalizedWorkDir(payload.workspace) !== normalizedWorkDir(currentWorkDir.value)) return
  gitAvailability.value = {
    isRepository: false, root: '', currentBranch: '', changeCount: 0, ahead: 0, hasConflicts: false,
    ...payload.availability,
  }
  gitWorkspaceRevision.value += 1
  if (!gitAvailability.value.isRepository) gitDialogOpen.value = false
}

async function refreshGitAvailability() {
  const sessionId = Number(activeTaskId.value) || 0
  const request = ++gitAvailabilityRequest
  try {
    const result = await getSessionGitAvailability(sessionId)
    if (request !== gitAvailabilityRequest || sessionId !== (Number(activeTaskId.value) || 0)) return
    gitAvailability.value = { isRepository: false, root: '', currentBranch: '', changeCount: 0, ahead: 0, hasConflicts: false, ...(result || {}) }
    if (!gitAvailability.value.isRepository) gitDialogOpen.value = false
  } catch {
    if (request !== gitAvailabilityRequest) return
    gitAvailability.value = { isRepository: false, root: '', currentBranch: '', changeCount: 0, ahead: 0, hasConflicts: false }
    gitDialogOpen.value = false
  }
}

watch(activeTaskId, () => { void refreshGitAvailability() }, { immediate: true })
// 终端与 Git 只跟当前工作目录相关：切换活动工作区时刷新可用性（首页模式 activeTaskId 不变）。
watch(() => config.activeEnvId, () => { void refreshGitAvailability() })

function openGitManager() {
  if (!gitAvailability.value.isRepository) return
  gitDialogOpen.value = true
}

function updateGitAvailability(value) {
  gitAvailability.value = { ...gitAvailability.value, ...(value || {}) }
}

function requestAgentConflictResolution(payload) {
  const files = Array.isArray(payload?.files) && payload.files.length ? payload.files.join(', ') : t.value.gitConflictFilesUnknown
  const prompt = t.value.gitConflictAgentPrompt
    .replace('{branch}', payload?.branch || 'HEAD')
    .replace('{root}', payload?.root || gitAvailability.value.root || '')
    .replace('{files}', files)
  draft.value = draft.value.trim() ? `${draft.value.trim()}\n\n${prompt}` : prompt
  gitDialogOpen.value = false
  activePage.value = 'chat'
}
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
let extensionsRefreshRequest = 0
const figmaAuthorizationsDraft = ref([])
const figmaActiveAuthorizationIdDraft = ref('')
const showFigmaConfig = ref(false)
const figma = computed(() => extensionSnapshot.value.figma || { installed: false, enabled: false, running: false, hasToken: false, version: '' })
let activeAssistant = null
let preparingMessage = null
let offEvent
let offState
let offDocumentPreview
let offAttachmentDrop
let offShuttingDown
let offCleanupEvent
let offSubagentEvent
let offExtensionsChanged
let offStewardStatus
let offStewardPermission
let offMaximised
let offUnmaximised
let offGitWorkspace
let changeRefreshTimer
let changeRefreshRequest = 0
// Detached subagent events can race ahead of the parent tool message. Keep a
// bounded per-tool buffer so startup failures still reach the card/details UI.
const pendingSubagentEvents = new Map()

const t = computed(() => buildT(config.preferences.language || 'zh-CN'))
const { attachments, attachmentReadsPending, onAddAttachments, onRemoveAttachment } = useChatAttachments({ t, pushToast })
const activeTaskRunning = computed(() => (
  activeTaskId.value !== ''
  && (runningTaskIds.has(String(activeTaskId.value)) || !!extensionDialog.value)
))
const activeTaskStopping = computed(() => stoppingTaskIds.has(String(activeTaskId.value)))

function setTaskRunning(id, live) {
  const key = String(id ?? '')
  if (!key) return
  taskRuntimeVersions.set(key, (taskRuntimeVersions.get(key) || 0) + 1)
  if (live) runningTaskIds.add(key)
  else {
    runningTaskIds.delete(key)
    stoppingTaskIds.delete(key)
  }
  running.value = runningTaskIds.size > 0
  setTaskRuntimeStatus(id, live ? 'running' : 'active')
}

function applyTaskRuntimeState(id, state) {
  if (!state || typeof state.running !== 'boolean') return
  setTaskRunning(id, state.running)
  if (typeof state.processRunning === 'boolean') {
    setTaskRuntimeAvailable(id, state.processRunning)
  }
  const task = tasks.value.find(item => String(item.id) === String(id))
  if (task && state.known !== false && Number.isFinite(Number(state.execDurationMs))) {
    task.execDurationMs = Number(state.execDurationMs)
  }
  if (String(activeTaskId.value) === String(id)) {
    executionRunning.value = state.running
    if (state.known !== false && Number.isFinite(Number(state.execDurationMs))) {
      executionElapsedMs.value = Number(state.execDurationMs)
    }
  }
}

// 当前会话是否仍有后台子 agent 在运行（含已请求中止但未终结的）。
// 主 agent 回合可以先于其派发的子 agent 结束：子 agent 完成时会通过
// follow-up 消息再次驱动主 agent 继续，因此会话整体仍处于"等待子 agent"
// 的进行中状态，不应在 agent_settled 时表现为会话已结束。
// 状态来源与 SubAgentCard 一致：优先 tool 卡片上实时合并的 subagent 运行时
// 状态，其次为工具返回/持久化结果里的 status 字段。
function hasRunningSubagents() {
  return messagesList.value.some(message => {
    if (message.role !== 'tool') return false
    const detail = message.detail && typeof message.detail === 'object' ? message.detail : {}
    const subagent = detail.subagent && typeof detail.subagent === 'object' ? detail.subagent : {}
    if (['running', 'aborted_requested'].includes(String(subagent.status))) return true
    const output = detail.output && typeof detail.output === 'object' ? detail.output : null
    if (output && ['running', 'aborted_requested'].includes(String(output.status))) return true
    if (output?.details && ['running', 'aborted_requested'].includes(String(output.details.status))) return true
    return false
  })
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
function openPrimaryNav(item) {
  activePage.value = item.id
  if (item.id === 'models') void refreshCurrentProviderQuota({ force: true })
}
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
const agentList = computed(() => newAgentDraft.value ? [...config.agents, newAgentDraft.value] : config.agents)
const selectedProvider = computed(() => config.providers.find(p => p.name === config.defaultProvider) || config.providers[0])
const selectedModelsProvider = computed(() => config.providers.find(p => p.name === selectedModelsProviderName.value) || config.providers[0] || null)
// ChatGPT 订阅凭据为全局共享（Pi 默认目录，同步到所有智能体），因此登录态
// 统一用一个全局状态判断，而非按智能体分别缓存。
const chatgptAuth = reactive({ loaded: false, loggedIn: false })
let chatgptAuthRequestVersion = 0
const chatgptModelAvailabilityReady = ref(false)
// selectedAgent is the Agent that the next prompt will actually use. During
// history loading it is synchronized from task.agentId before reconciliation.
const conversationAgentId = computed(() => selectedAgent.value?.id || currentTask()?.agentId || '')

function isOpenAICodexProvider(provider) {
  return provider?.name === 'openai-codex'
    || provider?.vendor === 'openai-codex'
    || (provider?.models || []).some(model => model.api === 'openai-codex-responses')
}

function setChatgptAgentAuth(account) {
  chatgptAuth.loaded = true
  chatgptAuth.loggedIn = account?.loggedIn === true
}

async function refreshChatgptAgentAuth() {
  const version = ++chatgptAuthRequestVersion
  try {
    const account = await chatgptAccount()
    if (version === chatgptAuthRequestVersion) setChatgptAgentAuth(account)
  } catch {
    if (version === chatgptAuthRequestVersion) {
      chatgptAuth.loaded = true
      chatgptAuth.loggedIn = false
    }
  }
}

const availableModels = computed(() => selectedProvider.value?.models || [])
const selectedModel = computed(() => availableModels.value.find(model => model.id === config.defaultModel) || availableModels.value[0])
const modelOptions = computed(() => {
  return config.providers.filter(p => p.enabled !== false).flatMap(p => {
    const codexUnavailable = isOpenAICodexProvider(p) && chatgptAuth.loggedIn !== true
    return (p.models || []).map(m => ({
      value: `${p.name}/${m.id}`,
      provider: p.name,
      model: m.id,
      label: `${p.label || p.name} · ${m.name || m.id}`,
      disabled: codexUnavailable,
      disabledLabel: codexUnavailable ? t.value.chatgpt_model_not_authorized_short : '',
      disabledReason: codexUnavailable ? t.value.chatgpt_model_not_authorized : ''
    }))
  })
})
const supportsImages = computed(() => selectedModel.value?.input?.includes('image'))
const supportsTools = computed(() => selectedModel.value?.capabilities?.toolCall !== false)
const thinkingLevels = computed(() => {
  const known = ['off', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max']
  const mapping = selectedModel.value?.thinkingLevelMap
  return mapping ? known.filter(level => level === 'off' || mapping[level] !== null) : known
})
const selectedModelValue = computed({
  get: () => {
    // 有当前会话时以会话落库模型为准，避免 UI 显示智能体默认模型而实际
    // 发送（runPrompt 以 task.model 优先）用的是会话旧模型的错位。
    const task = currentTask()
    if (task?.provider && task?.model) return `${task.provider}/${task.model}`
    // 无会话（新建对话）时按智能体默认模型显示；智能体默认可能仍指向
    // 已改名/删除的服务商旧标识，解析到当前有效服务商再展示。
    const requestedModel = selectedAgent.value?.defaultModel
    const providerName = resolveProviderName(selectedAgent.value?.defaultProvider, requestedModel)
    const provider = config.providers.find(p => p.name === providerName)
    const modelName = provider?.models.some(m => m.id === requestedModel) ? requestedModel : provider?.models?.[0]?.id || ''
    return `${providerName}/${modelName}`
  },
  set: value => {
    if (modelOptions.value.find(option => option.value === value)?.disabled) return
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
const selectedModelUnavailable = computed(() => modelOptions.value.find(option => option.value === selectedModelValue.value)?.disabled === true)

const {
  currentProviderQuotaText,
  refreshCurrentProviderQuota,
  startProviderQuotaRefresh
} = useCurrentProviderQuota({
  activeTaskId,
  config,
  isOpenAICodexProvider,
  modelOptions,
  selectedModelValue,
  t
})

function applyTheme() {
  const pref = config.preferences.theme
  const dark = pref === 'dark' || (pref === 'system' && matchMedia('(prefers-color-scheme: dark)').matches)
  document.documentElement.dataset.theme = dark ? 'dark' : 'light'
  document.documentElement.lang = config.preferences.language
  document.documentElement.style.setProperty('--amber', config.preferences.accentColor || '#d9a441')
  // 界面字号档位：小（默认，12/13/14px）、中（+1，13/14/15px）、大（+2，14/15/16px）。
  // 全系统字号只声明三档变量（--fs-12/13/14），此处整体改写三档数值即可缩放全界面。
  const fontSize = config.preferences.fontSize || 'small'
  const base = 12 + ({ small: 0, medium: 1, large: 2 }[fontSize] ?? 0)
  document.documentElement.style.setProperty('--fs-12', `${base}px`)
  document.documentElement.style.setProperty('--fs-13', `${base + 1}px`)
  document.documentElement.style.setProperty('--fs-14', `${base + 2}px`)
  // 页头标题（.page-heading h2）独立档位：小=22、中=23、大=24px。
  document.documentElement.style.setProperty('--fs-heading', `${base + 10}px`)
}

watch(() => [config.preferences.theme, config.preferences.language, config.preferences.accentColor, config.preferences.fontSize], applyTheme)
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

let reconcilingUnavailableModel = false
async function reconcileUnavailableConversationModel() {
  if (reconcilingUnavailableModel) return
  if (!chatgptModelAvailabilityReady.value || loadingHistory.value) return
  const agentId = conversationAgentId.value
  if (!agentId || !chatgptAuth.loaded) return
  const selected = modelOptions.value.find(option => option.value === selectedModelValue.value)
  if (!selected?.disabled) return
  const fallback = modelOptions.value.find(option => !option.disabled)
  if (!fallback) return

  reconcilingUnavailableModel = true
  try {
    const agent = config.agents.find(item => item.id === agentId)
    if (agent) {
      agent.defaultProvider = fallback.provider
      agent.defaultModel = fallback.model
    }
    config.defaultProvider = fallback.provider
    config.defaultModel = fallback.model
    for (const prompt of pendingPromptList()) {
      if (prompt.agentId === agentId && isOpenAICodexProvider(config.providers.find(provider => provider.name === prompt.provider))) {
        prompt.provider = fallback.provider
        prompt.model = fallback.model
      }
    }
    const sessionSynced = await syncSessionModel(fallback.provider, fallback.model)
    await persist({ silent: true })
    if (sessionSynced) pushToast('info', t.value.chatgpt_model_auto_switched.replace('{model}', fallback.label))
  } finally {
    reconcilingUnavailableModel = false
  }
}

watch(
  () => `${chatgptModelAvailabilityReady.value}\u0000${loadingHistory.value}\u0000${conversationAgentId.value}\u0000${chatgptAuth.loaded}\u0000${chatgptAuth.loggedIn}\u0000${selectedModelValue.value}\u0000${modelOptions.value.map(option => `${option.value}:${option.disabled}`).join('|')}`,
  () => { void reconcileUnavailableConversationModel() },
  { flush: 'post' }
)

watch(showFigmaConfig, open => {
  if (!open) return
  const figmaConfig = config.extensions?.figma || {}
  figmaAuthorizationsDraft.value = safeClone(figmaConfig.authorizations || [])
  figmaActiveAuthorizationIdDraft.value = figmaConfig.activeAuthorizationId || figmaAuthorizationsDraft.value[0]?.id || ''
})


async function load() {
  chatgptModelAvailabilityReady.value = false
  bootstrap.value = await getBootstrap()
  const rawConfig = JSON.parse(JSON.stringify(bootstrap.value?.config ?? {}))
  Object.assign(config, rawConfig)
  if (!config.preferences) config.preferences = {}
  config.preferences.accentColor ||= '#d9a441'
  config.preferences.fontSize ||= 'small'
  config.providers ||= []
  config.configVersion ||= 4
  config.providers.forEach(normalizeProvider)
  selectedModelsProviderName.value = config.providers.find(provider => provider.name === config.defaultProvider)?.name || config.providers[0]?.name || ''
  config.extensions ||= { figma: { enabled: false, activeAuthorizationId: '', authorizations: [] } }
  config.extensions.figma ||= { enabled: false, activeAuthorizationId: '', authorizations: [] }
  config.extensions.figma.authorizations ||= []
  config.extensions.globalMcp ||= []
  config.extensions.globalPlugins ||= []
  config.extensions.db ||= { connections: [] }
  config.extensions.db.connections ||= []
  config.extensions.db.connections.forEach(normalizeDbConnection)
  config.agents ||= []
  config.agents.forEach(normalizeAgent)
  if (config.agents.length && !config.agents.some(agent => agent.id === config.activeAgentId)) config.activeAgentId = config.agents[0].id
  if (config.agents.length && !config.agents.some(agent => agent.id === currentAgentId.value)) currentAgentId.value = config.activeAgentId
  config.environments ||= []
  config.sshConfigs ||= []
  config.sshConfigs.forEach(normalizeSsh)
  config.environments.forEach(normalizeWorkspace)
  // The active workspace is process-local. Startup selects the first workspace
  // shown in the conversation list; tempwork is only the no-workspace fallback.
  config.activeEnvId = ''
  currentWorkDir.value = bootstrap.value?.defaultWorkDir || config.lastEnvironment || ''
  selectedWorkspaceId.value = ''
  draft.value = loadDraftForEnv('')
  // 清理已不存在的工作空间缓存（折叠/排序），避免残留幽灵项。
  const envIds = new Set(config.environments.map(ws => ws.id))
  for (const id of [...collapsedWorkspaceIds]) if (!envIds.has(id)) collapsedWorkspaceIds.delete(id)
  workspaceOrder.value = workspaceOrder.value.filter(id => envIds.has(id))
  const initialWorkspaceId = workspaceOrder.value[0] || config.environments[0]?.id || ''
  selectedWorkspaceId.value = initialWorkspaceId
  await initializeWorkspace(initialWorkspaceId)
  draft.value = loadDraftForEnv(initialWorkspaceId)
  const request = ++sessionRefreshRequest
  const requestedVersions = new Map(taskRuntimeVersions)
  const initialTasks = (await listSessions()) || []
  if (request !== sessionRefreshRequest) return
  reconcileTaskRuntimeStatus(initialTasks, requestedVersions)
  tasks.value = initialTasks
  reconcileStaleSessionProviders(initialTasks)
  reconcileStaleAgentProviders()
  restorePendingAttentionState(initialTasks)
  chatgptModelAvailabilityReady.value = true
  if (config.providers.some(isOpenAICodexProvider)) {
    await refreshChatgptAgentAuth()
  }
  // 仅首次加载时做一次全量扩展扫描；之后返回列表/编辑扩展都只静默刷新单个 agent。
  await refreshExtensions()
  await refreshSkills()
  applyTheme()
  connected.value = true
  void refreshGitAvailability()
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
  const requestId = ++extensionsRefreshRequest
  if (showLoading) extensionLoading.value = true
  try {
    const snapshot = await getExtensions()
    if (requestId !== extensionsRefreshRequest) return
    snapshot.tools ||= []
    snapshot.figma ||= { installed: false, enabled: false, running: false, pid: 0, hasToken: false, version: '' }
    snapshot.builtinCatalog ||= []
    snapshot.globalMcp ||= []
    snapshot.globalPlugins ||= []
    snapshot.builtins ||= {}
    snapshot.recommended ||= {}
    snapshot.packages ||= {}
    snapshot.directory ||= {}
    snapshot.mcp ||= {}
    // Keep the current extension UI mounted during install/enable/remove
    // operations and replace only its backing snapshot when fresh data arrives.
    extensionSnapshot.value = snapshot
  } catch (err) {
    if (requestId === extensionsRefreshRequest) {
      extensionNotice.value = { error: true, text: String(err) }
    }
  } finally {
    if (requestId === extensionsRefreshRequest) extensionLoading.value = false
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
    current.directory = { ...(current.directory || {}), [agentId]: status.directory || [] }
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
  const selection = { document: true, memory: true }
  for (const tool of builtinCatalog()) {
    if (tool.key !== 'skills-list') selection[tool.key] = true
  }
  return selection
}

function isGlobalRTKInstalled() {
  return Boolean(extensionSnapshot.value?.tools?.find(tool => tool.key === 'rtk')?.installed)
}

function defaultAgent() {
  const id = `agent-${crypto.randomUUID().slice(0, 8)}`
  return {
    id,
    name: `Agent ${config.agents.length + 1}`,
    description: '',
    dataDir: '',
    builtin: defaultBuiltinSelection(),
    recommended: { dcg: true, rtk: isGlobalRTKInstalled() },
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
  agent.recommended ||= {}
  if (!Object.hasOwn(agent.recommended, 'dcg')) agent.recommended.dcg = true
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
  // 数据库勾选只保留仍存在的连接 ID（与后端 Normalize 的引用完整性一致）。
  const dbIds = new Set((config.extensions?.db?.connections || []).map(conn => conn.id))
  ws.dbConnections = Array.isArray(ws.dbConnections)
    ? [...new Set(ws.dbConnections.filter(id => dbIds.has(id)))]
    : []
  // 默认智能体只保留仍存在的智能体 ID，失效引用清空后回退第一个智能体。
  const agentIds = new Set(config.agents.map(agent => agent.id))
  if (ws.defaultAgentId && !agentIds.has(ws.defaultAgentId)) ws.defaultAgentId = ''
  return ws
}

// --- SSH config ---
const {
  captureSshSaveState,
  closeSshEditor,
  confirmDeleteSsh,
  editingNewSsh,
  newSshId,
  normalizeSsh,
  openSshEditor,
  pendingDeleteSsh,
  persistSshChange,
  pickSshKeyFile,
  reconcileSshSaveResult,
  requestDeleteSsh,
  saveNewSsh,
  sshBusy,
  sshDraft,
  sshEditorOpen,
  sshTestStates,
  testSsh
} = useSshConnections({
  config,
  getWorkspaceDraft: () => wsDraft.value,
  normalizeWorkspace,
  persist,
  pushToast,
  t
})

const pendingExtensionDelete = ref(null)

const {
  closeDbEditor,
  confirmDeleteDb,
  normalizeDbConnection,
  dbAuditLoading,
  dbAuditRows,
  dbBusy,
  dbDraft,
  dbEditorOpen,
  dbTestStates,
  editingNewDb,
  loadDbAudit,
  newDbId,
  openDbEditor,
  pendingDeleteDb,
  persistDbChange,
  requestDeleteDb,
  saveNewDb,
  testDb,
  toggleWorkspaceDb,
  workspaceDbConnections
} = useDatabaseConnections({
  config,
  getWorkspaceDraft: () => wsDraft.value,
  persist,
  persistWorkspaceChange: persistWsChange,
  pushToast,
  t
})

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
    } else if (payload.type === 'directory') {
      if (selectedAgent.value) await deleteAgentExtensionDir(selectedAgent.value.id, payload.key, { name: payload.name })
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
function addWorkspaceSsh() {
  if (!wsDraft.value) return
  wsDraft.value.remotes.push(defaultRemote())
}
function removeWorkspaceSsh(index) {
  if (!wsDraft.value) return
  wsDraft.value.remotes.splice(index, 1)
  if (!wsDraft.value.remotes.length) wsDraft.value.remotes.push(defaultRemote())
  persistWsChange()
}
function handleWorkspaceSshChange(remote) {
  if (remote && !remote.sshConfigId) remote.remotePath = ''
  persistWsChange()
}
async function saveNewWs() {
  const ws = wsDraft.value
  if (!ws || wsBusy.value) return
  if (!ws.path) { pushToast('error', t.value.wsLocalRequired); return }
  const selectedRemotes = (ws.remotes || []).filter(remote => remote.sshConfigId)
  if (selectedRemotes.some(remote => !remote.remotePath?.trim())) { pushToast('error', t.value.wsRemoteRequired); return }
  if (new Set(selectedRemotes.map(remote => remote.sshConfigId)).size !== selectedRemotes.length) { pushToast('error', t.value.wsSshDuplicate); return }
  ws.remotes = selectedRemotes.length ? selectedRemotes : [defaultRemote()]
  const previousWorkspaceOrder = [...workspaceOrder.value]
  config.environments.push(ws)
  selectedWorkspaceId.value = ws.id
  const ok = await persist()
  if (ok) {
    // 新环境创建后立即加入主菜单排序首位，避免被已有环境列表挤到末尾。
    bumpWorkspaceToTop(ws.id)
    newWsId.value = ''
    wsDraft.value = null
    editingNewWs.value = false
    wsEditorOpen.value = false
    pushToast('success', t.value.wsCreated)
  } else {
    config.environments = config.environments.filter(item => item.id !== ws.id)
    workspaceOrder.value = previousWorkspaceOrder
    persistWorkspaceOrder()
    selectedWorkspaceId.value = ''
    pushToast('error', t.value.wsCreateFailed)
  }
}
function requestDeleteWs(ws) { if (config.environments.length <= 1 || wsBusy.value) return; pendingDeleteWs.value = ws }
async function confirmDeleteWs() {
  const ws = pendingDeleteWs.value
  if (!ws || config.environments.length <= 1 || wsBusy.value) return
  wsBusy.value = true
  const index = config.environments.indexOf(ws)
  config.environments.splice(index, 1)
  if (config.activeEnvId === ws.id) {
    const nextWorkspaceId = workspaceOrder.value.find(id => config.environments.some(item => item.id === id)) || config.environments[0]?.id || ''
    void initializeWorkspace(nextWorkspaceId)
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
  }
}

// A workspace can reference multiple SSH profiles, each with its own remote directory.
function defaultRemote() {
  return { id: `remote-${crypto.randomUUID().slice(0, 8)}`, remotePath: '', sshConfigId: '' }
}
function workspaceRemotes(ws) {
  return (ws?.remotes || []).filter(remote => remote.sshConfigId)
}
function remoteSsh(remote) {
  const sshId = remote?.sshConfigId
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
  newAgentId.value = ''
  newAgentDraft.value = null
  previousAgentId.value = ''
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
// Open agent config page with the initial tab (e.g. jump to extensions tab from the chat security-policy hint).
const agentConfigInitialTab = ref('basics')

function openAgentConfig(agent, initialTab = 'basics') {
  if (!agent) return
  editingAgentId.value = agent.id
  agentConfigInitialTab.value = initialTab
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

function requestDeleteAgent(agent) {
  if (!agent || config.agents.length <= 1 || agentDeleteBusy.value) return
  pendingDeleteAgent.value = agent
}

async function confirmDeleteAgent() {
  const agent = pendingDeleteAgent.value
  if (!agent || config.agents.length <= 1 || agentDeleteBusy.value) return
  agentDeleteBusy.value = true
  try {
    const result = await deleteAgent(agent.id)
    Object.assign(config, result)
    config.environments.forEach(normalizeWorkspace)
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

// agentOverride 允许为任意智能体切换扩展（主菜单“插件”页的分配智能体对话框使用），
// 不传时沿用原行为：操作当前选中的智能体。
async function toggleAgentExtension(group, key, desiredState, agentOverride) {
  const agent = agentOverride || selectedAgent.value
  if (!agent) return
  agent[group] ||= {}
  const previous = !!agent[group][key]
  const next = typeof desiredState === 'boolean' ? desiredState : !previous
  if (next === previous) return
  // Binary-backed recommended extensions need their shared runtime before an
  // agent-local bridge can be enabled.
  if (group === 'recommended' && ['rtk', 'dcg'].includes(key) && next) {
    const runtime = (extensionSnapshot.value?.recommended?.[agent.id] || []).find(tool => tool.key === key)
    if (runtime && !runtime.installed) {
      pushToast('error', key === 'dcg' ? t.value.dcgNotInstalledHint : t.value.rtkNotInstalledHint)
      return
    }
  }
  agent[group][key] = next
  const names = { rtk: 'RTK', dcg: 'DCG', figma: t.value.piFigma, 'pi-plugins': t.value.piPlugins }
  const name = names[key] || key.toUpperCase()
  if (agent.id !== newAgentId.value) {
    const ok = await persist()
    if (ok) {
      if (group === 'builtin' || key === 'rtk' || key === 'dcg' || key === 'figma' || key === 'pi-plugins') {
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

// 对话输入框盾牌菜单的 DCG 策略切换：
// - 关闭危险命令检测：仅作用于本次对话（写会话标记、实时生效），
//   不修改智能体 recommended.dcg 配置，不触发 Agent 重启；
// - 切回危险命令拦截模式：清除会话标记恢复拦截；仅当智能体本身未开启
//   DCG（此时拦截本来就不生效）才回退到开启智能体扩展配置。
async function onChatDcgPolicyChange(enabled) {
  const sessionId = Number(activeTaskId.value) || 0
  if (enabled) {
    sessionDcgDisabled.value = false
    if (sessionId > 0) {
      try { await setSessionDcgDisabled(sessionId, false) } catch (err) { pushToast('error', localizeError(String(err))) }
    }
    if (selectedAgent.value?.recommended?.dcg === false) {
      await toggleAgentExtension('recommended', 'dcg', true)
    }
    return
  }
  sessionDcgDisabled.value = true
  if (sessionId > 0) {
    try { await setSessionDcgDisabled(sessionId, true) } catch (err) { pushToast('error', localizeError(String(err))) }
  }
}

// 判断某个智能体当前是否已启用对应的推荐扩展。
// RTK/DCG 的 status.installed 表示全局运行时二进制，per-agent 状态在 enabled 上；
// Figma 的 installed 已包含 recommended 标记 + pi-mcp-adapter + mcp.json 配置。
function agentRecommendedEnabled(agent, key) {
  const status = (extensionSnapshot.value?.recommended?.[agent.id] || []).find(tool => tool.key === key)
  if (!status) return false
  return key === 'figma' ? !!status.installed : !!status.enabled
}

// 从主菜单“插件”页为指定智能体快速安装/卸载推荐扩展（RTK / DCG / Figma）。
async function assignAgentExtension(agentId, key, install) {
  const agent = (config.agents || []).find(item => item.id === agentId)
  if (!agent) return
  const names = { rtk: 'RTK', dcg: 'DCG', figma: t.value.piFigma }
  const name = names[key] || key.toUpperCase()
  const busyKey = `assign:${key}:${agentId}`
  if (extensionBusy.value === busyKey) return
  if (install === agentRecommendedEnabled(agent, key)) return
  extensionBusy.value = busyKey
  try {
    // Figma 除了写入 recommended 标记，还需要先在目标智能体的数据目录安装
    // pi-mcp-adapter，否则 mcp.json 里的 figma 条目不会被 Pi 加载。
    if (install && key === 'figma') {
      if (!figma.value.installed) {
        pushToast('error', t.value.piFigmaGlobalMissing)
        return
      }
      if (!figma.value.hasToken) {
        pushToast('error', t.value.piFigmaAuthorizationMissing)
        return
      }
      const result = await installAgentExtension(agentId, 'pi install npm:pi-mcp-adapter', { busyKey, name })
      if (result?.success === false) return
    }
    await toggleAgentExtension('recommended', key, install, agent)
    // 插件页展示的是所有智能体的分配情况，单智能体刷新不足以更新其他卡片。
    await refreshExtensions()
  } finally {
    if (extensionBusy.value === busyKey) extensionBusy.value = ''
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
    await refreshExtensions()
    if (action === 'install') pushToast('success', t.value.toastInstalled.replace('{name}', tool.name))
    else if (action === 'uninstall') pushToast('info', t.value.toastRemoved.replace('{name}', tool.name))
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

async function addManualMCP(payload) {
  if (extensionBusy.value) return
  extensionBusy.value = 'manual-mcp-add'
  try {
    const result = await beAddManualMCP(payload)
    await refreshExtensions()
    pushToast('success', result?.message || t.value.toastInstalled.replace('{name}', payload.key))
    return result
  } catch (err) {
    pushToast('error', t.value.toastExtensionError.replace('{error}', String(err)))
    throw err
  } finally {
    extensionBusy.value = ''
  }
}

async function removeAgentMcpServer(agentId, key) {
  if (!agentId || !key || extensionBusy.value) return
  extensionBusy.value = 'agent-mcp-remove'
  try {
    const result = await beRemoveAgentMcpServer(agentId, key)
    await refreshExtensions()
    pushToast('success', result?.message || t.value.toastRemoved.replace('{name}', key))
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

// 删除一个未纳管的扩展目录（extensions/ 下不属于 CodingTo 管理的条目，如手动
// 拷入的 ask-user）。后端会拒绝删除内置/系统/RTK 等受管扩展。
async function deleteAgentExtensionDir(agentId, key, options = {}) {
  const displayName = options.name || key
  extensionDeleteBusy.value = true
  try {
    const result = await beDeleteAgentExtensionDir(agentId, key)
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
    extensionDeleteBusy.value = false
  }
}

async function figmaAction(action) {
  extensionBusy.value = 'figma-' + action
  try {
    await manageExtension({ key: 'figma', action })
    await refreshExtensions()
    if (action === 'install') pushToast('success', t.value.toastInstalled.replace('{name}', t.value.figma))
    else if (action === 'uninstall') pushToast('info', t.value.toastRemoved.replace('{name}', t.value.figma))
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
      if (!currentTask()?.environmentId && sessionChanges.value.root) {
        currentWorkDir.value = sessionChanges.value.root
      }
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

async function ensureConversation(title, options = {}) {
  const forceNew = options.forceNew === true
  if (!forceNew) {
    const existing = currentTask()
    if (existing) return existing
  }
  const agent = selectedAgent.value
  const requestedModel = agent?.defaultModel || config.defaultModel
  // 智能体默认模型可能仍指向已改名/删除的服务商旧标识，创建前解析到当前
  // 有效服务商，避免新会话落库旧 provider 前缀。
  const providerName = resolveProviderName(agent?.defaultProvider || config.defaultProvider, requestedModel)
  const provider = config.providers.find(p => p.name === providerName)
  const modelName = provider?.models.some(m => m.id === requestedModel) ? requestedModel : provider?.models?.[0]?.id || ''
  const task = await createSession({
    agentId: agent?.id || config.activeAgentId,
    environmentId: options.environmentId ?? config.activeEnvId,
    title: Array.from(title.trim()).slice(0, 50).join('') || t.value.chatNewSession,
    provider: providerName,
    model: modelName
  })
  tasks.value.unshift(task)
  activeTaskId.value = task.id
  return task
}

// 「添加到对话」：在当前会话绑定的工作空间中新建对话（标题为“新对话”），
// 并把内容写入输入框。若当前已停留在本次添加任务打开的对话，则直接复用
// 并继续追加；追加前输入框已有内容按内容类型分隔（文件路径用分号、文本
// 用换行），避免拼接成不可解析的串。全程不关闭 Git 管理弹窗。
async function ensureAddToChatSession() {
  // 只有在当前查看的正是本次添加任务打开的对话时才复用，避免误合并到
  // 用户手动切到的其他会话。
  const reusable = !!addToChatSessionId.value && String(activeTaskId.value) === String(addToChatSessionId.value)
  if (reusable) return
  await ensureConversation(t.value.chatNewSession, {
    forceNew: true,
    environmentId: currentTask()?.environmentId || config.activeEnvId,
  })
  addToChatSessionId.value = activeTaskId.value
  // 把界面切到该新建对话并重置为空对话状态，输入框随后写入内容。
  thinkingLevel.value = defaultThinkingLevelForModel(selectedModel.value)
  sessionDcgDisabled.value = false
  changeRefreshRequest += 1
  messagesList.value = []
  sessionChanges.value = { root: '', nodes: [], files: [], added: 0, deleted: 0 }
  sessionChangesLoading.value = false
  activeAssistant = null
  error.value = ''
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
  draft.value = ''
  goHome()
}

async function addFileToChat(filePath) {
  const normalized = String(filePath || '').replaceAll('\\', '/').trim()
  if (!normalized) return
  await ensureAddToChatSession()
  draft.value = draft.value.trim() ? `${draft.value.trim()};${normalized}` : normalized
}

async function addTextToChat(text) {
  const normalized = String(text || '').trim()
  if (!normalized) return
  await ensureAddToChatSession()
  draft.value = draft.value.trim() ? `${draft.value.trim()}\n${normalized}` : normalized
}

// 后端列表已合并权威运行时状态，可用于修复漏收终态事件后残留的 running。
// 但列表请求期间可能收到更新的本地生命周期事件，因此仅在对应版本未变化时
// 应用列表状态；有新事件时保留较新的 runningTaskIds 状态，避免旧响应回退 UI。
function reconcileTaskRuntimeStatus(list, requestedVersions) {
  for (const task of list) {
    const key = String(task.id)
    const requestedVersion = requestedVersions.get(key) || 0
    const currentVersion = taskRuntimeVersions.get(key) || 0
    if (currentVersion !== requestedVersion) {
      task.status = runningTaskIds.has(key) ? 'running' : 'active'
      continue
    }
    if (task.status === 'running') runningTaskIds.add(key)
    else {
      runningTaskIds.delete(key)
      stoppingTaskIds.delete(key)
    }
  }
  running.value = runningTaskIds.size > 0
}

async function refreshSessions() {
  const request = ++sessionRefreshRequest
  const requestedVersions = new Map(taskRuntimeVersions)
  const latest = (await listSessions()) || []
  if (request !== sessionRefreshRequest) return
  reconcileTaskRuntimeStatus(latest, requestedVersions)
  tasks.value = latest
  reconcileStaleSessionProviders(latest)
  const active = latest.find(item => item.id === activeTaskId.value)
  if (active) {
    executionElapsedMs.value = Number(active.execDurationMs) || 0
    executionRunning.value = active.status === 'running'
  }
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
// 工作空间默认智能体：优先使用工作空间配置的 defaultAgentId，未配置（或
// 指向已删除智能体，normalize 已清空）时回退第一个智能体。
function envDefaultAgentId(envId) {
  const env = config.environments.find(item => item.id === envId)
  if (env?.defaultAgentId && config.agents.some(agent => agent.id === env.defaultAgentId)) {
    return env.defaultAgentId
  }
  return config.agents[0]?.id || ''
}
// 工作空间在主菜单中的手动排序。只保存环境 ID，不改动配置中的环境数据，
// 因此拖动排序不会触发后端配置写入，也不会影响当前会话运行。
const draggedWorkspaceId = ref('')
const dragOverWorkspaceId = ref('')
function startWorkspaceDrag(group, event) {
  if (!group?.id) return
  draggedWorkspaceId.value = group.id
  dragOverWorkspaceId.value = ''
  if (event?.dataTransfer) {
    event.dataTransfer.effectAllowed = 'move'
    event.dataTransfer.setData('text/plain', group.id)
  }
}
function handleWorkspaceDragOver(group) {
  if (!draggedWorkspaceId.value || !group?.id || group.id === draggedWorkspaceId.value) return
  dragOverWorkspaceId.value = group.id
}
function dropWorkspace(group) {
  const sourceId = draggedWorkspaceId.value
  const targetId = group?.id
  if (!sourceId || !targetId || sourceId === targetId) {
    endWorkspaceDrag()
    return
  }
  const ids = sessionGroups.value.filter(item => item.id).map(item => item.id)
  const sourceIndex = ids.indexOf(sourceId)
  const targetIndex = ids.indexOf(targetId)
  if (sourceIndex < 0 || targetIndex < 0) {
    endWorkspaceDrag()
    return
  }
  ids.splice(sourceIndex, 1)
  ids.splice(ids.indexOf(targetId), 0, sourceId)
  workspaceOrder.value = ids
  persistWorkspaceOrder()
  endWorkspaceDrag()
}
function endWorkspaceDrag() {
  draggedWorkspaceId.value = ''
  dragOverWorkspaceId.value = ''
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
  return buildSessionGroups({
    environments: config.environments,
    tasks: tasks.value,
    archivedTaskIds,
    workspaceOrder: workspaceOrder.value,
    visibleSessionCounts,
    pageSize: SESSION_PAGE_SIZE,
    ungroupedLabel: t.value.ungrouped || '未分类'
  })
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
  // 显式 total 缺失或为 0（如后端聚合计不出 totalTokens）时回退到分项累加，
  // 避免 total 显示 0 而 input/output 有值
  const explicitTotal = firstNumber(usage.total)
  tokenStats.value = {
    input,
    cached,
    cacheWrite,
    output,
    total: explicitTotal > 0 ? explicitTotal : input + cached + cacheWrite + output
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

// 所有仍待应答的子 Agent 权限申请 / 计划确认弹窗。数据实时增量维护在各
// codingto_subagent 工具消息的 detail.subagentUI.dialog 中（handleSubagentEvent
// → mergeSubagentRuntime），这里统一收集后提升到主对话弹窗区渲染，每个弹窗
// 标注来源子 Agent 名称。
const subagentDialogs = computed(() => {
  const dialogs = []
  for (const message of messagesList.value) {
    if (message.role !== 'tool') continue
    const detail = message.detail && typeof message.detail === 'object' ? message.detail : {}
    const dialog = detail.subagentUI?.dialog
    if (!dialog || typeof dialog !== 'object' || !dialog.id) continue
    const subagent = detail.subagent && typeof detail.subagent === 'object' ? detail.subagent : {}
    const agentName = subagent.agentName
      || config.agents.find(agent => agent.id === subagent.agentKey)?.name
      || subagent.agentKey
      || ''
    // 子 Agent 申请执行计划时先 setWidget('plan-todos') 写入完整步骤再弹确认框，
    // 这里随弹窗一并提取，供主对话弹窗区展示完整计划（不再只有标题与步数）。
    const ui = detail.subagentUI && typeof detail.subagentUI === 'object' ? detail.subagentUI : {}
    const widgets = ui.widgets && typeof ui.widgets === 'object' ? ui.widgets : {}
    const planLines = Array.isArray(widgets['plan-todos']) ? widgets['plan-todos']
      : Array.isArray(widgets['plan-execution']) ? widgets['plan-execution']
      : []
    dialogs.push({
      messageId: message.id,
      sessionId: Number(activeTaskId.value) || 0,
      runId: String(subagent.runId || dialog.runId || ''),
      agentName,
      planLines,
      dialog
    })
  }
  return dialogs
})

// 弹窗成功渲染后由 SubagentDialogDock 经 ChatComposer/ChatView 转发至此，向
// 后端回传子 Agent 的 extension_ui_ack，解除桥接端渲染超时（与主 Agent 一致）。
function ackSubagentDialog({ item, id }) {
  if (!item?.sessionId || !item?.runId || !id) return
  ackSubagentUI(item.sessionId, item.runId, String(id)).catch(() => {})
}

// 应答后本地立即清除该 dialog（后端随后会通过 subagent_ui_response 事件再次
// 驱动 applySubagentUIEvent 清理，本地先清避免残留闪烁）。清除前校验当前
// dialog ID 仍为本次应答的 ID：应答期间同一 run 可能已发起新的弹窗，无条件
// 清除会误删新请求。
function clearSubagentDialog(item) {
  const message = messagesList.value.find(entry => entry.id === item?.messageId)
  const currentDialog = message?.detail?.subagentUI?.dialog
  if (!message || !currentDialog || currentDialog.id !== item?.dialog?.id) return
  message.detail = {
    ...message.detail,
    subagentUI: { ...message.detail.subagentUI, dialog: undefined }
  }
}

// A permission can be answered from an IM robot while its desktop card is
// visible. The agent response alone does not mutate frontend state, so consume
// the steward lifecycle event and dismiss the exact matching card here.
function handleStewardPermissionUpdate(event) {
  if (!isResolvedStewardPermission(event) || !event?.requestId) return
  const requestId = String(event.requestId)
  const sessionId = String(event.sessionId ?? '')
  const runId = String(event.runId || '')
  const activeId = String(activeTaskId.value ?? '')

  if (runId) {
    if (!sessionId || sessionId === activeId) {
      const item = subagentDialogs.value.find(entry => (
        String(entry.runId || '') === runId && String(entry.dialog?.id || '') === requestId
      ))
      if (item) clearSubagentDialog(item)
    }
    if (sessionId) clearTaskPendingAttention(sessionId, `subagent:${runId}:${requestId}`)
    return
  }

  // Background dialogs live only in per-task storage until the user opens the
  // conversation. Clear that copy as well, otherwise switching back restores
  // a request that has already been answered by the robot.
  if (sessionId) {
    const persisted = readPersistedExtDialog(sessionId)
    if (stewardPermissionMatchesDialog(event, persisted, sessionId)) {
      persistExtDialogForTask(sessionId, null)
    }
  }

  const targetId = sessionId || activeId
  if (targetId === activeId && stewardPermissionMatchesDialog(event, extensionDialog.value, activeId)) {
    extensionDialog.value = null
    clearPersistedExtDialog(targetId)
  }
  if (targetId === activeId && String(pendingPlanDialog.value?.id || '') === requestId) {
    flushPendingPlanDialog()
  }
}

async function respondSubagentDialog({ item, payload }) {
  const dialog = item?.dialog
  if (!dialog?.id || !item?.runId) return
  const command = { id: dialog.id, ...payload }
  try {
    if (payload?.browserProfile) {
      const form = payload.browserProfile
      const profile = await saveBrowserProfile({
        key: form.key,
        targetUrl: form.targetUrl,
        loginUrl: form.targetUrl,
        authMode: 'manual'
      })
      command.value = profile.id
    }
    await respondSubagentUI(item.sessionId, item.runId, command)
    clearSubagentDialog(item)
  } catch (err) {
    pushToast('error', localizeError(String(err?.message || err)))
  }
}

// 计划确认弹窗与 plan-todos Widget 的竞态治理（主 agent 链路）：
// 1. 收到计划确认但 planItems 仍为空时缓存弹窗，不立即显示；
// 2. setWidget('plan-todos') 到达后由事件处理把缓存弹窗提升为正式弹窗；
// 3. 等待超时（远小于后端看门狗 90s）后仍显示弹窗，但标记 planMissing，
//    ChatPlanPanel 禁用批准按钮（取消仍可用），用户不会被永远挂起。
function clearPendingPlanTimer() {
  if (pendingPlanTimer) {
    clearTimeout(pendingPlanTimer)
    pendingPlanTimer = null
  }
}
function flushPendingPlanDialog() {
  pendingPlanDialog.value = null
  clearPendingPlanTimer()
}
function cachePendingPlanDialog(dialog, taskId) {
  pendingPlanDialog.value = { ...dialog }
  clearPendingPlanTimer()
  pendingPlanTimer = setTimeout(() => {
    pendingPlanTimer = null
    const cached = pendingPlanDialog.value
    if (!cached) return
    // plan-todos 迟迟未到：仍显示弹窗（标记计划缺失、禁用批准），保证弹窗
    // 最终渲染、ack 发出，后端看门狗不会超时代答取消。
    pendingPlanDialog.value = null
    extensionDialog.value = { ...cached, planMissing: true }
    setTaskRunning(taskId, true)
    persistExtDialog()
  }, PLAN_WIDGET_WAIT_MS)
}

// 弹窗成功渲染后由 ChatPlanPanel 经 ChatComposer/ChatView 转发至此，向后端回传
// extension_ui_ack 解除交互请求看门狗。ack 只是“弹窗已展示”的确认，不是答案；
// 静默失败，不影响主流程。taskId 用于后台任务弹窗的确认路由。
function ackExtensionDialog(payload, taskId) {
  const id = payload?.id
  if (!id) return
  sendAgentCommand(taskId || activeTaskId.value, { type: 'extension_ui_ack', id }).catch(() => {})
}

async function respondExtensionDialog(payload) {
  const dialog = extensionDialog.value
  if (!dialog) return
  // 应答弹窗时清理可能残留的计划等待缓存（缓存尚未提升就用户取消/切换到其他
  // 弹窗的场景），避免旧缓存把下一轮弹窗错误地延迟提升。
  flushPendingPlanDialog()

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
  const childEvent = parseSubagentEvent(event?.event)
  const attentionTaskId = sourceTaskId || activeTaskId.value
  const childRequestKey = `subagent:${event?.runId || event?.childRunId || event?.toolCallId || ''}:${childEvent?.id || ''}`
  if (childEvent?.type === 'subagent_ui_response') {
    clearTaskPendingAttention(attentionTaskId, childRequestKey)
  }
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
  const agentName = config.agents.find(agent => agent.id === event.agentKey)?.name || ''
  if (!tool) {
    const pending = pendingSubagentEvents.get(toolCallId) || []
    pendingSubagentEvents.set(toolCallId, [...pending, event].slice(-32))
    return
  }
  tool.detail = mergeSubagentRuntime(tool.detail, { ...event, agentName })
  pendingSubagentEvents.delete(toolCallId)
}

function handleAgentEvent(event) {
  const type = event?.type
  const sourceTaskId = eventTaskId(event)
  const hasSourceTask = sourceTaskId !== '' && sourceTaskId != null
  const sourceIsActive = !hasSourceTask || String(sourceTaskId) === String(activeTaskId.value)

  // 常驻管家自主运行：其计划确认无需用户在“管家设置-消息”中手动确认，
  // 直接自动确认并跳过前端弹窗，消息 Tab 仅保留会话详情。
  if (
    type === 'extension_ui_request' &&
    event.method === 'confirm' &&
    isPlanConfirmationDialog(event) &&
    sourceTaskId &&
    String(sourceTaskId) === String(residentSessionId.value)
  ) {
    sendAgentCommand(sourceTaskId, { type: 'extension_ui_response', id: event.id, confirmed: true, value: '' }).catch(() => {})
    return
  }

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
        // 后台任务的弹窗已被前端持久化，切换到该任务时会恢复展示，因此同样
        // 向后端确认，避免看门狗误判为“弹窗未渲染”而自动取消。
        ackExtensionDialog({ id: event.id }, sourceTaskId)
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
      resetTerminalErrors(sourceTaskId)
      markTaskForSidebar(sourceTaskId)
      clearTaskPendingAttention(sourceTaskId)
      setTaskRunning(sourceTaskId, false)
      persistExtDialogForTask(sourceTaskId, null)
      refreshSessions().catch(() => {})
      void sendNextPendingPrompt(sourceTaskId)
    } else if (type === 'extension_ui_timeout') {
      // 后台任务的弹窗超时被自动取消：清理持久化的残留弹窗，避免切回该任务
      // 时恢复出一个已失效的对话框。
      persistExtDialogForTask(sourceTaskId, null)
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
    resetTerminalErrors(sourceTaskId || activeTaskId.value)
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
      pushTerminalErrorMessage(sourceTaskId || activeTaskId.value, msg)
      setTaskRunning(sourceTaskId || activeTaskId.value, false)
    }
  } else if (type === 'message_end') {
    reconcileAssistantMessage(event.message)
    const endError = agentEventErrorMessage(event)
    if (endError) {
      const msg = localizeError(endError)
      error.value = msg
      pushTerminalErrorMessage(sourceTaskId || activeTaskId.value, msg)
    }
  } else if (type === 'turn_end') {
    const turnError = agentEventErrorMessage(event)
    if (turnError) {
      const msg = localizeError(turnError)
      error.value = msg
      pushTerminalErrorMessage(sourceTaskId || activeTaskId.value, msg)
    }
  } else if (type === 'tool_execution_start') {
    upsertToolMessage(event)
  } else if (type === 'tool_execution_timeout') {
    // 工具执行超时：后端 fireToolWatchdog 先发 abort_bash 并记录本事件，把工具
    // 调用标记为完成，避免在 abort/强杀收尾完成前工具一直显示"运行中"。
    const toolId = toolEventId(event)
    const tool = [...messagesList.value].reverse().find(item => item.role === 'tool' && (item.detail?.toolCallId || item.detail?.id) === toolId)
    if (tool) {
      const startedAt = tool.detail?.startedAt || Date.now()
      tool.detail = {
        ...tool.detail,
        ...event,
        status: 'done',
        cancelled: true,
        timedOut: true,
        startedAt,
        durationMs: Date.now() - startedAt
      }
    }
    const errMsg = localizeError(event.message || event.errorMessage || 'Tool execution timed out.')
    error.value = errMsg
    pushErrorMessage(errMsg)
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
    resetTerminalErrors(sourceTaskId || activeTaskId.value)
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
      if (activeAssistant && !activeAssistant.content && !activeAssistant.thinkingContent && !activeAssistant.thinking) {
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
    clearPreparingMessage()
    if (succeeded) {
      error.value = ''
      messagesList.value = messagesList.value.filter(message => message.role !== 'error')
    }
    const endError = agentEventErrorMessage(event)
    if (endError) {
      const msg = localizeError(endError)
      error.value = msg
      pushTerminalErrorMessage(sourceTaskId || activeTaskId.value, msg)
    }
    pushChangeMessage(event.changeSummary, event._recordedAt)
    clearPersistedExtDialog(currentTask()?.id)
    flushPendingPlanDialog()
    requestSessionState()
    refreshSessions().catch(() => {})
    scheduleSessionChangesRefresh(60)
  } else if (type === 'agent_settled') {
    resetTerminalErrors(sourceTaskId || activeTaskId.value)
    markTaskForSidebar(sourceTaskId || activeTaskId.value)
    clearTaskPendingAttention(sourceTaskId || activeTaskId.value)
    // 主 agent 回合结束但仍有后台子 agent 在运行：会话整体尚未结束，保持
    // 运行中表现（转圈/终止按钮保留），等最后一批 follow-up 回合真正收尾。
    // 后端也会在 agent:state 中以 running:true 兜底（权威判定见 run.json）。
    if (!hasRunningSubagents()) {
      setTaskRunning(sourceTaskId || activeTaskId.value, false)
      executionRunning.value = false
    }
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
        // 计划数据已就绪：若此前有缓存的计划确认弹窗（confirm 先于 setWidget 到达），
        // 立即提升为正式弹窗，保证用户看到完整计划后再确认。
        if (pendingPlanDialog.value) {
          const cached = pendingPlanDialog.value
          pendingPlanDialog.value = null
          clearPendingPlanTimer()
          extensionDialog.value = cached
          setTaskRunning(sourceTaskId || activeTaskId.value, true)
          persistExtDialog()
        }
      } else if (key === 'plan-execution') {
        executionPlan.value = parsePlanLines(lines)
      } else if (key) {
        if (lines) extensionWidgets[key] = lines
        else delete extensionWidgets[key]
      }
    } else if (['select', 'confirm', 'input', 'editor'].includes(event.method)) {
      // 计划确认弹窗可能在 plan-todos（setWidget）之前到达：若计划数据尚未就绪，
      // 先缓存等待，避免用户看到没有计划内容、无法核对的确认框。消息内已内嵌
      // 结构化步骤（__CODINGTO_PLAN_STEPS__）时优先直接填充计划，不依赖 setWidget。
      let dialogEvent = event
      const embeddedSteps = parsePlanStepsFromMessage(event.message)
      if (embeddedSteps) {
        dialogEvent = { ...event, message: stripPlanStepsMarker(event.message) }
        if (planItems.value.length === 0) planItems.value = embeddedSteps
      }
      if (isPlanConfirmationDialog(dialogEvent) && planItems.value.length === 0) {
        cachePendingPlanDialog(dialogEvent, sourceTaskId || activeTaskId.value)
      } else {
        extensionDialog.value = dialogEvent
        setTaskRunning(sourceTaskId || activeTaskId.value, true)
        persistExtDialog()
      }
    }
  } else if (type === 'extension_ui_timeout') {
    // 后端看门狗超时：弹窗从未被前端确认，已代答取消解除 agent 阻塞。清理
    // 可能残留的弹窗状态并提示用户；running 状态由后续 agent_settled 收尾。
    if (extensionDialog.value && String(extensionDialog.value.id) === String(event.id)) {
      extensionDialog.value = null
      clearPersistedExtDialog()
    }
    flushPendingPlanDialog()
    pushToast('info', t.value.extensionUiTimeout)
  } else if (type === 'error') {
    const msg = event.code === 'model_first_response_timeout'
      ? t.value.modelFirstResponseTimeout
      : localizeError(typeof event.error === 'string' ? event.error : event.error?.message || 'Agent error')
    error.value = msg
    pushTerminalErrorMessage(sourceTaskId || activeTaskId.value, msg)
    setTaskRunning(sourceTaskId || activeTaskId.value, false)
  }
  syncCurrentTask()
}

function clearPreparingMessage() {
  if (preparingMessage && messagesList.value.includes(preparingMessage)) {
    messagesList.value = messagesList.value.filter((message) => message !== preparingMessage)
  }
  preparingMessage = null
}

function ensureAssistant(thinking = false) {
  if (!activeAssistant) {
    clearPreparingMessage()
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
  } else if (activeAssistant && !activeAssistant.content && !activeAssistant.thinkingContent && !activeAssistant.thinking) {
    messagesList.value = messagesList.value.filter(messageItem => messageItem !== activeAssistant)
  }
  for (const toolCall of toolCalls) upsertToolMessage({ toolCall })
  activeAssistant = null
}

// agentEventErrorMessage 提取终止性事件携带的模型/Provider 错误。Pi 把
// message_end / turn_end 的错误写在 event.message.errorMessage，把 agent_end
// 的错误写在 messages[].errorMessage，顶层 errorMessage 也可能存在（error 事件）。
// 与后端 readSessionMessages 的 eventErrorMessage 保持同一提取口径，保证实时
// 显示与历史重读一致。
function agentEventErrorMessage(event) {
  if (!event) return ''
  if (event.errorMessage) return event.errorMessage
  const message = event.message || {}
  if (message.errorMessage) return message.errorMessage
  if (Array.isArray(event.messages)) {
    for (let index = event.messages.length - 1; index >= 0; index--) {
      const item = event.messages[index]
      if (item?.role === 'assistant' && item.errorMessage) return item.errorMessage
    }
  }
  if (event.error) return typeof event.error === 'string' ? event.error : (event.error?.message || '')
  return ''
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
  const pending = pendingSubagentEvents.get(String(toolId))
  if (pending?.length) {
    for (const subagentEvent of pending) {
      const agentName = config.agents.find(agent => agent.id === subagentEvent.agentKey)?.name || ''
      tool.detail = mergeSubagentRuntime(tool.detail, { ...subagentEvent, agentName })
    }
    pendingSubagentEvents.delete(String(toolId))
  }
  return tool
}

async function runPrompt({ message, images, attachments: promptAttachments, skillPath, agentId, provider, model, workDir, mode: promptMode, thinkingLevel: promptThinking }) {
  error.value = ''
  const needCreateSession = !currentTask()
  const attachmentMeta = (promptAttachments || []).map((a) => ({ name: a.name, kind: a.kind, size: a.size, path: a.path || '' }))
  // 首次发起问题到后端创建会话（pi 建会话）会有 2-3 秒延迟，期间页面无任何反馈。
  // 这里先把用户消息与一条一次性的「正在准备会话中」占位推入消息流，消除空白期；
  // 该占位为纯前端临时消息，不写入后端会话消息表，真实助手消息开始流式输出即被移除。
  messagesList.value.push({
    id: crypto.randomUUID(),
    role: 'user',
    content: message || (promptAttachments?.length ? '' : `[${images?.length || 0} images]`),
    images: images || [],
    attachments: attachmentMeta
  })
  if (needCreateSession) {
    preparingMessage = reactive({
      id: crypto.randomUUID(),
      role: 'assistant',
      content: '',
      live: true,
      preparing: true,
      createdAt: Date.now()
    })
    messagesList.value.push(preparingMessage)
  }
  let task
  try {
    task = await ensureConversation(message)
    clearTaskSidebarDot(task.id)
    const promptAgent = config.agents.find(agent => agent.id === agentId) || selectedAgent.value
    // 新问题真正进入消息流时，折叠此前所有轮次的思考；本轮新产生的
    // assistant 消息会由 ensureAssistant 以展开状态创建。
    collapsePreviousThinking()
    draft.value = ''
    setTaskRunning(task.id, true)
    activeAssistant = null
    await startPrompt({
      agentId: promptAgent?.id,
      message,
      provider: provider || task.provider || promptAgent?.defaultProvider || config.defaultProvider,
      model: model || task.model || promptAgent?.defaultModel || config.defaultModel,
      // 当 agent 未单独设置默认模型，且前端也没显式指定时，回退到首个启用服务商，
      // 避免把空 provider/model 发到后端去触发全局 openai 默认。
      fallbackProvider: selectedAgent.value?.defaultProvider || config.defaultProvider,
      fallbackModel: selectedAgent.value?.defaultModel || config.defaultModel,
      workDir: workDir || currentWorkDir.value,
      mode: promptMode || mode.value,
      thinkingLevel: promptThinking || thinkingLevel.value,
      skillPath: skillPath || '',
      images,
      attachments: toAttachmentInputs(promptAttachments || []),
      sessionId: task.id,
      sessionPath: task.sessionPath || '',
      // 会话级 DCG 开关：新会话首次发送时由后端落盘会话标记，
      // 运行中切换则已通过 setSessionDcgDisabled 实时生效。
      disableDcg: sessionDcgDisabled.value
    })
    // StartPrompt has persisted the prompt node before returning. Refresh now
    // so the right sidebar shows the running node even before the first edit.
    scheduleSessionChangesRefresh(0)
    // 在当前工作空间发消息：将该工作空间置顶。
    bumpWorkspaceToTop(config.activeEnvId)
    requestSessionState()
  } catch (err) {
    clearPreparingMessage()
    if (task?.id) setTaskRunning(task.id, false)
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
  clearTaskSidebarDot(taskId)
  setTaskRunning(taskId, true)
  try {
    await startPrompt({
      agentId: task.agentId || prompt.agentId,
      message: prompt.message,
      provider: prompt.provider || task.provider,
      model: prompt.model || task.model,
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
  if (attachmentReadsPending.value > 0 || selectedModelUnavailable.value || (!message && !promptImages.value.length && !attachments.value.length) || !selectedModelValue.value) return
  const task = currentTask()
  const prompt = {
    id: crypto.randomUUID(),
    message,
    images: safeClone(promptImages.value),
    attachments: safeClone(attachments.value),
    createdAt: Date.now(),
    agentId: selectedAgent.value?.id,
    // 无会话时智能体默认可能仍指向已改名/删除的服务商旧标识，解析到当前
    // 有效服务商，避免 runPrompt 用旧 provider 覆盖新建会话的落库模型。
    provider: task?.provider || resolveProviderName(selectedAgent.value?.defaultProvider, selectedAgent.value?.defaultModel) || config.defaultProvider,
    model: task?.model || selectedAgent.value?.defaultModel || config.defaultModel,
    workDir: currentWorkDir.value,
    mode: mode.value,
    thinkingLevel: thinkingLevel.value,
    skillPath: selectedSkill.value?.agents?.find(agent => agent.id === selectedAgent.value?.id)?.path || '',
    sessionPath: task?.sessionPath || ''
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
    const state = await abortPrompt(taskId)
    applyTaskRuntimeState(taskId, state)
    // agent_settled/agent:state normally settles the UI. Re-read the same
    // authoritative state on a bounded backoff as a fallback for a terminal
    // event that raced with WebView subscription or conversation switching.
    const delays = [100, 200, 400, 800, 1000, 1000, 1000]
    for (const delay of delays) {
      if (!runningTaskIds.has(key)) break
      await new Promise(resolve => setTimeout(resolve, delay))
      try {
        applyTaskRuntimeState(taskId, await getSessionRuntimeState(taskId))
      } catch {
        // The original abort was accepted; a transient fallback read must not
        // turn it into a user-visible stop failure.
      }
    }
    stoppingTaskIds.delete(key)
  } catch (err) {
    stoppingTaskIds.delete(key)
    error.value = localizeError(String(err))
  }
}

async function pickSessionDirectory() {
  const value = await chooseSessionDir()
  if (value) config.sessionDir = value
}

// --- ChatView 交互回调 ---

async function chatNewSession() {
  syncCurrentTask()
  activeTaskId.value = ''
  // Even before the first message, this workspace is considered in use and
  // its Git cache/watcher is initialized in the background.
  void initializeWorkspace(config.activeEnvId)
  // 新建对话默认选中该工作空间的默认智能体；未配置时回退第一个智能体。
  const envAgentId = envDefaultAgentId(config.activeEnvId)
  currentAgentId.value = envAgentId
  const defaultAgent = config.agents.find(agent => agent.id === envAgentId)
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
  // 新建对话默认跟随智能体配置，会话级拦截开关复位。
  sessionDcgDisabled.value = false
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
  clearTaskSidebarDot(task.id)
  if (String(task.id) === String(activeTaskId.value)) {
    goHome()
    return
  }
  // 离开首页（新建对话）进入历史会话前，先把未发送草稿存好；
  // 历史会话的输入框不显示该草稿。
  if (isHomeMode.value) persistDraftForEnv(config.activeEnvId, draft.value)
  syncCurrentTask()
  // 先切到对话框并展示加载动画，同时立即预热该对话工作目录的 Git 缓存。
  activeTaskId.value = task.id
  const taskEnvironmentId = task.environmentId && config.environments.some(env => env.id === task.environmentId)
    ? task.environmentId
    : ''
  config.activeEnvId = taskEnvironmentId
  currentWorkDir.value = environmentWorkDir(taskEnvironmentId) || bootstrap.value?.defaultWorkDir || currentWorkDir.value
  void initializeWorkspace(taskEnvironmentId)
  draft.value = ''
  sessionChanges.value = { root: '', nodes: [], files: [], added: 0, deleted: 0 }
  refreshSessionChanges()
  goHome()
  loadingHistory.value = true
  messagesList.value = []
  try {
    // 恢复该对话的会话级命令拦截状态（标记文件持久化在会话目录）。
    try { sessionDcgDisabled.value = await getSessionDcgDisabled(task.id) } catch { sessionDcgDisabled.value = false }
    const history = await getSessionHistory(task.id)
    messagesList.value = initializeThinkingVisibility(safeClone(history?.messages || []))
    // Re-entering a conversation reconciles the stop button/sidebar against
    // the backend runtime instead of retaining a missed terminal event in the
    // frontend's in-memory running set.
    applyTaskRuntimeState(task.id, history?.runtime)
    // Token stats come from the backend aggregation (authoritative for history,
    // including sessions recorded before this fix). Fall back to the snapshot
    // saved on the task when no fresh aggregation is available.
    tokenStats.value = { input: 0, cached: 0, cacheWrite: 0, output: 0, total: 0 }
    if (history?.tokenStats) {
      applySessionStats({ tokens: history.tokenStats })
    } else if (task.tokenStats) {
      tokenStats.value = { ...tokenStats.value, ...task.tokenStats }
    }
    // contextUsage 与 tokenStats 同源：后端聚合的 tokens 为权威值，任务快照仅兜底；
    // contextWindow/percent 后端聚合为 0，沿用快照或当前配置
    const snapshotUsage = task.contextUsage || {}
    contextUsage.value = {
      tokens: firstNumber(history?.contextUsage?.tokens) || firstNumber(snapshotUsage.tokens),
      contextWindow: firstNumber(snapshotUsage.contextWindow) || contextWindow.value,
      percent: firstNumber(snapshotUsage.percent)
    }
    activeSessionPath.value = task.sessionPath || ''
    planItems.value = safeClone(task.planItems || [])
    restorePlanItems(task.id)
    executionPlan.value = []
    restoreExecPlan(task.id)
    // 若存在待确认的计划，则隐藏上一轮残留的执行计划条，确保计划面板正常显示
    if (planItems.value.length) executionPlan.value = []
    executionElapsedMs.value = Number(history?.runtime?.execDurationMs ?? task.execDurationMs) || 0
    executionRunning.value = activeTaskRunning.value
    if (task.agentId && config.agents.some(agent => agent.id === task.agentId)) currentAgentId.value = task.agentId
    if (task.provider && config.providers.some(provider => provider.name === task.provider)) {
      config.defaultProvider = task.provider
      config.defaultModel = task.model || config.defaultModel
    }
    activeAssistant = executionRunning.value
      ? [...messagesList.value].reverse().find(message => message.role === 'assistant' && message.live) || null
      : null
    extensionDialog.value = null
    // 任务仍在进行（如等待用户确认计划）时，恢复持久化的扩展对话框，避免刷新后无对话框可操作
    if (history?.runtime?.running) restoreExtDialog(task.id)
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
  if (!env?.id) return
  if (env.id === config.activeEnvId) return
  // Switching workspaces is process-local and never saves the global config.
  if (isHomeMode.value) persistDraftForEnv(config.activeEnvId, draft.value)
  config.activeEnvId = env.id
  currentWorkDir.value = env.path
  draft.value = isHomeMode.value ? loadDraftForEnv(env.id) : ''
  // 恢复目标工作空间的默认智能体；未配置时回退第一个智能体，
  // 保证切换过来后直接发消息 / 新建对话都使用该工作空间的智能体。
  const targetAgentId = envDefaultAgentId(env.id)
  const targetAgent = config.agents.find(agent => agent.id === targetAgentId)
  if (targetAgent) {
    currentAgentId.value = targetAgent.id
    if (targetAgent.defaultProvider && targetAgent.defaultModel) {
      config.defaultProvider = targetAgent.defaultProvider
      config.defaultModel = targetAgent.defaultModel
    } else {
      const first = config.providers.find(p => p.enabled !== false && (p.models || []).length)
      config.defaultProvider = first?.name || ''
      config.defaultModel = first?.models?.[0]?.id || ''
    }
  }
  void initializeWorkspace(env.id)
}

function chatSelectAgent(agent) {
  if (!agent) return
  currentAgentId.value = agent.id
  selectedSkill.value = null
  // 切换智能体即同步为当前工作空间的默认智能体（随配置持久化）
  const env = config.environments.find(item => item.id === config.activeEnvId)
  if (env && env.defaultAgentId !== agent.id) {
    env.defaultAgentId = agent.id
  }
  if (agent.defaultProvider && agent.defaultModel) {
    config.defaultProvider = agent.defaultProvider
    config.defaultModel = agent.defaultModel
  } else {
    const first = config.providers.find(p => p.enabled !== false && (p.models || []).length)
    config.defaultProvider = first?.name || ''
    config.defaultModel = first?.models?.[0]?.id || ''
  }
  void syncSessionModel(config.defaultProvider, config.defaultModel)
  persist()
}

function chatNewEnvironment() {
  openWsEditor(null)
}

function chatOpenAgentConfig() {
  activePage.value = 'agents'
}

// 对话详情切换思考程度时，自动保存为当前模型默认思考程序；防抖合并
// 快速连续切换，避免并发 SaveConfig 乱序（最后一次选择为准）。
let thinkingDefaultSaveTimer = null
function onThinkingLevelChange(level) {
  thinkingLevel.value = level
  if (thinkingDefaultSaveTimer) clearTimeout(thinkingDefaultSaveTimer)
  thinkingDefaultSaveTimer = setTimeout(() => { void saveThinkingAsDefault() }, 300)
}

async function saveThinkingAsDefault() {
  // 与 selectedModelValue 同源解析：有会话用会话落库模型，无会话用智能体默认模型。
  const value = selectedModelValue.value
  const index = value.indexOf('/')
  if (index < 0) return
  const providerName = value.slice(0, index)
  const modelId = value.slice(index + 1)
  const provider = config.providers.find(p => p.name === providerName)
  const model = provider?.models.find(m => m.id === modelId)
  if (!model || model.defaultThinkingLevel === thinkingLevel.value) return
  model.defaultThinkingLevel = thinkingLevel.value
  // silent：避免每次切换弹「配置已保存」；失败时 persist 内部已有错误 toast。
  await persist({ silent: true })
}

function onModelChange(value) {
  if (modelOptions.value.find(option => option.value === value)?.disabled) return
  const index = value.indexOf('/')
  if (index < 0) return
  config.defaultProvider = value.slice(0, index)
  config.defaultModel = value.slice(index + 1)
  if (selectedAgent.value) {
    selectedAgent.value.defaultProvider = config.defaultProvider
    selectedAgent.value.defaultModel = config.defaultModel
  }
  void syncSessionModel(config.defaultProvider, config.defaultModel)
  persist()
}

// 切换模型后同步当前会话的落库模型：历史会话续写（runPrompt 以 task.model
// 优先）与重新进入回显都用新模型，而不是停留在创建会话时的默认模型。
async function syncSessionModel(provider, model) {
  const task = currentTask()
  if (!task || !provider || !model) return true
  try {
    await updateSessionModel(task.id, provider, model)
    const index = tasks.value.findIndex(item => item.id === task.id)
    if (index >= 0) tasks.value[index] = { ...task, provider, model }
    return true
  } catch (err) {
    pushToast('error', localizeError(String(err)))
    return false
  }
}

function onRemoveImage(index) {
  if (index >= 0 && index < promptImages.value.length) promptImages.value.splice(index, 1)
}

function onAddImages(images) {
  if (!images || !images.length) return
  for (const image of images) promptImages.value.push(image)
  if (promptImages.value.length > 10) promptImages.value = promptImages.value.slice(-10)
}

async function persist(options = {}) {
  saving.value = true
  saved.value = false
  const sshSaveState = captureSshSaveState()
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
    result.sshConfigs = reconcileSshSaveResult(result.sshConfigs, sshSaveState)
    Object.assign(config, result)
    saved.value = true
    setTimeout(() => { saved.value = false }, 1800)
    if (!options.silent) pushToast('success', t.value.toastConfigSaved)
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
  const oldName = target.name
  Object.assign(target, copy)
  if (oldName !== target.name) migrateProviderReferences(oldName, target.name)
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

// 服务商改名后迁移所有引用旧标识的地方（智能体默认模型、会话落库模型、
// 模型页选中项），否则对话详情的选中模型仍显示旧前缀，且无法匹配新选项。
function migrateProviderReferences(oldName, newName) {
  if (!oldName || oldName === newName) return
  for (const agent of config.agents || []) {
    if (agent.defaultProvider === oldName) agent.defaultProvider = newName
  }
  for (const task of tasks.value) {
    if (task.provider !== oldName || !task.model) continue
    task.provider = newName
    void updateSessionModel(task.id, newName, task.model).catch(err => pushToast('error', localizeError(String(err))))
  }
  if (selectedModelsProviderName.value === oldName) selectedModelsProviderName.value = newName
}

// 兜底：服务商曾改名/删除时，会话落库的 provider 可能已不存在于配置。
// 模型 id 能唯一命中某个服务商时，把会话引用修正到当前标识，避免对话详情
// 选中模型显示旧前缀；无法唯一判定时保持原样，由用户手动切换。
function reconcileStaleSessionProviders(list) {
  for (const task of list) {
    if (!task.provider || !task.model || config.providers.some(p => p.name === task.provider)) continue
    const matches = config.providers.filter(p => (p.models || []).some(m => m.id === task.model))
    if (matches.length === 1) task.provider = matches[0].name
  }
}

// 服务商改名/删除后旧标识可能残留在引用处；按「配置存在 → 模型 id 唯一
// 命中 → 全局默认/首个启用服务商」解析到当前有效标识，空串表示无可用。
function resolveProviderName(providerName, modelId) {
  if (providerName && config.providers.some(p => p.name === providerName)) return providerName
  if (modelId) {
    const matches = config.providers.filter(p => p.enabled !== false && (p.models || []).some(m => m.id === modelId))
    if (matches.length === 1) return matches[0].name
  }
  if (config.providers.some(p => p.name === config.defaultProvider && (p.models || []).length)) return config.defaultProvider
  return config.providers.find(p => p.enabled !== false && (p.models || []).length)?.name || ''
}

// 兜底：服务商改名/删除后，智能体默认模型可能仍指向旧标识（早期改名流程
// 未迁移引用，属存量脏数据）。启动加载时把不存在的 provider 引用修正到
// 当前配置，避免新建对话仍按旧前缀创建会话。仅内存修正，随下次持久化落盘。
function reconcileStaleAgentProviders() {
  for (const agent of config.agents || []) {
    if (!agent.defaultProvider || config.providers.some(p => p.name === agent.defaultProvider)) continue
    const providerName = resolveProviderName(agent.defaultProvider, agent.defaultModel)
    const provider = config.providers.find(p => p.name === providerName)
    if (!provider) continue
    agent.defaultProvider = providerName
    if (!provider.models.some(m => m.id === agent.defaultModel)) {
      agent.defaultModel = provider.models[0]?.id || ''
    }
  }
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
  migrateProviderReferences(previousName, nextName)
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
function removeProvider(index) {
  config.providers.splice(index, 1)
}

const testingModel = ref('') // 兼容旧逻辑：非空表示有模型正在测试
const testingModels = reactive({}) // 按模型独立记录，避免一个卡住全部禁用
const testResult = reactive({})
function testModelKey(provider, model) { return `${provider.name}/${model.id}` }
async function runModelTest(provider, model, agentId = config.activeAgentId) {
  const key = testModelKey(provider, model)
  if (testingModels[key]) return
  testingModels[key] = true
  testingModel.value = key
  delete testResult[key]
  try {
    const result = await testModel({ provider: provider.name, model: model.id, agentId })
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
  refreshChatgptAgentAuth,
  setChatgptAgentAuth,
  selectedAgent,
  activeAgentId,
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
  agentConfigInitialTab,
  openAgentConfig,
  backToAgentList,
  providerEditorOpen,
  providerDraft,
  editingNewProvider,
  pendingDeleteSsh,
  sshBusy,
  sshTestStates,
  pendingDeleteDb,
  dbBusy,
  dbDraft,
  editingNewDb,
  newDbId,
  dbEditorOpen,
  dbTestStates,
  dbAuditRows,
  dbAuditLoading,
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
  openMemoryConfig,
  openPlanConfig,
  openGlobalPromptConfig,
  figmaAuthorizationsDraft,
  figmaActiveAuthorizationIdDraft,
  installPiNow,
  createAgent,
  openAgentEditor,
  requestDeleteAgent,
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
  assignAgentExtension,
  installGlobalPackage,
  removeGlobalPackage,
  installAgentMcp,
  addManualMCP,
  removeAgentMcpServer,
  installAgentExtension,
  uninstallAgentExtension,
  openWsEditor,
  openSshEditor,
  requestDeleteWs,
  startWorkspaceDrag,
  handleWorkspaceDragOver,
  dropWorkspace,
  endWorkspaceDrag,
  workspaceDragOverId: dragOverWorkspaceId,
  workspaceRemotes,
  remoteSsh,
  requestDeleteSsh,
  testSsh,
  pickSshKeyFile,
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
  confirmDeleteDb,
  openDbEditor,
  closeDbEditor,
  persistDbChange,
  saveNewDb,
  requestDeleteDb,
  testDb,
  loadDbAudit,
  toggleWorkspaceDb,
  workspaceDbConnections,
  confirmDeleteWs,
  closeSshEditor,
  persistSshChange,
  saveNewSsh,
  closeWsEditor,
  persistWsChange,
  handleWsPathChange,
  pickWorkspacePath,
  handleWorkspaceSshChange,
  addWorkspaceSsh,
  removeWorkspaceSsh,
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
  offShuttingDown = onEvent('app:shutting-down', () => {
    shuttingDown.value = true
  })
  offAttachmentDrop = onEvent('attachments:dropped', payload => {
    const files = Array.isArray(payload?.files) ? payload.files : []
    if (files.length) void onAddAttachments(files)
  })
  offCleanupEvent = onEvent('session-cleanup:done', applySessionCleanupResult)
  offGitWorkspace = onEvent('git:workspace', applyGitWorkspaceUpdate)
  await load()
  startProviderQuotaRefresh()
  // 启动异步清理会话数据的结果通常在 window 渲染后不久就绪，主动拉取一次并展示。
  void fetchSessionCleanupNotice()
  void refreshStewardConnected()
  void refreshResidentSessionId()
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
    if (
      state?.running === false
      && targetTaskId !== ''
      && String(targetTaskId) === String(activeTaskId.value)
    ) void refreshGitAvailability()
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
  offStewardStatus = onEvent('steward:status', () => {
    void refreshStewardConnected()
    void refreshResidentSessionId()
  })
  offStewardPermission = onEvent('steward:permission', handleStewardPermissionUpdate)
  offMaximised = onEvent('window:maximised', () => { isMaximised.value = true })
  offUnmaximised = onEvent('window:unmaximised', () => { isMaximised.value = false })
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
  offShuttingDown?.()
  offCleanupEvent?.()
  offGitWorkspace?.()
  offExtensionsChanged?.()
  offStewardStatus?.()
  offStewardPermission?.()
  offMaximised?.()
  offUnmaximised?.()
  document.removeEventListener('click', closeArchivePop)
})
</script>

<template>
  <div class="app-shell" :class="{ 'window--maximised': isMaximised }">
    <AppTitleBar :running="running" :running-label="t.statusRunning" />

    <div class="workspace-shell">
      <aside class="sidebar" :class="{ 'sidebar--closed': !sidebarOpen, 'sidebar--resizing': sidebarResizing }" :style="sidebarOpen ? { width: sidebarWidth + 'px', flexBasis: sidebarWidth + 'px' } : null">
        <div v-if="sidebarOpen" class="sidebar-resizer" @pointerdown="startSidebarResize"></div>
        <nav class="primary-nav">
          <button v-for="item in nav" :key="item.id" :class="{ active: activePage === item.id }" @click="openPrimaryNav(item)">
            <component :is="item.icon" :size="14" />
            <span v-if="sidebarOpen" class="primary-nav__label">{{ item.label }}</span>
            <span
              v-if="sidebarOpen && item.id === 'models' && currentProviderQuotaText"
              class="primary-nav__quota"
              :title="`${t.providerQuotaRemainingTitle}: ${currentProviderQuotaText}`"
            >{{ currentProviderQuotaText }}</span>
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
              <div
                v-for="group in sessionGroups"
                :key="group.id || 'orphan'"
                class="sidebar-group"
                :class="{ 'sidebar-group--dragging': group.id === draggedWorkspaceId, 'sidebar-group--drag-over': group.id === dragOverWorkspaceId }"
                @dragover.prevent="handleWorkspaceDragOver(group)"
                @drop.prevent="dropWorkspace(group)"
              >
                <div
                  class="sidebar-group__head"
                  :class="{ active: group.id && group.id === config.activeEnvId }"
                >
                  <button
                    class="sidebar-group__title"
                    :draggable="!!group.id"
                    :title="group.id ? (collapsedWorkspaceIds.has(group.id) ? t.expand : t.collapse) : ''"
                    @click="group.id && toggleWorkspaceCollapse(group.id)"
                    @dragstart="startWorkspaceDrag(group, $event)"
                    @dragend="endWorkspaceDrag"
                  >
                    <Folder :size="14" />
                    <span :title="group.env?.path || group.name">{{ group.name }}</span>
                  </button>
                  <div class="sidebar-group__actions">
                    <span v-if="group.all.length > 0" class="sidebar-group__count">{{ group.all.length }}</span>
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
                    <span v-if="taskHasSidebarDot(task.id)" class="sidebar-dot sidebar-dot--session"></span>
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
          <button
            class="sidebar__steward-btn"
            :class="{ active: activePage === 'steward', 'is-connected': stewardConnected }"
            :title="t.stewardMenu || '管家'"
            @click="activePage = 'steward'"
          >
            <Smartphone :size="17" />
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
        <div v-if="sessionCleanupNotice" class="session-cleanup-notice" :class="sessionCleanupNotice.type" role="status">
          <span>{{ sessionCleanupNotice.text }}</span>
          <button :aria-label="t.close" @click="sessionCleanupNotice = null"><X :size="13" /></button>
        </div>
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
            :current-work-dir="currentWorkDir"
            :workspace-revision="runtimeWorkspaceRevision"
            :workspace-activating="workspaceActivating"
            :selected-agent="selectedAgent"
            :dcg-status="(extensionSnapshot?.recommended?.[selectedAgent?.id] || []).find(tool => tool.key === 'dcg') || null"
            :session-dcg-disabled="sessionDcgDisabled"
            :tasks="tasks"
            :draft="draft"
            :pending-prompts="pendingPrompts"
            :mode="mode"
            :model-options="modelOptions"
            :selected-model-value="selectedModelValue"
            :selected-model-unavailable="selectedModelUnavailable"
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
            :git-availability="gitAvailability"
            :document-preview-request="documentPreviewRequest"
            :document-artifact-focus="documentArtifactFocus"
            :plan-items="planItems"
            :execution-plan="executionPlan"
            :extension-dialog="extensionDialog"
            :subagent-dialogs="subagentDialogs"
            :compaction="compaction"
            :error="error"
            @update:draft="draft = $event"
            @send="sendPrompt"
            @edit-pending="editPendingPrompt"
            @delete-pending="deletePendingPrompt"
            @stop="stop"
            @select-agent="chatSelectAgent"
            @open-agent-config="chatOpenAgentConfig"
            @open-plugins="activePage = 'plugins'"
            @open-settings="openSettingsToConcise"
            @open-agent-extensions="openAgentConfig($event, 'extensions')"
            @update:dcg="onChatDcgPolicyChange($event)"
            @update:mode="mode = $event"
            @update:model="onModelChange"
            @add-images="onAddImages"
            @remove-image="onRemoveImage"
            @add-attachments="onAddAttachments"
            @remove-attachment="onRemoveAttachment"
            @update:thinking="onThinkingLevelChange"
            @update:skill="selectedSkill = $event"
            @update-thinking-open="updateThinkingOpen"
            @compact="compactContext"
            @respond-extension="respondExtensionDialog"
            @ack-extension="ackExtensionDialog"
            @respond-subagent-dialog="respondSubagentDialog"
            @ack-subagent-dialog="ackSubagentDialog"
            @refresh-session-changes="refreshSessionChanges"
            @open-git="openGitManager"
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
    <MemoryConfigDialog v-model="showMemoryConfig" />
    <PlanConfigDialog v-model="showPlanConfig" />
    <GlobalPromptConfigDialog v-model="showGlobalPromptConfig" />
    <GitManagementDialog
      :open="gitDialogOpen"
      :session-id="Number(activeTaskId) || 0"
      :language="config.preferences.language || 'zh-CN'"
      :agent-running="activeTaskRunning"
      :workspace="currentWorkDir"
      :workspace-revision="gitWorkspaceRevision"
      :model-options="modelOptions"
      :selected-model-value="selectedModelValue"
      :t="t"
      @close="gitDialogOpen = false"
      @updated="updateGitAvailability"
      @resolve-conflicts="requestAgentConflictResolution"
      @add-to-chat="addFileToChat"
      @add-selection-to-chat="addTextToChat"
    />

    <div v-if="shuttingDown" class="shutdown-overlay" role="status" aria-live="polite">
      <div class="shutdown-overlay__card">
        <LoaderCircle :size="30" class="spin shutdown-overlay__spinner" aria-hidden="true" />
        <p class="shutdown-overlay__text">正在关闭中…</p>
      </div>
    </div>
  </div>
</template>
