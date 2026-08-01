<script setup>
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Bot, CheckCircle2, LoaderCircle, X, XCircle } from 'lucide-vue-next'
import { getSubagentTranscript } from '../../backend.js'
import { formatDuration } from './chatFormatters.js'
import { toolOutput, toolStatus } from './chatToolPresentation.js'
import {
  backfillSubagentTimeline, subagentTimelineMessages, subagentUIState
} from './subagentRuntime.js'
import { agentAvatar, isImageAvatar, useAppContext } from '../../composables/appContext.js'
import ChatMessageItem from './ChatMessageItem.vue'
import SubAgentInteraction from './SubAgentInteraction.vue'

const props = defineProps({
  open: { type: Boolean, default: true },
  details: { type: Object, required: true },
  agents: { type: Array, default: () => [] },
  now: { type: Number, default: 0 },
  t: { type: Object, required: true }
})

const emit = defineEmits(['close', 'artifact-error'])
const { config } = useAppContext()
const conversationEl = ref(null)
const thinkingOpenByID = ref({})
let backfillPending = false

// 与主对话一致的身份展示：用户头像取自个人信息设置，头像昵称开关跟随全局配置。
const userAvatar = computed(() => (config.userProfile && config.userProfile.avatar) || '')
const chatLayout = computed(() => (config.preferences && config.preferences.chatLayout) || 'left')
const showIdentity = computed(() => !(config.preferences && config.preferences.showIdentity === false))

function resultStatus(message) {
  const queue = [toolOutput(message)]
  const seen = new Set()
  while (queue.length && seen.size < 128) {
    let current = queue.shift()
    if (typeof current === 'string') {
      try { current = JSON.parse(current) } catch { continue }
    }
    if (!current || typeof current !== 'object' || seen.has(current)) continue
    seen.add(current)
    if (current.runId && current.status) return current.status
    queue.push(...(Array.isArray(current) ? current : Object.values(current)))
  }
  return ''
}

const runtimeStatus = computed(() => {
  const message = props.details?.message
  return resultStatus(message)
    || message?.detail?.subagent?.status
    || props.details?.status
    || (message && toolStatus(message) === 'done' ? 'completed' : 'running')
})
const displayDuration = computed(() => {
  const detail = props.details?.message?.detail || {}
  return detail.durationMs
    || (detail.startedAt && runtimeStatus.value === 'running'
      ? (props.now || Date.now()) - detail.startedAt
      : props.details?.duration || 0)
})
const statusLabel = computed(() => {
  if (runtimeStatus.value === 'running') return props.t.subagentRunning
  if (runtimeStatus.value === 'completed') return props.t.subagentCompleted
  if (runtimeStatus.value === 'timeout') return props.t.subagentTimeout
  if (runtimeStatus.value === 'aborted') return props.t.subagentAborted
  return props.t.subagentFailed
})
// 与卡片共用同一实时 timeline（mergeSubagentRuntime 增量维护，事件逐条到达），
// 打开弹窗即可看到与主对话详情一致的实时消息流，无需轮询 transcript。
const displayMessages = computed(() => (
  subagentTimelineMessages(props.details?.message?.detail, { includeUser: true })
    .map(message => (
      message.role === 'assistant' && message.thinkingContent
        ? { ...message, thinkingOpen: thinkingOpenByID.value[message.id] ?? !message.thinkingComplete }
        : message
    ))
))
const displayVersion = computed(() => displayMessages.value.map(message => (
  `${message.id}:${message.content?.length || 0}:${message.thinkingContent?.length || 0}:${message.detail?.status || ''}:${message.detail?.output?.length || 0}`
)).join('|'))
// 按“用户问题”切分节点（与主对话结构一致）：用户消息在前，其后是 Agent 身份头
// 与 Agent 的消息流。子智能体的首条用户消息即父 agent 下发的任务提示。
const messageNodes = computed(() => {
  const nodes = []
  let current = null
  for (const message of displayMessages.value) {
    if (message.role === 'user') {
      current = { key: `node-${message.id}`, user: message, agentMessages: [] }
      nodes.push(current)
    } else if (current) {
      current.agentMessages.push(message)
    } else {
      nodes.push({ key: `node-pre-${message.id}`, user: null, agentMessages: [message] })
    }
  }
  return nodes
})
const uiState = computed(() => subagentUIState(props.details?.message?.detail))
const dialogAgentAvatar = computed(() => {
  const agent = props.agents.find(item => item.id === props.details?.agentKey)
  return agent ? agentAvatar(agent) : ''
})

// 一次性拉取 transcript 回填进共享 detail（幂等，仅执行一次；有意改写共享的
// message.detail，与卡片依赖同一引用同步刷新）。实时事件可能先到达，由
// backfillSubagentTimeline 负责历史前置合并，这里不再按 timeline 非空拦截。
async function ensureBackfill() {
  const message = props.details?.message
  const detail = message?.detail || {}
  if (
    backfillPending
    || !message
    || detail.subagentBackfilled
    || !props.details?.sessionId
    || !props.details?.runId
  ) return
  const requestedRunKey = `${props.details.sessionId}:${props.details.runId}`
  backfillPending = true
  try {
    const history = await getSubagentTranscript(props.details.sessionId, props.details.runId)
    if (requestedRunKey !== `${props.details?.sessionId || ''}:${props.details?.runId || ''}`) return
    message.detail = backfillSubagentTimeline(message.detail, history?.messages || [])
  } catch {
    // 实时事件流仍可正常渲染，回填失败仅影响历史补齐。
  } finally {
    backfillPending = false
  }
}

// 仅在用户贴近底部时跟随新内容滚动，上翻阅读历史时不被打断。
const pinnedToBottom = ref(true)
function onConversationScroll() {
  const element = conversationEl.value
  if (!element) return
  pinnedToBottom.value = element.scrollHeight - element.scrollTop - element.clientHeight < 40
}

async function scrollConversationToBottom() {
  if (!props.open) return
  await nextTick()
  const element = conversationEl.value
  if (element && pinnedToBottom.value) element.scrollTop = element.scrollHeight
}

function setThinkingOpen(messageID, open) {
  thinkingOpenByID.value = { ...thinkingOpenByID.value, [messageID]: open }
}

function onKeydown(event) {
  if (props.open && event.key === 'Escape') emit('close')
}

watch(displayVersion, scrollConversationToBottom, { flush: 'post' })
watch(
  [() => props.open, () => `${props.details?.sessionId || ''}:${props.details?.runId || ''}`],
  ([open]) => {
    if (open) {
      void ensureBackfill()
      void scrollConversationToBottom()
    }
  },
  { immediate: true }
)

onMounted(() => {
  window.addEventListener('keydown', onKeydown)
})
onBeforeUnmount(() => {
  window.removeEventListener('keydown', onKeydown)
})
</script>

<template>
  <teleport to="body">
    <div v-show="open" class="subagent-dialog-backdrop" @click.self="emit('close')">
      <section class="subagent-dialog" role="dialog" aria-modal="true" :aria-label="`${details.agentName} ${t.subagentDetails}`">
        <header class="subagent-dialog__header">
          <span class="subagent-dialog__avatar"><Bot :size="17" /></span>
          <span class="subagent-dialog__title">
            <strong>{{ details.agentName }}</strong>
            <small>{{ details.agentKey }} · {{ t.subagentDetails }}</small>
          </span>
          <span class="subagent-dialog__status" :class="`is-${runtimeStatus}`">
            <LoaderCircle v-if="runtimeStatus === 'running'" class="spin" :size="14" />
            <CheckCircle2 v-else-if="runtimeStatus === 'completed'" :size="14" />
            <XCircle v-else :size="14" />
            {{ statusLabel }}
            <small v-if="displayDuration">· {{ formatDuration(displayDuration) }}</small>
          </span>
          <button type="button" :title="t.close" @click="emit('close')"><X :size="18" /></button>
        </header>

        <div ref="conversationEl" class="subagent-dialog__conversation" @scroll.passive="onConversationScroll">
          <p v-if="!displayMessages.length" class="subagent-dialog__notice">{{ t.subagentTranscriptEmpty }}</p>
          <div v-else class="subagent-dialog__messages">
            <section v-for="node in messageNodes" :key="node.key" class="subagent-dialog__node">
              <ChatMessageItem
                v-if="node.user"
                :message="node.user"
                :session-id="details.sessionId || 0"
                :agents="agents"
                :agent-avatar="dialogAgentAvatar"
                :agent-name="details.agentName"
                :user-avatar="userAvatar"
                :chat-layout="chatLayout"
                :show-identity="showIdentity"
                :now="0"
                :t="t"
                @artifact-error="emit('artifact-error', $event)"
              />
              <header v-if="node.agentMessages.length && showIdentity" class="subagent-dialog__node-header">
                <span class="subagent-dialog__node-avatar" :class="{ 'has-emoji': dialogAgentAvatar }">
                  <img v-if="isImageAvatar(dialogAgentAvatar)" :src="dialogAgentAvatar" class="subagent-dialog__node-img" alt="" />
                  <span v-else-if="dialogAgentAvatar" class="subagent-dialog__node-emoji">{{ dialogAgentAvatar }}</span>
                  <Bot v-else :size="19" />
                </span>
                <strong class="subagent-dialog__node-name">{{ details.agentName }}</strong>
              </header>
              <ChatMessageItem
                v-for="message in node.agentMessages"
                :key="message.id"
                :message="message"
                :session-id="details.sessionId || 0"
                :agents="agents"
                :agent-avatar="dialogAgentAvatar"
                :agent-name="details.agentName"
                :user-avatar="userAvatar"
                :chat-layout="chatLayout"
                :show-identity="showIdentity"
                :now="message.live ? now : 0"
                :t="t"
                @update-thinking-open="setThinkingOpen(message.id, $event)"
                @artifact-error="emit('artifact-error', $event)"
              />
            </section>
          </div>
        </div>

        <SubAgentInteraction
          v-if="details.sessionId && details.runId"
          :session-id="details.sessionId"
          :run-id="details.runId"
          :agent-key="details.agentKey"
          :ui-state="uiState"
          :t="t"
          @error="emit('artifact-error', $event)"
        />
      </section>
    </div>
  </teleport>
</template>

<style scoped>
.subagent-dialog-backdrop { position: fixed; inset: 0; z-index: 1200; isolation: isolate; display: grid; place-items: center; padding: 28px; background: rgb(0 0 0 / .42); }
.subagent-dialog { width: min(960px, 94vw); height: min(820px, 88vh); min-height: 420px; display: flex; flex-direction: column; contain: layout paint; overflow: hidden; border: 1px solid var(--border); border-radius: 14px; background: var(--surface); box-shadow: 0 24px 80px rgb(0 0 0 / .28); }
.subagent-dialog__header { flex: 0 0 auto; display: flex; align-items: center; gap: 10px; min-height: 58px; padding: 10px 14px; border-bottom: 1px solid var(--border); }
.subagent-dialog__avatar { flex: 0 0 auto; width: 32px; height: 32px; display: grid; place-items: center; border-radius: 9px; color: var(--amber); background: var(--amber-soft); }
.subagent-dialog__title { min-width: 0; flex: 1; display: grid; gap: 1px; }
.subagent-dialog__title strong { overflow: hidden; font-size: 14px; text-overflow: ellipsis; white-space: nowrap; }
.subagent-dialog__title small { color: var(--faint); font-size: 11px; }
.subagent-dialog__status { display: inline-flex; align-items: center; gap: 5px; color: var(--faint); font-size: 12px; white-space: nowrap; }
.subagent-dialog__status.is-completed { color: var(--success); }
.subagent-dialog__status.is-failed,.subagent-dialog__status.is-timeout,.subagent-dialog__status.is-aborted { color: var(--danger); }
.subagent-dialog__status small { color: inherit; }
.subagent-dialog__header > button { width: 30px; height: 30px; display: grid; place-items: center; border: 0; border-radius: 7px; color: var(--muted); background: transparent; cursor: pointer; }
.subagent-dialog__header > button:hover { color: var(--text); background: var(--surface-2); }
.subagent-dialog__conversation { min-height: 0; flex: 1; overflow: auto; padding: 22px clamp(16px, 4vw, 48px); background: color-mix(in srgb, var(--surface-2) 35%, var(--surface)); }
.subagent-dialog__messages { width: min(760px, 100%); margin: 0 auto; }
.subagent-dialog__node { margin-bottom: 22px; }
.subagent-dialog__node:last-child { margin-bottom: 0; }
.subagent-dialog__node-header { display: flex; align-items: center; gap: 10px; margin-bottom: 12px; }
.subagent-dialog__node-avatar { width: 27px; height: 27px; flex: 0 0 27px; display: grid; place-items: center; border-radius: 50%; background: transparent; color: #546fa5; font-size: 15px; line-height: 1; user-select: none; position: relative; overflow: hidden; }
.subagent-dialog__node-emoji { font-size: 16px; }
.subagent-dialog__node-img { position: absolute; inset: 0; width: 100%; height: 100%; object-fit: cover; border-radius: 50%; display: block; }
.subagent-dialog__node-name { color: var(--text); font-size: 14px; font-weight: 650; }
.subagent-dialog__messages :deep(.message) { content-visibility: auto; contain-intrinsic-size: auto 96px; }
.subagent-dialog__messages :deep(.message-body) { width: 100%; max-width: min(88%, 720px); }
.subagent-dialog__messages :deep(.message--tool .message-body),.subagent-dialog__messages :deep(.message--subagent .message-body) { max-width: 100%; }
.subagent-dialog__messages :deep(.tool-call),.subagent-dialog__messages :deep(.change-message) { width: min(680px, 82vw); }
.subagent-dialog__notice { display: flex; align-items: center; justify-content: center; gap: 7px; min-height: 120px; color: var(--muted); }
.subagent-dialog > :deep(.subagent-interaction) { flex: 0 0 auto; padding: 10px 14px 14px; border-top: 1px solid var(--border); background: var(--surface); }
@media (max-width: 700px) {
  .subagent-dialog-backdrop { padding: 0; }
  .subagent-dialog { width: 100vw; height: 100vh; max-height: none; border: 0; border-radius: 0; }
  .subagent-dialog__status small { display: none; }
  .subagent-dialog__conversation { padding: 16px 10px; }
}
</style>
