<script setup>
import { computed, getCurrentInstance, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import {
  AlertCircle, ArrowUpRight, Bot, Check, CheckCircle2, ChevronDown, ChevronUp, Copy, File,
  FileAudio, FileCode2, FilePlus2, FileText, FileVideo, FileX2, GitBranch, Image, LoaderCircle, User
} from 'lucide-vue-next'
import { openExternal, openSessionArtifact, openSessionWorkspaceFile } from '../../backend.js'
import { localFileURL } from '../../localFileUrl.js'
import { formatDetail, formatDuration, imageSrc, renderMarkdown } from './chatFormatters.js'
import SubAgentCard from './SubAgentCard.vue'
import DocumentDownloadList from './DocumentDownloadList.vue'
import EditDiff from './EditDiff.vue'
import {
  toolDuration, toolIcon, toolInput, toolOutput, toolStatus,
  isReadTool, readToolMeta, readToolBlocks, isSubagentRunTool, toolEditDiff, toolFilePath, toolLineChanges, toolName, toolSummary, toolUrl, toolUrlTitle
} from './chatToolPresentation.js'
import { compactionMessageText } from './compactionMessages.js'
import { hasThinkingTrace } from './conciseChat.js'
import { isImageAvatar } from '../../composables/appContext.js'

const props = defineProps({
  message: { type: Object, required: true },
  // 会话级渲染由 ChatMessages 传入；该组件在子 Agent 卡片内通过
  // :is="messageItemComponent" 递归渲染子消息时不会透传 sessionId，
  // 故此处设为可选，避免无意义的「Missing required prop」告警。
  // sessionId 仅用于 DocumentDownloadList，子代理工具消息不会出现该组件。
  sessionId: { type: Number, default: 0 },
  agents: { type: Array, default: () => [] },
  now: { type: Number, required: true },
  t: { type: Object, required: true },
  disableSubagentCard: { type: Boolean, default: false },
  collapseToolsByDefault: { type: Boolean, default: false },
  agentAvatar: { type: String, default: '' },
  agentName: { type: String, default: '' },
  userAvatar: { type: String, default: '' },
  chatLayout: { type: String, default: 'left' },
  showIdentity: { type: Boolean, default: true },
  // 精简对话模式：思考过程已折叠到独立摘要块中，单独渲染本条消息时跳过思考块。
  hideThinking: { type: Boolean, default: false },
  // 精简对话模式：本条消息的内容/占位气泡已在摘要块外单独渲染，块内只保留思考块或工具调用。
  hideContent: { type: Boolean, default: false }
})

const emit = defineEmits(['update-thinking-open', 'artifact-error', 'open-change-file', 'open-git-diff', 'open-subagent-details', 'preview-image'])
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

// 模型在发起工具调用前常先输出一段仅含空白的 text（如 "\n\n"），
// 若直接渲染会得到一个空段落造成页面空白行。仅当存在非空白文本时才视为有可见内容。
const hasVisibleText = computed(() => {
  const c = props.message.content
  if (Array.isArray(c)) {
    return c.some(part => {
      if (typeof part === 'string') return part.trim().length > 0
      const text = part?.type === 'text'
        ? String(part.text ?? '')
        : String(part?.content ?? '')
      return text.trim().length > 0
    })
  }
  return String(c || '').trim().length > 0
})

// 判断整条消息是否有任一个可见的展示分支；否则该消息只是纯空白的空壳
// （例如模型工具调用前留下的 "\n\n" 文本被拆成独立 tool 消息后剩余的空壳），
// 继续渲染会占据一行空白，故在 article 层整条隐藏。
const hasVisibleMessage = computed(() => {
  const m = props.message
  if (m.role === 'compaction' || m.role === 'error' || m.role === 'tool') return true
  if (m.role === 'changes') return true // 空 changes 已由 isEmptyChangeMessage 拦截
  if (m.live) return true // 运行中保留状态/占位
  if (m.images?.length || m.attachments?.length) return true
  if (isSubagentTool.value && !props.disableSubagentCard) return true
  if (!props.hideThinking && hasThinkingTrace(m)) return true
  if (!props.hideContent && m.role !== 'tool' && m.role !== 'compaction' && hasVisibleText.value) return true
  if (m.role === 'user' && m.content) return true
  return false
})

// `now` changes while a live message/tool is running. Cache markdown by the
// actual message content so duration ticks never re-parse an unchanged answer.
const renderedContent = computed(() => renderMarkdown(props.message.content, {
  codeCopy: props.message.role === 'assistant'
    ? { copy: props.t.copyCode, copied: props.t.copiedCode }
    : null
}))
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
// 用户问题复制：点击复制问题原文，短暂显示“已复制”后回落。
const copied = ref(false)
let copyResetTimer = 0
async function copyUserMessage() {
  const text = String(props.message.content || '')
  if (!text) return
  try {
    await navigator.clipboard.writeText(text)
    copied.value = true
    clearTimeout(copyResetTimer)
    copyResetTimer = window.setTimeout(() => { copied.value = false }, 1600)
  } catch {
    // 剪贴板不可用时静默失败，不打断阅读。
  }
}
onBeforeUnmount(() => {
  clearTimeout(copyResetTimer)
  clearTimeout(codeCopyResetTimer)
})

// Markdown 使用 v-html 渲染，代码块复制按钮通过事件委托保持对流式更新有效。
const codeCopyState = ref(null)
let codeCopyResetTimer = 0
async function copyCodeBlock(event) {
  const button = event.target.closest?.('[data-code-copy]')
  if (!button) return
  const code = button.parentElement?.querySelector('pre code')
  if (!code) return
  try {
    await navigator.clipboard.writeText(code.textContent || '')
    if (codeCopyState.value && codeCopyState.value !== button) {
      const previous = codeCopyState.value
      previous.classList.remove('is-copied')
      previous.title = previous.dataset.copyLabel || props.t.copyCode
      previous.setAttribute('aria-label', previous.dataset.copyLabel || props.t.copyCode)
    }
    codeCopyState.value = button
    button.classList.add('is-copied')
    button.title = button.dataset.copiedLabel || props.t.copiedCode
    button.setAttribute('aria-label', button.dataset.copiedLabel || props.t.copiedCode)
    clearTimeout(codeCopyResetTimer)
    codeCopyResetTimer = window.setTimeout(() => {
      if (codeCopyState.value === button) {
        button.classList.remove('is-copied')
        button.title = button.dataset.copyLabel || props.t.copyCode
        button.setAttribute('aria-label', button.dataset.copyLabel || props.t.copyCode)
        codeCopyState.value = null
      }
    }, 1600)
  } catch {
    // 剪贴板不可用时静默失败，不打断阅读。
  }
}
const readTool = computed(() => isReadTool(props.message))
const readMeta = computed(() => readToolMeta(props.message) || { path: '', params: [] })
const readBlocks = computed(() => readToolBlocks(props.message) || [])
const toolPath = computed(() => toolFilePath(props.message))
const toolNameSlug = computed(() => {
  const raw = toolName(props.message).toLowerCase().replace(/[^a-z0-9]+/g, '-')
  return raw.replace(/^-+|-+$/g, '') || 'tool'
})
// 基础工具（edit/read/write/bash）已有专属图标，展示时省略工具名文本，
// 避免图标+名称重复占用一行空间；其余工具仍显示名称。
const BASIC_ICON_ONLY_TOOLS = /^(?:edit|read|write|bash)$/i
const hideToolName = computed(() => BASIC_ICON_ONLY_TOOLS.test(toolName(props.message)))
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
// 文件列表默认只展示前 5 项，超出部分由底部「显示全部」小按钮展开，避免变更消息过长。
const MAX_VISIBLE_CHANGED_FILES = 5
const filesExpanded = ref(false)
const visibleChangedFiles = computed(() =>
  filesExpanded.value ? changedFiles.value : changedFiles.value.slice(0, MAX_VISIBLE_CHANGED_FILES)
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

// 行尾斜箭头：直接弹出该文件的 Git 对比框（上层 ChatRightSidebar 复用唯一 GitDiffDialog 打开）。
// 注：changes 消息只出现在主会话时间线，SubAgentCard 内递归渲染的子代理消息无 changes 角色，
// 故子代理卡片内部无需透传 open-git-diff 事件。
function openGitDiff(file) {
  emit('open-git-diff', {
    nodeId: props.message.changes?.nodeId || '',
    path: file.path,
    source: file.source,
  })
}

async function openToolFile() {
  if (!toolPath.value || !props.sessionId) return
  try {
    await openSessionWorkspaceFile(toolPath.value, props.sessionId)
  } catch (err) {
    emit('artifact-error', String(err?.message || err))
  }
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
    v-if="!isEmptyChangeMessage && hasVisibleMessage"
    class="message"
    :class="[
      `message--${message.role}`,
      {
        'message--live': message.live,
        'message--subagent': isSubagentTool,
        'message--user-right': chatLayout === 'side' && message.role === 'user'
      }
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
        v-if="!hideThinking && hasThinkingTrace(message)"
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
          <small v-if="thinkingTimeText()" :title="thinkingTimeText()">{{ thinkingTimeText() }}</small>
          <ChevronDown class="details-chevron" :size="13" />
        </summary>
        <pre ref="thinkingPreRef">{{ (message.thinkingContent || '').replace(/\u200B/g, '') }}</pre>
      </details>
      <div v-if="message.role !== 'compaction' && message.role !== 'tool' && !hideContent && hasVisibleText" class="message-bubble">
        <div class="message-markdown" v-html="renderedContent" @click="copyCodeBlock"></div>
      </div>
      <div v-else-if="message.role === 'assistant' && message.live && message.preparing && !hideContent" class="message-bubble message-bubble--pending">
        <LoaderCircle class="spin" :size="14" />
        <span>{{ t.preparingSession }}</span>
      </div>
      <div v-else-if="message.role === 'assistant' && message.live && !hideContent" class="message-bubble message-bubble--pending">
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
          <!-- 外层用 div role=button（非 button 嵌套 button），支持行内独立「Git 对比」斜箭头按钮 -->
          <div
            v-for="file in visibleChangedFiles"
            :key="`${file.source?.runId || 'main'}:${file.path}`"
            class="change-message__file"
            role="button"
            tabindex="0"
            :title="t.changeMessageOpenFile.replace('{path}', file.path)"
            :aria-label="t.changeMessageOpenFile.replace('{path}', file.path)"
            @click="openChangedFile(file)"
            @keydown.enter.self="openChangedFile(file)"
            @keydown.space.self.prevent="openChangedFile(file)"
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
            <button
              class="change-message__diff"
              type="button"
              :title="t.changeMessageDiff"
              :aria-label="t.changeMessageDiff"
              @click.stop="openGitDiff(file)"
            >
              <ArrowUpRight :size="13" />
            </button>
          </div>
          <button
            v-if="changedFiles.length > MAX_VISIBLE_CHANGED_FILES"
            class="change-message__more"
            type="button"
            @click="filesExpanded = !filesExpanded"
          >
            <ChevronDown v-if="!filesExpanded" :size="12" />
            <ChevronUp v-else :size="12" />
            <span>{{ filesExpanded ? t.changeMessageCollapse : t.changeMessageShowAll.replace('{count}', changedFiles.length) }}</span>
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
          <span v-if="!hideToolName" class="tool-call__name">{{ toolName(message) }}</span>
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
          <button
            v-else-if="toolPath"
            class="tool-call__file tool-call__file--open"
            type="button"
            :disabled="!sessionId"
            :title="`${t.openOutputArtifact}: ${toolPath}`"
            @click.stop.prevent="openToolFile"
          >{{ toolPath }}</button>
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
          <span v-if="!hideToolName" class="tool-call__name">{{ toolName(message) }}</span>
          <button
            class="tool-call__file tool-call__file--open"
            type="button"
            :disabled="!readMeta.path || !sessionId"
            :title="readMeta.path ? `${t.openOutputArtifact}: ${readMeta.path}` : ''"
            @click.stop.prevent="openToolFile"
          >{{ readMeta.path }}</button>
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
              role="button"
              tabindex="0"
              :title="t.zoomImage"
              @click="emit('preview-image', block)"
              @keyup.enter="emit('preview-image', block)"
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
      <!-- 用户问题复制按钮：位于 v-if/v-else-if 渲染链之后，不打断分支配对。 -->
      <button
        v-if="isUser && message.content"
        class="message-copy-btn"
        type="button"
        :title="copied ? t.copiedMessage : t.copyMessage"
        :aria-label="copied ? t.copiedMessage : t.copyMessage"
        @click="copyUserMessage"
      >
        <Check v-if="copied" :size="12" />
        <Copy v-else :size="12" />
        <span>{{ copied ? t.copiedMessage : t.copyMessage }}</span>
      </button>
    </div>
  </article>
</template>

<style scoped src="../../styles/chat/message-item.css"></style>
