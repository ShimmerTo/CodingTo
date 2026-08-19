<script setup>
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { Bot, CheckCircle2, Clock, FileCode2, LoaderCircle, Square, XCircle } from 'lucide-vue-next'
import { abortSubagent, getSubagentTranscript } from '../../backend.js'
import { formatDuration } from './chatFormatters.js'
import { toolInput, toolOutput, toolStatus } from './chatToolPresentation.js'
import {
  backfillSubagentTimeline, mergeSubagentRuntime, mergeSubagentUIState, resolvedSubagentStatus, subagentActivity,
  subagentTimelineMessages, subagentUIState
} from './subagentRuntime.js'
import SubAgentInteraction from './SubAgentInteraction.vue'
import ChatImagePreview from './ChatImagePreview.vue'
import { agentAvatar, isImageAvatar } from '../../composables/appContext.js'

const props = defineProps({
  message: { type: Object, required: true },
  messageItemComponent: { type: Object, required: true },
  agents: { type: Array, default: () => [] },
  now: { type: Number, default: 0 },
  showIdentity: { type: Boolean, default: true },
  t: { type: Object, required: true },
})

const emit = defineEmits(['open-details', 'artifact-error'])
const conversationEl = ref(null)
const thinkingOpenByID = ref({})
const aborting = ref(false)
const previewImage = ref(null)
let backfillPending = false

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
const result = computed(() => {
  const output = outputResult.value
  const live = runtime.value
  const status = resolvedSubagentStatus(output.status, live)
  if (live.status && live.status !== 'running') return { ...output, ...live, status }
  if (Object.keys(output).length) return { ...live, ...output, status }
  return { ...live, status }
})
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
// 纯事件驱动：消息流直接来自 mergeSubagentRuntime 增量维护的 subagentTimeline
// （text/thinking/tool 流式更新实时到达），历史场景由 backfill 一次性补齐，不轮询。
const displayMessages = computed(() => {
  const messages = subagentTimelineMessages(props.message.detail)
  if (messages.length) {
    return messages.map(message => (
      message.role === 'assistant' && message.thinkingContent
        ? { ...message, thinkingOpen: thinkingOpenByID.value[message.id] ?? !message.thinkingComplete }
        : message
    ))
  }
  const text = String(result.value.text || '')
  return text ? [{ id: 'subagent-result', role: 'assistant', content: text, live: false }] : []
})
const displayVersion = computed(() => {
  const messages = displayMessages.value
  // 空 timeline（子 agent 任务刚派发、尚无任何事件）时 indexes 必须为空数组，
  // 否则 messages[0] 为 undefined，message.id 抛 TypeError 导致整个卡片渲染崩溃。
  const indexes = messages.length
    ? [0, Math.floor(messages.length / 2), messages.length - 1]
      .filter((index, position, all) => index >= 0 && all.indexOf(index) === position)
    : []
  return `${messages.length}:${indexes.map(index => {
    const message = messages[index]
    return `${message?.id ?? ''}:${message?.content?.length || 0}:${message?.thinkingContent?.length || 0}:${message?.detail?.status || ''}:${message?.detail?.output == null ? 0 : String(message.detail.output).length}`
  }).join(';')}`
})
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
  if (status.value === 'aborted_requested') return props.t.subagentAborting
  if (status.value === 'completed') return props.t.subagentCompleted
  if (status.value === 'timeout') return props.t.subagentTimeout
  if (status.value === 'aborted') return props.t.subagentAborted
  return props.t.subagentFailed
})
// 任务简述：取自 codingto_subagent run 入参的 task 字段，用于在卡片顶部
// 直接展示当前子 agent 正在做什么，无需展开详情。
const taskText = computed(() => {
  const value = input.value && typeof input.value === 'object' && !Array.isArray(input.value) ? input.value : {}
  return String(value.task || '').trim()
})
// 距最后一次实时事件已过去的秒数。子 agent 的模型如果一次性返回（无流式
// token），事件会长时间中断，此时用 idle 提示替代“正在运行”的空转感。
const lastEventAt = computed(() => {
  const events = props.message.detail?.subagentEvents || []
  const last = events[events.length - 1]
  return last?.receivedAt || 0
})
const idleSeconds = computed(() => {
  if (!lastEventAt.value) return 0
  return Math.max(0, Math.floor(((props.now || Date.now()) - lastEventAt.value) / 1000))
})
const statusText = computed(() => {
  if (status.value !== 'running' && status.value !== 'aborted_requested') return statusLabel.value

  if (activity.value) return activity.value
  if (idleSeconds.value >= 5) return `${props.t.subagentThinking} (${idleSeconds.value}s)`
  return statusLabel.value
})

// 一次性拉取 transcript 回填进共享 detail（幂等，仅执行一次；有意改写共享的
// message.detail，依赖父级 reactive 数组的引用共享，卡片与弹窗同步刷新）。
// 运行中重新打开页面时实时事件可能已先到，由 backfillSubagentTimeline 负责
// 历史前置合并，这里不再按 timeline 非空拦截。
async function ensureBackfill() {
  const detail = props.message.detail || {}
  if (
    backfillPending
    || detail.subagentBackfilled
    || !sessionId.value
    || !runId.value
  ) return
  const requestedRunKey = `${sessionId.value}:${runId.value}`
  backfillPending = true
  try {
    const history = await getSubagentTranscript(sessionId.value, runId.value)
    if (requestedRunKey !== `${sessionId.value}:${runId.value}`) return
    if (history?.subagent) {
      props.message.detail = mergeSubagentRuntime(props.message.detail, {
        kind: 'subagent_event', ...history.subagent, event: null
      })
    }
    if (history?.subagentUi) {
      props.message.detail = {
        ...props.message.detail,
        subagentUI: mergeSubagentUIState(props.message.detail?.subagentUI, history.subagentUi)
      }
    }
    props.message.detail = backfillSubagentTimeline(props.message.detail, history?.messages || [])
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
  await nextTick()
  const element = conversationEl.value
  if (element && pinnedToBottom.value) element.scrollTop = element.scrollHeight
}

watch(displayVersion, scrollConversationToBottom, { flush: 'post' })
watch([sessionId, runId], () => { void ensureBackfill() }, { immediate: true })
onMounted(() => { void scrollConversationToBottom() })

function setThinkingOpen(messageID, open) {
  thinkingOpenByID.value = { ...thinkingOpenByID.value, [messageID]: open }
}

async function abortRun() {
  if (aborting.value || !sessionId.value || !runId.value) return
  aborting.value = true
  try {
    await abortSubagent(sessionId.value, runId.value)
    // Abort is a request, not proof of a terminal state. Keep the run visibly
    // in the stopping transition until run.json/follow-up confirms aborted.
    const detail = props.message.detail || {}
    props.message.detail = {
      ...detail,
      subagent: { ...(detail.subagent || {}), status: 'running', abortRequested: true },
    }
  } catch (err) {
    emit('artifact-error', String(err?.message || err))
  } finally {
    aborting.value = false
  }
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
  <section class="subagent-card" :class="`is-${status}`">
    <div class="subagent-card__body">
    <header>
      <span v-if="showIdentity" class="subagent-card__avatar" aria-hidden="true">
        <img v-if="isImageAvatar(agentAvatarValue)" :src="agentAvatarValue" class="subagent-card__avatar-img" alt="" />
        <span v-else-if="agentAvatarValue" class="subagent-card__avatar-emoji">{{ agentAvatarValue }}</span>
        <Bot v-else :size="15" />
      </span>
      <strong class="subagent-card__name">{{ agentName }}</strong>
      <span class="subagent-card__state" :title="statusText">
        <LoaderCircle v-if="status === 'running' || status === 'aborted_requested'" class="spin" :size="13" />
        <CheckCircle2 v-else-if="status === 'completed'" :size="13" />
        <XCircle v-else :size="13" />
      </span>
      <span class="subagent-card__status">{{ statusText }}</span>
      <span v-if="duration" class="subagent-card__duration" :title="t.subagentElapsed">
        <Clock :size="11" />{{ formatDuration(duration) }}
      </span>
      <span class="subagent-card__spacer"></span>
      <button type="button" :disabled="!runId" @click="openDetails">{{ t.subagentDetails }}</button>
      <button
        v-if="status === 'running' || status === 'aborted_requested'"
        type="button"
        class="subagent-card__abort"
        :disabled="aborting"
        :title="aborting ? t.subagentAborting : t.subagentStop"
        :aria-label="aborting ? t.subagentAborting : t.subagentStop"
        @click="abortRun"
      >
        <LoaderCircle v-if="aborting" class="spin" :size="13" />
        <Square v-else :size="12" />
      </button>
    </header>
    <div v-if="taskText" class="subagent-card__task" :title="taskText">{{ taskText }}</div>

    <div ref="conversationEl" class="subagent-card__conversation" aria-live="polite" @scroll.passive="onConversationScroll">
      <component
        v-for="messageItem in displayMessages"
        :is="messageItemComponent"
        :key="messageItem.id"
        :message="messageItem"
        :session-id="sessionId"
        :agents="agents"
        :now="messageItem.live ? now : 0"
        :t="t"
        :show-identity="false"
        disable-subagent-card
        collapse-tools-by-default
        @update-thinking-open="setThinkingOpen(messageItem.id, $event)"
        @artifact-error="emit('artifact-error', $event)"
        @preview-image="previewImage = $event"
      />
      <component
        v-if="result.error"
        :is="messageItemComponent"
        :message="{ id: 'subagent-error', role: 'error', content: result.error }"
        :session-id="sessionId"
        :agents="agents"
        :now="0"
        :t="t"
        :show-identity="false"
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
    </div>
    <transition name="preview-fade">
      <ChatImagePreview v-if="previewImage" :image="previewImage" :close-title="t.close" @close="previewImage = null" />
    </transition>
  </section>
</template>

<style scoped>
/* 引用块形态：顶部高亮色条（运行中为琥珀色渐变），顶部圆角，随内容自动扩展
   高度（最多对话区 44/100，比之前更紧凑）。头像与名字并列在顶部 header 左侧。 */
.subagent-card { width: 80%; max-height: 44vh; min-height: 0; display: flex; flex-direction: row; align-items: flex-start; text-align: left; }
.subagent-card__avatar { flex: 0 0 22px; width: 22px; height: 22px; display: grid; place-items: center; border-radius: 50%; color: #546fa5; font-size: var(--fs-12); line-height: 1; user-select: none; position: relative; overflow: hidden; }
.subagent-card__avatar-emoji { font-size: var(--fs-14); }
.subagent-card__avatar-img { position: absolute; inset: 0; width: 100%; height: 100%; object-fit: cover; border-radius: 50%; display: block; }
.subagent-card__body { flex: 1 1 auto; min-width: 0; min-height: 200px; max-height: 44vh; display: flex; flex-direction: column; border-top: 3px solid var(--amber); border-radius: 10px 10px 0 0; background: color-mix(in srgb, var(--surface-2) 45%, transparent); overflow: hidden; }
/* 运行中：顶部上边框改为高亮渐变（琥珀→透明渐隐），与右侧问题导航的 agent 横杠呼应；
   结束态回落到纯色（成功绿 / 失败红）。 */
.subagent-card.is-running .subagent-card__body, .subagent-card.is-aborted_requested .subagent-card__body { border-top-color: transparent; background: linear-gradient(90deg, color-mix(in srgb, var(--amber) 70%, transparent), color-mix(in srgb, var(--amber) 18%, transparent)) top / 100% 3px no-repeat, color-mix(in srgb, var(--surface-2) 45%, transparent); }
.subagent-card.is-completed .subagent-card__body { border-top-color: var(--success); }
.subagent-card.is-failed .subagent-card__body,.subagent-card.is-timeout .subagent-card__body,.subagent-card.is-aborted .subagent-card__body { border-top-color: var(--danger); }
.subagent-card__body > header { flex: 0 0 auto; min-height: 34px; display: flex; align-items: center; gap: 7px; padding: 5px 10px; }
.subagent-card__task { flex: 0 0 auto; min-width: 0; display: block; padding: 2px 10px 6px; overflow: hidden; color: var(--muted); font-size: var(--fs-12); line-height: 1.5; text-overflow: ellipsis; white-space: nowrap; }
.subagent-card__task::before { content: '· '; color: var(--amber); }
.subagent-card__state { flex: 0 0 auto; display: inline-flex; color: var(--amber); }
.subagent-card.is-completed .subagent-card__state { color: var(--success); }
.subagent-card.is-failed .subagent-card__state,.subagent-card.is-timeout .subagent-card__state,.subagent-card.is-aborted .subagent-card__state { color: var(--danger); }
.subagent-card__name { flex: 0 1 auto; min-width: 0; overflow: hidden; font-size: var(--fs-12); text-overflow: ellipsis; white-space: nowrap; }
.subagent-card__status { flex: 0 1 auto; min-width: 0; overflow: hidden; color: var(--faint); font-size: var(--fs-12); text-overflow: ellipsis; white-space: nowrap; }
.subagent-card__duration { flex: 0 0 auto; display: inline-flex; align-items: center; gap: 3px; padding: 1px 6px; border-radius: 9px; color: var(--amber); background: color-mix(in srgb, var(--amber) 12%, transparent); font-size: var(--fs-12); font-variant-numeric: tabular-nums; white-space: nowrap; }
.subagent-card.is-completed .subagent-card__duration { color: var(--success); background: color-mix(in srgb, var(--success) 12%, transparent); }
.subagent-card.is-failed .subagent-card__duration,.subagent-card.is-timeout .subagent-card__duration,.subagent-card.is-aborted .subagent-card__duration { color: var(--danger); background: color-mix(in srgb, var(--danger) 12%, transparent); }
.subagent-card__spacer { flex: 1 1 auto; }
.subagent-card__body > header button { flex: 0 0 auto; padding: 3px 8px; border: 1px solid var(--border); border-radius: 6px; color: var(--text); background: transparent; font-size: var(--fs-12); cursor: pointer; }
.subagent-card__body > header button:hover:not(:disabled) { background: var(--surface-2); }
.subagent-card__body > header button:disabled { opacity: .45; cursor: default; }
.subagent-card__body > header button.subagent-card__abort { display: inline-flex; align-items: center; justify-content: center; width: 24px; height: 24px; padding: 0; color: var(--danger); }
.subagent-card__body > header button.subagent-card__abort:hover:not(:disabled) { background: color-mix(in srgb, var(--danger) 12%, var(--surface-2)); }
.subagent-card__conversation { min-height: 108px; flex: 1 1 auto; display: block; margin: 0 6px 6px; padding: 4px 8px 8px; overflow-y: auto; overscroll-behavior: contain; scrollbar-gutter: stable; scrollbar-width: thin; scrollbar-color: var(--border) transparent; border: 1px solid var(--border-soft); border-radius: 8px; background: var(--surface); }
.subagent-card__conversation :deep(.message) { width: 100%; margin-bottom: 4px; }
.subagent-card__conversation :deep(.message-body) { width: 100%; max-width: 100%; }
.subagent-card__conversation :deep(.message-bubble) { padding: 8px 10px; font-size: var(--fs-12); }
.subagent-card__conversation :deep(.message-markdown) { font-size: var(--fs-12); line-height: 1.55; }
.subagent-card__conversation :deep(.tool-call) { width: 100%; max-width: 100%; }
.subagent-card__conversation :deep(.tool-call summary) { min-height: 30px; padding: 4px 7px; font-size: var(--fs-12); }
.subagent-card__conversation :deep(.tool-call__name),.subagent-card__conversation :deep(.tool-call__summary) { font-size: var(--fs-12); }
.subagent-card__conversation :deep(.tool-call__details pre) { max-height: min(22vh, 160px); font-size: var(--fs-12); }
.subagent-card__conversation :deep(.thinking-block) { max-width: 100%; }
.subagent-card__conversation :deep(.thinking-block pre) { max-height: min(36vh, 320px); overflow-y: auto; overscroll-behavior: contain; scrollbar-width: thin; scrollbar-color: var(--border) transparent; font-size: var(--fs-12) !important; }
.subagent-card__conversation :deep(.message-error) { font-size: var(--fs-12); }
.subagent-card__body > :deep(.subagent-interaction),.subagent-card__files { flex: 0 0 auto; }
.subagent-card__files { display: flex; flex-wrap: wrap; gap: 6px; padding: 0 10px 8px; }
.subagent-card__files span { display: inline-flex; align-items: center; gap: 4px; max-width: 220px; padding: 3px 7px; overflow: hidden; border-radius: 6px; color: var(--muted); background: var(--surface); font-size: var(--fs-12); text-overflow: ellipsis; white-space: nowrap; }
@media (max-width: 720px) {
  .subagent-card { width: 100%; }
  .subagent-card__status { display: none; }
}
</style>
