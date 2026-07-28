<script setup>
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ArrowDown, LoaderCircle, TerminalSquare } from 'lucide-vue-next'
import { useChatAutoScroll } from '../../composables/useChatAutoScroll.js'
import ChatMessageItem from './ChatMessageItem.vue'
import SubAgentDetailsDialog from './SubAgentDetailsDialog.vue'
import { findActiveQuestionIndex } from './questionNavigation.js'

const props = defineProps({
  messages: { type: Array, required: true },
  sessionId: { type: Number, required: true },
  agents: { type: Array, default: () => [] },
  loadingHistory: { type: Boolean, default: false },
  t: { type: Object, required: true }
})

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
  stopRuntimeWatch = watch(
    [
      () => props.messages.some(needsRuntimeUpdate),
      () => Boolean(subagentDetails.value)
    ],
    ([live, detailsOpen]) => {
      if (live && !detailsOpen) startRuntimeTimer()
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

      <div v-else ref="messageListEl" class="message-list">
        <ChatMessageItem
          v-for="message in displayMessages"
          :key="message.id"
          :message="message"
          :session-id="sessionId"
          :agents="agents"
          :now="needsRuntimeUpdate(message) ? runtimeNow : 0"
          :t="t"
          :data-question-index="message.role === 'user' ? questionIndexById.get(message.id) : undefined"
          @update-thinking-open="emit('update-thinking-open', { id: message.id, open: $event })"
          @artifact-error="emit('artifact-error', $event)"
          @open-change-file="emit('open-change-file', $event)"
          @open-subagent-details="openSubagentDetails"
        />
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
      :now="subagentDetails ? runtimeNow : 0"
      :t="t"
      @close="closeSubagentDetails"
      @artifact-error="emit('artifact-error', $event)"
    />
  </div>
</template>

<style scoped src="../../styles/chat/messages.css"></style>
