<script setup>
import { computed, getCurrentInstance } from 'vue'
import {
  AlertCircle, CheckCircle2, ChevronDown, ChevronRight, File, FileAudio,
  FileCode2, FilePlus2, FileText, FileVideo, FileX2, GitBranch, Image, LoaderCircle
} from 'lucide-vue-next'
import { openExternal, openSessionArtifact } from '../../backend.js'
import { localFileURL } from '../../localFileUrl.js'
import { formatDetail, formatDuration, imageSrc, renderMarkdown } from './chatFormatters.js'
import SubAgentCard from './SubAgentCard.vue'
import DocumentDownloadList from './DocumentDownloadList.vue'
import {
  toolDuration, toolIcon, toolInput, toolOutput, toolStatus,
  isSubagentRunTool, toolEditDiff, toolLineChanges, toolName, toolSummary, toolUrl, toolUrlTitle
} from './chatToolPresentation.js'
import { compactionMessageText } from './compactionMessages.js'

const props = defineProps({
  message: { type: Object, required: true },
  sessionId: { type: Number, required: true },
  agents: { type: Array, default: () => [] },
  now: { type: Number, required: true },
  t: { type: Object, required: true },
  disableSubagentCard: { type: Boolean, default: false },
  collapseToolsByDefault: { type: Boolean, default: false }
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
</script>

<template>
  <article
    v-if="!isEmptyChangeMessage"
    class="message"
    :class="[
      `message--${message.role}`,
      { 'message--live': message.live, 'message--subagent': isSubagentTool }
    ]"
  >
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
        <pre>{{ message.thinkingContent.replace(/\u200B/g, '') }}</pre>
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
        :t="t"
        @open-details="emit('open-subagent-details', $event)"
        @artifact-error="emit('artifact-error', $event)"
      />
      <template v-else-if="message.role === 'tool'">
      <details
        class="tool-call"
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
        <div v-if="editDiff" class="tool-edit-diff">
          <section v-for="edit in editDiff.edits" :key="`${edit.path}-${edit.index}`" class="tool-edit-diff__file">
            <header class="tool-edit-diff__head">
              <span>{{ edit.path || toolName(message) }}</span>
              <small>@@ -1,{{ edit.oldLineCount }} +1,{{ edit.newLineCount }} @@</small>
            </header>
            <div class="tool-edit-diff__lines">
              <div v-for="(line, index) in edit.lines" :key="index" class="tool-edit-diff__line" :class="`is-${line.kind}`">
                <span class="tool-edit-diff__number">{{ line.oldNumber ?? '' }}</span>
                <span class="tool-edit-diff__number">{{ line.newNumber ?? '' }}</span>
                <span class="tool-edit-diff__sign">{{ line.kind === 'added' ? '+' : line.kind === 'deleted' ? '-' : ' ' }}</span>
                <code>{{ line.text }}</code>
              </div>
            </div>
          </section>
          <div v-if="toolOutput(message)" class="tool-edit-diff__output">
            <label>{{ t.toolOutput }}</label>
            <pre>{{ formatDetail(toolOutput(message)) }}</pre>
          </div>
        </div>
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
