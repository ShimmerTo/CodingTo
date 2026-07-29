<script setup>
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import {
  Bot, Brain, Check, ChevronDown, CircleStop, File, FileAudio, FileText, FileVideo, Image,
  LoaderCircle, Paperclip, Pencil, Send, Sparkles, Trash2, X
} from 'lucide-vue-next'
import { formatFileSize, formatTokenCount, imageSrc } from './chatFormatters.js'
import { agentAvatar } from '../../composables/appContext'
import ChatExecutionPlan from './ChatExecutionPlan.vue'
import ChatPlanPanel from './ChatPlanPanel.vue'

const props = defineProps({
  config: { type: Object, required: true },
  running: { type: Boolean, default: false },
  stopping: { type: Boolean, default: false },
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
  tokenStats: { type: Object, required: true },
  contextWindow: { type: Number, default: 0 },
  contextUsage: { type: Object, required: true },
  planItems: { type: Array, default: () => [] },
  executionPlan: { type: Array, default: () => [] },
  extensionDialog: { type: Object, default: null },
  compaction: { type: Object, required: true },
  hasMessages: { type: Boolean, default: false },
  loadingHistory: { type: Boolean, default: false },
  t: { type: Object, required: true }
})

const emit = defineEmits([
  'update:draft', 'send', 'stop', 'select-agent', 'open-agent-config',
  'update:model', 'add-images', 'update:thinking',
  'update:skill',
  'remove-image', 'preview-image', 'compact', 'respond-extension',
  'edit-pending', 'delete-pending',
  'add-attachments', 'remove-attachment'
])

const agentSwitcherOpen = ref(false)
const agentWrapEl = ref(null)
const modelMenuOpen = ref(false)
const modelWrapEl = ref(null)
const skillMenuOpen = ref(false)
const skillWrapEl = ref(null)
const textareaEl = ref(null)

const modelGroups = computed(() => {
  const groups = {}
  for (const option of props.modelOptions) (groups[option.provider] ||= []).push(option)
  return Object.entries(groups).map(([provider, options]) => ({ provider, options }))
})
const selectedModelLabel = computed(() => {
  return props.modelOptions.find(option => option.value === props.selectedModelValue)?.model || props.selectedModelValue
})
const selectedSkillOptions = computed(() => {
  const agentID = props.selectedAgent?.id
  if (!agentID) return []
  // Show skills assigned to this agent, plus pi-launched skills that are
  // globally available to every agent (sourceType === 'pi', loaded at startup).
  return props.skills.filter(skill =>
    (skill.agents || []).some(agent => agent.id === agentID) ||
    skill.sourceType === 'pi'
  )
})

function skillTypeLabel(skill) {
  return skill.loadMode === 'startup'
    ? (props.t.skillTypeStartup || '随启动')
    : (props.t.skillTypeOndemand || '按需')
}
function chooseSkill(id) {
  emit('update:skill', selectedSkillOptions.value.find(skill => skill.id === id) || null)
  skillMenuOpen.value = false
}
const agentNameById = computed(() => {
  const map = {}
  for (const a of props.config.agents) map[a.id] = a.name
  return map
})
function subagentNames(agent) {
  return (agent.subagents || []).map(id => agentNameById.value[id]).filter(Boolean)
}
const contextPercent = computed(() => {
  if (Number.isFinite(Number(props.contextUsage?.percent)) && Number(props.contextUsage.percent) > 0) {
    return Math.min(100, Math.round(Number(props.contextUsage.percent)))
  }
  const windowSize = Number(props.contextUsage?.contextWindow) || props.contextWindow
  if (!windowSize) return 0
  return Math.min(100, Math.round(((Number(props.contextUsage?.tokens) || 0) / windowSize) * 100))
})
const contextTokens = computed(() => Number(props.contextUsage?.tokens) || 0)
const contextLimit = computed(() => Number(props.contextUsage?.contextWindow) || props.contextWindow || 0)
const contextUsageTitle = computed(() => {
  return `${props.t.contextUsage}: ${contextTokens.value.toLocaleString()} / ${contextLimit.value.toLocaleString()} (${contextPercent.value}%)`
})

function onDocumentPointerDown(event) {
  if (agentSwitcherOpen.value && agentWrapEl.value && !agentWrapEl.value.contains(event.target)) agentSwitcherOpen.value = false
  if (modelMenuOpen.value && modelWrapEl.value && !modelWrapEl.value.contains(event.target)) modelMenuOpen.value = false
  if (skillMenuOpen.value && skillWrapEl.value && !skillWrapEl.value.contains(event.target)) skillMenuOpen.value = false
}

function pickAgent(agent) {
  agentSwitcherOpen.value = false
  emit('select-agent', agent)
}

function selectModel(value) {
  modelMenuOpen.value = false
  emit('update:model', value)
}

function sendOnEnter(event) {
  if (event.isComposing || event.keyCode === 229) return
  event.preventDefault()
  emit('send')
}

function editPending(id) {
  emit('edit-pending', id)
  nextTick(() => textareaEl.value?.focus())
}

  function onPaste(event) {
    const data = event.clipboardData
    if (!data) return
    // Pasted files (copied from the OS file manager, or a screenshot from the
    // clipboard) become attachments – the same path as the file picker and
    // drag-and-drop.
    const files = data.files ? Array.from(data.files).filter(Boolean) : []
    if (files.length) {
      event.preventDefault()
      emit('add-attachments', files)
      return
    }
    // Fallback for runtimes that only expose pasted images through items.
    if (!props.supportsImages) return
    const items = Array.from(data.items || [])
    const readers = items
      .filter(item => item.kind === 'file' && item.type?.startsWith('image/'))
      .map(item => new Promise(resolve => {
        const file = item.getAsFile()
        if (!file) return resolve(null)
        const reader = new FileReader()
        reader.onload = () => {
          const dataUrl = String(reader.result)
          const comma = dataUrl.indexOf(',')
          resolve({
            type: 'image',
            name: file.name || `pasted-${Date.now()}.${file.type.split('/')[1] || 'png'}`,
            mimeType: file.type || 'image/png',
            data: comma >= 0 ? dataUrl.slice(comma + 1) : dataUrl
          })
        }
        reader.onerror = () => resolve(null)
        reader.readAsDataURL(file)
      }))
    if (!readers.length) return
    event.preventDefault()
    Promise.all(readers).then(result => {
      const images = result.filter(Boolean)
      if (images.length) emit('add-images', images)
    })
  }

// ---- Attachment upload (design §A2) ----
const fileInput = ref(null)

function triggerAttachments() {
  fileInput.value?.click()
}

function onFilePicked(event) {
  const files = event.target.files
  if (files && files.length) emit('add-attachments', Array.from(files))
  event.target.value = ''
}

// 仅允许往输入框（composer）区域拖入文件以添加附件。
const composerDrag = ref(0)
function dragHasFiles(event) {
  const types = event.dataTransfer && event.dataTransfer.types
  return !!(types && Array.from(types).includes('Files'))
}
function onDragEnter(event) {
  if (!dragHasFiles(event)) return
  event.preventDefault()
  composerDrag.value++
}
function onDragOver(event) {
  if (!dragHasFiles(event)) return
  event.preventDefault()
  if (event.dataTransfer) event.dataTransfer.dropEffect = 'copy'
}
function onDragLeave() {
  // dragleave 时 dataTransfer.types 常为空，故不在此判断是否含文件。
  composerDrag.value = Math.max(0, composerDrag.value - 1)
}
function readDroppedFiles(event) {
  const dt = event.dataTransfer
  const out = []
  if (dt && dt.files && dt.files.length) {
    for (const file of Array.from(dt.files)) out.push(file)
  } else if (dt && dt.items && dt.items.length) {
    for (const item of Array.from(dt.items)) {
      if (item.kind === 'file') {
        const file = item.getAsFile()
        if (file) out.push(file)
      }
    }
  }
  return out
}
function nativeFileDropEnabled() {
  return window?._wails?.flags?.enableFileDrop === true
}
function onDropAttachments(event) {
  composerDrag.value = 0
  event.preventDefault()
  // Wails resolves native file paths and emits attachments:dropped. Let that
  // path handle desktop drops so large files do not need to be base64 encoded
  // in the WebView. Browser development mode still uses ordinary File objects.
  if (nativeFileDropEnabled()) return
  const files = readDroppedFiles(event)
  if (files.length) {
    emit('add-attachments', files)
  }
}

function kindIcon(kind) {
  switch (kind) {
    case 'image': return Image
    case 'audio': return FileAudio
    case 'video': return FileVideo
    case 'document': return FileText
    default: return File
  }
}

function pendingPromptSummary(prompt) {
  if (prompt.message) return prompt.message
  const imageCount = prompt.images?.length || 0
  const attachmentCount = prompt.attachments?.length || 0
  if (imageCount && attachmentCount) {
    return `${props.t.pendingImagesOnly.replace('{count}', imageCount)} · ${props.t.pendingAttachments.replace('{count}', attachmentCount)}`
  }
  if (attachmentCount) return props.t.pendingAttachments.replace('{count}', attachmentCount)
  return props.t.pendingImagesOnly.replace('{count}', imageCount)
}

onMounted(() => document.addEventListener('mousedown', onDocumentPointerDown))
onBeforeUnmount(() => document.removeEventListener('mousedown', onDocumentPointerDown))
</script>

<template>
  <footer v-show="!loadingHistory" class="chat-main__input">
    <div v-if="extensionDialog" class="chat-plan-dock">
      <ChatPlanPanel
        :items="planItems"
        :dialog="extensionDialog"
        :t="t"
        @respond="emit('respond-extension', $event)"
      />
    </div>
    <ChatExecutionPlan v-else-if="executionPlan.length" :items="executionPlan" :t="t" />

    <section v-if="pendingPrompts.length" class="pending-prompts" :aria-label="t.pendingPrompts" aria-live="polite">
      <div class="pending-prompts__header">
        <span>{{ t.pendingPrompts }}</span>
        <span class="pending-prompts__count">{{ pendingPrompts.length }}</span>
      </div>
      <ol class="pending-prompts__list">
        <li v-for="(prompt, index) in pendingPrompts" :key="prompt.id" class="pending-prompt">
          <span class="pending-prompt__index">{{ index + 1 }}</span>
          <div class="pending-prompt__body">
            <p>{{ pendingPromptSummary(prompt) }}</p>
            <span v-if="prompt.message && (prompt.images?.length || prompt.attachments?.length)" class="pending-prompt__attachments">
              <template v-if="prompt.images?.length">{{ t.pendingImages.replace('{count}', prompt.images.length) }}</template>
              <template v-if="prompt.images?.length && prompt.attachments?.length"> · </template>
              <template v-if="prompt.attachments?.length">{{ t.pendingAttachments.replace('{count}', prompt.attachments.length) }}</template>
            </span>
          </div>
          <div class="pending-prompt__actions">
            <button type="button" :title="t.editPending" :aria-label="t.editPending" @click="editPending(prompt.id)">
              <Pencil :size="14" />
            </button>
            <button type="button" :title="t.deletePending" :aria-label="t.deletePending" @click="emit('delete-pending', prompt.id)">
              <Trash2 :size="14" />
            </button>
          </div>
        </li>
      </ol>
    </section>

    <div
      id="chat-attachment-drop-target"
      class="composer"
      data-file-drop-target
      :class="{ 'is-dragover': composerDrag }"
      @dragenter="onDragEnter"
      @dragover="onDragOver"
      @dragleave="onDragLeave"
      @drop="onDropAttachments"
    >
      <div v-if="promptImages.length" class="attachment-strip">
        <div v-for="(image, index) in promptImages" :key="`${image.name}-${index}`" class="attachment">
          <button class="attachment__thumb" type="button" :title="t.imagePreview" @click="emit('preview-image', image)">
            <img :src="imageSrc(image)" :alt="image.name" />
          </button>
          <button class="attachment__remove" type="button" :title="t.remove" @click="emit('remove-image', index)"><X :size="12" /></button>
        </div>
      </div>

      <div v-if="attachments.length" class="attachment-strip attachment-strip--files">
        <div
          v-for="(att, index) in attachments"
          :key="att.id || `${att.name}-${index}`"
          class="attachment"
          :class="{ 'attachment--file': !att.imagePreview, 'attachment--reading': att.reading }"
        >
          <button v-if="att.imagePreview" class="attachment__thumb" type="button" :title="t.imagePreview" @click="emit('preview-image', att)">
            <img :src="att.imagePreview" :alt="att.name" />
          </button>
          <template v-else>
            <div class="attachment__icon"><component :is="kindIcon(att.kind)" :size="16" /></div>
            <div class="attachment__meta">
              <span class="attachment__name" :title="att.name">{{ att.name }}</span>
              <span class="attachment__size">{{ formatFileSize(att.size) }}</span>
            </div>
          </template>
          <button class="attachment__remove" type="button" :title="t.remove" @click="emit('remove-attachment', index)"><X :size="12" /></button>
        </div>
      </div>

      <div v-if="selectedSkill" class="selected-skill-chip" :title="selectedSkill.path">
        <Sparkles :size="13" /><span>{{ selectedSkill.name }}</span><button type="button" :title="t.remove" @click="emit('update:skill', null)"><X :size="12" /></button>
      </div>

      <textarea
        ref="textareaEl"
        :value="draft"
        :placeholder="t.placeholder"
        rows="3"
        @input="emit('update:draft', $event.target.value)"
        @keydown.enter.exact="sendOnEnter"
        @paste="onPaste"
      ></textarea>

      <div class="composer-toolbar">
        <div class="composer-tools">
          <div ref="modelWrapEl" class="model-select-wrap">
            <button class="model-select-btn" :disabled="running || !modelOptions.length" :title="t.chatModel" @click="modelMenuOpen = !modelMenuOpen">
              <Brain :size="14" class="model-select-btn__icon" />
              <span class="model-select-btn__label">{{ selectedModelLabel }}</span>
              <ChevronDown :size="13" :class="{ 'model-select-btn__arrow--open': modelMenuOpen }" />
            </button>
            <div v-if="modelMenuOpen" class="model-menu">
              <div class="model-menu__pop">
                <template v-for="group in modelGroups" :key="group.provider">
                  <div class="model-menu__label">{{ group.provider }}</div>
                  <button
                    v-for="option in group.options"
                    :key="option.value"
                    class="model-menu__item"
                    :class="{ 'model-menu__item--active': option.value === selectedModelValue }"
                    @click="selectModel(option.value)"
                  >
                    <span>{{ option.model }}</span>
                    <Check v-if="option.value === selectedModelValue" :size="14" />
                  </button>
                </template>
              </div>
            </div>
          </div>

          <button class="field-button" :disabled="attachmentsBusy" :title="t.attachmentButton" @click="triggerAttachments">
            <LoaderCircle v-if="attachmentsBusy" class="spin" :size="15" />
            <Paperclip v-else :size="15" /><span>{{ t.attachment }}</span>
          </button>
          <input ref="fileInput" type="file" multiple hidden @change="onFilePicked" />

          <div v-if="selectedSkillOptions.length" ref="skillWrapEl" class="skill-select-wrap">
            <button class="skill-select-btn" :title="t.skillsSelect || 'Skill'" @click="skillMenuOpen = !skillMenuOpen">
              <Sparkles :size="14" class="skill-select-btn__icon" />
              <span class="skill-select-btn__label">{{ selectedSkill?.name || (t.skillsSelect || 'Skill') }}</span>
              <ChevronDown :size="13" :class="{ 'skill-select-btn__arrow--open': skillMenuOpen }" />
            </button>
            <div v-if="skillMenuOpen" class="skill-menu">
              <div class="skill-menu__pop">
                <button
                  class="skill-menu__item"
                  :class="{ 'skill-menu__item--active': !selectedSkill }"
                  @click="chooseSkill('')"
                >
                  <span class="skill-menu__text">{{ t.skillsSelect || 'Skill' }}</span>
                  <Check v-if="!selectedSkill" :size="14" />
                </button>
                <button
                  v-for="skill in selectedSkillOptions"
                  :key="skill.id"
                  class="skill-menu__item"
                  :class="{ 'skill-menu__item--active': selectedSkill && skill.id === selectedSkill.id }"
                  @click="chooseSkill(skill.id)"
                >
                  <span class="skill-menu__text">{{ skill.name }}</span>
                  <span class="skill-menu__type" :class="{ 'skill-menu__type--startup': skill.loadMode === 'startup' }">{{ skillTypeLabel(skill) }}</span>
                  <Check v-if="selectedSkill && skill.id === selectedSkill.id" :size="14" />
                </button>
              </div>
            </div>
          </div>

        </div>

        <div class="composer-stats">
          <div class="context-usage" :title="contextUsageTitle" :aria-label="contextUsageTitle">
            <svg class="context-usage__ring" viewBox="0 0 24 24" aria-hidden="true">
              <circle class="context-usage__track" cx="12" cy="12" r="9" />
              <circle class="context-usage__value" cx="12" cy="12" r="9" pathLength="100" :stroke-dasharray="`${contextPercent} 100`" />
            </svg>
          </div>
          <div class="token-stats">
            <span><b>{{ formatTokenCount(tokenStats.input) }}</b> {{ t.tokenInput }}</span>
            <span><b>{{ formatTokenCount(tokenStats.cached) }}</b> {{ t.tokenCached }}</span>
            <span><b>{{ formatTokenCount(tokenStats.output) }}</b> {{ t.tokenOutput }}</span>
          </div>
          <!-- 注释压缩（压缩上下文）按钮：按需求暂时注释
          <button
            class="compact-button"
            :disabled="!compaction.available || compaction.running || running || !hasMessages"
            :title="compaction.available ? (t.compactContext || 'Compact context') : t.compactContextUnavailable"
            @click="emit('compact')"
          ><RotateCcw :class="{ spin: compaction.running }" :size="13" /></button>
          -->
        </div>

        <div ref="agentWrapEl" class="composer-agent-wrap">
          <button class="composer-agent" :title="t.chatSwitchAgent" @click="agentSwitcherOpen = !agentSwitcherOpen">
            <span v-if="agentAvatar(selectedAgent)" class="composer-agent__emoji">{{ agentAvatar(selectedAgent) }}</span>
            <Bot v-else class="composer-agent__badge" :size="15" />
            <span>{{ selectedAgent?.name || t.chatAgentSwitcher }}</span>
            <ChevronDown :size="13" />
          </button>
          <div v-if="agentSwitcherOpen" class="agent-menu">
            <div class="agent-menu__pop">
              <div class="agent-menu__label">{{ t.chatSwitchAgent }}</div>
              <button
                v-for="agent in config.agents"
                :key="agent.id"
                class="agent-menu__item"
                :class="{ 'agent-menu__item--active': agent.id === selectedAgent?.id }"
                @click="pickAgent(agent)"
              >
                <span v-if="agentAvatar(agent)" class="agent-menu__emoji">{{ agentAvatar(agent) }}</span>
                <Bot v-else :size="15" class="agent-menu__icon" />
                <span class="agent-menu__item-text">
                  <span class="agent-menu__name">{{ agent.name }}</span>
                  <small v-if="subagentNames(agent).length" class="agent-menu__subagents">{{ subagentNames(agent).join(', ') }}</small>
                </span>
                <Check v-if="agent.id === selectedAgent?.id" :size="14" />
              </button>
              <button class="agent-menu__config" @click="agentSwitcherOpen = false; emit('open-agent-config')">{{ t.chatConfig }}</button>
            </div>
          </div>
        </div>

        <div class="composer-right">
          <button
            v-if="running"
            class="send-button stop"
            type="button"
            :disabled="stopping"
            :title="stopping ? t.stopping : t.stop"
            :aria-label="stopping ? t.stopping : t.stop"
            @click="emit('stop')"
          >
            <LoaderCircle v-if="stopping" class="spin" :size="17" />
            <CircleStop v-else :size="17" />
          </button>
          <button
            class="send-button"
            type="button"
            :disabled="attachmentsBusy || (!draft.trim() && !promptImages.length && !attachments.length) || !selectedModelValue"
            :title="running ? t.queuePrompt : t.send"
            :aria-label="running ? t.queuePrompt : t.send"
            @click="emit('send')"
          ><Send :size="17" /></button>
        </div>
      </div>
    </div>
    <p class="chat-ai-disclaimer" aria-live="polite">{{ t.ai_task_disclaimer }}</p>
  </footer>
</template>

<style scoped src="../../styles/chat/composer.css"></style>
