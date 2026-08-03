<script setup>
import { computed, ref, watch } from 'vue'
import { FileCog, Paperclip, PanelRightClose, PanelRightOpen } from 'lucide-vue-next'
import { buildT } from './i18n.js'
import ChatComposer from './components/chat/ChatComposer.vue'
import ChatHeader from './components/chat/ChatHeader.vue'
import ChatImagePreview from './components/chat/ChatImagePreview.vue'
import ChatMessages from './components/chat/ChatMessages.vue'
import ChatRightSidebar from './components/chat/ChatRightSidebar.vue'

const props = defineProps({
  config: { type: Object, required: true },
  agents: { type: Array, default: () => [] },
  messagesList: { type: Array, required: true },
  sessionId: { type: Number, required: true },
  running: { type: Boolean, default: false },
  stopping: { type: Boolean, default: false },
  connected: { type: Boolean, default: false },
  selectedAgent: { type: Object, default: null },
  draft: { type: String, default: '' },
  pendingPrompts: { type: Array, default: () => [] },
  modelOptions: { type: Array, default: () => [] },
  selectedModelValue: { type: String, default: '' },
  supportsImages: { type: Boolean, default: false },
  promptImages: { type: Array, default: () => [] },
  attachments: { type: Array, default: () => [] },
  attachmentsBusy: { type: Boolean, default: false },
  thinkingLevels: { type: Array, default: () => [] },
  thinkingLevel: { type: String, default: 'off' },
  skills: { type: Array, default: () => [] },
  selectedSkill: { type: Object, default: null },
  tokenStats: { type: Object, default: () => ({ input: 0, cached: 0, output: 0 }) },
  contextWindow: { type: Number, default: 0 },
  contextUsage: { type: Object, default: () => ({ tokens: 0, contextWindow: 0, percent: 0 }) },
  activeTitle: { type: String, default: '' },
  activeCreatedAt: { type: Number, default: 0 },
  executionElapsedMs: { type: Number, default: 0 },
  executionRunning: { type: Boolean, default: false },
  sessionChanges: { type: Object, default: () => ({ root: '', files: [], added: 0, deleted: 0 }) },
  sessionChangesLoading: { type: Boolean, default: false },
  documentPreviewRequest: { type: Object, default: null },
  documentArtifactFocus: { type: Object, default: null },
  planItems: { type: Array, default: () => [] },
  executionPlan: { type: Array, default: () => [] },
  extensionDialog: { type: Object, default: null },
  subagentDialogs: { type: Array, default: () => [] },
  compaction: { type: Object, default: () => ({ running: false, notice: '', error: '' }) },
  error: { type: String, default: '' },
  loadingHistory: { type: Boolean, default: false }
})

const emit = defineEmits([
  'update:draft', 'send', 'stop', 'select-agent', 'open-agent-config',
  'update:model', 'add-images', 'update:thinking',
  'update:skill',
  'remove-image', 'add-attachments', 'remove-attachment',
  'compact', 'respond-extension', 'ack-extension', 'clear-error',
  'respond-subagent-dialog', 'ack-subagent-dialog',
  'update-thinking-open', 'refresh-session-changes', 'edit-pending', 'delete-pending',
  'artifact-error'
])

const rightSidebarOpen = ref(false)
const previewImage = ref(null)
const changeFocusRequest = ref(null)
// 变更消息行尾斜箭头：请求打开 Git 对比框（由 ChatRightSidebar 复用 GitDiffDialog 处理）。
const changeDiffRequest = ref(null)

// 未读提示：顶部「文件改动」按钮。当本轮对话产生新的文件变动（git 改动或
// 用户输入附件）且用户尚未点开查看时，在按钮左侧显示红点；点击按钮后清除。
const inputArtifactCount = computed(() =>
  (props.sessionChanges?.nodes || []).reduce((sum, node) => sum + (node.inputArtifacts?.length || 0), 0)
)
const documentArtifactCount = computed(() =>
  (props.sessionChanges?.nodes || []).reduce((sum, node) => sum + (node.documentArtifacts?.length || 0), 0)
)

// 仅在确实存在文件改动或附件时展示「文件改动」按钮（含图标），避免出现 0 个文件/+0/-0/0 的空指示。
const changeSummaryVisible = computed(() =>
  !!(props.sessionChanges?.files?.length || props.sessionChanges?.added || props.sessionChanges?.deleted ||
    inputArtifactCount.value || documentArtifactCount.value)
)
// 仅当存在文件改动（新增/删除的文件或增删行）时才展示文件图标；仅有附件而无文件改动时隐藏该图标。
const hasFileChanges = computed(() =>
  !!(props.sessionChanges?.files?.length || props.sessionChanges?.added || props.sessionChanges?.deleted)
)
function changesSignature(changes) {
  const c = changes || {}
  return `${(c.files?.length) || 0}:${(c.added) || 0}:${(c.deleted) || 0}:${inputArtifactCount.value}:${documentArtifactCount.value}`
}
const seenChangesSignature = ref(changesSignature(props.sessionChanges))
const changesDotVisible = ref(false)
watch(
  () => changesSignature(props.sessionChanges),
  () => {
    // 红点仅用于「运行中」实时提示：运行中的变更才点亮；
    // 加载/切换历史会话（非运行态下签名变化）则重置，避免历史状态或串台残留红点。
    if (!props.running) {
      changesDotVisible.value = false
      return
    }
    const c = props.sessionChanges || {}
    if ((c.files?.length || c.added || c.deleted || inputArtifactCount.value)) changesDotVisible.value = true
  }
)
watch(
  () => props.documentPreviewRequest?.nonce,
  nonce => {
    if (!nonce) return
    rightSidebarOpen.value = true
    changesDotVisible.value = false
    emit('refresh-session-changes')
  }
)
watch(
  () => props.documentArtifactFocus?.nonce,
  nonce => {
    if (!nonce || !props.documentArtifactFocus?.nodeId) return
    rightSidebarOpen.value = true
    changesDotVisible.value = false
    changeFocusRequest.value = { nodeId: props.documentArtifactFocus.nodeId, nonce }
    emit('refresh-session-changes')
  }
)
function openChangesSummary() {
  rightSidebarOpen.value = true
  changesDotVisible.value = false
  seenChangesSignature.value = changesSignature(props.sessionChanges)
}

function openChangedFile(request) {
  if (!request?.path) return
  rightSidebarOpen.value = true
  changesDotVisible.value = false
  changeFocusRequest.value = { ...request, nonce: Date.now() }
  emit('refresh-session-changes')
}

// 变更消息行尾斜箭头：打开右侧边栏并请求 ChatRightSidebar 弹出该文件的 Git 对比框。
function openFileDiff(request) {
  if (!request?.path) return
  rightSidebarOpen.value = true
  changesDotVisible.value = false
  changeDiffRequest.value = { ...request, nonce: Date.now() }
  emit('refresh-session-changes')
}

// 关闭右侧边栏时清空未处理的 Git 对比请求：节点数据可能晚于请求到达，
// 若不清空，之后任意一次数据摘要变化都会延迟弹出对比框（幽灵弹窗）。
function closeSidebar() {
  rightSidebarOpen.value = false
  changeDiffRequest.value = null
}

// 顶栏按钮切换侧边栏：关闭时同样丢弃未处理的 Git 对比请求。
function toggleSidebar() {
  if (rightSidebarOpen.value) changeDiffRequest.value = null
  rightSidebarOpen.value = !rightSidebarOpen.value
}
const t = computed(() => buildT(props.config.preferences?.language || 'zh-CN'))
</script>

<template>
  <div class="chat-view">
    <main class="chat-main">
      <div class="chat-main__topbar">
        <div class="chat-main__topbar__actions">
          <button
            v-if="changeSummaryVisible"
            class="change-summary"
            type="button"
            :title="t.changeSummaryTitle"
            :aria-expanded="rightSidebarOpen"
            @click="openChangesSummary"
          >
            <span v-if="changesDotVisible" class="change-summary__dot" aria-hidden="true"></span>
            <FileCog v-if="hasFileChanges" :size="14" />
            <span v-if="sessionChanges.files?.length">{{ t.changeFileCount.replace('{count}', sessionChanges.files?.length) }}</span>
            <strong v-if="sessionChanges.added" class="change-count change-count--added">+{{ sessionChanges.added }}</strong>
            <strong v-if="sessionChanges.deleted" class="change-count change-count--deleted">-{{ sessionChanges.deleted }}</strong>
            <span v-if="inputArtifactCount" class="change-summary__attach" :title="t.changesInputArtifacts">
              <Paperclip :size="13" />
              {{ inputArtifactCount }}
            </span>
          </button>
          <button
            class="topbar-btn"
            type="button"
            :title="rightSidebarOpen ? t.rightSidebarClose : t.rightSidebarOpen"
            :aria-label="rightSidebarOpen ? t.rightSidebarClose : t.rightSidebarOpen"
            :aria-pressed="rightSidebarOpen"
            @click="toggleSidebar"
          >
            <component :is="rightSidebarOpen ? PanelRightClose : PanelRightOpen" :size="18" />
          </button>
        </div>
      </div>

      <ChatHeader
        :title="activeTitle"
        :created-at="activeCreatedAt"
        :connected="connected"
        :execution-elapsed-ms="executionElapsedMs"
        :execution-running="executionRunning"
        :t="t"
      />

      <ChatMessages
        :messages="messagesList"
        :session-id="sessionId"
        :agents="agents"
        :loading-history="loadingHistory"
        :t="t"
        :selected-agent="selectedAgent"
        @update-thinking-open="emit('update-thinking-open', $event)"
        @artifact-error="emit('artifact-error', $event)"
        @open-change-file="openChangedFile"
        @open-git-diff="openFileDiff"
      />

      <ChatComposer
        :config="config"
        :running="running"
        :stopping="stopping"
        :selected-agent="selectedAgent"
        :draft="draft"
        :pending-prompts="pendingPrompts"
        :model-options="modelOptions"
        :selected-model-value="selectedModelValue"
        :supports-images="supportsImages"
        :prompt-images="promptImages"
        :attachments="attachments"
        :attachments-busy="attachmentsBusy"
        :thinking-levels="thinkingLevels"
        :thinking-level="thinkingLevel"
        :skills="skills"
        :selected-skill="selectedSkill"
        :token-stats="tokenStats"
        :context-window="contextWindow"
        :context-usage="contextUsage"
        :plan-items="planItems"
        :execution-plan="executionPlan"
        :extension-dialog="extensionDialog"
        :subagent-dialogs="subagentDialogs"
        :compaction="compaction"
        :has-messages="messagesList.length > 0"
        :loading-history="loadingHistory"
        :t="t"
        @update:draft="emit('update:draft', $event)"
        @send="emit('send')"
        @edit-pending="emit('edit-pending', $event)"
        @delete-pending="emit('delete-pending', $event)"
        @stop="emit('stop')"
        @select-agent="emit('select-agent', $event)"
        @open-agent-config="emit('open-agent-config')"
        @update:model="emit('update:model', $event)"
        @add-images="emit('add-images', $event)"
        @update:thinking="emit('update:thinking', $event)"
        @update:skill="emit('update:skill', $event)"
        @remove-image="emit('remove-image', $event)"
        @add-attachments="emit('add-attachments', $event)"
        @remove-attachment="emit('remove-attachment', $event)"
        @preview-image="previewImage = $event"
        @compact="emit('compact')"
        @respond-extension="emit('respond-extension', $event)"
        @ack-extension="emit('ack-extension', $event)"
        @respond-subagent-dialog="emit('respond-subagent-dialog', $event)"
        @ack-subagent-dialog="emit('ack-subagent-dialog', $event)"
      />
    </main>

    <ChatRightSidebar
      :open="rightSidebarOpen"
      :session-id="sessionId"
      :changes="sessionChanges"
      :loading="sessionChangesLoading"
      :focus-request="changeFocusRequest"
      :diff-request="changeDiffRequest"
      :t="t"
      @close="closeSidebar"
      @refresh="emit('refresh-session-changes')"
      @artifact-error="emit('artifact-error', $event)"
    />

    <transition name="preview-fade">
      <ChatImagePreview v-if="previewImage" :image="previewImage" :close-title="t.close" @close="previewImage = null" />
    </transition>
  </div>
</template>

<style scoped src="./styles/chat/view.css"></style>
