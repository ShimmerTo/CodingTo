<script setup>
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ArrowLeft, ArrowRight, Brain, Check, FileWarning, LoaderCircle, Save, Settings2, Sparkles, Trash2, X } from 'lucide-vue-next'
import { generateSessionGitConflictAI, getSessionGitConflictDetail, resolveSessionGitConflict } from '../../backend.js'
import ConfirmDialog from '../ConfirmDialog.vue'
import GitAIPromptDialog from '../GitAIPromptDialog.vue'
import { renderMarkdown } from './chatFormatters.js'
import { parseGitConflictPoints } from '../../utils/gitConflict.js'

const GIT_CONFLICT_MODEL_CACHE_KEY = 'codingto:git-conflict-model'

const props = defineProps({
  open: { type: Boolean, default: false },
  sessionId: { type: Number, required: true },
  files: { type: Array, default: () => [] },
  index: { type: Number, default: 0 },
  t: { type: Object, required: true },
  language: { type: String, default: 'zh-CN' },
  modelOptions: { type: Array, default: () => [] },
  selectedModelValue: { type: String, default: '' },
})

const emit = defineEmits(['close', 'resolved', 'update:index'])
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const detail = ref(null)
const resultText = ref('')
const initialResultText = ref('')
const resultEditor = ref(null)
const deleteConfirmOpen = ref(false)
const discardConfirmOpen = ref(false)
const pendingAction = ref(null)
const aiPanelOpen = ref(false)
const aiPromptOpen = ref(false)
const aiLoading = ref(false)
const aiError = ref('')
const aiExplanation = ref('')
const aiApplied = ref('')
const aiScope = ref('point')
const aiPointIndex = ref(-1)
const aiPointNumber = ref(0)
const aiModel = ref(loadCachedAIModel())
let requestNonce = 0

const activeFile = computed(() => props.files[props.index] || null)
const positionLabel = computed(() => `${Math.min(props.index + 1, props.files.length)} / ${props.files.length}`)
const isText = computed(() => detail.value?.kind === 'text')
const dirty = computed(() => isText.value && resultText.value !== initialResultText.value)
const hasConflictMarkers = computed(() => /^(<{7}|={7}|>{7})(?:\s|$)/m.test(resultText.value))
const canSaveText = computed(() => isText.value && !loading.value && !saving.value)
const conflictPoints = computed(() => parseGitConflictPoints(resultText.value))
const enabledModels = computed(() => props.modelOptions.filter(option => !option.disabled))
const aiModelGroups = computed(() => {
  const groups = {}
  for (const option of props.modelOptions) (groups[option.provider] ||= []).push(option)
  return Object.entries(groups).map(([provider, options]) => ({ provider, options }))
})
const renderedAIExplanation = computed(() => renderMarkdown(aiExplanation.value))

function loadCachedAIModel() {
  try { return localStorage.getItem(GIT_CONFLICT_MODEL_CACHE_KEY) || '' } catch { return '' }
}

function cacheAIModel() {
  if (!aiModel.value) return
  try { localStorage.setItem(GIT_CONFLICT_MODEL_CACHE_KEY, aiModel.value) } catch { /* 当前弹窗仍保留选择 */ }
}

watch(
  () => [props.open, props.selectedModelValue, props.modelOptions],
  ([open, selected]) => {
    if (!open || enabledModels.value.some(option => option.value === aiModel.value)) return
    const cached = loadCachedAIModel()
    aiModel.value = enabledModels.value.some(option => option.value === cached)
      ? cached
      : enabledModels.value.some(option => option.value === selected)
        ? selected
        : enabledModels.value[0]?.value || ''
    cacheAIModel()
  },
  { immediate: true }
)

watch(
  [() => props.open, () => props.index, () => activeFile.value?.path, () => props.files],
  loadDetail,
  { immediate: true }
)

async function loadDetail() {
  if (!props.open || !activeFile.value?.path) return
  const nonce = ++requestNonce
  loading.value = true
  error.value = ''
  detail.value = null
  resultText.value = ''
  initialResultText.value = ''
  aiPanelOpen.value = false
  aiExplanation.value = ''
  aiApplied.value = ''
  aiError.value = ''
  try {
    const loaded = await getSessionGitConflictDetail(props.sessionId, activeFile.value.path)
    if (nonce !== requestNonce) return
    detail.value = loaded
    const text = loaded?.result?.exists ? String(loaded.result.text || '') : ''
    resultText.value = text
    initialResultText.value = text
    await nextTick()
    if (loaded?.kind === 'text') resultEditor.value?.focus()
  } catch (cause) {
    if (nonce === requestNonce) error.value = formatError(cause)
  } finally {
    if (nonce === requestNonce) loading.value = false
  }
}

function sideLines(version) {
  if (!version?.exists) return []
  const normalized = String(version.text || '').replace(/\r\n/g, '\n').replace(/\r/g, '\n')
  const lines = normalized.split('\n')
  if (normalized.endsWith('\n')) lines.pop()
  return lines
}

function focusConflictPoint(point) {
  if (!point || !resultEditor.value) return
  resultEditor.value.focus()
  resultEditor.value.setSelectionRange(point.start, point.end)
}

function openAIForPoint(point) {
  if (!point) return
  aiScope.value = 'point'
  aiPointIndex.value = point.index
  aiPointNumber.value = point.index + 1
  aiPanelOpen.value = true
  aiError.value = ''
  aiExplanation.value = ''
  aiApplied.value = ''
  focusConflictPoint(point)
}

function openAIForFile() {
  aiScope.value = 'file'
  aiPointIndex.value = -1
  aiPointNumber.value = 0
  aiPanelOpen.value = true
  aiError.value = ''
  aiExplanation.value = ''
  aiApplied.value = ''
}

async function runConflictAI(mode) {
  if (aiLoading.value || !detail.value || !aiModel.value) return
  const selected = props.modelOptions.find(option => option.value === aiModel.value && !option.disabled)
  if (!selected) return
  const point = aiScope.value === 'point' ? conflictPoints.value[aiPointIndex.value] : null
  if (aiScope.value === 'point' && !point) {
    aiError.value = props.t.gitConflictPointChanged
    return
  }
  const sourceText = resultText.value
  aiLoading.value = true
  aiError.value = ''
  aiExplanation.value = ''
  aiApplied.value = ''
  try {
    const response = await generateSessionGitConflictAI({
      sessionId: Number(props.sessionId),
      path: detail.value.path,
      language: props.language,
      provider: selected.provider,
      model: selected.model,
      mode,
      scope: aiScope.value,
      currentResult: aiScope.value === 'file' ? sourceText : '',
      pointOurs: point?.ours || '',
      pointTheirs: point?.theirs || '',
      pointBase: point?.base || '',
      contextBefore: point ? sourceText.slice(Math.max(0, point.start - 4000), point.start) : '',
      contextAfter: point ? sourceText.slice(point.end, point.end + 4000) : '',
    })
    if (resultText.value !== sourceText) {
      aiError.value = props.t.gitConflictPointChanged
      return
    }
    if (mode === 'explain') {
      aiExplanation.value = response?.explanation || ''
      return
    }
    const replacement = String(response?.replacement ?? '')
    if (aiScope.value === 'file') {
      resultText.value = replacement
    } else {
      resultText.value = sourceText.slice(0, point.start) + replacement + sourceText.slice(point.end)
    }
    aiApplied.value = aiScope.value === 'file' ? props.t.gitConflictAiAllApplied : props.t.gitConflictAiPointApplied
    aiPointIndex.value = -1
    await nextTick()
    resultEditor.value?.focus()
  } catch (cause) {
    aiError.value = formatError(cause)
  } finally {
    aiLoading.value = false
  }
}

function adoptSide(side) {
  const version = detail.value?.[side]
  if (!isText.value || !version?.exists || saving.value) return
  resultText.value = String(version.text || '')
  void nextTick(() => resultEditor.value?.focus())
}

async function resolve(resolution) {
  if (saving.value || loading.value || !detail.value) return
  saving.value = true
  error.value = ''
  try {
    const result = await resolveSessionGitConflict({
      sessionId: Number(props.sessionId),
      path: detail.value.path,
      expectedResultHash: detail.value.resultHash,
      resolution,
      resultText: resolution === 'content' ? resultText.value : '',
      language: props.language,
    })
    emit('resolved', { path: detail.value.path, message: result?.message || props.t.gitConflictResolved })
  } catch (cause) {
    const message = formatError(cause)
    await loadDetail()
    error.value = message
  } finally {
    saving.value = false
    deleteConfirmOpen.value = false
  }
}

function resolveSide(side) {
  if (isText.value) {
    adoptSide(side)
    return
  }
  void resolve(side)
}

function runOrConfirmDiscard(action) {
  if (!dirty.value) {
    action()
    return
  }
  pendingAction.value = action
  discardConfirmOpen.value = true
}

function requestClose() {
  if (saving.value) return
  runOrConfirmDiscard(() => emit('close'))
}

function navigate(delta) {
  const next = props.index + delta
  if (next < 0 || next >= props.files.length || saving.value) return
  runOrConfirmDiscard(() => emit('update:index', next))
}

function confirmDiscard() {
  const action = pendingAction.value
  pendingAction.value = null
  discardConfirmOpen.value = false
  action?.()
}

function formatSize(bytes) {
  const value = Number(bytes) || 0
  if (value < 1024) return `${value} B`
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KB`
  return `${(value / (1024 * 1024)).toFixed(1)} MB`
}

function formatError(cause) {
  return String(cause?.message || cause || props.t.gitOperationUnknownError).replace(/^Error:\s*/i, '')
}

function onKeydown(event) {
  if (!props.open) return
  if (aiPromptOpen.value) return
  if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 's' && canSaveText.value) {
    event.preventDefault()
    void resolve('content')
    return
  }
  if (event.key === 'Escape' && !deleteConfirmOpen.value && !discardConfirmOpen.value) requestClose()
}

onMounted(() => window.addEventListener('keydown', onKeydown))
onBeforeUnmount(() => window.removeEventListener('keydown', onKeydown))
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="git-conflict-backdrop" @pointerdown.self="requestClose">
      <section class="git-conflict-dialog" role="dialog" aria-modal="true" :aria-label="t.gitResolveConflictTitle">
        <header class="git-conflict-dialog__head">
          <span class="git-conflict-dialog__icon"><FileWarning :size="18" /></span>
          <span class="git-conflict-dialog__title">
            <strong :title="activeFile?.path">{{ activeFile?.path }}</strong>
            <small>{{ t.gitResolveConflictTitle }} · {{ detail?.conflictStatus || activeFile?.conflictStatus || '' }}</small>
          </span>
          <button class="git-conflict-dialog__ai-all" type="button" :disabled="loading || saving || aiLoading || !conflictPoints.length || !enabledModels.length" :title="t.gitConflictAiResolveAll" @click="openAIForFile">
            <Brain :size="15" />{{ t.gitConflictAiResolveAll }}
          </button>
          <nav class="git-conflict-dialog__nav" :aria-label="t.gitChangeNavigation">
            <button type="button" :disabled="index <= 0 || saving" :title="t.gitPreviousChange" @click="navigate(-1)"><ArrowLeft :size="16" /></button>
            <span>{{ positionLabel }}</span>
            <button type="button" :disabled="index >= files.length - 1 || saving" :title="t.gitNextChange" @click="navigate(1)"><ArrowRight :size="16" /></button>
          </nav>
          <button class="git-conflict-dialog__close" type="button" :title="t.close" :disabled="saving" @click="requestClose"><X :size="18" /></button>
        </header>

        <p v-if="error" class="git-conflict-dialog__error" role="alert">{{ error }}</p>
        <div v-if="loading" class="git-conflict-dialog__state"><LoaderCircle class="spin" :size="22" />{{ t.gitConflictLoading }}</div>

        <main v-else-if="detail" class="git-conflict-dialog__panes">
          <section class="git-conflict-pane is-ours">
            <header>
              <div><strong>{{ t.gitConflictOurs }}</strong><small>{{ detail.ours.exists ? `${detail.ours.lineCount || 0} ${t.gitLines}` : t.gitConflictDeleted }}</small></div>
              <button type="button" :disabled="saving || !detail.ours.exists" :title="t.gitConflictUseOurs" @click="resolveSide('ours')"><ArrowRight :size="14" />{{ t.gitConflictUseOurs }}</button>
            </header>
            <ol v-if="isText && detail.ours.exists" class="git-conflict-code">
              <li v-for="(line, lineIndex) in sideLines(detail.ours)" :key="lineIndex"><code>{{ line }}</code></li>
            </ol>
            <div v-else-if="detail.kind === 'image' && detail.ours.imageData" class="git-conflict-preview"><img :src="detail.ours.imageData" :alt="t.gitConflictOurs" /></div>
            <div v-else class="git-conflict-pane__empty">{{ detail.ours.exists ? `${t.gitBinaryFile} · ${formatSize(detail.ours.size)}` : t.gitConflictDeleted }}</div>
          </section>

          <section class="git-conflict-pane is-result">
            <header>
              <div><strong>{{ t.gitConflictResult }}</strong><small v-if="dirty">{{ t.gitConflictUnsaved }}</small><small v-else>{{ t.gitConflictFinalHint }}</small></div>
              <span v-if="hasConflictMarkers" class="git-conflict-pane__warning"><FileWarning :size="13" />{{ t.gitConflictMarkersRemain }}</span>
            </header>
            <template v-if="isText">
              <div v-if="conflictPoints.length" class="git-conflict-points" :aria-label="t.gitConflictPoints">
                <div v-for="point in conflictPoints" :key="`${point.start}:${point.end}`" class="git-conflict-point" @click="focusConflictPoint(point)">
                  <span><FileWarning :size="13" />{{ t.gitConflictPointLabel.replace('{index}', point.index + 1).replace('{start}', point.startLine).replace('{end}', point.endLine) }}</span>
                  <button type="button" :disabled="aiLoading || !enabledModels.length" @click.stop="openAIForPoint(point)"><Sparkles :size="13" />{{ t.gitConflictAskAi }}</button>
                </div>
              </div>
              <textarea
                ref="resultEditor"
                v-model="resultText"
                class="git-conflict-result-editor"
                :aria-label="t.gitConflictResult"
                :disabled="saving"
                spellcheck="false"
              ></textarea>
            </template>
            <div v-else-if="detail.kind === 'image' && detail.result.imageData" class="git-conflict-preview"><img :src="detail.result.imageData" :alt="t.gitConflictResult" /></div>
            <div v-else class="git-conflict-pane__empty">{{ detail.result.exists ? `${t.gitBinaryFile} · ${formatSize(detail.result.size)}` : t.gitConflictDeleted }}</div>
          </section>

          <section class="git-conflict-pane is-theirs">
            <header>
              <div><strong>{{ t.gitConflictTheirs }}</strong><small>{{ detail.theirs.exists ? `${detail.theirs.lineCount || 0} ${t.gitLines}` : t.gitConflictDeleted }}</small></div>
              <button type="button" :disabled="saving || !detail.theirs.exists" :title="t.gitConflictUseTheirs" @click="resolveSide('theirs')"><ArrowLeft :size="14" />{{ t.gitConflictUseTheirs }}</button>
            </header>
            <ol v-if="isText && detail.theirs.exists" class="git-conflict-code">
              <li v-for="(line, lineIndex) in sideLines(detail.theirs)" :key="lineIndex"><code>{{ line }}</code></li>
            </ol>
            <div v-else-if="detail.kind === 'image' && detail.theirs.imageData" class="git-conflict-preview"><img :src="detail.theirs.imageData" :alt="t.gitConflictTheirs" /></div>
            <div v-else class="git-conflict-pane__empty">{{ detail.theirs.exists ? `${t.gitBinaryFile} · ${formatSize(detail.theirs.size)}` : t.gitConflictDeleted }}</div>
          </section>
        </main>

        <section v-if="aiPanelOpen" class="git-conflict-ai">
          <header>
            <span><Brain :size="16" /><strong>{{ aiScope === 'file' ? t.gitConflictAiAllTitle : t.gitConflictAiPointTitle.replace('{index}', aiPointNumber) }}</strong></span>
            <button type="button" :aria-label="t.close" :disabled="aiLoading" @click="aiPanelOpen = false"><X :size="15" /></button>
          </header>
          <div class="git-conflict-ai__controls">
            <label :title="enabledModels.length ? t.gitCommitModel : t.gitCommitModelUnavailable">
              <Brain :size="14" />
              <select v-model="aiModel" :disabled="aiLoading || !enabledModels.length" @change="cacheAIModel">
                <optgroup v-for="group in aiModelGroups" :key="group.provider" :label="group.provider">
                  <option v-for="option in group.options" :key="option.value" :value="option.value" :disabled="option.disabled">{{ option.model }}{{ option.disabled ? ` · ${option.disabledLabel}` : '' }}</option>
                </optgroup>
              </select>
            </label>
            <button type="button" :title="t.gitEditPrompt" :disabled="aiLoading" @click="aiPromptOpen = true"><Settings2 :size="14" />{{ t.gitEditPrompt }}</button>
            <button v-if="aiScope === 'point'" type="button" :disabled="aiLoading || !aiModel || !!aiApplied" @click="runConflictAI('explain')"><Brain :size="14" />{{ t.gitConflictAiExplain }}</button>
            <button class="is-primary" type="button" :disabled="aiLoading || !aiModel || !!aiApplied" @click="runConflictAI('resolve')">
              <LoaderCircle v-if="aiLoading" class="spin" :size="14" /><Sparkles v-else :size="14" />{{ aiScope === 'file' ? t.gitConflictAiResolveAll : t.gitConflictAiResolvePoint }}
            </button>
          </div>
          <div v-if="aiLoading" class="git-conflict-ai__state"><LoaderCircle class="spin" :size="18" />{{ t.gitConflictAiThinking }}</div>
          <div v-else-if="aiError" class="git-conflict-ai__state is-error">{{ aiError }}</div>
          <div v-else-if="aiApplied" class="git-conflict-ai__state is-success"><Check :size="16" />{{ aiApplied }}</div>
          <div v-else-if="aiExplanation" class="git-conflict-ai__result" v-html="renderedAIExplanation"></div>
          <div v-else class="git-conflict-ai__state">{{ aiScope === 'file' ? t.gitConflictAiAllHint : t.gitConflictAiPointHint }}</div>
        </section>

        <footer class="git-conflict-dialog__foot">
          <span>{{ t.gitConflictSaveHint }}</span>
          <div>
            <button class="is-danger" type="button" :disabled="loading || saving" @click="deleteConfirmOpen = true"><Trash2 :size="14" />{{ t.gitConflictResolveDelete }}</button>
            <button type="button" :disabled="saving" @click="requestClose">{{ t.cancel }}</button>
            <button v-if="isText" class="is-primary" type="button" :disabled="!canSaveText" @click="resolve('content')">
              <LoaderCircle v-if="saving" class="spin" :size="14" /><Save v-else :size="14" />{{ t.gitConflictSaveResult }}
            </button>
            <span v-else class="git-conflict-dialog__resolved-hint"><Check :size="14" />{{ t.gitConflictChooseSideHint }}</span>
          </div>
        </footer>
      </section>
    </div>
  </Teleport>

  <ConfirmDialog
    v-model="deleteConfirmOpen"
    :title="t.gitConflictDeleteTitle"
    :description="t.gitConflictDeleteDesc"
    :confirm-label="t.gitConflictResolveDelete"
    :busy="saving"
    @confirm="resolve('delete')"
  />
  <ConfirmDialog
    v-model="discardConfirmOpen"
    :title="t.gitConflictDiscardTitle"
    :description="t.gitConflictDiscardDesc"
    :confirm-label="t.gitConflictDiscardConfirm"
    @confirm="confirmDiscard"
    @cancel="pendingAction = null"
  />
  <GitAIPromptDialog v-model="aiPromptOpen" kind="conflict_resolution" :language="language" :t="t" />
</template>

<style scoped>
.git-conflict-backdrop { position: fixed; z-index: 1350; inset: 0; display: grid; place-items: center; padding: 18px; background: rgb(8 9 9 / .56); backdrop-filter: blur(2px); }
.git-conflict-dialog { width: min(1520px, 98vw); height: min(900px, 94vh); min-height: 520px; display: flex; flex-direction: column; overflow: hidden; border: 1px solid var(--border); border-radius: 14px; background: var(--surface); box-shadow: 0 28px 90px rgb(0 0 0 / .34); }
.git-conflict-dialog__head { min-height: 60px; display: flex; align-items: center; gap: 10px; padding: 9px 14px; border-bottom: 1px solid var(--border); }
.git-conflict-dialog__icon { width: 34px; height: 34px; display: grid; place-items: center; flex: 0 0 34px; border-radius: 9px; color: #d97706; background: color-mix(in srgb, #f59e0b 12%, transparent); }
.git-conflict-dialog__title { min-width: 0; display: flex; flex: 1; flex-direction: column; gap: 2px; }
.git-conflict-dialog__title strong { overflow: hidden; font: var(--fs-13)/1.4 Consolas, "Cascadia Mono", monospace; text-overflow: ellipsis; white-space: nowrap; }
.git-conflict-dialog__title small { color: var(--muted); font-size: var(--fs-11); }
.git-conflict-dialog__nav { display: flex; align-items: center; gap: 4px; }
.git-conflict-dialog__nav button,.git-conflict-dialog__close { width: 32px; height: 32px; display: grid; place-items: center; border: 0; border-radius: 8px; color: var(--muted); background: transparent; cursor: pointer; }
.git-conflict-dialog__nav button:hover:not(:disabled),.git-conflict-dialog__close:hover:not(:disabled) { color: var(--text); background: var(--hover); }
.git-conflict-dialog__nav button:disabled,.git-conflict-dialog__close:disabled { opacity: .4; }
.git-conflict-dialog__nav span { min-width: 52px; color: var(--muted); font-size: var(--fs-12); text-align: center; }
.git-conflict-dialog__ai-all { height: 32px; display: inline-flex; align-items: center; gap: 6px; padding: 0 10px; border: 1px solid color-mix(in srgb, var(--accent) 45%, var(--border)); border-radius: 8px; color: var(--accent); background: color-mix(in srgb, var(--accent) 8%, var(--surface)); cursor: pointer; font-size: var(--fs-12); white-space: nowrap; }
.git-conflict-dialog__ai-all:hover:not(:disabled) { background: color-mix(in srgb, var(--accent) 14%, var(--surface)); }
.git-conflict-dialog__ai-all:disabled { opacity: .42; cursor: default; }
.git-conflict-dialog__error { margin: 0; padding: 8px 14px; color: var(--danger); border-bottom: 1px solid color-mix(in srgb, var(--danger) 24%, var(--border)); background: color-mix(in srgb, var(--danger) 7%, var(--surface)); font-size: var(--fs-12); }
.git-conflict-dialog__state { flex: 1; display: flex; align-items: center; justify-content: center; gap: 8px; color: var(--muted); font-size: var(--fs-13); }
.git-conflict-dialog__panes { min-height: 0; display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 1.18fr) minmax(0, 1fr); flex: 1; }
.git-conflict-pane { min-width: 0; min-height: 0; display: flex; flex-direction: column; background: color-mix(in srgb, var(--surface-2) 48%, var(--surface)); }
.git-conflict-pane + .git-conflict-pane { border-left: 1px solid var(--border); }
.git-conflict-pane.is-result { background: var(--surface); box-shadow: 0 0 20px rgb(0 0 0 / .05); }
.git-conflict-pane > header { min-height: 50px; display: flex; align-items: center; justify-content: space-between; gap: 8px; padding: 7px 10px; border-bottom: 1px solid var(--border); background: var(--surface); }
.git-conflict-pane > header > div { min-width: 0; display: flex; flex-direction: column; gap: 2px; }
.git-conflict-pane > header strong { font-size: var(--fs-13); }
.git-conflict-pane > header small { color: var(--faint); font-size: var(--fs-11); }
.git-conflict-pane > header button { height: 28px; display: inline-flex; align-items: center; gap: 5px; padding: 0 8px; border: 1px solid var(--border); border-radius: 7px; color: var(--accent); background: var(--surface); cursor: pointer; font-size: var(--fs-11); }
.git-conflict-pane > header button:hover:not(:disabled) { border-color: var(--accent); background: color-mix(in srgb, var(--accent) 8%, var(--surface)); }
.git-conflict-pane > header button:disabled { opacity: .4; cursor: default; }
.git-conflict-pane__warning { display: flex; align-items: center; gap: 4px; color: #d97706; font-size: var(--fs-11); }
.git-conflict-code { min-height: 0; margin: 0; padding: 6px 0 18px 50px; flex: 1; overflow: auto; color: var(--faint); background: transparent; font: var(--fs-12)/1.62 Consolas, "Cascadia Mono", monospace; }
.git-conflict-code li { min-width: max-content; padding: 0 12px 0 8px; border-left: 1px solid var(--border-soft); }
.git-conflict-code li::marker { color: var(--faint); font-size: var(--fs-11); }
.git-conflict-code code { color: var(--text); white-space: pre; }
.git-conflict-result-editor { min-width: 0; min-height: 0; width: 100%; flex: 1; resize: none; padding: 11px 13px; border: 0; outline: 0; color: var(--text); background: transparent; tab-size: 4; white-space: pre; overflow: auto; font: var(--fs-12)/1.62 Consolas, "Cascadia Mono", monospace; }
.git-conflict-points { max-height: 132px; display: flex; flex-direction: column; gap: 3px; padding: 6px 8px; overflow: auto; border-bottom: 1px solid var(--border-soft); background: color-mix(in srgb, #f59e0b 5%, var(--surface)); }
.git-conflict-point { min-height: 29px; display: flex; align-items: center; justify-content: space-between; gap: 8px; padding: 3px 5px 3px 8px; border: 1px solid transparent; border-radius: 7px; color: #d97706; cursor: pointer; font-size: var(--fs-11); }
.git-conflict-point:hover,.git-conflict-point:focus-within { border-color: color-mix(in srgb, #f59e0b 30%, var(--border)); background: var(--surface); }
.git-conflict-point > span { display: flex; align-items: center; gap: 5px; }
.git-conflict-point > button { height: 23px; display: inline-flex; align-items: center; gap: 4px; padding: 0 7px; border: 1px solid color-mix(in srgb, var(--accent) 35%, var(--border)); border-radius: 6px; color: var(--accent); background: var(--surface); cursor: pointer; font-size: var(--fs-10); opacity: 0; pointer-events: none; }
.git-conflict-point:hover > button,.git-conflict-point:focus-within > button { opacity: 1; pointer-events: auto; }
.git-conflict-point > button:disabled { opacity: .4; cursor: default; }
.git-conflict-preview { min-height: 0; display: grid; place-items: center; flex: 1; overflow: auto; padding: 18px; background-image: linear-gradient(45deg, var(--surface-2) 25%, transparent 25%),linear-gradient(-45deg, var(--surface-2) 25%, transparent 25%),linear-gradient(45deg, transparent 75%, var(--surface-2) 75%),linear-gradient(-45deg, transparent 75%, var(--surface-2) 75%); background-position: 0 0,0 8px,8px -8px,-8px 0; background-size: 16px 16px; }
.git-conflict-preview img { max-width: 100%; max-height: 100%; object-fit: contain; }
.git-conflict-pane__empty { min-height: 0; display: grid; place-items: center; flex: 1; padding: 20px; color: var(--faint); font-size: var(--fs-12); text-align: center; }
.git-conflict-dialog__foot { min-height: 54px; display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 9px 14px; border-top: 1px solid var(--border); background: var(--surface); }
.git-conflict-dialog__foot > span { color: var(--faint); font-size: var(--fs-11); }
.git-conflict-dialog__foot > div { display: flex; align-items: center; gap: 7px; }
.git-conflict-dialog__foot button { height: 32px; display: inline-flex; align-items: center; gap: 6px; padding: 0 11px; border: 1px solid var(--border); border-radius: 8px; color: var(--muted); background: var(--surface); cursor: pointer; font-size: var(--fs-12); }
.git-conflict-dialog__foot button:hover:not(:disabled) { color: var(--text); background: var(--hover); }
.git-conflict-dialog__foot button.is-primary { color: var(--accent-fg); border-color: var(--accent); background: var(--accent); font-weight: 600; }
.git-conflict-dialog__foot button.is-danger { color: var(--danger); border-color: color-mix(in srgb, var(--danger) 35%, var(--border)); }
.git-conflict-dialog__foot button:disabled { opacity: .45; cursor: default; }
.git-conflict-dialog__resolved-hint { display: flex; align-items: center; gap: 5px; color: var(--muted); font-size: var(--fs-11); }
.git-conflict-ai { flex: 0 0 auto; max-height: 270px; display: flex; flex-direction: column; border-top: 1px solid color-mix(in srgb, var(--accent) 28%, var(--border)); background: var(--surface); }
.git-conflict-ai > header { min-height: 38px; display: flex; align-items: center; justify-content: space-between; padding: 5px 10px 5px 13px; border-bottom: 1px solid var(--border-soft); }
.git-conflict-ai > header > span { display: flex; align-items: center; gap: 7px; color: var(--accent); font-size: var(--fs-12); }
.git-conflict-ai > header button { width: 27px; height: 27px; display: grid; place-items: center; border: 0; border-radius: 7px; color: var(--muted); background: transparent; cursor: pointer; }
.git-conflict-ai > header button:hover { color: var(--text); background: var(--hover); }
.git-conflict-ai__controls { display: flex; align-items: center; gap: 6px; padding: 7px 10px; border-bottom: 1px solid var(--border-soft); }
.git-conflict-ai__controls label { min-width: 180px; height: 30px; display: flex; align-items: center; gap: 5px; padding: 0 7px; border: 1px solid var(--border); border-radius: 7px; color: var(--muted); }
.git-conflict-ai__controls select { min-width: 0; flex: 1; border: 0; outline: 0; color: var(--text); background: transparent; font-size: var(--fs-11); }
.git-conflict-ai__controls button { height: 30px; display: inline-flex; align-items: center; gap: 5px; padding: 0 9px; border: 1px solid var(--border); border-radius: 7px; color: var(--muted); background: var(--surface); cursor: pointer; font-size: var(--fs-11); }
.git-conflict-ai__controls button:hover:not(:disabled) { color: var(--text); background: var(--hover); }
.git-conflict-ai__controls button.is-primary { color: var(--accent-fg); border-color: var(--accent); background: var(--accent); }
.git-conflict-ai__controls button:disabled { opacity: .42; cursor: default; }
.git-conflict-ai__state { min-height: 74px; display: flex; align-items: center; justify-content: center; gap: 7px; padding: 12px; color: var(--muted); font-size: var(--fs-12); }
.git-conflict-ai__state.is-error { color: var(--danger); }
.git-conflict-ai__state.is-success { color: #16a34a; }
.git-conflict-ai__result { min-height: 74px; padding: 10px 14px 14px; overflow: auto; color: var(--text); font-size: var(--fs-12); line-height: 1.6; }
.git-conflict-ai__result :deep(p),.git-conflict-ai__result :deep(ul),.git-conflict-ai__result :deep(ol) { margin: 5px 0; }
.git-conflict-ai__result :deep(code) { padding: 1px 4px; border-radius: 4px; background: var(--code-bg); font-family: Consolas, "Cascadia Mono", monospace; }
@media (max-width: 900px) {
  .git-conflict-backdrop { padding: 0; }
  .git-conflict-dialog { width: 100vw; height: 100vh; border: 0; border-radius: 0; }
  .git-conflict-dialog__panes { grid-template-columns: 1fr; overflow: auto; }
  .git-conflict-pane { min-height: 330px; }
  .git-conflict-pane.is-result { order: -1; }
  .git-conflict-pane + .git-conflict-pane { border-top: 1px solid var(--border); border-left: 0; }
  .git-conflict-dialog__ai-all { width: 32px; justify-content: center; padding: 0; font-size: 0; }
  .git-conflict-ai__controls { align-items: stretch; flex-wrap: wrap; }
  .git-conflict-ai__controls label { flex: 1 1 180px; }
}
</style>
