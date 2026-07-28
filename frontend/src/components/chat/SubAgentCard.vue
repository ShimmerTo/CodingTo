<script setup>
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Bot, CheckCircle2, FileCode2, LoaderCircle, XCircle } from 'lucide-vue-next'
import { getSubagentTranscript } from '../../backend.js'
import { formatDuration } from './chatFormatters.js'
import { toolInput, toolOutput, toolStatus } from './chatToolPresentation.js'
import {
  subagentActivity, subagentTimeline, subagentUIState
} from './subagentRuntime.js'
import SubAgentInteraction from './SubAgentInteraction.vue'
import { agentAvatar } from '../../composables/appContext.js'

const props = defineProps({
  message: { type: Object, required: true },
  messageItemComponent: { type: Object, required: true },
  agents: { type: Array, default: () => [] },
  now: { type: Number, default: 0 },
  t: { type: Object, required: true },
})

const emit = defineEmits(['open-details', 'artifact-error'])
const cardEl = ref(null)
const conversationEl = ref(null)
const thinkingOpenByID = ref({})
const transcript = ref([])
const transcriptLoaded = ref(false)
const transcriptVisible = ref(false)
let transcriptCache = new Map()
let transcriptObserver = null
let transcriptTimer = 0
let transcriptPending = false

function parsed(value) {
  if (typeof value !== 'string') return value
  try { return JSON.parse(value) } catch { return value }
}

function findObject(value, predicate) {
  const queue = [parsed(value)]
  const seen = new Set()
  while (queue.length && seen.size < 256) {
    const current = parsed(queue.shift())
    if (!current || typeof current !== 'object' || seen.has(current)) continue
    seen.add(current)
    if (predicate(current)) return current
    if (Array.isArray(current)) queue.push(...current)
    else queue.push(...Object.values(current))
  }
  return null
}

const input = computed(() => parsed(toolInput(props.message)) || {})
const outputResult = computed(() => (
  findObject(toolOutput(props.message), value => Boolean(value.runId && value.status)) || {}
))
const runtime = computed(() => props.message.detail?.subagent || (
  findObject(props.message.detail, value => value.kind === 'subagent_event') || {}
))
const result = computed(() => Object.keys(outputResult.value).length ? outputResult.value : runtime.value)
const runId = computed(() => result.value.runId || runtime.value.runId || '')
const agentKey = computed(() => result.value.agentKey || runtime.value.agentKey || input.value.key || props.t.subagentUnknown)
const agentName = computed(() => (
  result.value.agentName
  || runtime.value.agentName
  || props.agents.find(agent => agent.id === agentKey.value)?.name
  || agentKey.value
))
const agentAvatarValue = computed(() => {
  const agent = props.agents.find(agent => agent.id === agentKey.value)
  return agent ? agentAvatar(agent) : ''
})
const status = computed(() => result.value.status || (toolStatus(props.message) === 'done' ? 'completed' : 'running'))
const files = computed(() => Array.isArray(result.value.files) ? result.value.files : [])
const activity = computed(() => subagentActivity(runtime.value, {
  tool: props.t.subagentToolEvent,
  thinking: props.t.subagentThinking,
  responding: props.t.subagentResponding
}))
const timelineItems = computed(() => {
  const items = subagentTimeline(props.message.detail)
  if (items.length) return items
  const text = String(result.value.text || '')
  return text ? [{ id: 'result-content', kind: 'content', text, complete: true }] : []
})
const timelineMessages = computed(() => timelineItems.value.map(item => {
  if (item.kind === 'tool') {
    return {
      id: `subagent-${item.id}`,
      role: 'tool',
      content: item.name,
      live: !item.complete,
      detail: {
        type: item.complete ? 'tool_execution_end' : 'tool_execution_start',
        status: item.complete ? 'done' : 'running',
        toolCallId: item.toolCallId,
        name: item.name,
        toolName: item.name,
        args: item.input,
        startedAt: item.startedAt
      }
    }
  }
  if (item.kind === 'thinking') {
    return {
      id: `subagent-${item.id}`,
      role: 'assistant',
      content: '',
      thinkingContent: item.text,
      thinkingOpen: Boolean(thinkingOpenByID.value[`subagent-${item.id}`]),
      live: false
    }
  }
  return {
    id: `subagent-${item.id}`,
    role: 'assistant',
    content: item.text,
    thinkingContent: '',
    live: !item.complete
  }
}))
const displayMessages = computed(() => {
  const messages = transcriptLoaded.value ? transcript.value : timelineMessages.value
  return messages
    .filter(message => {
      if (message.role === 'user') return false
      if (message.role !== 'tool') return true
      const detail = message.detail || {}
      const name = detail.toolCall?.name || detail.name || detail.toolName || message.content
      return !['codingto_plan_present', 'codingto_plan_update'].includes(name)
    })
    .map(message => (
      message.role === 'assistant'
        ? { ...message, thinkingOpen: Boolean(thinkingOpenByID.value[message.id]) }
        : message
    ))
})
const displayVersion = computed(() => displayMessages.value.map(message => (
  `${message.id}:${message.content?.length || 0}:${message.thinkingContent?.length || 0}:${message.detail?.status || ''}:${message.detail?.output?.length || 0}`
)).join('|'))
const uiState = computed(() => subagentUIState(props.message.detail))
const sessionId = computed(() => {
  const value = findObject(props.message.detail, object => (
    object.codingToSessionId != null || object.sessionId != null
  ))
  return Number(value?.codingToSessionId ?? value?.sessionId) || 0
})
const duration = computed(() => {
  const detail = props.message.detail || {}
  return detail.durationMs || (detail.startedAt && status.value === 'running' ? (props.now || Date.now()) - detail.startedAt : 0)
})
const statusLabel = computed(() => {
  if (status.value === 'running') return props.t.subagentRunning
  if (status.value === 'completed') return props.t.subagentCompleted
  if (status.value === 'timeout') return props.t.subagentTimeout
  if (status.value === 'aborted') return props.t.subagentAborted
  return props.t.subagentFailed
})
const statusText = computed(() => (
  status.value === 'running' && activity.value ? activity.value : statusLabel.value
))

function messageFingerprint(message) {
  const json = JSON.stringify(message)
  let hash = 2166136261
  for (let index = 0; index < json.length; index += 1) {
    hash ^= json.charCodeAt(index)
    hash = Math.imul(hash, 16777619)
  }
  return `${json.length}:${hash >>> 0}`
}

function updateTranscript(messages) {
  const nextCache = new Map()
  let changed = transcript.value.length !== messages.length
  const next = messages.map((message, index) => {
    const key = `${message.id ?? 'message'}:${index}`
    const fingerprint = messageFingerprint(message)
    const cached = transcriptCache.get(key)
    const value = cached?.fingerprint === fingerprint ? cached.message : message
    if (value !== transcript.value[index]) changed = true
    nextCache.set(key, { fingerprint, message: value })
    return value
  })
  transcriptCache = nextCache
  if (changed) transcript.value = next
  transcriptLoaded.value = true
}

async function refreshTranscript() {
  if (
    transcriptPending
    || !transcriptVisible.value
    || !sessionId.value
    || !runId.value
  ) return
  const requestedRunKey = `${sessionId.value}:${runId.value}`
  transcriptPending = true
  try {
    const history = await getSubagentTranscript(sessionId.value, runId.value)
    if (requestedRunKey !== `${sessionId.value}:${runId.value}`) return
    updateTranscript(history?.messages || [])
  } catch {
    // Live semantic events remain available as a fallback.
  } finally {
    transcriptPending = false
  }
}

function stopTranscriptRefresh() {
  if (!transcriptTimer) return
  window.clearInterval(transcriptTimer)
  transcriptTimer = 0
}

watch(
  [transcriptVisible, sessionId, runId, status],
  ([visible, currentSessionID, currentRunID, currentStatus], previous = []) => {
    const previousRunKey = `${previous[1] || ''}:${previous[2] || ''}`
    const runKey = `${currentSessionID || ''}:${currentRunID || ''}`
    if (runKey !== previousRunKey) {
      transcript.value = []
      transcriptLoaded.value = false
      transcriptCache = new Map()
    }
    stopTranscriptRefresh()
    if (!visible || !currentSessionID || !currentRunID) return
    void refreshTranscript()
    if (currentStatus === 'running') {
      transcriptTimer = window.setInterval(() => void refreshTranscript(), 2000)
    }
  }
)

async function scrollConversationToBottom() {
  await nextTick()
  const element = conversationEl.value
  if (element) element.scrollTop = element.scrollHeight
}

watch(
  [displayVersion, () => result.value.error],
  scrollConversationToBottom,
  { flush: 'post' }
)
onMounted(() => {
  void scrollConversationToBottom()
  if (window.IntersectionObserver) {
    transcriptObserver = new IntersectionObserver(entries => {
      transcriptVisible.value = entries.some(entry => entry.isIntersecting)
    }, { rootMargin: '280px 0px' })
    if (cardEl.value) transcriptObserver.observe(cardEl.value)
  } else {
    transcriptVisible.value = true
  }
})
onBeforeUnmount(() => {
  stopTranscriptRefresh()
  transcriptObserver?.disconnect()
})

function setThinkingOpen(messageID, open) {
  thinkingOpenByID.value = { ...thinkingOpenByID.value, [messageID]: open }
}

function openDetails() {
  emit('open-details', {
    message: props.message,
    runId: runId.value,
    sessionId: sessionId.value,
    agentKey: agentKey.value,
    agentName: agentName.value,
    status: status.value,
    statusLabel: statusLabel.value,
    duration: duration.value,
  })
}
</script>

<template>
  <section ref="cardEl" class="subagent-card" :class="`is-${status}`">
    <header>
      <span class="subagent-card__icon">
        <span v-if="agentAvatarValue" class="subagent-card__emoji">{{ agentAvatarValue }}</span>
        <Bot v-else :size="16" />
      </span>
      <span class="subagent-card__main">
        <strong>{{ agentName }}</strong>
      </span>
      <span class="subagent-card__state" :title="statusText">
        <LoaderCircle v-if="status === 'running'" class="spin" :size="14" />
        <CheckCircle2 v-else-if="status === 'completed'" :size="14" />
        <XCircle v-else :size="14" />
        <span>{{ statusText }}</span>
        <small v-if="duration">{{ formatDuration(duration) }}</small>
      </span>
      <button type="button" :disabled="!runId" @click="openDetails">{{ t.subagentDetails }}</button>
    </header>

    <div ref="conversationEl" class="subagent-card__conversation" aria-live="polite">
      <component
        v-for="messageItem in displayMessages"
        :is="messageItemComponent"
        :key="messageItem.id"
        :message="messageItem"
        :agents="agents"
        :now="messageItem.live ? now : 0"
        :t="t"
        disable-subagent-card
        collapse-tools-by-default
        @update-thinking-open="setThinkingOpen(messageItem.id, $event)"
        @artifact-error="emit('artifact-error', $event)"
      />
      <component
        v-if="result.error"
        :is="messageItemComponent"
        :message="{ id: 'subagent-error', role: 'error', content: result.error }"
        :agents="agents"
        :now="0"
        :t="t"
        disable-subagent-card
        collapse-tools-by-default
        @artifact-error="emit('artifact-error', $event)"
      />
    </div>

    <SubAgentInteraction
      v-if="sessionId && runId"
      compact
      :session-id="sessionId"
      :run-id="runId"
      :agent-key="agentKey"
      :ui-state="uiState"
      :t="t"
      @error="emit('artifact-error', $event)"
    />

    <div v-if="files.length" class="subagent-card__files">
      <span v-for="file in files" :key="`${file.change}:${file.path}`" :title="file.path">
        <FileCode2 :size="12" />{{ String(file.path).split(/[\\/]/).pop() }}
      </span>
    </div>
  </section>
</template>

<style scoped>
.subagent-card { width: 100%; height: 42.857vh; height: 42.857dvh; min-height: 0; display: flex; flex-direction: column; contain: layout paint; border: 1px solid var(--border); border-radius: 11px; background: var(--surface-2); overflow: hidden; text-align: left; }
.subagent-card.is-completed { border-color: color-mix(in srgb, var(--success) 38%, var(--border)); }
.subagent-card.is-failed,.subagent-card.is-timeout,.subagent-card.is-aborted { border-color: color-mix(in srgb, var(--danger) 38%, var(--border)); }
.subagent-card > header { flex: 0 0 auto; min-height: 49px; display: flex; align-items: center; gap: 9px; padding: 9px 11px; background: var(--surface); }
.subagent-card__icon { flex: 0 0 auto; display: grid; place-items: center; width: 28px; height: 28px; border-radius: 8px; color: var(--amber); background: var(--amber-soft); }
.subagent-card__emoji { font-size: 16px; line-height: 1; }
.subagent-card__main { display: grid; gap: 1px; flex: 1; min-width: 80px; }
.subagent-card__main strong { overflow: hidden; font-size: 13px; text-overflow: ellipsis; white-space: nowrap; }
.subagent-card__main small { overflow: hidden; color: var(--faint); font-size: 10px; text-overflow: ellipsis; white-space: nowrap; }
.subagent-card__state { flex: 0 1 auto; min-width: 0; display: flex; align-items: center; justify-content: flex-end; gap: 5px; color: var(--faint); font-size: 11px; white-space: nowrap; }
.subagent-card__state > span { max-width: 190px; overflow: hidden; text-overflow: ellipsis; }
.subagent-card__state > small { color: inherit; font-variant-numeric: tabular-nums; }
.subagent-card.is-completed .subagent-card__state { color: var(--success); }
.subagent-card.is-failed .subagent-card__state,.subagent-card.is-timeout .subagent-card__state,.subagent-card.is-aborted .subagent-card__state { color: var(--danger); }
.subagent-card > header button { flex: 0 0 auto; padding: 5px 9px; border: 1px solid var(--border); border-radius: 7px; color: var(--text); background: transparent; cursor: pointer; }
.subagent-card > header button:hover:not(:disabled) { background: var(--surface-2); }
.subagent-card > header button:disabled { opacity: .45; cursor: default; }
.subagent-card__conversation { min-height: 0; flex: 1 1 auto; display: block; padding: 9px 10px; overflow-y: auto; overscroll-behavior: contain; scrollbar-gutter: stable; scrollbar-width: thin; scrollbar-color: var(--border) transparent; border-top: 1px solid var(--border-soft); }
.subagent-card__conversation :deep(.message) { width: 100%; margin-bottom: 4px; }
.subagent-card__conversation :deep(.message-body) { width: 100%; max-width: 100%; }
.subagent-card__conversation :deep(.message-bubble) { max-height: min(24vh, 190px); padding: 8px 10px; overflow: auto; overscroll-behavior: contain; scrollbar-width: thin; font-size: 12px; }
.subagent-card__conversation :deep(.message-markdown) { font-size: 12px; line-height: 1.55; }
.subagent-card__conversation :deep(.tool-call) { width: 100%; }
.subagent-card__conversation :deep(.tool-call summary) { min-height: 30px; padding: 4px 7px; font-size: 11px; }
.subagent-card__conversation :deep(.tool-call__name),.subagent-card__conversation :deep(.tool-call__summary) { font-size: 11px; }
.subagent-card__conversation :deep(.tool-call__details pre) { max-height: min(22vh, 160px); font-size: 11px; }
.subagent-card__conversation :deep(.thinking-block pre) { max-height: min(22vh, 160px); font-size: 11px !important; }
.subagent-card__conversation :deep(.message-error) { font-size: 12px; }
.subagent-card > :deep(.subagent-interaction),.subagent-card__files { flex: 0 0 auto; }
.subagent-card__files { display: flex; flex-wrap: wrap; gap: 6px; padding: 0 12px 10px; }
.subagent-card__files span { display: inline-flex; align-items: center; gap: 4px; max-width: 220px; padding: 4px 7px; overflow: hidden; border-radius: 6px; color: var(--muted); background: var(--surface); font-size: 11px; text-overflow: ellipsis; white-space: nowrap; }
@media (max-width: 720px) {
  .subagent-card__state > span { max-width: 90px; }
  .subagent-card__state > small { display: none; }
}
</style>
