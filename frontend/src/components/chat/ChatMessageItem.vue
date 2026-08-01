<script setup>
import { computed, getCurrentInstance, nextTick, ref, watch } from 'vue'
import {
  AlertCircle, Bot, CheckCircle2, ChevronDown, ChevronRight, File, FileAudio,
  FileCode2, FilePlus2, FileText, FileVideo, FileX2, GitBranch, Image, LoaderCircle, User
} from 'lucide-vue-next'
import { openExternal, openSessionArtifact } from '../../backend.js'
import { localFileURL } from '../../localFileUrl.js'
import { formatDetail, formatDuration, imageSrc, renderMarkdown } from './chatFormatters.js'
import SubAgentCard from './SubAgentCard.vue'
import DocumentDownloadList from './DocumentDownloadList.vue'
import EditDiff from './EditDiff.vue'
import {
  toolDuration, toolIcon, toolInput, toolOutput, toolStatus,
  isReadTool, readToolMeta, readToolBlocks, isSubagentRunTool, toolEditDiff, toolLineChanges, toolName, toolSummary, toolUrl, toolUrlTitle
} from './chatToolPresentation.js'
import { compactionMessageText } from './compactionMessages.js'
import { isImageAvatar } from '../../composables/appContext.js'

const props = defineProps({
  message: { type: Object, required: true },
  sessionId: { type: Number, required: true },
  agents: { type: Array, default: () => [] },
  now: { type: Number, required: true },
  t: { type: Object, required: true },
  disableSubagentCard: { type: Boolean, default: false },
  collapseToolsByDefault: { type: Boolean, default: false },
  agentAvatar: { type: String, default: '' },
  agentName: { type: String, default: '' },
  userAvatar: { type: String, default: '' },
  chatLayout: { type: String, default: 'left' },
  showIdentity: { type: Boolean, default: true }
})

const emit = defineEmits(['update-thinking-open', 'artifact-error', 'open-change-file', 'open-subagent-details'])
const messageItemComponent = getCurrentInstance()?.type

function attachmentKindIcon(kind) {
  switch (kind) {
    case 'image': return Image
    case 'audio': return FileAudio
    case 'video': return FileVideo
    case 'document': return FileText
    default: return File
  }
}

function canOpenAttachment(att) {
  return Boolean(att && (att.absPath || att.path))
}

async function openAttachment(att) {
  if (!canOpenAttachment(att)) return
  try {
    if (att.absPath) {
      await openSessionArtifact(att.absPath)
      return
    }
    if (att.path) openExternal(localFileURL(att.path))
  } catch (err) {
    emit('artifact-error', String(err?.message || err))
  }
}

// `now` changes while a live message/tool is running. Cache markdown by the
// actual message content so duration ticks never re-parse an unchanged answer.
const renderedContent = computed(() => renderMarkdown(props.message.content))
const lineChanges = computed(() => toolLineChanges(props.message))
const editDiff = computed(() => toolEditDiff(props.message))
const documentDownloadList = computed(() => {
  if (toolName(props.message) !== 'codingto_document') return null
  const raw = toolOutput(props.message)
  if (!raw) return null
  let parsed = raw
  if (typeof raw === 'string') {
    try {
      parsed = JSON.parse(raw)
    } catch {
      // 工具输出文本可能带有 JSON 之外的说明文字，截取其中 JSON 片段再解析一次。
      const start = raw.indexOf('{')
      const end = raw.lastIndexOf('}')
      if (start !== -1 && end > start) {
        try {
          parsed = JSON.parse(raw.slice(start, end + 1))
        } catch {
          return null
        }
      } else {
        return null
      }
    }
  }
  const list = parsed?.downloadList ?? parsed?.details?.downloadList
  return Array.isArray(list) && list.length ? list : null
})
const isSubagentTool = computed(() => isSubagentRunTool(props.message))
const isUser = computed(() => props.message.role === 'user')
const readTool = computed(() => isReadTool(props.message))
const readMeta = computed(() => readToolMeta(props.message) || { path: '', params: [] })
const readBlocks = computed(() => readToolBlocks(props.message) || [])
const toolNameSlug = computed(() => {
  const raw = toolName(props.message).toLowerCase().replace(/[^a-z0-9]+/g, '-')
  return raw.replace(/^-+|-+$/g, '') || 'tool'
})
const agentAvatarValue = computed(() => props.agentAvatar || '')
const userAvatarValue = computed(() => props.userAvatar || '')
// 子 Agent 执行块（引用块形态）自带名称行且块内不显示任何头像，外层节点不再渲染头像列。
const changedFiles = computed(() =>
  [...(props.message.changes?.files || [])].sort((a, b) => (
    String(a.path).localeCompare(String(b.path)) ||
    String(a.source?.runId || '').localeCompare(String(b.source?.runId || ''))
  ))
)
// 本次问题未改动任何文件时，整条变更消息不渲染（历史缓存也可能带空 files）。
const isEmptyChangeMessage = computed(
  () => props.message.role === 'changes' && changedFiles.value.length === 0
)

function changeFileIcon(status) {
  if (status === 'added') return FilePlus2
  if (status === 'deleted') return FileX2
  return FileCode2
}

function changeFileName(path) {
  return String(path || '').split('/').pop() || path
}

function changeFileDir(path) {
  const parts = String(path || '').split('/')
  parts.pop()
  return parts.length ? `${parts.join('/')}/` : ''
}

function openChangedFile(file) {
  emit('open-change-file', {
    nodeId: props.message.changes?.nodeId || '',
    path: file.path,
    source: file.source,
  })
}

function thinkingDuration() {
  const message = props.message
  if (message.thinkingDurationMs) return message.thinkingDurationMs
  if (message.live && message.thinkingStartedAt) return props.now - message.thinkingStartedAt
  return 0
}

function thinkingStateText() {
  return props.message.live ? props.t.thinkingOngoing : props.t.thinkingDone
}

function thinkingTimeText() {
  const duration = thinkingDuration()
  return duration ? formatDuration(duration) : ''
}

function openToolUrl() {
  const url = toolUrl(props.message)
  if (url) openExternal(url)
}

function onThinkingToggle(event) {
  const open = event.currentTarget.open
  if (open !== Boolean(props.message.thinkingOpen)) emit('update-thinking-open', open)
}

const thinkingPreRef = ref(null)

watch(() => props.message.thinkingContent, async () => {
  if (!props.message.live || !props.message.thinkingOpen) return
  await nextTick()
  const el = thinkingPreRef.value
  if (el) el.scrollTop = el.scrollHeight
})
</script>

<template>
  <article
    v-if="!isEmptyChangeMessage"
    class="message"
    :class="[
      `message--${message.role}`,
      { 'message--live': message.live, 'message--subagent': isSubagentTool, 'message--user-right': chatLayout === 'side' && message.role === 'user' }
    ]"
  >
    <div
      v-if="showIdentity"
      class="message__avatar"
      :class="{ 'message__avatar--user': isUser }"
      aria-hidden="true"
    >
      <!-- 工具调用不显示头像，保留空列与助手消息主体左对齐 -->
      <template v-if="message.role !== 'tool'">
        <img v-if="!isUser && isImageAvatar(agentAvatarValue)" :src="agentAvatarValue" class="message__avatar-img" alt="" />
        <span v-else-if="!isUser && agentAvatarValue" class="message__avatar-emoji">{{ agentAvatarValue }}</span>
        <img v-else-if="isUser && userAvatarValue" :src="userAvatarValue" class="message__avatar-img" alt="" />
        <User v-else-if="isUser" :size="19" />
        <Bot v-else :size="19" />
      </template>
    </div>
    <div class="message-body">
      <div v-if="message.role === 'compaction'" class="message-compaction" role="status">
        <LoaderCircle v-if="message.status === 'running'" class="spin" :size="13" />
        <span>{{ compactionMessageText(message, t) }}</span>
      </div>
      <div v-if="message.images?.length" class="message-images">
        <img v-for="image in message.images" :key="image.name" :src="imageSrc(image)" :alt="image.name" />
      </div>
      <div v-if="message.attachments?.length" class="message-attachments">
        <span
          v-for="(att, index) in message.attachments"
          :key="`${att.name}-${index}`"
          class="message-attachments__item"
          :class="{ 'is-clickable': canOpenAttachment(att) }"
          :role="canOpenAttachment(att) ? 'button' : undefined"
          :tabindex="canOpenAttachment(att) ? 0 : undefined"
          :title="canOpenAttachment(att) ? t.openInputArtifact : undefined"
          @click="canOpenAttachment(att) && openAttachment(att)"
          @keyup.enter="canOpenAttachment(att) && openAttachment(att)"
        >
          <component :is="attachmentKindIcon(att.kind)" :size="13" />
          <span class="message-attachments__name">{{ att.name }}</span>
        </span>
      </div>
      <details
        v-if="message.thinkingContent && message.thinkingContent.replace(/\u200B/g, '').trim()"
        class="thinking-block"
        :class="{ 'thinking-live': !!message.live }"
        :open="!!message.thinkingOpen"
        @toggle="onThinkingToggle"
      >
        <summary>
          <span class="activity-state">
            <LoaderCircle v-if="message.live" class="spin" :size="13" />
            <CheckCircle2 v-else :size="13" />
          </span>
          <span>{{ thinkingStateText() }}</span>
          <small v-if="thinkingTimeText()">{{ thinkingTimeText() }}</small>
          <ChevronDown class="details-chevron" :size="13" />
        </summary>
        <pre ref="thinkingPreRef">{{ message.thinkingContent.replace(/\u200B/g, '') }}</pre>
      </details>
      <div v-if="message.role !== 'compaction' && message.role !== 'tool' && message.content" class="message-bubble">
        <div class="message-markdown" v-html="renderedContent"></div>
      </div>
      <div v-else-if="message.role === 'assistant' && message.live" class="message-bubble message-bubble--pending">
        <LoaderCircle class="spin" :size="14" />
        <span>{{ t.statusRunning }}</span>
      </div>
      <section v-else-if="message.role === 'changes'" class="change-message">
        <header class="change-message__head">
          <span class="change-message__icon"><GitBranch :size="14" /></span>
          <strong>
            {{ t.changeMessageTitle.replace('{count}', changedFiles.length) }}
          </strong>
          <span v-if="changedFiles.length" class="change-message__totals">
            <b class="is-added">+{{ message.changes?.added || 0 }}</b>
            <b class="is-deleted">-{{ message.changes?.deleted || 0 }}</b>
          </span>
        </header>
        <div v-if="changedFiles.length" class="change-message__files">
          <button
            v-for="file in changedFiles"
            :key="`${file.source?.runId || 'main'}:${file.path}`"
            class="change-message__file"
            type="button"
            :title="t.changeMessageOpenFile.replace('{path}', file.path)"
            @click="openChangedFile(file)"
          >
            <component :is="changeFileIcon(file.status)" :size="14" :class="`is-${file.status}`" />
            <span class="change-message__path">
              <strong>{{ changeFileName(file.path) }}</strong>
              <small v-if="changeFileDir(file.path)">{{ changeFileDir(file.path) }}</small>
            </span>
            <span class="change-message__counts">
              <small v-if="file.binary">BIN</small>
              <template v-else>
                <b class="is-added">+{{ file.added || 0 }}</b>
                <b class="is-deleted">-{{ file.deleted || 0 }}</b>
              </template>
            </span>
            <ChevronRight :size="13" />
          </button>
        </div>
      </section>
      <SubAgentCard
        v-else-if="isSubagentTool && !disableSubagentCard"
        :message="message"
        :message-item-component="messageItemComponent"
        :agents="agents"
        :now="now"
        :show-identity="showIdentity"
        :t="t"
        @open-details="emit('open-subagent-details', $event)"
        @artifact-error="emit('artifact-error', $event)"
      />
      <template v-else-if="message.role === 'tool'">
      <details
        v-if="!readTool"
        class="tool-call tool-call--generic"
        :class="`tool-call--${toolNameSlug}`"
        :open="!collapseToolsByDefault && !!editDiff"
      >
        <summary>
          <span class="tool-call__state">
            <CheckCircle2 v-if="toolStatus(message) === 'done'" :size="14" />
            <LoaderCircle v-else class="spin" :size="14" />
          </span>
          <span class="tool-call__icon"><component :is="toolIcon(message)" :size="13" /></span>
          <span class="tool-call__name">{{ toolName(message) }}</span>
          <a
            v-if="toolUrl(message)"
            class="tool-call__link"
            :href="toolUrl(message)"
            :title="t.openInBrowser"
            @click.stop.prevent="openToolUrl"
          >
            <span v-if="toolUrlTitle(message)" class="tool-call__link-title">{{ toolUrlTitle(message) }}</span>
            <span class="tool-call__link-url">{{ toolUrl(message) }}</span>
          </a>
          <span v-else-if="toolSummary(message)" class="tool-call__summary">{{ toolSummary(message) }}</span>
          <span v-if="lineChanges" class="tool-call__changes" :aria-label="`${lineChanges.files} ${t.fileUnit}, +${lineChanges.added} ${t.lineUnit}, -${lineChanges.deleted} ${t.lineUnit}`">
            <b class="is-files">{{ lineChanges.files }} {{ t.fileUnit }}</b>
            <b class="is-added">+{{ lineChanges.added }} {{ t.lineUnit }}</b>
            <b class="is-deleted">-{{ lineChanges.deleted }} {{ t.lineUnit }}</b>
          </span>
          <small v-if="toolStatus(message) !== 'done'">{{ formatDuration(toolDuration(message, now)) }}</small>
          <ChevronDown class="details-chevron" :size="13" />
        </summary>
        <EditDiff v-if="editDiff" :message="message" :t="t" />
        <div v-else class="tool-call__details">
          <div v-if="toolInput(message)">
            <label>{{ t.toolInput }}</label>
            <pre>{{ formatDetail(toolInput(message)) }}</pre>
          </div>
          <div v-if="toolOutput(message)">
            <label>{{ t.toolOutput }}</label>
            <pre>{{ formatDetail(toolOutput(message)) }}</pre>
          </div>
        </div>
      </details>
      <details v-else class="tool-call tool-call--read" :open="!collapseToolsByDefault">
        <summary>
          <span class="tool-call__state">
            <CheckCircle2 v-if="toolStatus(message) === 'done'" :size="14" />
            <LoaderCircle v-else class="spin" :size="14" />
          </span>
          <span class="tool-call__icon"><component :is="toolIcon(message)" :size="13" /></span>
          <span class="tool-call__name">{{ toolName(message) }}</span>
          <span class="tool-call__file" :title="readMeta.path">{{ readMeta.path }}</span>
          <span v-if="readMeta.params.length" class="tool-call__params">{{ readMeta.params.join(', ') }}</span>
          <ChevronDown class="details-chevron" :size="13" />
        </summary>
        <div class="tool-call__read">
          <template v-for="(block, bi) in readBlocks" :key="bi">
            <pre v-if="block.kind === 'text'">{{ block.text }}</pre>
            <img
              v-else-if="block.kind === 'image'"
              class="tool-call__read-img"
              :src="`data:${block.mimeType};base64,${block.data}`"
              alt="read image"
            />
          </template>
        </div>
      </details>
      <DocumentDownloadList v-if="documentDownloadList" :files="documentDownloadList" :session-id="sessionId" :t="t" />
      </template>
      <div v-else-if="message.role === 'error'" class="message-error" role="alert">
        <AlertCircle :size="15" />
        <span class="message-error__text">{{ message.content }}</span>
      </div>
    </div>
  </article>
</template>

<style scoped src="../../styles/chat/message-item.css"></style>
