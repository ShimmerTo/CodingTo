<script setup>
import { computed, ref, watch } from 'vue'
import { ListChecks, RotateCcw, Save, X } from 'lucide-vue-next'
import { getPlanConfig, savePlanConfig } from '../backend'
import { useAppContext } from '../composables/appContext'

const props = defineProps({ modelValue: { type: Boolean, default: false } })
const emit = defineEmits(['update:modelValue'])
const { config, pushToast } = useAppContext()
const chinese = computed(() => config.preferences.language === 'zh-CN')
const ui = computed(() => chinese.value ? {
  title: 'Plan Mode 配置', loading: '正在读取提示词…', close: '关闭', save: '保存', saving: '正在保存…', restore: '还原默认', saved: 'Plan Mode 提示词已保存', restored: '已还原为内置默认提示词',
  prompt: '行为提示词', hint: '全局生效。工具参数、结构化返回格式和界面协议由扩展固定，不受此处修改影响。保存后应用于所有 Agent 的后续请求。', bytes: '字节', loadFailed: '读取失败', defaultBadge: '正在使用内置默认值', customBadge: '正在使用自定义值',
} : {
  title: 'Plan Mode configuration', loading: 'Loading prompt…', close: 'Close', save: 'Save', saving: 'Saving…', restore: 'Restore default', saved: 'Plan Mode prompt saved', restored: 'Restored the built-in default prompt',
  prompt: 'Behavior prompt', hint: 'Applies globally. Tool parameters, structured results, and UI protocol remain fixed by the extension. Saved changes apply to subsequent requests for every agent.', bytes: 'bytes', loadFailed: 'Unable to load', defaultBadge: 'Using built-in default', customBadge: 'Using custom prompt',
})

const snapshot = ref(null)
const prompt = ref('')
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const byteLength = computed(() => new TextEncoder().encode(prompt.value).length)
const dirty = computed(() => snapshot.value && prompt.value !== snapshot.value.prompt)
const valid = computed(() => byteLength.value <= (snapshot.value?.maxPromptBytes || 32768))

function apply(value) { snapshot.value = value; prompt.value = value?.prompt || '' }
async function load() {
  loading.value = true; error.value = ''
  try { apply(await getPlanConfig()) } catch (e) { error.value = String(e?.message || e) } finally { loading.value = false }
}
async function persist() {
  if (!dirty.value || !valid.value || saving.value) return
  saving.value = true; error.value = ''
  try { apply(await savePlanConfig({ prompt: prompt.value, restoreDefault: false })); pushToast('success', ui.value.saved) } catch (e) { error.value = String(e?.message || e) } finally { saving.value = false }
}
async function restoreDefault() {
  if (saving.value || snapshot.value?.isDefault) return
  saving.value = true; error.value = ''
  try { apply(await savePlanConfig({ restoreDefault: true })); pushToast('success', ui.value.restored) } catch (e) { error.value = String(e?.message || e) } finally { saving.value = false }
}
function close() { emit('update:modelValue', false) }
watch(() => props.modelValue, (open) => { if (open) void load() }, { immediate: true })
</script>

<template>
  <div v-if="modelValue" class="modal-backdrop" @pointerdown.self="close">
    <section class="prompt-config-dialog" role="dialog" aria-modal="true" :aria-label="ui.title">
      <header>
        <div class="prompt-config-dialog__title"><span><ListChecks :size="18" /></span><h2>{{ ui.title }}</h2></div>
        <button class="icon-button" :aria-label="ui.close" @click="close"><X :size="16" /></button>
      </header>
      <div v-if="loading" class="prompt-config-dialog__state">{{ ui.loading }}</div>
      <div v-else-if="!snapshot" class="prompt-config-dialog__state prompt-config-dialog__error">{{ ui.loadFailed }}：{{ error }}</div>
      <template v-else>
        <div class="prompt-config-dialog__body">
          <div v-if="error" class="prompt-config-dialog__error">{{ error }}</div>
          <label class="prompt-config-field">
            <span><strong>{{ ui.prompt }}</strong><small>{{ ui.hint }}</small><em>{{ snapshot.isDefault ? ui.defaultBadge : ui.customBadge }}</em></span>
            <textarea v-model="prompt" spellcheck="false"></textarea>
            <i :class="{ invalid: !valid }">{{ byteLength }} / {{ snapshot.maxPromptBytes }} {{ ui.bytes }}</i>
          </label>
        </div>
        <footer>
          <button class="secondary-button" :disabled="snapshot.isDefault || saving" @click="restoreDefault"><RotateCcw :size="14" />{{ ui.restore }}</button>
          <div><button class="secondary-button" @click="close">{{ ui.close }}</button><button class="primary-button" :disabled="!dirty || !valid || saving" @click="persist"><Save :size="14" />{{ saving ? ui.saving : ui.save }}</button></div>
        </footer>
      </template>
    </section>
  </div>
</template>

<style scoped>
.prompt-config-dialog { width: min(760px, 94vw); height: min(650px, 90vh); display: flex; flex-direction: column; overflow: hidden; border: 1px solid var(--border); border-radius: 14px; background: var(--surface); box-shadow: 0 24px 80px rgba(0,0,0,.28); }
.prompt-config-dialog > header { min-height: 64px; display: flex; align-items: center; justify-content: space-between; padding: 14px 20px; border-bottom: 1px solid var(--border-soft); }
.prompt-config-dialog__title { display: flex; align-items: center; gap: 11px; }
.prompt-config-dialog__title > span { width: 36px; height: 36px; display: grid; place-items: center; border-radius: 9px; color: #4f79b8; background: rgba(79,121,184,.14); }
.prompt-config-dialog__title h2 { margin: 0; font-size: var(--fs-15); }
.prompt-config-dialog__body { flex: 1; min-height: 0; display: flex; padding: 18px 20px; overflow: auto; }
.prompt-config-field { flex: 1; min-height: 0; display: flex; flex-direction: column; gap: 8px; }
.prompt-config-field > span { display: flex; flex-direction: column; gap: 4px; }
.prompt-config-field strong { font-size: var(--fs-13); }
.prompt-config-field small { color: var(--muted); font-size: var(--fs-12); line-height: 1.5; }
.prompt-config-field em { align-self: flex-start; padding: 2px 7px; border-radius: 999px; color: var(--muted); background: var(--surface-2); font-size: var(--fs-11); font-style: normal; }
.prompt-config-field textarea { flex: 1; min-height: 320px; padding: 12px; resize: none; border: 1px solid var(--border); outline: 0; border-radius: 8px; color: var(--text); background: var(--surface-2); font: var(--fs-12)/1.6 "SFMono-Regular", Consolas, monospace; }
.prompt-config-field textarea:focus { border-color: var(--faint); box-shadow: 0 0 0 2px rgba(113,113,109,.08); }
.prompt-config-field i { align-self: flex-end; color: var(--faint); font-size: var(--fs-11); font-style: normal; }
.prompt-config-field i.invalid, .prompt-config-dialog__error { color: var(--danger); }
.prompt-config-dialog__state { flex: 1; display: grid; place-items: center; color: var(--muted); }
.prompt-config-dialog__error { padding: 9px 11px; border-radius: 7px; background: rgba(209,67,67,.08); font-size: var(--fs-12); }
.prompt-config-dialog > footer { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 13px 20px; border-top: 1px solid var(--border-soft); }
.prompt-config-dialog > footer > div { display: flex; gap: 8px; }
@media (max-width: 640px) { .prompt-config-dialog > footer { align-items: stretch; flex-direction: column; } .prompt-config-dialog > footer > div { justify-content: flex-end; } }
</style>
