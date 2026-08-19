<script setup>
import { computed, ref, watch } from 'vue'
import { FolderOpen, RotateCcw, Save, ShieldAlert, X } from 'lucide-vue-next'
import { getDcgSettings, saveDcgSettings } from '../backend'
import { useAppContext } from '../composables/appContext'

const props = defineProps({ modelValue: { type: Boolean, default: false } })
const emit = defineEmits(['update:modelValue', 'saved'])
const { config, pushToast } = useAppContext()
const chinese = computed(() => config.preferences.language === 'zh-CN')

const ui = computed(() => chinese.value ? {
  title: 'DCG 危险命令策略', loading: '正在读取策略…', loadFailed: '读取策略失败', close: '关闭', save: '保存', saving: '正在保存…', reset: '撤销修改', saved: 'DCG 策略已保存', syncFailed: '策略已保存，但同步 DCG 放行规则失败：',
  severityHint: '按 DCG 判定的危险等级设置处置方式：放行=直接执行，询问=执行前请求授权，拒绝=直接阻止。未设置时：灾难级/高危默认询问，中危/低危默认放行。',
  actionAsk: '询问', actionAllow: '放行', actionDeny: '拒绝',
  severityLevels: [
    { key: 'critical', zh: '灾难级', en: 'Critical', hint: '如 rm -rf /、git reset --hard' },
    { key: 'high', zh: '高危', en: 'High', hint: '如强制推送、删除文件树' },
    { key: 'medium', zh: '中危', en: 'Medium', hint: '如危险管道、权限修改' },
    { key: 'low', zh: '低危', en: 'Low', hint: '如高风险单条命令' },
  ],
  workspaceAllow: '工作目录放行', workspaceAllowHint: '开启后，DCG 会在其用户级放行列表（allowlist）中为当前全部工作空间目录生成放行规则（含子目录递归），工作空间内的危险命令直接放行、不受上方等级策略限制；关闭时移除全部工作目录放行规则，不影响手动添加的规则。', workspaceCount: '当前 {count} 个工作空间', workspaceEmpty: '尚未配置工作空间，放行规则将为空',
} : {
  title: 'DCG dangerous command policy', loading: 'Loading policy…', loadFailed: 'Unable to load policy', close: 'Close', save: 'Save', saving: 'Saving…', reset: 'Discard changes', saved: 'DCG policy saved', syncFailed: 'Policy saved, but syncing DCG allow rules failed: ',
  severityHint: 'Choose how DCG detections are disposed per severity level: allow runs the command, ask requests authorization, deny blocks it. Unset levels default to ask for critical/high and allow for medium/low.',
  actionAsk: 'Ask', actionAllow: 'Allow', actionDeny: 'Deny',
  severityLevels: [
    { key: 'critical', zh: 'Critical', en: 'Critical', hint: 'e.g. rm -rf /, git reset --hard' },
    { key: 'high', zh: 'High', en: 'High', hint: 'e.g. force push, recursive delete' },
    { key: 'medium', zh: 'Medium', en: 'Medium', hint: 'e.g. dangerous pipelines, permission changes' },
    { key: 'low', zh: 'Low', en: 'Low', hint: 'e.g. high-risk single commands' },
  ],
  workspaceAllow: 'Allow workspace directories', workspaceAllowHint: 'When enabled, DCG generates allow rules for every workspace directory (including subdirectories) in its user-level allowlist, so dangerous commands inside them run without interception and ignore the severity policy above. Disabling removes only those rules; manually added rules stay untouched.', workspaceCount: '{count} workspace(s) currently', workspaceEmpty: 'No workspace configured yet; the allow rules will be empty',
})

const ACTIONS = ['ask', 'allow', 'deny']

const snapshot = ref(null)
const policy = ref({})
const workspaceAllow = ref(false)
const loading = ref(false)
const saving = ref(false)
const error = ref('')

const workspaces = computed(() => (config.environments || []).map(ws => ws.path).filter(Boolean))
const dirty = computed(() => snapshot.value && (workspaceAllow.value !== snapshot.value.workspaceAllow || ui.value.severityLevels.some(level => levelAction(level) !== (snapshot.value.severityPolicy?.[level.key] ?? defaultAction(level.key)))))

function localLevel(level) {
  return chinese.value ? level.zh : level.en
}

function defaultAction(key) {
  return key === 'medium' || key === 'low' ? 'allow' : 'ask'
}

function levelAction(level) {
  return policy.value[level.key] || defaultAction(level.key)
}

function setLevelAction(key, value) {
  policy.value[key] = value
}

function apply(next) {
  snapshot.value = next
  policy.value = { ...(next?.severityPolicy || {}) }
  workspaceAllow.value = !!next?.workspaceAllow
  error.value = ''
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    apply(await getDcgSettings())
  } catch (loadError) {
    error.value = String(loadError?.message || loadError)
  } finally {
    loading.value = false
  }
}

async function persist() {
  if (saving.value || !dirty.value) return
  saving.value = true
  error.value = ''
  try {
    const next = await saveDcgSettings({
      severityPolicy: { ...policy.value },
      workspaceAllow: workspaceAllow.value,
    })
    apply(next)
    config.dcgSettings = next
    pushToast('success', ui.value.saved)
    emit('saved', next)
  } catch (saveError) {
    // 配置已保存，仅 DCG 放行规则同步失败时后端返回错误；保留新值供下次重试。
    error.value = `${ui.value.syncFailed}${String(saveError?.message || saveError)}`
  } finally {
    saving.value = false
  }
}

function discard() {
  if (snapshot.value) apply(snapshot.value)
}

function close() {
  emit('update:modelValue', false)
}

watch(() => props.modelValue, (open) => { if (open) void load() }, { immediate: true })
</script>

<template>
  <div v-if="modelValue" class="modal-backdrop" @pointerdown.self="close">
    <section class="dcg-policy-dialog" role="dialog" aria-modal="true" :aria-label="ui.title">
      <header class="dcg-policy-dialog__head">
        <div class="dcg-policy-dialog__title">
          <span><ShieldAlert :size="18" /></span>
          <div><h2>{{ ui.title }}</h2></div>
        </div>
        <button class="icon-button" :aria-label="ui.close" @click="close"><X :size="16" /></button>
      </header>

      <div v-if="loading" class="dcg-policy-dialog__state">{{ ui.loading }}</div>
      <div v-else-if="!snapshot" class="dcg-policy-dialog__state dcg-policy-dialog__error">{{ ui.loadFailed }}：{{ error }}</div>
      <template v-else>
        <div class="dcg-policy-dialog__body">
          <div v-if="error" class="dcg-policy-dialog__error">{{ error }}</div>
          <p class="dcg-policy-dialog__hint">{{ ui.severityHint }}</p>

          <div class="dcg-policy-list">
            <label v-for="level in ui.severityLevels" :key="level.key" class="dcg-policy-row" :class="`dcg-policy-row--${level.key}`">
              <span class="dcg-policy-row__label">
                <strong>{{ localLevel(level) }}</strong>
                <small>{{ level.hint }}</small>
              </span>
              <select :value="levelAction(level)" @change="setLevelAction(level.key, $event.target.value)">
                <option v-for="action in ACTIONS" :key="action" :value="action">{{ ui[`action${action[0].toUpperCase()}${action.slice(1)}`] }}</option>
              </select>
            </label>
          </div>

          <label class="dcg-policy-workspace">
            <span class="dcg-policy-workspace__head">
              <span><FolderOpen :size="15" /></span>
              <strong>{{ ui.workspaceAllow }}</strong>
              <input type="checkbox" v-model="workspaceAllow" />
            </span>
            <small>{{ ui.workspaceAllowHint }}</small>
            <small class="dcg-policy-workspace__count">{{ workspaces.length ? ui.workspaceCount.replace('{count}', workspaces.length) : ui.workspaceEmpty }}</small>
          </label>
        </div>

        <footer class="dcg-policy-dialog__footer">
          <button class="secondary-button" type="button" :disabled="!dirty || saving" @click="discard"><RotateCcw :size="14" />{{ ui.reset }}</button>
          <div class="dcg-policy-dialog__footer-actions">
            <button class="secondary-button" type="button" @click="close">{{ ui.close }}</button>
            <button class="primary-button" type="button" :disabled="!dirty || saving" @click="persist">{{ saving ? ui.saving : ui.save }}</button>
          </div>
        </footer>
      </template>
    </section>
  </div>
</template>

<style scoped>
.dcg-policy-dialog { width: min(560px, 100%); display: flex; flex-direction: column; border: 1px solid var(--border); border-radius: 14px; background: var(--surface); box-shadow: 0 24px 80px rgba(0,0,0,.28); overflow: hidden; }
.dcg-policy-dialog__head { min-height: 64px; display: flex; align-items: center; justify-content: space-between; gap: 18px; padding: 13px 20px; border-bottom: 1px solid var(--border-soft); }
.dcg-policy-dialog__title { display: flex; align-items: center; gap: 11px; }
.dcg-policy-dialog__title > span { width: 36px; height: 36px; display: grid; place-items: center; border-radius: 9px; color: #b57924; background: rgba(217,164,65,.14); }
.dcg-policy-dialog__title h2 { margin: 0; font-size: var(--fs-15); }
.dcg-policy-dialog__body { display: flex; flex-direction: column; gap: 14px; padding: 18px 20px 20px; overflow-y: auto; }
.dcg-policy-dialog__hint { margin: 0; color: var(--muted); font-size: var(--fs-12); line-height: 1.55; }
.dcg-policy-list { display: flex; flex-direction: column; gap: 10px; }
.dcg-policy-row { min-width: 0; display: flex; align-items: center; gap: 14px; padding: 11px 12px; border: 1px solid var(--border-soft); border-radius: 9px; background: var(--surface-2); }
.dcg-policy-row__label { min-width: 0; flex: 1 1 auto; display: flex; flex-direction: column; gap: 3px; }
.dcg-policy-row__label strong { color: var(--text); font-size: var(--fs-13); }
.dcg-policy-row__label small { color: var(--faint); font-size: var(--fs-11); line-height: 1.5; }
.dcg-policy-row select { width: 108px; flex: 0 0 auto; height: 32px; padding: 0 8px; border: 1px solid var(--border); outline: 0; border-radius: 7px; color: var(--text); background: var(--surface); font-size: var(--fs-13); }
.dcg-policy-row select:focus { border-color: var(--faint); box-shadow: 0 0 0 2px rgba(113,113,109,.08); }
.dcg-policy-row--critical strong { color: var(--danger); }
.dcg-policy-row--high strong { color: #c87f1a; }
.dcg-policy-workspace { display: flex; flex-direction: column; gap: 6px; padding: 12px; border: 1px solid var(--border-soft); border-radius: 9px; background: var(--surface-2); }
.dcg-policy-workspace__head { display: flex; align-items: center; gap: 8px; }
.dcg-policy-workspace__head svg { color: var(--accent); }
.dcg-policy-workspace__head strong { color: var(--text); font-size: var(--fs-13); }
.dcg-policy-workspace__head input { width: 15px; height: 15px; margin-left: auto; accent-color: var(--accent); cursor: pointer; }
.dcg-policy-workspace > small { color: var(--muted); font-size: var(--fs-11); line-height: 1.5; }
.dcg-policy-workspace__count { color: var(--faint) !important; }
.dcg-policy-dialog__error { padding: 9px 11px; border: 1px solid rgba(209,75,66,.3); border-radius: 8px; color: var(--danger); background: rgba(209,75,66,.07); font-size: var(--fs-12); line-height: 1.5; }
.dcg-policy-dialog__state { flex: 1; display: flex; align-items: center; justify-content: center; gap: 9px; padding: 40px; color: var(--muted); font-size: var(--fs-13); }
.dcg-policy-dialog__state.dcg-policy-dialog__error { color: var(--danger); }
.dcg-policy-dialog__footer { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 13px 20px; border-top: 1px solid var(--border-soft); background: var(--surface-2); }
.dcg-policy-dialog__footer-actions { display: flex; align-items: center; gap: 8px; }
</style>
