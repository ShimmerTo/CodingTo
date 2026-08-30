<script setup>
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import IconPlus from '../icons/IconPlus.vue'
import IconPlay from '../icons/IconPlay.vue'
import IconImage from '../icons/IconImage.vue'
import IconBrain from '../icons/IconBrain.vue'
import IconWrench from '../icons/IconWrench.vue'
import { LoaderCircle, Settings, Trash2 } from 'lucide-vue-next'
import { useAppContext } from '../../composables/appContext'
import ConfirmDialog from '../ConfirmDialog.vue'
import {
  applyCodexProvider,
  chatgptAccount,
  chatgptLoginCancel,
  chatgptLoginStart,
  chatgptLoginStatus,
  chatgptLogout,
  chatgptUsage,
  getProviderBalance,
  getProviderUsage,
  queryModelUsageStats,
  getModelUsageSessionRequests,
  getModelUsageRequestDetail,
  openExternal
} from '../../backend'
import { formatCacheHitRate, formatTokenCount } from '../chat/chatFormatters'
import {
  clearProviderQuotaCache,
  fetchProviderQuota,
  getProviderQuotaCache,
  normalizeChatGPTUsage,
  normalizeOpenCodeUsage
} from '../../utils/providerQuota'

const {
  t,
  config,
  selectedModelsProvider: selectedProvider,
  selectModelsProvider,
  openProviderEditor,
  openProviderEdit,
  requestDeleteProvider,
  saving,
  persist,
  apiKeyVisibilityLabel,
  showProviderApiKey,
  piCompatBooleanFields,
  formatCompat,
  updateCompatJson,
  piThinkingLevels,
  testingModels,
  testModelKey,
  runModelTest,
  testResult,
  modelRequestRoute,
  toggleImageInput,
  pushToast,
  setChatgptAgentAuth
} = useAppContext()

const providers = computed(() => config.providers || [])
const CHATGPT_PROVIDER_ID = 'openai-codex'
const initialChatgptQuota = getProviderQuotaCache('chatgpt', CHATGPT_PROVIDER_ID)?.data ?? null
const chatgptAccountInfo = ref({ loggedIn: Boolean(initialChatgptQuota) })

function isChatgptProvider(provider) {
  return provider?.name === CHATGPT_PROVIDER_ID
    || provider?.vendor === CHATGPT_PROVIDER_ID
    || (provider?.models || []).some(model => model.api === 'openai-codex-responses')
}

const configuredChatgptProvider = computed(() => providers.value.find(isChatgptProvider) || null)
const chatgptSidebarProvider = computed(() => configuredChatgptProvider.value
  ? { ...configuredChatgptProvider.value, label: 'ChatGPT' }
  : {
      name: CHATGPT_PROVIDER_ID,
      label: 'ChatGPT',
      vendor: CHATGPT_PROVIDER_ID,
      baseUrl: 'https://chatgpt.com/backend-api',
      enabled: true,
      models: []
    })
const sidebarProviders = computed(() => [
  chatgptSidebarProvider.value,
  ...providers.value.filter(provider => !isChatgptProvider(provider))
])
const chatgptSelected = ref(isChatgptProvider(selectedProvider.value))
const pageSelectedProvider = computed(() => chatgptSelected.value ? chatgptSidebarProvider.value : selectedProvider.value)

function selectSidebarProvider(provider) {
  if (isChatgptProvider(provider)) {
    chatgptSelected.value = true
    if (configuredChatgptProvider.value) selectModelsProvider(configuredChatgptProvider.value)
    refreshChatgptAccount()
    return
  }
  chatgptSelected.value = false
  selectModelsProvider(provider)
}

async function ensureChatgptProvider() {
  if (configuredChatgptProvider.value) return
  try {
    const result = await applyCodexProvider()
    Object.assign(config, result)
    if (chatgptSelected.value && configuredChatgptProvider.value) {
      selectModelsProvider(configuredChatgptProvider.value)
    }
  } catch (err) {
    pushToast('error', String(err))
  }
}

// ---- OpenCode Go / ChatGPT 用量与 DeepSeek 余额轮询 ----
// 仅在模型页可见时每 1 分钟拉取一次，离开页面即停止。
const USAGE_POLL_MS = 60 * 1000
const usageByProvider = reactive({})
const balanceByProvider = reactive({})
const chatgptQuota = ref(initialChatgptQuota)
const quotaNow = ref(Date.now())
let usageTimer = null
let usageFetching = false
let chatgptUsageFetching = false

function isZenGoProvider(p) {
  return /opencode\.ai\/zen\/go/i.test(p?.baseUrl || '')
}

function isDeepSeekProvider(provider) {
  return /^https:\/\/api\.deepseek\.com(?:[/:]|$)/i.test((provider?.baseUrl || '').trim())
}

function hydrateUsageCache() {
  const chatgpt = getProviderQuotaCache('chatgpt', CHATGPT_PROVIDER_ID)?.data
  if (chatgpt?.weekly) chatgptQuota.value = chatgpt

  for (const provider of providers.value) {
    if (isZenGoProvider(provider)) {
      const usage = getProviderQuotaCache('opencode', provider.name)?.data
      if (usage?.rolling && usage?.weekly && usage?.monthly) usageByProvider[provider.name] = usage
    } else if (isDeepSeekProvider(provider)) {
      const balance = getProviderQuotaCache('deepseek', provider.name)?.data
      if (balance?.balances?.length) balanceByProvider[provider.name] = balance
    }
  }
}

watch(
  () => providers.value.map(provider => `${provider.name}\u0000${provider.baseUrl || ''}`).join('\u0001'),
  hydrateUsageCache,
  { immediate: true }
)

async function refreshUsage({ force = false } = {}) {
  if (usageFetching) return
  const usageTargets = providers.value.filter(isZenGoProvider)
  const balanceTargets = providers.value.filter(isDeepSeekProvider)
  usageFetching = true
  try {
    await Promise.all([
      ...usageTargets.map(async provider => {
        const usage = await fetchProviderQuota({
          kind: 'opencode',
          providerName: provider.name,
          force,
          fetcher: async () => {
            const normalized = normalizeOpenCodeUsage(await getProviderUsage(provider.name))
            return normalized?.rolling && normalized?.weekly && normalized?.monthly
              ? { kind: 'opencode', rolling: normalized.rolling, weekly: normalized.weekly, monthly: normalized.monthly }
              : null
          }
        })
        if (usage?.rolling && usage?.weekly && usage?.monthly) usageByProvider[provider.name] = usage
      }),
      ...balanceTargets.map(async provider => {
        const balance = await fetchProviderQuota({
          kind: 'deepseek',
          providerName: provider.name,
          force,
          fetcher: async () => {
            const result = await getProviderBalance(provider.name)
            return result?.available && result?.balances?.length ? { ...result, kind: 'deepseek' } : null
          }
        })
        if (balance?.balances?.length) balanceByProvider[provider.name] = balance
      })
    ])
  } finally {
    usageFetching = false
  }
}

async function refreshChatgptUsage({ force = false } = {}) {
  if (chatgptUsageFetching || chatgptAccountInfo.value.loggedIn !== true) return
  chatgptUsageFetching = true
  try {
    const usage = await fetchProviderQuota({
      kind: 'chatgpt',
      providerName: CHATGPT_PROVIDER_ID,
      force,
      fetcher: async () => {
        const normalized = normalizeChatGPTUsage(await chatgptUsage())
        return normalized ? { kind: 'chatgpt', ...normalized } : null
      }
    })
    if (usage?.weekly) chatgptQuota.value = usage
    // Pi may refresh the canonical access token while querying usage.
    const account = await chatgptAccount()
    chatgptAccountInfo.value = account
    setChatgptAgentAuth(account)
  } catch (_) {
    // 失败保留上次成功数据；认证状态由独立账户查询维护。
  } finally {
    chatgptUsageFetching = false
  }
}

function quotaLevel(percent) {
  const raw = Number(percent)
  if (!Number.isFinite(raw)) return ''
  const value = Math.round(raw)
  if (value >= 50) return 'healthy'
  if (value >= 20) return 'warning'
  return 'critical'
}

function quotaPercent(window) {
  if (!window) return ''
  const percent = Math.max(0, Math.min(100, Math.round(Number(window.percent) || 0)))
  return `${percent}%${percent === 0 ? '!' : ''}`
}

function balanceLevel(amount) {
  const raw = Number(amount)
  if (!Number.isFinite(raw)) return ''
  const value = Math.round(raw * 100) / 100
  if (value >= 10) return 'healthy'
  if (value > 1) return 'warning'
  return 'critical'
}

function formatBalanceAmount(amount) {
  const value = Number(amount)
  if (!Number.isFinite(value)) return '-'
  const rounded = Math.round(value * 100) / 100
  const formatted = rounded.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })
  return `${formatted}${rounded === 0 ? '!' : ''}`
}

function formatResetSeconds(seconds) {
  const s = Number(seconds)
  if (!s || s <= 0) return ''
  const d = Math.floor(s / 86400)
  const h = Math.floor((s % 86400) / 3600)
  const m = Math.floor((s % 3600) / 60)
  if (d >= 1) return `${d}d ${h}h`
  if (h >= 1) return `${h}h ${m}m`
  return `${m}m`
}

function isChatgptCredentialExpired() {
  const expires = Number(chatgptAccountInfo.value.expires)
  return expires > 0 && expires <= quotaNow.value
}

onMounted(() => {
  ensureChatgptProvider()
  refreshUsage()
  refreshChatgptAccount()
  usageTimer = setInterval(() => {
    quotaNow.value = Date.now()
    refreshUsage()
    refreshChatgptUsage()
  }, USAGE_POLL_MS)
})

onUnmounted(() => {
  if (usageTimer) clearInterval(usageTimer)
  usageTimer = null
  stopChatgptPolling()
})

// ---- ChatGPT (Codex 订阅) 登录 ----
// 页面内两个 Tab：服务商与模型 / Token 统计。ChatGPT 订阅已合并进固定服务商。
const activeModelsTab = ref('providers')

// ---- Token 用量统计（最近 60 天，按模型 / 会话 / 请求） ----
const usageReferenceDay = new Date()
usageReferenceDay.setHours(12, 0, 0, 0)

function usageDayOffset(offset) {
  const value = new Date(usageReferenceDay)
  value.setDate(value.getDate() + offset)
  const pad = item => String(item).padStart(2, '0')
  return `${value.getFullYear()}-${pad(value.getMonth() + 1)}-${pad(value.getDate())}`
}

const usageToday = usageDayOffset(0)
const usageEarliestDay = usageDayOffset(-(60 - 1))
const modelUsage = ref(null)
const modelUsageLoading = ref(false)
const modelUsageError = ref('')
const usageDimension = ref('model')
const usageStartDay = ref(usageToday)
const usageEndDay = ref(usageToday)
const modelUsageMoreLoading = ref(false)
const modelUsagePage = ref(1)
const modelUsagePageCursors = ref([''])
let modelUsageLoadVersion = 0

// 指定自然日 + 会话的逐请求明细弹窗状态。
const usageSessionDetail = ref(null)
const usageSessionRequests = ref([])
const usageSessionLoading = ref(false)
const usageSessionError = ref('')
const usageSessionHasMore = ref(false)
const usageSessionCursor = ref('')
const usageSessionPage = ref(1)
const usageSessionPageCursors = ref([''])
let usageSessionLoadVersion = 0

// 单次请求完整参数与结果弹窗状态。
const usageRequestSource = ref(null)
const usageRequestDetail = ref(null)
const usageRequestLoading = ref(false)
const usageRequestError = ref('')

async function loadModelUsage(page = 1, cursor = '') {
  if (page > 1 && modelUsageMoreLoading.value) return
  const version = ++modelUsageLoadVersion
  const dimension = usageDimension.value
  const startDay = usageStartDay.value
  const endDay = usageEndDay.value
  if (page === 1) {
    modelUsagePage.value = 1
    modelUsagePageCursors.value = ['']
  }
  if (page > 1) modelUsageMoreLoading.value = true
  else modelUsageLoading.value = true
  modelUsageError.value = ''
  try {
    const result = await queryModelUsageStats({ dimension, startDay, endDay, cursor, pageSize: 20 })
    if (version === modelUsageLoadVersion) {
      modelUsage.value = result
      modelUsagePage.value = page
      const cursors = page === 1 ? [''] : modelUsagePageCursors.value.slice(0, page)
      cursors[page - 1] = cursor
      if (result.hasMore && result.nextCursor) cursors[page] = result.nextCursor
      modelUsagePageCursors.value = cursors
    }
  } catch (err) {
    if (version === modelUsageLoadVersion) modelUsageError.value = String(err)
  } finally {
    if (version === modelUsageLoadVersion) {
      modelUsageLoading.value = false
      modelUsageMoreLoading.value = false
    }
  }
}

async function loadNextModelUsagePage() {
  if (!modelUsage.value?.hasMore || !modelUsage.value?.nextCursor) return
  await loadModelUsage(modelUsagePage.value + 1, modelUsage.value.nextCursor)
}

async function loadPreviousModelUsagePage() {
  if (modelUsagePage.value <= 1) return
  const page = modelUsagePage.value - 1
  await loadModelUsage(page, modelUsagePageCursors.value[page - 1] || '')
}

async function selectUsageDimension(value) {
  if (usageDimension.value === value) return
  usageDimension.value = value
  await loadModelUsage()
}

function usagePresetRange(preset) {
  if (preset === 'yesterday') {
    const yesterday = usageDayOffset(-1)
    return { startDay: yesterday, endDay: yesterday }
  }
  const days = preset === 'last7' ? 7 : 30
  return { startDay: usageDayOffset(-(days - 1)), endDay: usageToday }
}

function isUsagePresetActive(preset) {
  const range = usagePresetRange(preset)
  return usageStartDay.value === range.startDay && usageEndDay.value === range.endDay
}

async function selectUsagePreset(preset) {
  const range = usagePresetRange(preset)
  if (usageStartDay.value === range.startDay && usageEndDay.value === range.endDay) return
  usageStartDay.value = range.startDay
  usageEndDay.value = range.endDay
  await loadModelUsage()
}

async function selectUsageStartDay(value) {
  if (!value || value === usageStartDay.value) return
  usageStartDay.value = value
  if (usageEndDay.value < value) usageEndDay.value = value
  await loadModelUsage()
}

async function selectUsageEndDay(value) {
  if (!value || value === usageEndDay.value) return
  usageEndDay.value = value
  if (usageStartDay.value > value) usageStartDay.value = value
  await loadModelUsage()
}

async function openUsageSessionDetail(item) {
  const sessionId = item?.sessionId ?? (item?.modelTest ? 0 : null)
  if (!item?.day || sessionId == null) return
  usageSessionDetail.value = { ...item, sessionId }
  usageSessionRequests.value = []
  usageSessionError.value = ''
  usageSessionHasMore.value = false
  usageSessionCursor.value = ''
  usageSessionPage.value = 1
  usageSessionPageCursors.value = ['']
  const version = ++usageSessionLoadVersion
  await loadUsageSessionRequests(version, 1, '')
}

async function loadUsageSessionRequests(version = usageSessionLoadVersion, page = 1, cursor = '') {
  const detail = usageSessionDetail.value
  if (!detail || usageSessionLoading.value) return
  usageSessionError.value = ''
  usageSessionLoading.value = true
  try {
    const pageResult = await getModelUsageSessionRequests({
      day: detail.day,
      sessionId: detail.sessionId,
      cursor,
      pageSize: 20
    })
    if (version !== usageSessionLoadVersion) return
    usageSessionRequests.value = pageResult.items || []
    usageSessionHasMore.value = pageResult.hasMore === true
    usageSessionCursor.value = pageResult.nextCursor || ''
    usageSessionPage.value = page
    const cursors = page === 1 ? [''] : usageSessionPageCursors.value.slice(0, page)
    cursors[page - 1] = cursor
    if (pageResult.hasMore && pageResult.nextCursor) cursors[page] = pageResult.nextCursor
    usageSessionPageCursors.value = cursors
  } catch (err) {
    if (version === usageSessionLoadVersion) usageSessionError.value = String(err)
  } finally {
    if (version === usageSessionLoadVersion) usageSessionLoading.value = false
  }
}

async function loadNextUsageSessionPage() {
  if (!usageSessionHasMore.value || !usageSessionCursor.value) return
  await loadUsageSessionRequests(usageSessionLoadVersion, usageSessionPage.value + 1, usageSessionCursor.value)
}

async function loadPreviousUsageSessionPage() {
  if (usageSessionPage.value <= 1) return
  const page = usageSessionPage.value - 1
  await loadUsageSessionRequests(usageSessionLoadVersion, page, usageSessionPageCursors.value[page - 1] || '')
}

function closeUsageSessionDetail() {
  usageSessionLoadVersion += 1
  usageSessionDetail.value = null
  usageSessionRequests.value = []
  usageSessionError.value = ''
  usageSessionHasMore.value = false
  usageSessionCursor.value = ''
  usageSessionPage.value = 1
  usageSessionPageCursors.value = ['']
  usageSessionLoading.value = false
}

async function openUsageRequestDetail(item) {
  if (usageRequestLoading.value || !item?.day || !item?.requestId) return
  usageRequestSource.value = item
  usageRequestDetail.value = null
  usageRequestError.value = ''
  usageRequestLoading.value = true
  try {
    usageRequestDetail.value = await getModelUsageRequestDetail(item.day, item.sessionId || 0, item.requestId)
  } catch (err) {
    usageRequestError.value = String(err)
  } finally {
    usageRequestLoading.value = false
  }
}

function closeUsageRequestDetail() {
  usageRequestSource.value = null
  usageRequestDetail.value = null
  usageRequestError.value = ''
}

function formatUsageTime(value) {
  if (!value) return '-'
  return new Date(value).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

const showRecordApiDetailsConfirm = ref(false)

async function setRecordApiDetails(event) {
  const enabled = Boolean(event.target.checked)
  if (enabled) {
    event.target.checked = false
    showRecordApiDetailsConfirm.value = true
    return
  }
  await applyRecordApiDetails(false)
}

async function applyRecordApiDetails(enabled) {
  const previous = config.recordApiDetails === true
  config.recordApiDetails = enabled
  try {
    const saved = await persist()
    if (!saved) {
      config.recordApiDetails = previous
    }
  } catch (err) {
    config.recordApiDetails = previous
    pushToast('error', String(err))
  }
}

async function confirmRecordApiDetails() {
  showRecordApiDetailsConfirm.value = false
  await applyRecordApiDetails(true)
}

// 仅当切到「Token 统计」页卡时才拉取，避免打开模型页就全量扫描会话日志。
watch(() => activeModelsTab.value, v => {
  if (v === 'stats') loadModelUsage()
})
const chatgptLoginState = ref(null)
let chatgptPollTimer = null

function formatLocalDate(ms) {
  if (!ms) return ''
  const d = new Date(ms)
  const pad = v => String(v).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

async function refreshChatgptAccount() {
  try {
    const account = await chatgptAccount()
    setChatgptAgentAuth(account)
    chatgptAccountInfo.value = account
    if (account.loggedIn) refreshChatgptUsage()
    else {
      chatgptQuota.value = null
      clearProviderQuotaCache('chatgpt', CHATGPT_PROVIDER_ID)
    }
  } catch {
    // 账户接口瞬时失败时保留已确认过的登录态和额度，避免缓存内容闪空。
    if (chatgptQuota.value?.weekly) return
    const account = { agentId: 'default', loggedIn: false }
    setChatgptAgentAuth(account)
    chatgptAccountInfo.value = account
    chatgptQuota.value = null
  }
}

async function startChatgptLogin() {
  if (chatgptLoginState.value) return
  let state
  try {
    state = await chatgptLoginStart()
  } catch (err) {
    pushToast('error', String(err))
    return
  }
  if (state.status !== 'pending' || !state.authUrl) {
    pushToast('error', state.error || t.value.chatgpt_login_failed)
    return
  }
  chatgptLoginState.value = state
  openExternal(state.authUrl)
  chatgptPollTimer = setInterval(pollChatgptLogin, 2000)
}

async function pollChatgptLogin() {
  let state
  try {
    state = await chatgptLoginStatus()
  } catch {
    return
  }
  if (state.status === 'pending') return
  stopChatgptPolling()
  chatgptLoginState.value = null
  if (state.status === 'completed') {
    clearProviderQuotaCache('chatgpt', CHATGPT_PROVIDER_ID)
    try {
      const result = await applyCodexProvider()
      Object.assign(config, result)
      pushToast('success', t.value.chatgpt_login_success)
    } catch (err) {
      pushToast('error', String(err))
    }
    await refreshChatgptAccount()
    return
  }
  pushToast('error', state.error || t.value.chatgpt_login_failed)
  await refreshChatgptAccount()
}

function stopChatgptPolling() {
  if (chatgptPollTimer) {
    clearInterval(chatgptPollTimer)
    chatgptPollTimer = null
  }
}

async function cancelChatgptLogin() {
  stopChatgptPolling()
  chatgptLoginState.value = null
  try {
    await chatgptLoginCancel()
  } catch {
    // 后端取消失败时由超时机制兜底。
  }
  await refreshChatgptAccount()
}

async function logoutChatgpt() {
  try {
    await chatgptLogout()
    chatgptAccountInfo.value = { agentId: 'default', loggedIn: false }
    chatgptQuota.value = null
    clearProviderQuotaCache('chatgpt', CHATGPT_PROVIDER_ID)
    setChatgptAgentAuth(chatgptAccountInfo.value)
    pushToast('success', t.value.chatgpt_logout_success)
  } catch (err) {
    pushToast('error', String(err))
  }
}

function toggleApiKeyVisible() {
  showProviderApiKey.value = !showProviderApiKey.value
}

const apiOptions = ['openai-completions', 'openai-responses', 'openai-codex-responses', 'azure-openai-responses', 'anthropic-messages', 'google-generative-ai', 'google-vertex']

const modelEditorOpen = ref(false)
const editingNewModel = ref(false)
const editingModelRef = ref(null)
const modelDraft = ref(null)
const modelDialogError = ref('')

function openAddModel() {
  const provider = selectedProvider.value
  if (!provider) return
  modelDraft.value = {
    id: '',
    name: '',
    api: 'openai-completions',
    baseUrl: '',
    contextWindow: 128000,
    maxTokens: 16384,
    reasoning: true,
    defaultThinkingLevel: 'high',
    input: ['text'],
    capabilities: { toolCall: true },
    compat: {}
  }
  editingNewModel.value = true
  editingModelRef.value = null
  modelDialogError.value = ''
  modelEditorOpen.value = true
}

function openEditModel(model) {
  const draft = JSON.parse(JSON.stringify(model))
  if (!draft.compat) draft.compat = {}
  modelDraft.value = draft
  editingNewModel.value = false
  editingModelRef.value = model
  modelDialogError.value = ''
  modelEditorOpen.value = true
}

function closeModelEditor() {
  modelEditorOpen.value = false
}

async function saveModel() {
  const provider = selectedProvider.value
  const draft = modelDraft.value
  if (!draft) return
  if (!draft.id.trim()) {
    modelDialogError.value = '请填写模型 ID'
    return
  }
  if (!draft.name.trim()) {
    modelDialogError.value = '请填写显示名称'
    return
  }
  const duplicate = provider.models.some(m => m.id === draft.id.trim() && m !== editingModelRef.value)
  if (duplicate) {
    modelDialogError.value = '模型 ID 已存在'
    return
  }
  draft.id = draft.id.trim()
  draft.name = draft.name.trim()
  if (editingNewModel.value) {
    provider.models.push(JSON.parse(JSON.stringify(draft)))
  } else if (editingModelRef.value) {
    Object.assign(editingModelRef.value, JSON.parse(JSON.stringify(draft)))
  }
  await persist()
  modelDialogError.value = ''
  closeModelEditor()
}

function deleteModel(model) {
  const provider = selectedProvider.value
  provider.models = provider.models.filter(m => m !== model)
  persist()
}

async function runModelTestFor(provider, model) {
  const agentId = provider?.name === 'openai-codex' || provider?.vendor === 'openai-codex'
    ? 'default'
    : config.activeAgentId
  await runModelTest(provider, model, agentId)
}

function modelTestResult(provider, model) {
  return testResult[testModelKey(provider, model)] || null
}
</script>

<template>
  <div class="models-page">
    <div class="page-heading models-page__heading">
      <div><h2>{{ t.modelsTitle }}</h2><p>{{ t.modelsIntro }}</p></div>
    </div>
    <div class="models-tabs">
      <button :class="{ active: activeModelsTab === 'providers' }" @click="activeModelsTab = 'providers'">
        {{ t.chatgpt_tab_providers }}
      </button>
      <button :class="{ active: activeModelsTab === 'stats' }" @click="activeModelsTab = 'stats'">
        {{ t.model_usage_title }}
      </button>
    </div>
    <aside v-show="activeModelsTab === 'providers'" class="models-sidebar">
      <div class="models-sidebar__head">
        <span class="models-sidebar__title">{{ t.providers }}</span>
        <button class="pill-btn" @click="openProviderEditor">+ {{ t.addProvider }}</button>
      </div>
      <ul class="models-provider-list">
        <li
          v-for="p in sidebarProviders"
          :key="p.name"
          :class="['models-provider-item', { active: isChatgptProvider(p) ? chatgptSelected : (!chatgptSelected && p.name === selectedProvider?.name) }]"
          @click="selectSidebarProvider(p)"
        >
          <span class="models-provider-item__dot" :class="{ on: isChatgptProvider(p) ? chatgptAccountInfo.loggedIn : p.enabled }"></span>
          <span class="models-provider-item__label">{{ p.label }}</span>
          <span class="models-provider-item__count">{{ (p.models || []).length }}</span>
          <div v-if="isChatgptProvider(p)" class="models-provider-item__usage models-provider-item__usage--chatgpt">
            <template v-if="chatgptAccountInfo.loggedIn">
              <div v-if="chatgptQuota?.rolling" class="models-provider-item__usage-row">
                <span class="models-provider-item__usage-label">5h</span>
                <span :class="['models-provider-item__quota', quotaLevel(chatgptQuota.rolling.percent)]">{{ quotaPercent(chatgptQuota.rolling) }}</span>
                <span class="models-provider-item__usage-reset">{{ formatResetSeconds(chatgptQuota.rolling.resetSeconds) }}</span>
              </div>
              <div class="models-provider-item__usage-row">
                <span class="models-provider-item__usage-label">7d</span>
                <template v-if="chatgptQuota?.weekly">
                  <span :class="['models-provider-item__quota', quotaLevel(chatgptQuota.weekly.percent)]">{{ quotaPercent(chatgptQuota.weekly) }}</span>
                  <span class="models-provider-item__usage-reset">{{ formatResetSeconds(chatgptQuota.weekly.resetSeconds) }}</span>
                </template>
                <span v-else class="models-provider-item__usage-loading">…</span>
              </div>
              <div class="models-provider-item__usage-row">
                <span class="models-provider-item__usage-label">{{ t.chatgpt_expires }}</span>
                <span
                  :class="['models-provider-item__auth-expiry', { expired: isChatgptCredentialExpired(), valid: !isChatgptCredentialExpired() }]"
                >
                  {{ formatLocalDate(chatgptAccountInfo.expires) || '-' }}
                </span>
              </div>
            </template>
            <span v-else class="models-provider-item__auth-expiry expired">{{ t.chatgpt_not_authorized }}</span>
          </div>
          <div v-else-if="isZenGoProvider(p)" class="models-provider-item__usage">
            <template v-if="usageByProvider[p.name]">
              <span class="models-provider-item__usage-label">5h</span>
              <span :class="['models-provider-item__quota', quotaLevel(usageByProvider[p.name].rolling.percent)]">{{ quotaPercent(usageByProvider[p.name].rolling) }}</span>
              <span class="models-provider-item__usage-reset">{{ formatResetSeconds(usageByProvider[p.name].rolling.resetSeconds) }}</span>
              <span class="models-provider-item__usage-sep">|</span>
              <span class="models-provider-item__usage-label">7d</span>
              <span :class="['models-provider-item__quota', quotaLevel(usageByProvider[p.name].weekly.percent)]">{{ quotaPercent(usageByProvider[p.name].weekly) }}</span>
              <span class="models-provider-item__usage-reset">{{ formatResetSeconds(usageByProvider[p.name].weekly.resetSeconds) }}</span>
              <span class="models-provider-item__usage-sep">|</span>
              <span class="models-provider-item__usage-label">30d</span>
              <span :class="['models-provider-item__quota', quotaLevel(usageByProvider[p.name].monthly.percent)]">{{ quotaPercent(usageByProvider[p.name].monthly) }}</span>
              <span class="models-provider-item__usage-reset">{{ formatResetSeconds(usageByProvider[p.name].monthly.resetSeconds) }}</span>
            </template>
            <span v-else class="models-provider-item__usage-loading">…</span>
          </div>
          <div v-else-if="isDeepSeekProvider(p)" class="models-provider-item__usage">
            <template v-if="balanceByProvider[p.name]?.balances?.length">
              <span class="models-provider-item__usage-label">{{ t.deepseek_balance }}</span>
              <span
                v-for="balance in balanceByProvider[p.name].balances"
                :key="balance.currency"
                :class="['models-provider-item__quota', balanceLevel(balance.totalBalance)]"
              >
                {{ balance.currency }} {{ formatBalanceAmount(balance.totalBalance) }}
              </span>
            </template>
            <span v-else class="models-provider-item__usage-loading">…</span>
          </div>
        </li>
      </ul>
    </aside>

    <section v-if="pageSelectedProvider" v-show="activeModelsTab === 'providers'" class="models-panel">
      <div class="provider-info-card">
        <div class="provider-info-card__head">
          <div>
            <h2 class="provider-info-card__title">{{ pageSelectedProvider.label }}</h2>
            <div class="provider-info-card__sub">{{ pageSelectedProvider.name }}</div>
          </div>
          <div v-if="!chatgptSelected" class="provider-info-card__actions">
            <button class="icon-button" :title="t.editProvider" :aria-label="t.editProvider" @click="openProviderEdit(selectedProvider)"><Settings :size="14" /></button>
            <button class="icon-button danger" :title="t.delete" :aria-label="t.delete" @click="requestDeleteProvider(selectedProvider)"><Trash2 :size="14" /></button>
          </div>
        </div>
        <div class="provider-info-grid">
          <div class="provider-info-item">
            <span class="provider-info-item__label">Base URL</span>
            <span class="provider-info-item__value">{{ pageSelectedProvider.baseUrl || '-' }}</span>
          </div>
          <div v-if="!chatgptSelected" class="provider-info-item">
            <span class="provider-info-item__label">API Key</span>
            <span class="provider-info-item__value">
              <template v-if="selectedProvider.apiKey">
                {{ showProviderApiKey ? selectedProvider.apiKey : '••••••••' }}
                <button class="link-btn" @click="toggleApiKeyVisible">{{ apiKeyVisibilityLabel }}</button>
              </template>
              <template v-else>-</template>
            </span>
          </div>
          <div class="provider-info-item">
            <span class="provider-info-item__label">{{ chatgptSelected ? t.chatgpt_authorization : t.enabled }}</span>
            <span v-if="chatgptSelected" :class="['provider-info-item__value', 'chatgpt-auth-state', { expired: !chatgptAccountInfo.loggedIn || isChatgptCredentialExpired(), valid: chatgptAccountInfo.loggedIn && !isChatgptCredentialExpired() }]">
              {{ chatgptAccountInfo.loggedIn ? (isChatgptCredentialExpired() ? t.chatgpt_authorization_expired : t.chatgpt_logged_in) : t.chatgpt_not_authorized }}
            </span>
            <span v-else class="provider-info-item__value">
              <label class="switch">
                <input type="checkbox" v-model="selectedProvider.enabled" @change="persist()" />
                <span class="switch__track"></span>
              </label>
            </span>
          </div>
        </div>
      </div>

      <div v-if="chatgptSelected" class="chatgpt-card">
        <div class="chatgpt-card__head">
          <div>
            <h3 class="chatgpt-card__title">ChatGPT（OpenAI Codex）</h3>
            <p class="chatgpt-card__intro">{{ t.chatgpt_intro }}</p>
          </div>
        </div>

        <div v-if="chatgptLoginState" class="chatgpt-flow">
          <div class="chatgpt-flow__icon spin"><LoaderCircle :size="18" /></div>
          <div class="chatgpt-flow__body">
            <p class="chatgpt-flow__title">{{ t.chatgpt_login_pending }}</p>
            <p class="chatgpt-flow__hint">{{ t.chatgpt_login_pending_hint }}</p>
            <div class="chatgpt-flow__actions">
              <button class="pill-btn" @click="openExternal(chatgptLoginState.authUrl)">{{ t.chatgpt_login_open_browser }}</button>
              <button class="ghost-btn" @click="cancelChatgptLogin">{{ t.chatgpt_login_cancel }}</button>
            </div>
          </div>
        </div>

        <div v-else-if="chatgptAccountInfo.loggedIn" class="chatgpt-account">
          <div class="chatgpt-account__status">
            <span class="chatgpt-account__dot"></span>
            <span>{{ t.chatgpt_logged_in }}</span>
          </div>
          <div class="chatgpt-account__grid">
            <div class="chatgpt-account__item">
              <span class="chatgpt-account__label">Account ID</span>
              <span class="chatgpt-account__value">{{ chatgptAccountInfo.accountId || '-' }}</span>
            </div>
            <div class="chatgpt-account__item">
              <span class="chatgpt-account__label">{{ t.chatgpt_expires }}</span>
              <span :class="['chatgpt-account__value', 'chatgpt-auth-state', { expired: isChatgptCredentialExpired(), valid: !isChatgptCredentialExpired() }]">
                {{ formatLocalDate(chatgptAccountInfo.expires) || '-' }}
              </span>
            </div>
            <div v-if="chatgptQuota?.rolling" class="chatgpt-account__item">
              <span class="chatgpt-account__label">{{ t.chatgptFiveHourQuota }}</span>
              <span :class="['chatgpt-account__value', 'models-provider-item__quota', quotaLevel(chatgptQuota.rolling.percent)]">
                {{ quotaPercent(chatgptQuota.rolling) }} · {{ t.chatgpt_usage_resets_in }} {{ formatResetSeconds(chatgptQuota.rolling.resetSeconds) || '-' }}
              </span>
            </div>
            <div class="chatgpt-account__item">
              <span class="chatgpt-account__label">{{ t.chatgpt_weekly_usage }}</span>
              <span :class="['chatgpt-account__value', 'models-provider-item__quota', quotaLevel(chatgptQuota?.weekly?.percent)]">
                <template v-if="chatgptQuota?.weekly">
                  {{ quotaPercent(chatgptQuota.weekly) }} · {{ t.chatgpt_usage_resets_in }} {{ formatResetSeconds(chatgptQuota.weekly.resetSeconds) || '-' }}
                </template>
                <template v-else>…</template>
              </span>
            </div>
          </div>
          <div class="chatgpt-account__actions">
            <button class="pill-btn" @click="startChatgptLogin">{{ t.chatgpt_login_again }}</button>
            <button class="ghost-btn" @click="logoutChatgpt">{{ t.chatgpt_logout }}</button>
          </div>
        </div>

        <div v-else class="chatgpt-empty">
          <p class="chatgpt-empty__hint">{{ t.chatgpt_require_subscription }}</p>
          <button class="pill-btn" @click="startChatgptLogin">{{ t.chatgpt_authorize }}</button>
        </div>
      </div>

      <div class="models-section">
        <div class="models-section__head">
          <h3 class="models-section__title">
            {{ t.models }}
            <span class="models-section__count">{{ pageSelectedProvider.models.length }}</span>
          </h3>
          <span v-if="chatgptSelected" class="models-section__readonly">{{ t.chatgpt_models_readonly }}</span>
          <button v-else class="pill-btn" @click="openAddModel">+ {{ t.addModel }}</button>
        </div>

        <div v-if="pageSelectedProvider.models.length" class="model-list">
          <div v-for="model in pageSelectedProvider.models" :key="model.id" :class="['model-card', { 'model-card--readonly': chatgptSelected }]">
            <div class="model-card__actions">
              <button
                class="icon-button"
                :title="t.test"
                :aria-label="t.test"
                :disabled="testingModels[testModelKey(pageSelectedProvider, model)] || (chatgptSelected && !chatgptAccountInfo.loggedIn)"
                @click="runModelTestFor(pageSelectedProvider, model)"
              >
                <IconPlay v-if="!testingModels[testModelKey(pageSelectedProvider, model)]" />
                <LoaderCircle v-else class="spin" :size="14" />
              </button>
              <button v-if="!chatgptSelected" class="icon-button" :title="t.edit" :aria-label="t.edit" @click="openEditModel(model)"><Settings :size="14" /></button>
              <button v-if="!chatgptSelected" class="icon-button danger" :title="t.delete" :aria-label="t.delete" @click="deleteModel(model)"><Trash2 :size="14" /></button>
            </div>
            <div class="model-card__main">
              <div class="model-card__title">{{ model.name }}</div>
              <div class="model-card__id">{{ model.id }}</div>
              <div class="model-card__meta">
                <span class="badge">{{ model.api }}</span>
                <span v-if="model.baseUrl" class="badge">{{ model.baseUrl }}</span>
                <span v-if="model.contextWindow" class="badge">上下文 {{ formatTokenCount(model.contextWindow) }}</span>
                <span v-if="model.maxTokens" class="badge">输出 {{ formatTokenCount(model.maxTokens) }}</span>
              </div>
              <div class="model-card__caps">
                <span class="cap-tag" :class="{ 'cap-tag--off': !model.input?.includes('image') }" :title="t.imageInput"><IconImage /></span>
                <span class="cap-tag" :class="{ 'cap-tag--off': !model.reasoning }" :title="t.reasoning"><IconBrain /></span>
                <span class="cap-tag" :class="{ 'cap-tag--off': !model.capabilities?.toolCall }" :title="t.toolCalling"><IconWrench /></span>
              </div>
            </div>
              <div
              v-if="modelTestResult(pageSelectedProvider, model)"
              :class="['model-card__result', modelTestResult(pageSelectedProvider, model).ok ? 'ok' : 'fail']"
            >
              {{ modelTestResult(pageSelectedProvider, model).ok ? t.testOk : t.testErr }}
              · {{ modelTestResult(pageSelectedProvider, model).ok ? (modelTestResult(pageSelectedProvider, model).latency + ' ms') : modelTestResult(pageSelectedProvider, model).error }}
            </div>
          </div>
        </div>
        <div v-else class="models-empty">
          {{ chatgptSelected ? t.chatgpt_models_unavailable : `暂无模型，点击「${t.addModel}」新建一个。` }}
        </div>
      </div>
    </section>

    <section v-else v-show="activeModelsTab === 'providers'" class="models-empty-state">
      <p>请选择左侧服务商，或点击「{{ t.addProvider }}」新增。</p>
    </section>

    <section v-if="activeModelsTab === 'stats'" class="model-usage-panel">
      <div class="model-usage-card">
        <div class="model-usage-card__head">
          <div>
            <h3 class="model-usage-card__title">{{ t.model_usage_title }}</h3>
            <p class="model-usage-card__intro">{{ t.model_usage_intro }}</p>
          </div>
          <button class="ghost-btn" :disabled="modelUsageLoading" @click="loadModelUsage()">
            {{ modelUsageLoading ? '…' : t.model_usage_refresh }}
          </button>
        </div>

        <div class="model-usage-detail-setting">
          <div>
            <div class="model-usage-detail-setting__title">{{ t.model_usage_record_details }}</div>
            <div class="model-usage-detail-setting__hint">{{ t.model_usage_record_hint }}</div>
          </div>
          <label class="switch">
            <input type="checkbox" :checked="config.recordApiDetails === true" @change="setRecordApiDetails" />
            <span class="switch__track"></span>
          </label>
        </div>

        <div class="model-usage-toolbar">
          <div class="model-usage-segmented" role="group" :aria-label="t.model_usage_dimension">
            <button v-for="item in ['model', 'session', 'request']" :key="item" type="button"
              :class="['model-usage-segmented__item', { active: usageDimension === item }]"
              @click="selectUsageDimension(item)">
              {{ t[`model_usage_dimension_${item}`] }}
            </button>
          </div>
          <div class="model-usage-range-controls">
            <div class="model-usage-segmented" role="group" :aria-label="t.model_usage_quick_range">
              <button v-for="preset in ['yesterday', 'last7', 'last30']" :key="preset" type="button"
                :class="['model-usage-segmented__item', { active: isUsagePresetActive(preset) }]"
                @click="selectUsagePreset(preset)">
                {{ t[`model_usage_range_${preset}`] }}
              </button>
            </div>
            <div class="model-usage-date-range" role="group" :aria-label="t.model_usage_custom_range">
              <label>
                <span>{{ t.model_usage_start_date }}</span>
                <input type="date" :value="usageStartDay" :min="usageEarliestDay" :max="usageEndDay"
                  @change="selectUsageStartDay($event.target.value)" />
              </label>
              <span class="model-usage-date-range__separator">—</span>
              <label>
                <span>{{ t.model_usage_end_date }}</span>
                <input type="date" :value="usageEndDay" :min="usageStartDay" :max="usageToday"
                  @change="selectUsageEndDay($event.target.value)" />
              </label>
            </div>
          </div>
        </div>

        <div v-if="modelUsage" class="model-usage-summary">
          <div><span>{{ t.token_total }}</span><strong>{{ formatTokenCount(modelUsage.totals?.total || 0) }}</strong></div>
          <div><span>{{ t.token_input }}</span><strong>{{ formatTokenCount(modelUsage.totals?.input || 0) }}</strong></div>
          <div><span>{{ t.token_cached }}</span><strong>{{ formatTokenCount(modelUsage.totals?.cached || 0) }}</strong></div>
          <div><span :title="t.model_usage_cache_hit_rate_help">{{ t.model_usage_cache_hit_rate }}</span><strong>{{ formatCacheHitRate(modelUsage.totals) }}</strong></div>
          <div><span>{{ t.token_output }}</span><strong>{{ formatTokenCount(modelUsage.totals?.output || 0) }}</strong></div>
          <div><span>{{ t.model_usage_requests }}</span><strong>{{ modelUsage.totals?.requestCount || 0 }}</strong></div>
          <div><span>{{ t.model_usage_sessions }}</span><strong>{{ modelUsage.totals?.sessionCount || 0 }}</strong></div>
          <div><span>{{ t.model_usage_models }}</span><strong>{{ modelUsage.totals?.modelCount || 0 }}</strong></div>
        </div>

        <p v-if="modelUsageError" class="model-usage-error">{{ modelUsageError }}</p>
        <div v-else-if="modelUsageLoading && !modelUsage" class="model-usage-loading">{{ t.model_usage_loading }}</div>
        <div v-else-if="!(modelUsage && modelUsage.rows && modelUsage.rows.length)" class="model-usage-empty">{{ t.model_usage_empty }}</div>

        <template v-else>
          <div class="model-usage-section">
            <p class="model-usage-hint">{{ t.model_usage_retention_hint }}</p>
            <div class="model-usage-table-wrap">
              <table class="model-usage-table">
                <thead>
                  <tr>
                    <th>{{ t.model_usage_date }}</th>
                    <th class="model-usage-table__label">{{ t[`model_usage_dimension_${usageDimension}`] }}</th>
                    <th>{{ t.model_usage_requests }}</th>
                    <th>{{ t.token_input }}</th>
                    <th>{{ t.token_cached }}</th>
                    <th :title="t.model_usage_cache_hit_rate_help">{{ t.model_usage_cache_hit_rate }}</th>
                    <th>{{ t.token_output }}</th>
                    <th>{{ t.token_total }}</th>
                    <th v-if="usageDimension === 'session' || usageDimension === 'request'" class="model-usage-table__action">{{ t.model_usage_action }}</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="item in modelUsage.rows" :key="`${usageDimension}:${item.day}:${item.requestId || item.sessionId || item.label}`">
                    <td class="model-usage-day-name">{{ item.day }}</td>
                    <td class="model-usage-table__label">
                      <div class="model-usage-session">
                        <span class="model-usage-session__name">{{ item.modelTest ? t.model_usage_model_test : (item.label || '-') }}</span>
                        <span v-if="usageDimension === 'request'" class="model-usage-session__meta">
                          {{ formatUsageTime(item.requestTime) }} · {{ [item.provider, item.model, item.api].filter(Boolean).join(' / ') || '-' }}
                        </span>
                        <span v-else-if="usageDimension === 'session'" class="model-usage-session__meta">
                          {{ item.modelCount || 0 }} {{ t.model_usage_models }}
                        </span>
                        <span v-else class="model-usage-session__meta">
                          {{ item.sessionCount || 0 }} {{ t.model_usage_sessions }}
                        </span>
                      </div>
                    </td>
                    <td>{{ item.requestCount || 0 }}</td>
                    <td>{{ formatTokenCount(item.input) }}</td>
                    <td>{{ formatTokenCount(item.cached) }}</td>
                    <td>{{ formatCacheHitRate(item) }}</td>
                    <td>{{ formatTokenCount(item.output) }}</td>
                    <td class="model-usage-table__total">{{ formatTokenCount(item.total) }}</td>
                    <td v-if="usageDimension === 'session'" class="model-usage-table__action">
                      <button type="button" class="ghost-btn model-usage-detail-btn" @click="openUsageSessionDetail(item)">
                        {{ t.model_usage_view_requests }}
                      </button>
                    </td>
                    <td v-else-if="usageDimension === 'request'" class="model-usage-table__action">
                      <button v-if="!item.synthetic" type="button" class="ghost-btn model-usage-detail-btn" @click="openUsageRequestDetail(item)">
                        {{ t.model_usage_view_detail }}
                      </button>
                      <span v-else>-</span>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
            <div v-if="(usageDimension === 'session' || usageDimension === 'request') && (modelUsagePage > 1 || modelUsage.hasMore || modelUsageMoreLoading)" class="model-usage-pagination">
              <button type="button" class="ghost-btn" :disabled="modelUsageMoreLoading || modelUsagePage <= 1" @click="loadPreviousModelUsagePage">
                {{ t.model_usage_previous_page }}
              </button>
              <span>{{ t.model_usage_page.replace('{page}', modelUsagePage) }}</span>
              <button type="button" class="ghost-btn" :disabled="modelUsageMoreLoading || !modelUsage.hasMore" @click="loadNextModelUsagePage">
                {{ t.model_usage_next_page }}
              </button>
            </div>
          </div>
        </template>
      </div>
    </section>

    <!-- 某一自然日内指定会话的逐请求 token 明细 -->
    <div v-if="usageSessionDetail" class="modal-backdrop" @click.self="closeUsageSessionDetail">
      <div class="modal model-usage-dialog">
        <div class="modal__header">
          <div>
            <h3>{{ usageSessionDetail.modelTest ? t.model_usage_model_test : (usageSessionDetail.label || t.model_usage_session) }}</h3>
            <p class="model-usage-dialog__date">{{ usageSessionDetail.day }}</p>
          </div>
          <div class="model-usage-dialog__header-actions">
            <div class="model-usage-dialog__total">
              <span class="model-usage-day-summary__label">{{ t.model_usage_day_total }}</span>
              <span class="model-usage-day-summary__value">{{ formatTokenCount(usageSessionDetail.total) }}</span>
            </div>
            <button class="modal__close" @click="closeUsageSessionDetail">×</button>
          </div>
        </div>
        <div class="modal__body">
          <p v-if="usageSessionError && !usageSessionRequests.length" class="model-usage-error">{{ usageSessionError }}</p>
          <div v-else-if="usageSessionLoading && !usageSessionRequests.length" class="model-usage-loading">{{ t.model_usage_loading }}</div>
          <div v-else-if="!usageSessionRequests.length" class="model-usage-empty">{{ t.model_usage_request_empty }}</div>
          <div v-else>
            <div class="model-usage-table-wrap">
              <table class="model-usage-table">
                <thead>
                  <tr>
                    <th>{{ t.model_usage_request_time }}</th>
                    <th class="model-usage-table__label">{{ t.model_usage_model }}</th>
                    <th>{{ t.model_usage_status }}</th>
                    <th>{{ t.token_input }}</th>
                    <th>{{ t.token_cached }}</th>
                    <th :title="t.model_usage_cache_hit_rate_help">{{ t.model_usage_cache_hit_rate }}</th>
                    <th>{{ t.token_output }}</th>
                    <th>{{ t.token_total }}</th>
                    <th class="model-usage-table__action">{{ t.model_usage_action }}</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="item in usageSessionRequests" :key="item.requestId">
                    <td>{{ formatUsageTime(item.requestTime) }}</td>
                    <td class="model-usage-table__label">
                      <div class="model-usage-session">
                        <span class="model-usage-session__name">{{ [item.provider, item.model].filter(Boolean).join(' / ') || '-' }}</span>
                        <span class="model-usage-session__meta">{{ item.api || '-' }}</span>
                      </div>
                    </td>
                    <td :class="item.success ? 'model-usage-status--success' : 'model-usage-status--failed'">
                      {{ item.success ? t.model_usage_success : t.model_usage_failed }}
                    </td>
                    <td>{{ formatTokenCount(item.input) }}</td>
                    <td>{{ formatTokenCount(item.cached) }}</td>
                    <td>{{ formatCacheHitRate(item) }}</td>
                    <td>{{ formatTokenCount(item.output) }}</td>
                    <td class="model-usage-table__total">{{ formatTokenCount(item.total) }}</td>
                    <td class="model-usage-table__action">
                      <button v-if="!item.synthetic" type="button" class="ghost-btn model-usage-detail-btn" @click="openUsageRequestDetail(item)">
                        {{ t.model_usage_view_detail }}
                      </button>
                      <span v-else>-</span>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
            <p v-if="usageSessionError" class="model-usage-error">{{ usageSessionError }}</p>
            <div v-if="usageSessionPage > 1 || usageSessionHasMore || usageSessionLoading" class="model-usage-pagination">
              <button type="button" class="ghost-btn" :disabled="usageSessionLoading || usageSessionPage <= 1" @click="loadPreviousUsageSessionPage">
                {{ t.model_usage_previous_page }}
              </button>
              <span>{{ t.model_usage_page.replace('{page}', usageSessionPage) }}</span>
              <button type="button" class="ghost-btn" :disabled="usageSessionLoading || !usageSessionHasMore" @click="loadNextUsageSessionPage">
                {{ t.model_usage_next_page }}
              </button>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- 单次请求完整参数与结果 -->
    <div v-if="usageRequestSource" class="modal-backdrop" @click.self="closeUsageRequestDetail">
      <div class="modal model-usage-request-dialog">
        <div class="modal__header">
          <div>
            <h3>{{ t.model_usage_request_detail_title }}</h3>
            <p class="model-usage-dialog__date">
              {{ usageRequestSource.day }} {{ formatUsageTime(usageRequestSource.requestTime) }} ·
              {{ [usageRequestSource.provider, usageRequestSource.model].filter(Boolean).join(' / ') || '-' }}
            </p>
          </div>
          <button class="modal__close" @click="closeUsageRequestDetail">×</button>
        </div>
        <div class="modal__body model-usage-request-dialog__body">
          <p v-if="usageRequestError" class="model-usage-error">{{ usageRequestError }}</p>
          <div v-else-if="usageRequestLoading" class="model-usage-loading">{{ t.model_usage_loading }}</div>
          <div v-else-if="!usageRequestDetail?.available" class="model-usage-empty">
            {{ t.model_usage_detail_unavailable }}
          </div>
          <template v-else>
            <p class="model-usage-request-file">{{ usageRequestDetail.fileName }}</p>
            <section class="model-usage-json-section">
              <h4>{{ t.model_usage_request_parameters }}</h4>
              <pre>{{ usageRequestDetail.request }}</pre>
            </section>
            <section class="model-usage-json-section">
              <h4>{{ t.model_usage_request_result }}</h4>
              <pre>{{ usageRequestDetail.response }}</pre>
            </section>
          </template>
        </div>
      </div>
    </div>

    <div v-if="modelEditorOpen" class="modal-backdrop" @click.self="closeModelEditor">
      <div class="modal model-editor-dialog">
        <div class="modal__header">
          <h3>{{ editingNewModel ? t.addModel : t.editModel }}</h3>
          <button class="modal__close" @click="closeModelEditor">×</button>
        </div>
        <div class="modal__body" v-if="modelDraft">
          <div class="model-form">
            <div class="field">
              <label>{{ t.modelId }}</label>
              <input v-model="modelDraft.id" placeholder="gpt-4o" />
            </div>
            <div class="field">
              <label>{{ t.displayName }}</label>
              <input v-model="modelDraft.name" placeholder="GPT-4o" />
            </div>
            <div class="field">
              <label>API 协议</label>
              <select v-model="modelDraft.api">
                <option v-for="opt in apiOptions" :key="opt" :value="opt">{{ opt }}</option>
              </select>
            </div>
            <div class="field field--wide">
              <label>前缀</label>
              <input v-model="modelDraft.baseUrl" placeholder="留空则使用服务商 Base URL" />
              <span class="hint">实际请求地址：{{ modelRequestRoute(selectedProvider, modelDraft) }}</span>
            </div>
            <div class="field">
              <label>上下文窗口</label>
              <input type="number" v-model.number="modelDraft.contextWindow" />
            </div>
            <div class="field">
              <label>最大输出 Token</label>
              <input type="number" v-model.number="modelDraft.maxTokens" />
            </div>
            <div class="field">
              <label>默认思考级别</label>
              <select v-model="modelDraft.defaultThinkingLevel">
                <option v-for="level in piThinkingLevels" :key="level" :value="level">{{ t['thinking_' + level] || level }}</option>
              </select>
            </div>
            <div class="field field--wide">
              <label>模型能力</label>
              <div class="capability-options">
                <label class="capability-option">
                  <input type="checkbox" v-model="modelDraft.reasoning" />
                  <span>推理模型</span>
                </label>
                <label class="capability-option">
                  <input type="checkbox" :checked="modelDraft.input.includes('image')" @change="toggleImageInput(modelDraft, $event.target.checked)" />
                  <span>图片输入</span>
                </label>
                <label class="capability-option">
                  <input type="checkbox" v-model="modelDraft.capabilities.toolCall" />
                  <span>工具调用</span>
                </label>
              </div>
            </div>

            <details class="compat-section">
              <summary>高级参数 (compat)</summary>
              <div class="compat-section__body">
                <div class="compat-bools">
                  <label v-for="item in piCompatBooleanFields" :key="item.key" class="capability-option">
                    <input type="checkbox" v-model="modelDraft.compat[item.key]" />
                    <span>{{ t[item.hint] }}</span>
                  </label>
                </div>
                <div class="compat-json">
                  <label>原始 JSON</label>
                  <textarea :value="formatCompat(modelDraft)" @input="updateCompatJson(modelDraft, $event)"></textarea>
                </div>
              </div>
            </details>

            <p v-if="modelDialogError" class="model-form__error">{{ modelDialogError }}</p>
          </div>
        </div>
        <div class="modal__footer">
          <button class="ghost-btn" @click="closeModelEditor">{{ t.cancel }}</button>
          <button class="pill-btn" :disabled="saving" @click="saveModel">{{ t.save }}</button>
        </div>
      </div>
    </div>
  </div>

  <ConfirmDialog
    v-model="showRecordApiDetailsConfirm"
    :title="t.model_usage_record_confirm_title"
    :description="t.model_usage_record_warning"
    :confirm-label="t.model_usage_record_confirm_ok"
    @cancel="showRecordApiDetailsConfirm = false"
    @confirm="confirmRecordApiDetails"
  />
</template>
