<script setup>
import { computed, ref, watch } from 'vue'
import { RotateCcw, Save, ScrollText, X } from 'lucide-vue-next'
import { getGitAIPromptConfig, saveGitAIPromptConfig } from '../backend.js'
import { useAppContext } from '../composables/appContext.js'

const props = defineProps({
  modelValue: { type: Boolean, default: false },
  kind: { type: String, required: true },
  language: { type: String, default: 'zh-CN' },
  t: { type: Object, required: true },
})
const emit = defineEmits(['update:modelValue'])
const { pushToast } = useAppContext()
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const snapshot = ref(null)
const prompt = ref('')
const restoring = ref(false)

const title = computed(() => {
  if (props.kind === 'commit') return props.t.gitPromptCommitTitle
  if (props.kind === 'conflict_resolution') return props.t.gitPromptConflictTitle
  return props.t.gitPromptAnalysisTitle
})
const byteLength = computed(() => new TextEncoder().encode(prompt.value).length)
const dirty = computed(() => !!snapshot.value && (restoring.value || prompt.value !== snapshot.value.prompt))
const valid = computed(() => !!prompt.value.trim() && byteLength.value <= (snapshot.value?.maxPromptBytes || 32768))

async function load() {
  loading.value = true
  error.value = ''
  snapshot.value = null
  restoring.value = false
  try {
    const value = await getGitAIPromptConfig(props.kind, props.language)
    snapshot.value = value
    prompt.value = value?.prompt || ''
  } catch (cause) {
    error.value = String(cause?.message || cause)
  } finally {
    loading.value = false
  }
}

function restoreDefault() {
  if (!snapshot.value || snapshot.value.isDefault || saving.value) return
  prompt.value = snapshot.value.defaultPrompt || ''
  restoring.value = true
}

async function save() {
  if (!dirty.value || !valid.value || saving.value) return
  saving.value = true
  error.value = ''
  try {
    const value = await saveGitAIPromptConfig({
      kind: props.kind,
      prompt: prompt.value,
      restoreDefault: restoring.value,
      language: props.language,
    })
    snapshot.value = value
    prompt.value = value?.prompt || ''
    restoring.value = false
    pushToast('success', props.t.gitPromptSaved)
  } catch (cause) {
    error.value = String(cause?.message || cause)
  } finally {
    saving.value = false
  }
}

function close() {
  if (!saving.value) emit('update:modelValue', false)
}

watch(() => [props.modelValue, props.kind], ([open]) => {
  if (open) void load()
}, { immediate: true })
</script>

<template>
  <Teleport to="body">
    <div v-if="modelValue" class="modal-backdrop git-ai-prompt-backdrop" @pointerdown.self="close">
      <section class="git-ai-prompt-dialog" role="dialog" aria-modal="true" :aria-label="title">
        <header>
          <div><span><ScrollText :size="18" /></span><h2>{{ title }}</h2></div>
          <button class="icon-button" type="button" :aria-label="t.close" :disabled="saving" @click="close"><X :size="16" /></button>
        </header>
        <div v-if="loading" class="git-ai-prompt-dialog__state">{{ t.gitPromptLoading }}</div>
        <div v-else-if="!snapshot" class="git-ai-prompt-dialog__state is-error">{{ t.gitPromptLoadFailed }}：{{ error }}</div>
        <template v-else>
          <main>
            <div v-if="error" class="git-ai-prompt-dialog__error">{{ error }}</div>
            <label>
              <span>
                <strong>{{ t.gitPromptLabel }}</strong>
                <small>{{ t.gitPromptHint }}</small>
                <em>{{ snapshot.isDefault && !restoring ? t.gitPromptDefaultBadge : t.gitPromptCustomBadge }}</em>
              </span>
              <textarea v-model="prompt" spellcheck="false" @input="restoring = false"></textarea>
              <i :class="{ invalid: !valid }">{{ byteLength }} / {{ snapshot.maxPromptBytes }} {{ t.gitPromptBytes }}</i>
            </label>
          </main>
          <footer>
            <button class="secondary-button" type="button" :disabled="snapshot.isDefault || restoring || saving" @click="restoreDefault"><RotateCcw :size="14" />{{ t.gitPromptRestore }}</button>
            <div>
              <button class="secondary-button" type="button" :disabled="saving" @click="close">{{ t.close }}</button>
              <button class="primary-button" type="button" :disabled="!dirty || !valid || saving" @click="save"><Save :size="14" />{{ saving ? t.gitPromptSaving : t.gitPromptSave }}</button>
            </div>
          </footer>
        </template>
      </section>
    </div>
  </Teleport>
</template>

<style scoped>
.git-ai-prompt-backdrop { z-index: 1600; }
.git-ai-prompt-dialog { width: min(760px, 94vw); height: min(650px, 90vh); display: flex; flex-direction: column; overflow: hidden; border: 1px solid var(--border); border-radius: 14px; color: var(--text); background: var(--surface); box-shadow: 0 24px 80px rgb(0 0 0 / .28); }
.git-ai-prompt-dialog > header { min-height: 64px; display: flex; align-items: center; justify-content: space-between; padding: 14px 20px; border-bottom: 1px solid var(--border-soft); }
.git-ai-prompt-dialog > header > div { display: flex; align-items: center; gap: 11px; }
.git-ai-prompt-dialog > header > div > span { width: 36px; height: 36px; display: grid; place-items: center; border-radius: 9px; color: var(--accent); background: color-mix(in srgb, var(--accent) 14%, transparent); }
.git-ai-prompt-dialog h2 { margin: 0; font-size: var(--fs-15); }
.git-ai-prompt-dialog > main { flex: 1 1 auto; min-height: 0; display: flex; flex-direction: column; padding: 18px 20px; overflow: auto; }
.git-ai-prompt-dialog label { flex: 1; min-height: 0; display: flex; flex-direction: column; gap: 8px; }
.git-ai-prompt-dialog label > span { display: flex; flex-direction: column; gap: 4px; }
.git-ai-prompt-dialog label strong { font-size: var(--fs-13); }
.git-ai-prompt-dialog label small { color: var(--muted); font-size: var(--fs-12); line-height: 1.5; }
.git-ai-prompt-dialog label em { align-self: flex-start; padding: 2px 7px; border-radius: 999px; color: var(--muted); background: var(--surface-2); font-size: var(--fs-11); font-style: normal; }
.git-ai-prompt-dialog textarea { flex: 1; min-height: 320px; padding: 12px; resize: none; border: 1px solid var(--border); outline: 0; border-radius: 8px; color: var(--text); background: var(--surface-2); font: var(--fs-12)/1.6 "SFMono-Regular", Consolas, monospace; }
.git-ai-prompt-dialog textarea:focus { border-color: var(--faint); box-shadow: 0 0 0 2px rgb(113 113 109 / .08); }
.git-ai-prompt-dialog label i { align-self: flex-end; color: var(--faint); font-size: var(--fs-11); font-style: normal; }
.git-ai-prompt-dialog label i.invalid,.git-ai-prompt-dialog__error,.git-ai-prompt-dialog__state.is-error { color: var(--danger); }
.git-ai-prompt-dialog__error { margin-bottom: 10px; padding: 9px 11px; border-radius: 7px; background: color-mix(in srgb, var(--danger) 8%, transparent); font-size: var(--fs-12); }
.git-ai-prompt-dialog__state { flex: 1; display: grid; place-items: center; padding: 20px; color: var(--muted); }
.git-ai-prompt-dialog > footer { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 13px 20px; border-top: 1px solid var(--border-soft); }
.git-ai-prompt-dialog > footer > div { display: flex; gap: 8px; }
@media (max-width: 640px) { .git-ai-prompt-dialog > footer { align-items: stretch; flex-direction: column; } .git-ai-prompt-dialog > footer > div { justify-content: flex-end; } }
</style>
