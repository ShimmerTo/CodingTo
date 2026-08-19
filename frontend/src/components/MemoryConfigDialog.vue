<script setup>
import { computed, ref, watch } from 'vue'
import { BrainCircuit, RotateCcw, Save, X } from 'lucide-vue-next'
import { getMemoryConfig, saveMemoryConfig } from '../backend'
import { useAppContext } from '../composables/appContext'

const props = defineProps({ modelValue: { type: Boolean, default: false } })
const emit = defineEmits(['update:modelValue', 'saved'])
const { config, pushToast } = useAppContext()
const chinese = computed(() => config.preferences.language === 'zh-CN')
const ui = computed(() => chinese.value ? {
  title: 'Memory 配置', loading: '正在读取记忆配置…', close: '关闭', save: '保存', saving: '正在保存…', reset: '撤销修改', saved: 'Memory 配置已保存',
  userMemory: '全局用户记忆', userHint: '仅保存用户明确表达且长期、稳定、可跨项目复用的偏好。新会话注入一次；不要保存项目事实、一次性要求、客户原话或敏感信息。',
  placeholder: '# User Memory\n\n- 偏好简洁的实现说明', historyLimit: '项目历史保留条数', historyHint: '每个项目的 .codingto/history 默认保留最近 100 条。仅在新记录写入时清理，不扫描所有工作区。', bytes: '字节', loadFailed: '读取失败',
} : {
  title: 'Memory configuration', loading: 'Loading memory configuration…', close: 'Close', save: 'Save', saving: 'Saving…', reset: 'Discard changes', saved: 'Memory configuration saved',
  userMemory: 'Global user memory', userHint: 'Keep only explicit, stable preferences reusable across projects. It is injected once per new session; do not store project facts, one-off requests, customer quotes, or secrets.',
  placeholder: '# User Memory\n\n- Prefer concise implementation notes', historyLimit: 'Project history retention', historyHint: 'Each project keeps the newest records in .codingto/history (100 by default). Pruning runs only when a record is written.', bytes: 'bytes', loadFailed: 'Unable to load',
})

const snapshot = ref(null)
const userMemory = ref('')
const projectHistoryLimit = ref(100)
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const byteLength = computed(() => new TextEncoder().encode(userMemory.value).length)
const dirty = computed(() => snapshot.value && (userMemory.value !== snapshot.value.userMemory || Number(projectHistoryLimit.value) !== Number(snapshot.value.projectHistoryLimit)))
const valid = computed(() => projectHistoryLimit.value >= 1 && projectHistoryLimit.value <= 10000 && byteLength.value <= (snapshot.value?.maxUserMemoryBytes || 8192))

function apply(value) {
  snapshot.value = value
  userMemory.value = value?.userMemory || ''
  projectHistoryLimit.value = value?.projectHistoryLimit || 100
}
async function load() {
  loading.value = true
  error.value = ''
  try { apply(await getMemoryConfig()) } catch (e) { error.value = String(e?.message || e) } finally { loading.value = false }
}
async function persist() {
  if (!dirty.value || !valid.value || saving.value) return
  saving.value = true
  error.value = ''
  try {
    const next = await saveMemoryConfig({ userMemory: userMemory.value, projectHistoryLimit: Number(projectHistoryLimit.value) })
    apply(next)
    pushToast('success', ui.value.saved)
    emit('saved', next)
  } catch (e) { error.value = String(e?.message || e) } finally { saving.value = false }
}
function close() { emit('update:modelValue', false) }
watch(() => props.modelValue, (open) => { if (open) void load() }, { immediate: true })
</script>

<template>
  <div v-if="modelValue" class="modal-backdrop" @pointerdown.self="close">
    <section class="memory-dialog" role="dialog" aria-modal="true" :aria-label="ui.title">
      <header>
        <div class="memory-dialog__title"><span><BrainCircuit :size="18" /></span><div><h2>{{ ui.title }}</h2><small v-if="snapshot">{{ snapshot.userMemoryPath }}</small></div></div>
        <button class="icon-button" :aria-label="ui.close" @click="close"><X :size="16" /></button>
      </header>
      <div v-if="loading" class="memory-dialog__state">{{ ui.loading }}</div>
      <div v-else-if="!snapshot" class="memory-dialog__state memory-dialog__error">{{ ui.loadFailed }}：{{ error }}</div>
      <template v-else>
        <div class="memory-dialog__body">
          <div v-if="error" class="memory-dialog__error">{{ error }}</div>
          <label class="memory-field memory-field--memory">
            <span><strong>{{ ui.userMemory }}</strong><small>{{ ui.userHint }}</small></span>
            <textarea v-model="userMemory" :placeholder="ui.placeholder" spellcheck="false"></textarea>
            <em :class="{ invalid: !valid }">{{ byteLength }} / {{ snapshot.maxUserMemoryBytes }} {{ ui.bytes }}</em>
          </label>
          <label class="memory-field memory-field--limit">
            <span><strong>{{ ui.historyLimit }}</strong><small>{{ ui.historyHint }}</small></span>
            <input v-model.number="projectHistoryLimit" type="number" min="1" max="10000" step="1" />
          </label>
        </div>
        <footer>
          <div class="memory-dialog__left"><button class="secondary-button" :disabled="!dirty || saving" @click="apply(snapshot)"><RotateCcw :size="14" />{{ ui.reset }}</button></div>
          <div><button class="secondary-button" @click="close">{{ ui.close }}</button><button class="primary-button" :disabled="!dirty || !valid || saving" @click="persist"><Save :size="14" />{{ saving ? ui.saving : ui.save }}</button></div>
        </footer>
      </template>
    </section>
  </div>
</template>

<style scoped>
.memory-dialog { width: min(760px, 94vw); height: min(680px, 90vh); display: flex; flex-direction: column; overflow: hidden; border: 1px solid var(--border); border-radius: 14px; background: var(--surface); box-shadow: 0 24px 80px rgba(0,0,0,.28); }
.memory-dialog > header { min-height: 68px; display: flex; align-items: center; justify-content: space-between; padding: 14px 20px; border-bottom: 1px solid var(--border-soft); }
.memory-dialog__title { min-width: 0; display: flex; align-items: center; gap: 11px; }
.memory-dialog__title > span { width: 36px; height: 36px; display: grid; flex: 0 0 36px; place-items: center; border-radius: 9px; color: #8767c7; background: rgba(135,103,199,.14); }
.memory-dialog__title h2 { margin: 0; font-size: var(--fs-15); }
.memory-dialog__title small { display: block; max-width: 590px; margin-top: 3px; overflow: hidden; color: var(--faint); font: var(--fs-12) "SFMono-Regular", Consolas, monospace; text-overflow: ellipsis; white-space: nowrap; }
.memory-dialog__body { flex: 1; min-height: 0; display: flex; flex-direction: column; gap: 14px; padding: 18px 20px; overflow-y: auto; }
.memory-field { display: flex; flex-direction: column; gap: 8px; }
.memory-field--memory { min-height: 300px; }
.memory-field--limit { flex-direction: row; align-items: center; gap: 16px; }
.memory-field--limit > span { flex: 1; min-width: 0; }
.memory-field > span { display: flex; flex-direction: column; gap: 4px; }
.memory-field strong { font-size: var(--fs-13); }
.memory-field small { color: var(--muted); font-size: var(--fs-12); line-height: 1.5; }
.memory-field textarea, .memory-field input { border: 1px solid var(--border); outline: 0; border-radius: 8px; color: var(--text); background: var(--surface-2); }
.memory-field textarea { flex: 1; min-height: 260px; padding: 12px; resize: vertical; font: var(--fs-12)/1.6 "SFMono-Regular", Consolas, monospace; }
.memory-field input { width: 180px; height: 36px; padding: 0 10px; font-size: var(--fs-13); }
.memory-field textarea:focus, .memory-field input:focus { border-color: var(--faint); box-shadow: 0 0 0 2px rgba(113,113,109,.08); }
.memory-field em { align-self: flex-end; color: var(--faint); font-size: var(--fs-11); font-style: normal; }
.memory-field em.invalid, .memory-dialog__error { color: var(--danger); }
.memory-dialog__state { flex: 1; display: grid; place-items: center; color: var(--muted); }
.memory-dialog__error { padding: 9px 11px; border-radius: 7px; background: rgba(209,67,67,.08); font-size: var(--fs-12); }
.memory-dialog > footer { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 13px 20px; border-top: 1px solid var(--border-soft); }
.memory-dialog > footer > div { display: flex; gap: 8px; }
.memory-dialog > footer .memory-dialog__left { flex-wrap: wrap; }
@media (max-width: 640px) { .memory-dialog > footer { align-items: stretch; flex-direction: column; } .memory-dialog > footer > div { justify-content: flex-end; } }
</style>