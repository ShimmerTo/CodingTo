<script setup>
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ArrowDown, Bot, LoaderCircle, TerminalSquare } from 'lucide-vue-next'
import { useChatAutoScroll } from '../../composables/useChatAutoScroll.js'
import ChatMessageItem from './ChatMessageItem.vue'
import SubAgentDetailsDialog from './SubAgentDetailsDialog.vue'
import { findActiveQuestionIndex } from './questionNavigation.js'
import { agentAvatar, isImageAvatar, useAppContext } from '../../composables/appContext.js'

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

const emit = defineEmits(['update-thinking-open', 'artifact-error', 'open-change-file'])

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

function needsRuntimeUpdate(message) {
  if (message.live) return true
  if (message.role !== 'tool') return false
  const detail = message.detail || {}
  return Boolean(detail.startedAt && detail.status !== 'done' && detail.type !== 'tool_execution_end')
}

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
        <section v-for="node in questionNodes" :key="node.key" class="message-node">
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
            @update-thinking-open="emit('update-thinking-open', { id: message.id, open: $event })"
            @artifact-error="emit('artifact-error', $event)"
            @open-change-file="emit('open-change-file', $event)"
            @open-subagent-details="openSubagentDetails"
          />
          <header v-if="node.agentMessages.length && showIdentity" class="message-node__header">
            <span class="message-node__avatar" :class="{ 'has-emoji': node.agentAvatar }">
              <img v-if="isImageAvatar(node.agentAvatar)" :src="node.agentAvatar" class="message-node__img" alt="" />
              <span v-else-if="node.agentAvatar" class="message-node__emoji">{{ node.agentAvatar }}</span>
              <Bot v-else :size="19" />
            </span>
            <strong class="message-node__name">{{ node.agentName }}</strong>
          </header>
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
            @update-thinking-open="emit('update-thinking-open', { id: message.id, open: $event })"
            @artifact-error="emit('artifact-error', $event)"
            @open-change-file="emit('open-change-file', $event)"
            @open-subagent-details="openSubagentDetails"
          />
        </section>
      </div>

    </div>
    <nav v-if="questionMessages.length" class="question-nav" :aria-label="t.questionNavigation">
      <button
        v-for="(question, index) in questionMessages"
        :key="question.id"
        class="question-nav__item"
        :class="{ 'is-active': index === activeQuestionIndex }"
        type="button"
        :aria-label="questionText(question)"
        :aria-current="index === activeQuestionIndex ? 'step' : undefined"
        @click="scrollToQuestion(index)"
      >
        <span class="question-nav__bar" aria-hidden="true"></span>
        <span class="question-nav__tooltip" role="tooltip">{{ questionText(question) }}</span>
      </button>
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
