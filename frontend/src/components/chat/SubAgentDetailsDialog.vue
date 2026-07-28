<script setup>
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Bot, CheckCircle2, LoaderCircle, X, XCircle } from 'lucide-vue-next'
import { getSubagentTranscript } from '../../backend.js'
import { formatDuration } from './chatFormatters.js'
import { toolOutput, toolStatus } from './chatToolPresentation.js'
import { subagentUIState } from './subagentRuntime.js'
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
const transcript = ref([])
const transcriptUI = ref({ widgets: {} })
const loading = ref(false)
const error = ref('')
let refreshTimer = 0
let refreshPending = false
let transcriptCache = new Map()

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
}

function resetTranscript() {
  transcript.value = []
  transcriptUI.value = { widgets: {} }
  transcriptCache = new Map()
  error.value = ''
}

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
const runtimeUI = computed(() => subagentUIState(props.details?.message?.detail))
const uiState = computed(() => {
  const live = runtimeUI.value
  if (live.dialog || Object.keys(live.widgets || {}).length) return live
  return transcriptUI.value
})
const displayTranscript = computed(() => transcript.value.filter(message => {
  if (message.role !== 'tool') return true
  const detail = message.detail || {}
  const name = detail.toolCall?.name || detail.name || detail.toolName || message.content
  return !['codingto_plan_present', 'codingto_plan_update'].includes(name)
}))

async function refresh(showLoading = false) {
  if (!props.open || refreshPending || !props.details?.sessionId || !props.details?.runId) return
  const requestedRunKey = `${props.details.sessionId}:${props.details.runId}`
  refreshPending = true
  if (showLoading && !transcript.value.length) loading.value = true
  try {
    const history = await getSubagentTranscript(props.details.sessionId, props.details.runId)
    const currentRunKey = `${props.details?.sessionId || ''}:${props.details?.runId || ''}`
    if (requestedRunKey !== currentRunKey) return
    updateTranscript(history?.messages || [])
    const nextUI = history?.subagentUi || { widgets: {} }
    if (JSON.stringify(nextUI) !== JSON.stringify(transcriptUI.value)) transcriptUI.value = nextUI
    error.value = ''
  } catch (reason) {
    if (!transcript.value.length) error.value = String(reason?.message || reason)
  } finally {
    refreshPending = false
    loading.value = false
    const currentRunKey = `${props.details?.sessionId || ''}:${props.details?.runId || ''}`
    if (props.open && requestedRunKey !== currentRunKey) void refresh(true)
  }
}

function stopRefresh() {
  if (!refreshTimer) return
  window.clearInterval(refreshTimer)
  refreshTimer = 0
}

function onKeydown(event) {
  if (props.open && event.key === 'Escape') emit('close')
}

watch(
  [
    () => props.open,
    runtimeStatus,
    () => `${props.details?.sessionId || ''}:${props.details?.runId || ''}`
  ],
  ([open, status, runKey], [wasOpen, , previousRunKey] = []) => {
    if (runKey !== previousRunKey) resetTranscript()
    stopRefresh()
    if (!open) return
    if (!wasOpen || runKey !== previousRunKey) void refresh(true)
    if (status === 'running') refreshTimer = window.setInterval(() => void refresh(), 1000)
  },
  { immediate: true }
)

onMounted(() => {
  window.addEventListener('keydown', onKeydown)
})
onBeforeUnmount(() => {
  stopRefresh()
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

        <div class="subagent-dialog__conversation">
          <p v-if="loading" class="subagent-dialog__notice"><LoaderCircle class="spin" :size="16" />{{ t.loadingHistory }}</p>
          <p v-else-if="error" class="subagent-dialog__error">{{ error }}</p>
          <p v-else-if="!displayTranscript.length" class="subagent-dialog__notice">{{ t.subagentTranscriptEmpty }}</p>
          <div v-else class="subagent-dialog__messages">
            <ChatMessageItem
              v-for="message in displayTranscript"
              :key="message.id"
              :message="message"
              :agents="agents"
              :now="message.live ? now : 0"
              :t="t"
              @artifact-error="emit('artifact-error', $event)"
            />
          </div>
        </div>

        <SubAgentInteraction
          :session-id="details.sessionId"
          :run-id="details.runId"
          :agent-key="details.agentKey"
          :ui-state="uiState"
          :t="t"
          @responded="refresh"
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
.subagent-dialog__messages :deep(.message) { content-visibility: auto; contain-intrinsic-size: auto 96px; }
.subagent-dialog__messages :deep(.message-body) { max-width: min(88%, 720px); }
.subagent-dialog__messages :deep(.tool-call),.subagent-dialog__messages :deep(.change-message) { width: min(680px, 82vw); }
.subagent-dialog__notice { display: flex; align-items: center; justify-content: center; gap: 7px; min-height: 120px; color: var(--muted); }
.subagent-dialog__error { color: var(--danger); text-align: center; }
.subagent-dialog > :deep(.subagent-interaction) { flex: 0 0 auto; padding: 10px 14px 14px; border-top: 1px solid var(--border); background: var(--surface); }
@media (max-width: 700px) {
  .subagent-dialog-backdrop { padding: 0; }
  .subagent-dialog { width: 100vw; height: 100vh; max-height: none; border: 0; border-radius: 0; }
  .subagent-dialog__status small { display: none; }
  .subagent-dialog__conversation { padding: 16px 10px; }
}
</style>
