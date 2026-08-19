<script setup>
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ArrowDown, Bot, LoaderCircle, TerminalSquare, X } from 'lucide-vue-next'
import { useChatAutoScroll } from '../../composables/useChatAutoScroll.js'
import ChatMessageItem from './ChatMessageItem.vue'
import ChatFoldBlock from './ChatFoldBlock.vue'
import SubAgentDetailsDialog from './SubAgentDetailsDialog.vue'
import { findActiveQuestionIndex } from './questionNavigation.js'
import { formatDuration } from './chatFormatters.js'
import { isSubagentRunTool, toolInput, toolOutput } from './chatToolPresentation.js'
import { resolvedSubagentStatus, subagentActivity } from './subagentRuntime.js'
import { agentAvatar, isImageAvatar, useAppContext } from '../../composables/appContext.js'
import { buildConciseRenderList, countConciseSteps, hasThinkingTrace } from './conciseChat.js'

const props = defineProps({
  messages: { type: Array, required: true },
  sessionId: { type: Number, required: true },
  agents: { type: Array, default: () => [] },
  loadingHistory: { type: Boolean, default: false },
  t: { type: Object, required: true },
  selectedAgent: { type: Object, default: null }
})

const { config } = useAppContext()
// 用户头像：取自个人信息设置，无则回退默认图标。
const userAvatar = computed(() => (config.userProfile && config.userProfile.avatar) || '')
// 对话展示形式：'side' 左右布局，'left' 靠左（默认）。
const chatLayout = computed(() => (config.preferences && config.preferences.chatLayout) || 'left')
// 头像昵称展示：默认开启，关闭后对话详情不再显示 agent / 用户头像与昵称。
const showIdentity = computed(() => !(config.preferences && config.preferences.showIdentity === false))
// 精简对话：默认关闭，开启后思考过程与工具调用折叠为精简摘要块。
const conciseChat = computed(() => config.preferences && config.preferences.conciseChat === true)

const emit = defineEmits(['update-thinking-open', 'artifact-error', 'open-change-file', 'open-git-diff', 'preview-image', 'open-settings'])

const PLAN_TOOL_NAMES = ['codingto_plan_present', 'codingto_plan_update']
const runtimeNow = ref(Date.now())
const finishingHistoryLoad = ref(false)
const activeQuestionIndex = ref(-1)
const subagentDetails = ref(null)
const cachedSubagentDetails = ref(null)
const messageListEl = ref(null)
let runtimeTimer = 0
let questionStateFrame = 0
let messagesResizeObserver = null
let stopRuntimeWatch = null

const displayMessages = computed(() => props.messages.filter(message => {
  if (message.role !== 'tool') return true
  const detail = message.detail || {}
  const name = detail.toolCall?.name || detail.name || detail.toolName || message.content
  return !PLAN_TOOL_NAMES.includes(name)
}))

const questionMessages = computed(() => displayMessages.value.filter(message => message.role === 'user'))
const questionIndexById = computed(() => new Map(
  questionMessages.value.map((message, index) => [message.id, index])
))

// ---- 子 Agent 快捷导航 ----
// 从消息流中收集 codingto_subagent 工具调用（每个子 agent 一次），构建右侧
// 导航项：运行中高亮渐变、结束后纯色；hover 显示 agent 名/任务/时长/动作。
function findSubagentOutput(message) {
  const raw = toolOutput(message)
  if (raw == null || raw === '') return null
  const queue = [raw]
  const seen = new Set()
  while (queue.length && seen.size < 128) {
    let current = queue.shift()
    if (typeof current === 'string') {
      try { current = JSON.parse(current) } catch { continue }
    }
    if (!current || typeof current !== 'object' || seen.has(current)) continue
    seen.add(current)
    if (current.runId && current.status) return current
    queue.push(...(Array.isArray(current) ? current : Object.values(current)))
  }
  return null
}

// codingto_subagent 的工具入参可能是 JSON 字符串（如 detail.args 以字符串形式
// 持久化），这里安全解析为对象，保证 agent 名/任务名可提取。
function parsedToolInput(value) {
  if (value && typeof value === 'object' && !Array.isArray(value)) return value
  if (typeof value !== 'string') return {}
  try {
    const parsed = JSON.parse(value)
    return parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? parsed : {}
  } catch {
    return {}
  }
}

function subagentNavInfo(message) {
  const detail = message.detail || {}
  const subagent = detail.subagent && typeof detail.subagent === 'object' ? detail.subagent : {}
  const inputObj = parsedToolInput(toolInput(message))
  const output = findSubagentOutput(message)
  const status = resolvedSubagentStatus(
    output?.status || '',
    subagent,
    (detail.status === 'done' || detail.type === 'tool_execution_end') ? 'completed' : 'running'
  )
  const live = status === 'running' || status === 'aborted_requested'
  const agentKey = subagent.agentKey || inputObj.key
  const agent = agentKey ? props.agents.find(candidate => candidate.id === agentKey) : undefined
  const agentName = subagent.agentName || agent?.name || agentKey || props.t.subagentUnknown
  // 头像：优先运行时数据，其次按 agentKey 在 agents 列表中匹配（emoji 字符或图片 data URL）。
  const avatar = (subagent.agentAvatar || (agent ? agentAvatar(agent) : '')) || ''
  const startedAt = Number(subagent.startedAt || detail.startedAt || 0)
  // 结束态优先用持久化时长（可能落在 detail / subagent / output 任一层），
  // 运行中用 now - startedAt 实时刷新。
  const duration = Number(detail.durationMs || subagent.durationMs || output?.durationMs || 0)
    || (startedAt && live ? Math.max(0, (runtimeNow.value || Date.now()) - startedAt) : 0)
  const task = String(inputObj.task || '').trim()
  const activity = subagentActivity(subagent, {
    tool: props.t.subagentToolEvent,
    thinking: props.t.subagentThinking,
    responding: props.t.subagentResponding
  })
  let statusText
  if (live) statusText = activity || props.t.subagentRunning
  else if (status === 'completed') statusText = props.t.subagentCompleted
  else if (status === 'aborted') statusText = props.t.subagentAborted
  else if (status === 'timeout') statusText = props.t.subagentTimeout
  else statusText = props.t.subagentFailed
  return { messageId: message.id, agentName, avatar, task, duration, statusText, live }
}

// ---- 右侧快捷导航（问题 + 子 Agent 按消息流顺序合并） ----
// 导航项必须与对话内出现顺序一致：用户问题开启节点，其回复中的子 Agent
// 调用紧随其后；并行派发的多个子 Agent 按调用先后排列。
const navItems = computed(() => {
  const items = []
  for (const message of displayMessages.value) {
    if (message.role === 'user') {
      items.push({
        type: 'question',
        key: `q-${message.id}`,
        index: questionIndexById.value.get(message.id),
        text: questionText(message)
      })
    } else if (isSubagentRunTool(message)) {
      // 仅 action=run 的子 Agent 调用生成导航项；list/status 等查询调用
      // 无独立子 Agent 执行，且拿不到 agentKey（名字会回退成占位文案），
      // 会与同一次 run 的导航项重复。与 SubAgentCard 的展示条件保持一致。
      items.push({
        type: 'subagent',
        key: `sub-${message.id}`,
        ...subagentNavInfo(message)
      })
    }
  }
  return items
})
// 子 Agent 索引由 navItems 唯一派生（subagentNavInfo 只计算一次，避免运行中
// 每秒 tick 对同一批子 Agent 重复 BFS）。
const subagentIndexById = computed(() => new Map(
  navItems.value
    .filter(item => item.type === 'subagent')
    .map((item, index) => [item.messageId, index])
))

function scrollToSubagent(index) {
  const element = scrollEl.value
  const target = element?.querySelector(`[data-subagent-index="${index}"]`)
  if (!element || !target) return
  element.scrollTo({
    top: Math.max(0, target.offsetTop - 24),
    behavior: 'smooth'
  })
}

// 节点头部的 Agent 身份：每个“问题节点”仅展示一次头像与名称。
const nodeAgentAvatar = computed(() => props.selectedAgent ? agentAvatar(props.selectedAgent) : '')
const nodeAgentName = computed(() => props.selectedAgent?.name || 'Agent')

// 把消息按“用户问题”切分为节点：一条用户消息开启一个新节点，其后直到下一条
// 用户消息之前的全部消息都属于该节点。节点内先放用户问题，再放 Agent 身份头与
// Agent 的回复，确保用户的问题始终排在 Agent 名称之前。
const questionNodes = computed(() => {
  const nodes = []
  let current = null
  let userMessages = []
  let agentMessages = []
  const pushCurrent = () => {
    if (current && (userMessages.length || agentMessages.length)) {
      nodes.push({ ...current, userMessages, agentMessages })
    }
  }
  let preIndex = 0
  for (const message of displayMessages.value) {
    if (message.role === 'user') {
      pushCurrent()
      current = {
        key: `node-${message.id}`,
        agentAvatar: nodeAgentAvatar.value,
        agentName: nodeAgentName.value
      }
      userMessages = [message]
      agentMessages = []
    } else {
      if (!current) {
        current = {
          key: `node-pre-${preIndex++}`,
          agentAvatar: nodeAgentAvatar.value,
          agentName: nodeAgentName.value
        }
        userMessages = []
        agentMessages = []
      }
      agentMessages.push(message)
    }
  }
  pushCurrent()
  return nodes
})

// 消息列表实际渲染的节点：精简模式用 conciseNodes（含折叠块），否则用原始节点。
const renderNodes = computed(() => conciseChat.value ? conciseNodes.value : questionNodes.value)

function needsRuntimeUpdate(message) {
  if (message.live) return true
  if (message.role !== 'tool') return false
  const detail = message.detail || {}
  return Boolean(detail.startedAt && detail.status !== 'done' && detail.type !== 'tool_execution_end')
}

// ---- 精简对话：把思考过程与工具调用折叠为摘要块 ----
// 以「思考输出的 content」为边界折叠思考与工具调用（见 conciseChat.js）。
const conciseNodes = computed(() => questionNodes.value.map(node => ({
  ...node,
  renderList: buildConciseRenderList(node.agentMessages)
})))

function conciseBlockNeedsRuntime(items) {
  return items.some(item => needsRuntimeUpdate(item.message))
}

// ---- 精简对话提示条：未开启精简且思考+工具调用超过 50 次时，顶部居中提示 ----
const CONCISE_HINT_DISMISS_KEY = 'codingto:concise-hint-dismissed'
const conciseHintDismissed = ref(false)
function loadConciseHintDismissed() {
  try { conciseHintDismissed.value = localStorage.getItem(CONCISE_HINT_DISMISS_KEY) === '1' } catch { conciseHintDismissed.value = false }
}
loadConciseHintDismissed()
function dismissConciseHint() {
  conciseHintDismissed.value = true
  try { localStorage.setItem(CONCISE_HINT_DISMISS_KEY, '1') } catch { /* ignore */ }
}
function openConciseSettings() {
  emit('open-settings')
}
const conciseStepCount = computed(() => countConciseSteps(displayMessages.value))
const showConciseHint = computed(() => (
  !conciseChat.value && !conciseHintDismissed.value && conciseStepCount.value > 50
))

const {
  scrollEl,
  showScrollToBottom,
  onMessagesScroll,
  onMessagesWheel,
  onMessagesResize,
  scrollToBottom,
  scrollToBottomInstant,
  scrollToBottomAndResume
} = useChatAutoScroll()

function questionText(message) {
  const text = String(message.content || '').replace(/\s+/g, ' ').trim()
  if (text) return text
  const { t } = props
  const names = [
    ...(message.attachments || []).map(att => att.name),
    ...(message.images || []).map(img => img.name)
  ].filter(Boolean)
  if (names.length) return names.join('、')
  return t.questionWithoutText
}

function updateActiveQuestion() {
  const element = scrollEl.value
  if (!element || questionMessages.value.length === 0) {
    activeQuestionIndex.value = -1
    return
  }
  const offsets = [...element.querySelectorAll('[data-question-index]')]
    .map(node => node.offsetTop)
  activeQuestionIndex.value = findActiveQuestionIndex(
    offsets,
    element.scrollTop,
    element.clientHeight
  )
}

function scheduleQuestionStateUpdate() {
  if (questionStateFrame) return
  questionStateFrame = window.requestAnimationFrame(() => {
    questionStateFrame = 0
    updateActiveQuestion()
  })
}

function onMessagesLayoutResize() {
  scheduleQuestionStateUpdate()
  void onMessagesResize()
}

function onScroll(event) {
  onMessagesScroll(event)
  scheduleQuestionStateUpdate()
}

function scrollToQuestion(index) {
  const element = scrollEl.value
  const target = element?.querySelector(`[data-question-index="${index}"]`)
  if (!element || !target) return
  activeQuestionIndex.value = index
  element.scrollTo({
    top: Math.max(0, target.offsetTop - 24),
    behavior: 'smooth'
  })
}

watch(() => props.loadingHistory, (now, was) => {
  if (was && !now) {
    finishingHistoryLoad.value = true
    nextTick(() => {
      scrollToBottomInstant()
      updateActiveQuestion()
      finishingHistoryLoad.value = false
    })
  }
})

watch(
  () => props.messages.map(message => `${message.id}:${message.content?.length || 0}:${message.thinkingContent?.length || 0}:${message.detail?.output?.length || 0}:${message.detail?.status || ''}`).join('|'),
  () => {
    if (!finishingHistoryLoad.value) scrollToBottom()
  }
)

watch(
  () => questionMessages.value.map(message => message.id),
  async (questionIDs, previousQuestionIDs = []) => {
    await nextTick()
    // Sending a new question is an explicit navigation action: always reveal
    // the newly-added user message and resume streaming auto-scroll, even when
    // the user had previously scrolled upward.
    if (!props.loadingHistory && questionIDs.length > previousQuestionIDs.length) {
      await scrollToBottom(true)
    }
    scheduleQuestionStateUpdate()
  }
)

function startRuntimeTimer() {
  if (runtimeTimer) return
  runtimeNow.value = Date.now()
  runtimeTimer = window.setInterval(() => { runtimeNow.value = Date.now() }, 1000)
}
function stopRuntimeTimer() {
  if (runtimeTimer) { window.clearInterval(runtimeTimer); runtimeTimer = 0 }
}

function openSubagentDetails(details) {
  cachedSubagentDetails.value = details
  subagentDetails.value = details
}

function closeSubagentDetails() {
  subagentDetails.value = null
}

onMounted(() => {
  // 详情弹窗打开时计时器也要继续走：弹窗内的运行时长、idle 秒数都依赖 now。
  stopRuntimeWatch = watch(
    () => props.messages.some(needsRuntimeUpdate),
    live => {
      if (live) startRuntimeTimer()
      else stopRuntimeTimer()
    },
    { immediate: true }
  )
  nextTick(updateActiveQuestion)
  if (window.ResizeObserver) {
    messagesResizeObserver = new ResizeObserver(onMessagesLayoutResize)
    if (scrollEl.value) messagesResizeObserver.observe(scrollEl.value)
    if (messageListEl.value) messagesResizeObserver.observe(messageListEl.value)
  }
})
watch(messageListEl, (element, previousElement) => {
  if (!messagesResizeObserver) return
  if (previousElement) messagesResizeObserver.unobserve(previousElement)
  if (element) messagesResizeObserver.observe(element)
})
onBeforeUnmount(() => {
  stopRuntimeWatch?.()
  stopRuntimeTimer()
  messagesResizeObserver?.disconnect()
  if (questionStateFrame) window.cancelAnimationFrame(questionStateFrame)
})
</script>

<template>
  <div class="chat-main__messages">
    <div v-if="showConciseHint" class="concise-hint" role="note">
      <span>{{ t.conciseHintPrefix }}</span>
      <button class="concise-hint__action" type="button" @click="openConciseSettings">{{ t.conciseHintAction }}</button>
      <span>{{ t.conciseHintSuffix }}</span>
      <button class="concise-hint__close" type="button" :title="t.conciseHintClose" :aria-label="t.conciseHintClose" @click="dismissConciseHint"><X :size="13" /></button>
    </div>
    <div
      ref="scrollEl"
      class="chat-main__scroll"
      :class="{ 'is-dialog-open': subagentDetails }"
      @scroll="onScroll"
      @wheel.passive="onMessagesWheel"
    >
      <div v-if="loadingHistory" class="chat-loading">
        <LoaderCircle class="spin" :size="30" />
        <span>{{ t.loadingHistory }}</span>
      </div>

      <div v-else-if="displayMessages.length === 0" class="chat-empty">
        <div class="chat-empty__icon"><TerminalSquare :size="26" /></div>
        <h2>{{ t.chatEmptyTitle }}</h2>
        <p>{{ t.chatEmptyHint }}</p>
      </div>

      <div v-else ref="messageListEl" class="message-list" :class="chatLayout === 'side' ? 'message-list--side' : ''">
        <section v-for="node in renderNodes" :key="node.key" class="message-node">
          <ChatMessageItem
            v-for="message in node.userMessages"
            :key="message.id"
            :message="message"
            :session-id="sessionId"
            :agents="agents"
            :agent-avatar="node.agentAvatar"
            :agent-name="node.agentName"
            :user-avatar="userAvatar"
            :chat-layout="chatLayout"
            :show-identity="showIdentity"
            :now="needsRuntimeUpdate(message) ? runtimeNow : 0"
            :t="t"
            :data-question-index="questionIndexById.get(message.id)"
            :data-subagent-index="subagentIndexById.get(message.id)"
            @update-thinking-open="emit('update-thinking-open', { id: message.id, open: $event })"
            @artifact-error="emit('artifact-error', $event)"
            @open-change-file="emit('open-change-file', $event)"
            @open-git-diff="emit('open-git-diff', $event)"
            @open-subagent-details="openSubagentDetails"
            @preview-image="emit('preview-image', $event)"
          />
          <header v-if="node.agentMessages.length && showIdentity" class="message-node__header">
            <span class="message-node__avatar" :class="{ 'has-emoji': node.agentAvatar }">
              <img v-if="isImageAvatar(node.agentAvatar)" :src="node.agentAvatar" class="message-node__img" alt="" />
              <span v-else-if="node.agentAvatar" class="message-node__emoji">{{ node.agentAvatar }}</span>
              <Bot v-else :size="19" />
            </span>
            <strong class="message-node__name">{{ node.agentName }}</strong>
          </header>
          <template v-if="conciseChat">
            <template
              v-for="entry in node.renderList"
              :key="entry.type === 'block' ? 'block-' + entry.items[0].message.id : 'msg-' + entry.message.id"
            >
              <ChatFoldBlock
                v-if="entry.type === 'block'"
                :items="entry.items"
                :session-id="sessionId"
                :agents="agents"
                :now="conciseBlockNeedsRuntime(entry.items) ? runtimeNow : 0"
                :t="t"
                :show-identity="showIdentity"
                :subagent-index-by-id="subagentIndexById"
                @update-thinking-open="emit('update-thinking-open', $event)"
                @artifact-error="emit('artifact-error', $event)"
                @open-change-file="emit('open-change-file', $event)"
                @open-git-diff="emit('open-git-diff', $event)"
                @open-subagent-details="openSubagentDetails"
                @preview-image="emit('preview-image', $event)"
              />
              <ChatMessageItem
                v-else
                :message="entry.message"
                :session-id="sessionId"
                :agents="agents"
                :agent-avatar="node.agentAvatar"
                :agent-name="node.agentName"
                :user-avatar="userAvatar"
                :chat-layout="chatLayout"
                :show-identity="showIdentity"
                :now="needsRuntimeUpdate(entry.message) ? runtimeNow : 0"
                :t="t"
                :hide-thinking="hasThinkingTrace(entry.message)"
                :data-subagent-index="subagentIndexById.get(entry.message.id)"
                @update-thinking-open="emit('update-thinking-open', { id: entry.message.id, open: $event })"
                @artifact-error="emit('artifact-error', $event)"
                @open-change-file="emit('open-change-file', $event)"
                @open-git-diff="emit('open-git-diff', $event)"
                @open-subagent-details="openSubagentDetails"
                @preview-image="emit('preview-image', $event)"
              />
            </template>
          </template>
          <template v-else>
            <ChatMessageItem
              v-for="message in node.agentMessages"
              :key="message.id"
              :message="message"
              :session-id="sessionId"
              :agents="agents"
              :agent-avatar="node.agentAvatar"
              :agent-name="node.agentName"
              :user-avatar="userAvatar"
              :chat-layout="chatLayout"
              :show-identity="showIdentity"
              :now="needsRuntimeUpdate(message) ? runtimeNow : 0"
              :t="t"
              :data-subagent-index="subagentIndexById.get(message.id)"
              @update-thinking-open="emit('update-thinking-open', { id: message.id, open: $event })"
              @artifact-error="emit('artifact-error', $event)"
              @open-change-file="emit('open-change-file', $event)"
              @open-git-diff="emit('open-git-diff', $event)"
              @open-subagent-details="openSubagentDetails"
              @preview-image="emit('preview-image', $event)"
            />
          </template>
        </section>
      </div>

    </div>
    <nav v-if="navItems.length" class="question-nav" :aria-label="t.questionNavigation">
      <template v-for="item in navItems" :key="item.key">
        <button
          v-if="item.type === 'subagent'"
          class="question-nav__item question-nav__item--subagent"
          :class="{ 'is-live': item.live }"
          type="button"
          :aria-label="`${item.agentName}${item.task ? ' · ' + item.task : ''}`"
          @click="scrollToSubagent(subagentIndexById.get(item.messageId))">
          <span class="question-nav__bar" aria-hidden="true"></span>
          <span class="question-nav__tooltip question-nav__tooltip--subagent" role="tooltip">
            <span class="question-nav__agent">
              <span class="question-nav__agent-avatar" :class="{ 'has-emoji': item.avatar }">
                <img v-if="isImageAvatar(item.avatar)" :src="item.avatar" class="question-nav__agent-img" alt="" />
                <span v-else-if="item.avatar" class="question-nav__agent-emoji">{{ item.avatar }}</span>
                <Bot v-else :size="13" />
              </span>
              <strong>{{ item.agentName }}</strong>
            </span>
            <span class="question-nav__meta"><template v-if="item.duration">{{ formatDuration(item.duration) }} · </template>{{ item.statusText }}</span>
            <small v-if="item.task">{{ item.task }}</small>
          </span>
        </button>
        <button
          v-else
          class="question-nav__item"
          :class="{ 'is-active': item.index === activeQuestionIndex }"
          type="button"
          :aria-label="item.text"
          :aria-current="item.index === activeQuestionIndex ? 'step' : undefined"
          @click="scrollToQuestion(item.index)">
          <span class="question-nav__bar" aria-hidden="true"></span>
          <span class="question-nav__tooltip" role="tooltip">{{ item.text }}</span>
        </button>
      </template>
    </nav>
    <button
      v-show="showScrollToBottom"
      class="scroll-to-bottom"
      type="button"
      :title="t.scrollToBottom"
      :aria-label="t.scrollToBottom"
      @click="scrollToBottomAndResume"
    ><ArrowDown :size="18" /></button>
    <SubAgentDetailsDialog
      v-if="cachedSubagentDetails"
      :open="Boolean(subagentDetails)"
      :details="cachedSubagentDetails"
      :agents="agents"
      :now="runtimeNow"
      :t="t"
      @close="closeSubagentDetails"
      @artifact-error="emit('artifact-error', $event)"
    />
  </div>
</template>

<style scoped src="../../styles/chat/messages.css"></style>
