<script setup>
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import {
  CheckCircle2, LoaderCircle, MessageSquare, Plus, PlugZap, RefreshCw,
  Send, Smartphone, Trash2, Wrench, X
} from 'lucide-vue-next'
import { useAppContext } from '../../composables/appContext'
import ChatMessages from '../chat/ChatMessages.vue'
import {
  deleteBotChannel, getSessionHistory, getStewardProfile, injectBotMessage, listBotChannels,
  onEvent, saveBotChannel, saveStewardProfile, testBotChannel, toggleBotChannel
} from '../../backend'
import { DEFAULT_STEWARD_PERSONA, stewardPersonaWithDefaults } from '../../stewardState.js'

const { t, config, pushToast } = useAppContext()

const activeTab = ref('channels')
const busy = ref(false)
const channels = ref([])
// 管家会话详情（消息 tab）：residentSessionId 来自人设，无则说明会话尚未创建。
const residentSessionId = ref(0)
const sessionLoading = ref(false)
const sessionMessages = ref([])

// ---- persona ----
const persona = ref({ ...DEFAULT_STEWARD_PERSONA })

// ---- channel form ----
const showForm = ref(false)
const form = ref({
  id: 0, platform: 'dingtalk', name: '', mode: '',
  config: { clientId: '', appId: '', corpId: '', agentId: '', token: '', encodingAESKey: '', callbackPort: '' },
  secrets: { clientSecret: '', appSecret: '', secret: '' },
  enabled: true
})
const platformLabels = computed(() => ({
  dingtalk: t.value.steward_platform_dingtalk,
  feishu: t.value.steward_platform_feishu,
  wecom: t.value.steward_platform_wecom,
  loopback: t.value.steward_platform_loopback
}))
const platformFields = computed(() => {
  switch (form.value.platform) {
    case 'dingtalk':
      return [
        { key: 'clientId', label: 'ClientID（AppKey）', secret: false },
        { key: 'clientSecret', label: 'ClientSecret（AppSecret）', secret: true }
      ]
    case 'feishu':
      return [
        { key: 'appId', label: 'AppID', secret: false },
        { key: 'appSecret', label: 'AppSecret', secret: true }
      ]
    case 'wecom':
      return [
        { key: 'corpId', label: 'CorpID', secret: false },
        { key: 'agentId', label: 'AgentID', secret: false },
        { key: 'secret', label: 'Secret', secret: true },
        { key: 'token', label: 'Token', secret: false },
        { key: 'encodingAESKey', label: 'EncodingAESKey', secret: false },
        { key: 'callbackPort', label: '回调端口', secret: false }
      ]
    default:
      return []
  }
})

const models = computed(() => {
  const list = []
  for (const provider of config.providers || []) {
    for (const model of provider.models || []) {
      list.push({ label: `${provider.label || provider.name} / ${model.name || model.id}`, value: `${provider.name}:${model.id}`, provider: provider.name, model: model.id })
    }
  }
  return list
})
const personaModelKey = computed({
  get: () => (persona.value.provider && persona.value.model) ? `${persona.value.provider}:${persona.value.model}` : '',
  set: (value) => {
    if (!value) {
      persona.value.provider = ''
      persona.value.model = ''
      return
    }
    const idx = value.indexOf(':')
    if (idx > 0) {
      persona.value.provider = value.slice(0, idx)
      persona.value.model = value.slice(idx + 1)
    }
  }
})

// ---- load ----
function applyStewardProfile(profileData) {
  persona.value = stewardPersonaWithDefaults(profileData)
  residentSessionId.value = Number(profileData?.residentSessionId) || 0
}

async function loadAll() {
  busy.value = true
  try {
    const [channelData, profileData] = await Promise.all([
      listBotChannels(), getStewardProfile()
    ])
    channels.value = channelData || []
    applyStewardProfile(profileData)
  } finally {
    busy.value = false
  }
  if (activeTab.value === 'messages') await loadSessionDetail()
}

// 管家会话详情：从后端加载常驻会话的历史消息，直接展示在"消息"页签。
async function loadSessionDetail() {
  if (!residentSessionId.value) {
    sessionMessages.value = []
    return
  }
  sessionLoading.value = true
  try {
    const history = await getSessionHistory(residentSessionId.value)
    sessionMessages.value = history?.messages || []
  } catch (err) {
    sessionMessages.value = []
  } finally {
    sessionLoading.value = false
  }
}


const stewardAgent = computed(() => ({ name: persona.value.name || t.value.stewardMenu }))

// 切换到消息 tab 时先刷新 Profile，避免页面打开后新建的常驻会话仍显示为空。
async function switchMessagesTab() {
  activeTab.value = 'messages'
  await getStewardProfile().then(applyStewardProfile)
  await loadSessionDetail()
}

// ---- channels ----
function openAdd() {
  form.value = {
    id: 0, platform: 'dingtalk', name: '', mode: '',
    config: { clientId: '', appId: '', corpId: '', agentId: '', token: '', encodingAESKey: '', callbackPort: '9588' },
    secrets: { clientSecret: '', appSecret: '', secret: '' },
    enabled: true
  }
  showForm.value = true
}
function openEdit(channel) {
  form.value = {
    id: channel.id, platform: channel.platform, name: channel.name, mode: channel.mode,
    config: { clientId: '', appId: '', corpId: '', agentId: '', token: '', encodingAESKey: '', callbackPort: '9588', ...(channel.config || {}) },
    secrets: { clientSecret: '', appSecret: '', secret: '' },
    enabled: channel.enabled
  }
  showForm.value = true
}
async function submitChannel() {
  if (!form.value.name.trim()) return
  const config = {}
  const secrets = {}
  for (const field of platformFields.value) {
    const value = String(form.value.config[field.key] || form.value.secrets[field.key] || '').trim()
    if (!value) continue
    if (field.secret) secrets[field.key] = value
    else config[field.key] = value
  }
  await saveBotChannel({
    id: form.value.id, platform: form.value.platform, name: form.value.name.trim(),
    mode: form.value.mode, config, secrets, enabled: form.value.enabled
  })
  showForm.value = false
  await loadAll()
}
async function removeChannel(channel) {
  if (!window.confirm(t.value.steward_channel_delete_confirm)) return
  await deleteBotChannel(channel.id)
  await loadAll()
}
async function toggleChannel(channel) {
  await toggleBotChannel(channel.id, !channel.enabled)
  await loadAll()
}
// Wails 运行时错误消息偶尔带 "undefined: " 等包装前缀，提取可读内容。
function errText(err) {
  const raw = String(err?.message ?? err ?? '')
  const cleaned = raw.replace(/^(undefined|RuntimeError|Error|TypeError|ReferenceError):\s*/i, '').trim()
  return cleaned || String(err)
}

async function sendTest(channel) {
  try {
    await testBotChannel(channel.id)
    pushToast('success', t.value.steward_test_sent)
  } catch (err) {
    pushToast('error', errText(err))
  }
}
const loopbackChannel = computed(() => channels.value.find(c => c.platform === 'loopback'))
const simulatedText = ref('')
async function sendSimulated() {
  if (!loopbackChannel.value || !simulatedText.value.trim()) return
  await injectBotMessage(loopbackChannel.value.id, simulatedText.value.trim())
  simulatedText.value = ''
}
function statusLabel(channel) {
  const map = {
    connected: t.value.steward_channel_status_connected,
    connecting: t.value.steward_channel_status_connecting,
    disconnected: t.value.steward_channel_status_disconnected,
    error: t.value.steward_channel_status_error
  }
  return map[channel.status] || channel.status || t.value.steward_channel_status_disconnected
}
function channelStatusClass(channel) {
  return channel?.status === 'connected' ? 'is-connected' : 'is-disconnected'
}
function handleChannelStatus(payload) {
  const channelId = Number(payload?.channelId)
  if (!channelId) return
  const channel = channels.value.find(item => Number(item.id) === channelId)
  if (!channel) return
  channel.status = payload.status || 'disconnected'
  channel.lastError = payload.lastError || ''
}

// ---- persona ----
async function savePersona() {
  try {
    await saveStewardProfile({ ...persona.value })
    pushToast('success', t.value.steward_persona_saved)
  } catch (err) {
    pushToast('error', errText(err))
  }
}

// ---- message stream ----
let offMessage = null
let offChannelStatus = null
// 实时 IM 消息合并进会话详情：in=用户发来的消息，out=管家回复。
function pushMessage(payload) {
  if (!residentSessionId.value) return
  const role = payload?.direction === 'out' ? 'assistant' : 'user'
  const text = String(payload?.text || '')
  if (!text) return
  // 避免与最近一条内容重复（历史加载后同一消息可能被事件再次推送）。
  const last = sessionMessages.value[sessionMessages.value.length - 1]
  if (last && last.role === role && last.content === text) return
  sessionMessages.value.push({
    id: `live-${Date.now()}-${Math.random()}`,
    role,
    content: text,
    thinkingContent: '',
    createdAt: Date.now()
  })
  if (sessionMessages.value.length > 500) sessionMessages.value.splice(0, sessionMessages.value.length - 500)
}

onMounted(async () => {
  // Subscribe before the initial load. Starting a channel can publish its
  // connected state while listBotChannels is in flight; registering after the
  // load would drop that event and leave the row in the orange connecting
  // state until the next manual refresh.
  offMessage = onEvent('steward:message', pushMessage)
  offChannelStatus = onEvent('steward:status', handleChannelStatus)
  await loadAll()
})
onBeforeUnmount(() => {
  offMessage?.()
  offChannelStatus?.()
})

</script>

<template>
  <section class="content-page steward-page">
    <div class="page-heading">
      <div>
        <h2>{{ t.stewardMenu }}</h2>
        <p>{{ t.steward_intro }}</p>
      </div>
      <div class="page-heading__actions">
        <button v-if="activeTab === 'channels'" class="primary-button compact" @click="openAdd"><Plus :size="14" />{{ t.steward_add_channel }}</button>
        <button class="secondary-button compact" @click="loadAll"><RefreshCw :size="14" />{{ t.refresh }}</button>
      </div>
    </div>

    <div class="agent-filter-bar steward-tabs">
      <button class="agent-filter-chip" :class="{ active: activeTab === 'channels' }" @click="activeTab = 'channels'">
        <PlugZap :size="14" />{{ t.steward_tab_channels }}<span class="agent-filter-chip__count">{{ channels.length }}</span>
      </button>
      <button class="agent-filter-chip" :class="{ active: activeTab === 'persona' }" @click="activeTab = 'persona'">
        <Wrench :size="14" />{{ t.steward_tab_persona }}
      </button>
      <button class="agent-filter-chip" :class="{ active: activeTab === 'messages' }" @click="switchMessagesTab">
        <MessageSquare :size="14" />{{ t.steward_tab_messages }}
      </button>
    </div>

    <!-- ============ 渠道 ============ -->
    <div v-if="activeTab === 'channels'" class="plugin-section">
      <div class="plugin-section__title">
        <span>{{ t.steward_tab_channels }}</span>
      </div>
      <div v-if="!channels.length" class="empty-integration">
        <Smartphone :size="26" />
        <strong>{{ t.steward_empty_channels }}</strong>
        <button class="primary-button compact" @click="openAdd"><Plus :size="14" />{{ t.steward_add_channel }}</button>
      </div>

      <div class="steward-channel-list">
        <article v-for="channel in channels" :key="channel.id" class="plugin-row steward-channel">
          <div class="plugin-icon"><Smartphone :size="19" /></div>
          <div class="plugin-copy">
            <div class="plugin-name">
              <strong>{{ channel.name }}</strong>
              <span class="steward-channel__status" :class="channelStatusClass(channel)" :title="statusLabel(channel)">
                <span class="steward-channel__status-dot" :class="channelStatusClass(channel)" aria-hidden="true"></span>
                {{ statusLabel(channel) }}
              </span>
              <small>{{ platformLabels[channel.platform] || channel.platform }}</small>
            </div>
            <p v-if="channel.lastSenderId" class="steward-channel__target">{{ t.steward_last_sender }}：{{ channel.lastSenderId }}</p>
            <p v-if="channel.lastError" class="steward-channel__error">{{ channel.lastError }}</p>
            <p v-if="channel.platform === 'wecom'" class="steward-channel__hint">{{ t.steward_channel_wecom_hint }}</p>
          </div>
          <div class="plugin-actions steward-channel__actions">
            <button class="secondary-button compact" :title="t.steward_channel_test" @click="sendTest(channel)"><Send :size="14" />{{ t.steward_channel_test }}</button>
            <button class="secondary-button compact" @click="openEdit(channel)">{{ t.edit }}</button>
            <button class="secondary-button compact" @click="toggleChannel(channel)">{{ channel.enabled ? t.disable : t.enable }}</button>
            <button class="icon-button danger" :title="t.steward_channel_delete" @click="removeChannel(channel)"><Trash2 :size="15" /></button>
          </div>
        </article>
      </div>

      <div v-if="loopbackChannel" class="steward-simulate">
        <div class="plugin-section__title"><span>{{ t.steward_inject_message }}</span></div>
        <div class="steward-simulate__row">
          <input v-model="simulatedText" class="steward-text-input" :placeholder="t.steward_inject_placeholder" @keyup.enter="sendSimulated" />
          <button class="primary-button compact" @click="sendSimulated"><Send :size="14" />{{ t.steward_inject_send }}</button>
        </div>
      </div>
    </div>

    <!-- ============ 人设 ============ -->
    <div v-if="activeTab === 'persona'" class="plugin-section steward-persona">
      <div class="plugin-section__title"><span>{{ t.steward_tab_persona }}</span></div>
      <div class="steward-persona-card">
        <div class="steward-persona-card__head">
          <div>
            <h3>{{ t.steward_tab_persona }}</h3>
            <p>{{ t.steward_persona_hint }}</p>
          </div>
        </div>
        <div class="steward-persona-card__body">
          <div class="steward-form-grid">
            <label class="steward-check-row field--wide">
              <input v-model="persona.enabled" type="checkbox" />
              <span>{{ t.steward_persona_enabled }}</span>
            </label>
            <div class="field">
              <label>{{ t.steward_persona_model }}</label>
              <select v-model="personaModelKey">
                <option value="">{{ t.steward_default_model }}</option>
                <option v-for="m in models" :key="m.value" :value="m.value">{{ m.label }}</option>
              </select>
            </div>
            <div class="field">
              <label>{{ t.steward_persona_name }}</label>
              <input v-model="persona.name" :placeholder="t.steward_persona_name_placeholder" maxlength="20" />
            </div>
            <div class="field">
              <label>{{ t.steward_persona_tone }}</label>
              <input v-model="persona.tone" :placeholder="`如：简洁、专业`" />
            </div>
            <div class="field field--wide">
              <label>{{ t.steward_persona_prompt }}</label>
              <textarea v-model="persona.prompt" rows="5" placeholder="补充的提示词…"></textarea>
            </div>
            <div class="field">
              <label>{{ t.steward_persona_compact }}</label>
              <input v-model.number="persona.compactAfterTurns" type="number" min="1" max="200" />
              <p class="steward-scope-hint">{{ t.steward_persona_compact_hint }}</p>
            </div>
            <div class="field field--wide">
              <label>{{ t.steward_scope_label }}</label>
              <div class="steward-scope-group">
                <label class="steward-radio">
                  <input v-model="persona.manageScope" type="radio" value="butler" />
                  <span>{{ t.steward_scope_butler }}</span>
                </label>
                <label class="steward-radio">
                  <input v-model="persona.manageScope" type="radio" value="all" />
                  <span>{{ t.steward_scope_all }}</span>
                </label>
              </div>
              <p class="steward-scope-hint">{{ t.steward_scope_hint }}</p>
            </div>
          </div>
        </div>
        <div class="steward-persona-card__foot">
          <button class="primary-button compact" :disabled="busy" @click="savePersona">
            <LoaderCircle v-if="busy" class="spin" :size="14" />{{ t.steward_save }}
          </button>
        </div>
      </div>
    </div>

    <!-- ============ 消息 ============ -->
    <div v-if="activeTab === 'messages'" class="plugin-section">
      <!-- 仅展示当前固定管家会话的只读对话详情。 -->
      <div v-if="!residentSessionId" class="empty-integration">
        <MessageSquare :size="26" />
        <strong>{{ t.steward_session_empty }}</strong>
      </div>
      <div v-else class="steward-session-chat">
        <ChatMessages
          :messages="sessionMessages"
          :session-id="residentSessionId"
          :agents="[]"
          :loading-history="sessionLoading"
          :t="t"
          :selected-agent="stewardAgent"
        />
      </div>
    </div>

    <!-- 会话 Tab 暂时注释，任务仍由机器人命令和后端管理。 -->

    <!-- ============ 渠道编辑弹窗 ============ -->
    <Teleport to="body">
      <div v-if="showForm" class="modal-backdrop" @click.self="showForm = false">
        <div class="agent-editor-dialog steward-dialog" role="dialog" aria-modal="true">
          <header class="agent-editor-dialog__head">
            <h2>{{ form.id ? t.steward_edit_channel : t.steward_add_channel }}</h2>
            <button class="icon-button" @click="showForm = false"><X :size="16" /></button>
          </header>
          <div class="agent-editor-dialog__body">
            <p class="steward-dialog-hint">{{ t.steward_channel_dialog_hint }}</p>
            <form id="steward-channel-form" class="steward-form-grid" @submit.prevent="submitChannel">
              <div class="field">
                <label>{{ t.steward_channel_name }}</label>
                <input v-model="form.name" :placeholder="t.steward_channel_name" required />
              </div>
              <div class="field">
                <label>{{ t.steward_channel_platform }}</label>
                <select v-model="form.platform">
                  <option value="dingtalk">{{ t.steward_platform_dingtalk }}</option>
                  <option value="feishu">{{ t.steward_platform_feishu }}</option>
                </select>
              </div>
              <template v-for="field in platformFields" :key="field.key">
                <div class="field" :class="{ 'field--wide': platformFields.length > 4 }">
                  <label>{{ field.label }}</label>
                  <input
                    v-if="!field.secret"
                    v-model="form.config[field.key]"
                    :placeholder="field.label"
                  />
                  <input
                    v-else
                    v-model="form.secrets[field.key]"
                    type="password"
                    :placeholder="field.label"
                    autocomplete="new-password"
                  />
                </div>
              </template>
              <label class="steward-check-row field--wide">
                <input v-model="form.enabled" type="checkbox" />
                <span>{{ t.steward_channel_enabled }}</span>
              </label>
            </form>
          </div>
          <footer class="agent-editor-dialog__footer">
            <button type="button" class="secondary-button" @click="showForm = false"><X :size="14" />{{ t.cancel }}</button>
            <button type="submit" form="steward-channel-form" class="primary-button"><CheckCircle2 :size="14" />{{ t.steward_save }}</button>
          </footer>
        </div>
      </div>
    </Teleport>
  </section>
</template>

<style scoped>
.steward-tabs .agent-filter-chip { display: inline-flex; align-items: center; gap: 6px; }
.steward-channel-list { display: flex; flex-direction: column; gap: 8px; }
.steward-channel__actions { display: flex; align-items: center; gap: 6px; }
.steward-channel__status { display: inline-flex; align-items: center; gap: 5px; color: #d97706; font-size: var(--fs-13); }
.steward-channel__status.is-connected { color: #39a56c; }
.steward-channel__status-dot { width: 6px; height: 6px; flex: 0 0 auto; border-radius: 50%; background: #d97706; }
.steward-channel__status-dot.is-connected { background: #39a56c; box-shadow: 0 0 0 3px rgba(57,165,108,.12); }
.steward-channel__error { color: var(--danger-color, #d64545); font-size: 12px; }
.steward-channel__target { color: var(--muted-color, #8a8a8a); font-size: 12px; word-break: break-all; }

/* 统一表单网格：字段样式复用全局 .field（与模型/服务商编辑弹窗一致） */
.steward-form-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 14px 16px; }
.steward-form-grid .field { min-width: 0; }
.steward-form-grid textarea { resize: vertical; line-height: 1.55; font-family: inherit; }

/* 空状态内的添加按钮 */
.steward-page .empty-integration .primary-button { margin-top: 14px; }

/* 勾选行 */
.steward-check-row { display: flex; align-items: center; gap: 9px; min-height: 36px; padding: 0 12px; border: 1px solid var(--border-soft); border-radius: 8px; background: var(--surface); cursor: pointer; font-size: var(--fs-13); color: var(--text); transition: border-color .12s; }
.steward-check-row:hover { border-color: var(--border); }
.steward-check-row input { width: 15px; height: 15px; margin: 0; accent-color: var(--accent); }

/* 接管范围单选 */
.steward-scope-group { display: flex; gap: 10px; flex-wrap: wrap; }
.steward-radio { display: inline-flex; align-items: center; gap: 8px; padding: 8px 14px; border: 1px solid var(--border-soft); border-radius: 8px; background: var(--surface); cursor: pointer; font-size: var(--fs-13); color: var(--text); transition: border-color .12s; }
.steward-radio:hover { border-color: var(--border); }
.steward-radio input { width: 15px; height: 15px; margin: 0; accent-color: var(--accent); }
.steward-scope-hint { margin: 8px 0 0; color: var(--faint); font-size: var(--fs-12); line-height: 1.5; }

/* 渠道编辑弹窗 */
.steward-dialog { width: min(620px, calc(100vw - 32px)); max-height: min(88vh, 720px); }
.steward-dialog-hint { margin: 0 0 14px; color: var(--faint); font-size: var(--fs-12); line-height: 1.5; }

/* 人设卡片 */
.steward-persona-card { overflow: hidden; border: 1px solid var(--border); border-radius: 10px; background: var(--surface); }
.steward-persona-card__head { display: flex; align-items: center; justify-content: space-between; gap: 10px; padding: 14px 18px; border-bottom: 1px solid var(--border-soft); }
.steward-persona-card__head h3 { margin: 0; font-size: var(--fs-14); font-weight: 600; }
.steward-persona-card__head p { margin: 3px 0 0; color: var(--faint); font-size: var(--fs-12); line-height: 1.5; }
.steward-persona-card__body { padding: 18px; }
.steward-persona-card__foot { display: flex; align-items: center; gap: 10px; padding: 12px 18px; border-top: 1px solid var(--border-soft); background: var(--surface-2); }

/* 通用输入框（模拟消息 / 授权回复） */
.steward-text-input { flex: 1; min-width: 0; height: 32px; padding: 0 10px; border: 1px solid var(--border); border-radius: 7px; outline: 0; color: var(--text); background: var(--surface); font-size: var(--fs-13); }
.steward-text-input:focus { border-color: var(--accent); box-shadow: 0 0 0 2px rgba(79,124,255,.14); }

.steward-simulate { margin-top: 16px; }
.steward-simulate__row { display: flex; gap: 8px; margin-top: 8px; max-width: 560px; }
/* 管家会话详情复用主对话消息区；不挂载 ChatComposer，因此整个区域只读。 */
.steward-session-chat { height: min(680px, 65vh); min-height: 360px; overflow: hidden; border: 1px solid var(--border); border-radius: 10px; background: var(--surface); }
.steward-session-chat :deep(.chat-main__messages) { height: 100%; }
</style>
