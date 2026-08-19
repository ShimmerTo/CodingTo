<script setup>
import { computed, ref, watch } from 'vue'
import { RotateCcw, Save, X, ScrollText } from 'lucide-vue-next'
import { getSessionStartupPromptConfig, saveSessionStartupPromptConfig } from '../backend'
import { useAppContext } from '../composables/appContext'

const props = defineProps({ modelValue: { type: Boolean, default: false } })
const emit = defineEmits(['update:modelValue'])
const { config, pushToast } = useAppContext()
const chinese = computed(() => config.preferences.language === 'zh-CN')
const ui = computed(() => chinese.value ? {
  title: '会话启动提示词', close: '关闭', save: '保存', saving: '正在保存…',
  restore: '还原默认', saved: '已保存', restored: '已还原为内置默认值',
  loading: '正在读取…', loadFailed: '读取失败',
  label: '提示词内容', hint: '每次新会话开始时注入。',
  bytes: '字节', defaultBadge: '内置默认值', customBadge: '自定义',
} : {
  title: 'Session startup prompt', close: 'Close', save: 'Save', saving: 'Saving…',
  restore: 'Restore default', saved: 'Saved', restored: 'Restored the built-in default',
  loading: 'Loading…', loadFailed: 'Unable to load',
  label: 'Prompt content', hint: 'Injected at each new session start.',
  bytes: 'bytes', defaultBadge: 'Built-in default', customBadge: 'Custom',
})

const promptState = ref({ loading: false, snapshot: null, text: '', restoring: false })
const loading = computed(() => promptState.value.loading)
const saving = ref(false)
const error = ref('')

const dirty = computed(() => promptState.value.snapshot && promptState.value.text !== promptState.value.snapshot.prompt)
const valid = computed(() => (!promptState.value.snapshot) || new TextEncoder().encode(promptState.value.text).length <= (promptState.value.snapshot?.maxPromptBytes || 32768))

async function load() {
  promptState.value.loading = true; error.value = ''
  try {
    const snap = await getSessionStartupPromptConfig()
    promptState.value.snapshot = snap; promptState.value.text = snap?.prompt || ''
  } catch (e) { error.value = String(e?.message || e) }
  promptState.value.loading = false
}

async function persist() {
  if (!dirty.value || !valid.value || saving.value) return
  saving.value = true; error.value = ''
  try {
    const snap = await saveSessionStartupPromptConfig({ prompt: promptState.value.text, restoreDefault: promptState.value.restoring })
    promptState.value.snapshot = snap; promptState.value.text = snap.prompt || ''
    promptState.value.restoring = false
    pushToast('success', ui.value.saved)
  } catch (e) { error.value = String(e?.message || e) } finally { saving.value = false }
}

function restorePrompt() {
  if (saving.value || promptState.value.snapshot?.isDefault) return
  promptState.value.text = promptState.value.snapshot.defaultPrompt || ''
  promptState.value.restoring = true
}

function close() { emit('update:modelValue', false) }
watch(() => props.modelValue, (open) => { if (open) void load() }, { immediate: true })
</script>

<template>
  <div v-if="modelValue" class="modal-backdrop" @pointerdown.self="close">
    <section class="startup-prompt-dialog" role="dialog" aria-modal="true" :aria-label="ui.title">
      <header>
        <div class="startup-prompt-dialog__title"><span><ScrollText :size="18" /></span><h2>{{ ui.title }}</h2></div>
        <button class="icon-button" :aria-label="ui.close" @click="close"><X :size="16" /></button>
      </header>
      <div v-if="loading" class="startup-prompt-dialog__state">{{ ui.loading }}</div>
      <div v-else-if="error && !promptState.snapshot" class="startup-prompt-dialog__state startup-prompt-dialog__error">{{ ui.loadFailed }}：{{ error }}</div>
      <template v-else>
        <div class="startup-prompt-dialog__body">
          <div v-if="error" class="startup-prompt-dialog__error">{{ error }}</div>
          <label class="startup-prompt-field">
            <span>
              <strong>{{ ui.label }}</strong>
              <small>{{ ui.hint }}</small>
              <em>{{ promptState.snapshot?.isDefault && !promptState.restoring ? ui.defaultBadge : ui.customBadge }}</em>
            </span>
            <textarea v-model="promptState.text" spellcheck="false" @input="promptState.restoring = false"></textarea>
          </label>
        </div>
        <footer>
          <button class="secondary-button" :disabled="promptState.snapshot?.isDefault || promptState.restoring || saving" @click="restorePrompt"><RotateCcw :size="14" />{{ ui.restore }}</button>
          <div><button class="secondary-button" @click="close">{{ ui.close }}</button><button class="primary-button" :disabled="!dirty || !valid || saving" @click="persist"><Save :size="14" />{{ saving ? ui.saving : ui.save }}</button></div>
        </footer>
      </template>
    </section>
  </div>
</template>

<style scoped>
.startup-prompt-dialog { width: min(760px, 94vw); height: min(650px, 90vh); display: flex; flex-direction: column; overflow: hidden; border: 1px solid var(--border); border-radius: 14px; background: var(--surface); box-shadow: 0 24px 80px rgba(0,0,0,.28); }
.startup-prompt-dialog > header { min-height: 64px; display: flex; align-items: center; justify-content: space-between; padding: 14px 20px; border-bottom: 1px solid var(--border-soft); }
.startup-prompt-dialog__title { display: flex; align-items: center; gap: 11px; }
.startup-prompt-dialog__title > span { width: 36px; height: 36px; display: grid; place-items: center; border-radius: 9px; color: #4f79b8; background: rgba(79,121,184,.14); }
.startup-prompt-dialog__title h2 { margin: 0; font-size: var(--fs-15); }
.startup-prompt-dialog__body { flex: 1; min-height: 0; display: flex; flex-direction: column; padding: 18px 20px; overflow: auto; }
.startup-prompt-field { flex: 1; min-height: 0; display: flex; flex-direction: column; gap: 8px; }
.startup-prompt-field > span { display: flex; flex-direction: column; gap: 4px; }
.startup-prompt-field strong { font-size: var(--fs-13); }
.startup-prompt-field small { color: var(--muted); font-size: var(--fs-12); line-height: 1.5; }
.startup-prompt-field em { align-self: flex-start; padding: 2px 7px; border-radius: 999px; color: var(--muted); background: var(--surface-2); font-size: var(--fs-11); font-style: normal; }
.startup-prompt-field textarea { flex: 1; min-height: 320px; padding: 12px; resize: none; border: 1px solid var(--border); outline: 0; border-radius: 8px; color: var(--text); background: var(--surface-2); font: var(--fs-12)/1.6 "SFMono-Regular", Consolas, monospace; }
.startup-prompt-field textarea:focus { border-color: var(--faint); box-shadow: 0 0 0 2px rgba(113,113,109,.08); }
.startup-prompt-dialog__state { flex: 1; display: grid; place-items: center; color: var(--muted); }
.startup-prompt-dialog__error { padding: 9px 11px; border-radius: 7px; background: rgba(209,67,67,.08); font-size: var(--fs-12); }
.startup-prompt-dialog > footer { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 13px 20px; border-top: 1px solid var(--border-soft); }
.startup-prompt-dialog > footer > div { display: flex; gap: 8px; }
@media (max-width: 640px) { .startup-prompt-dialog > footer { align-items: stretch; flex-direction: column; } .startup-prompt-dialog > footer > div { justify-content: flex-end; } }
</style>