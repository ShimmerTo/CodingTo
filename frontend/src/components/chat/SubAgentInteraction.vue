<script setup>
import { computed, nextTick, ref, watch } from 'vue'
import { ackSubagentUI, saveBrowserProfile, respondSubagentUI } from '../../backend.js'
import ChatPlanPanel from './ChatPlanPanel.vue'
import { planItemsFromLines } from './subagentRuntime.js'

const props = defineProps({
  sessionId: { type: Number, required: true },
  runId: { type: String, required: true },
  agentKey: { type: String, required: true },
  uiState: { type: Object, default: () => ({ widgets: {} }) },
  compact: { type: Boolean, default: false },
  t: { type: Object, required: true }
})

const emit = defineEmits(['responded', 'error'])
const submitting = ref(false)
const submittedID = ref('')
const submitError = ref('')
// 已向后端确认渲染成功的对话框 id，防止重复 ack。
const ackedID = ref('')

const dialog = computed(() => {
  const value = props.uiState?.dialog
  if (!value || value.id === submittedID.value) return null
  return { ...value, saving: submitting.value, error: submitError.value || value.error }
})
const planLines = computed(() => (
  props.uiState?.widgets?.['plan-execution']
  || props.uiState?.widgets?.['plan-todos']
  || []
))
const items = computed(() => planItemsFromLines(planLines.value))
const visible = computed(() => Boolean(dialog.value || items.value.length))

watch(() => props.uiState?.dialog?.id, id => {
  if (id !== submittedID.value) submittedID.value = ''
  submitError.value = ''
})

// 对话框渲染入 DOM 后确认渲染成功（ack），解除桥接端 60s 渲染窗口超时；此后
// 用户可任意长时间作答（如 editor 对话框长编辑），不会被强制取消、输入丢失。
// 与父 agent 的 extension_ui_ack 语义对齐：ack 只是“弹窗已展示”的确认，不是
// 答案；失败静默（仅失去超时豁免），按 id 去重。
watch(() => props.uiState?.dialog?.id, async id => {
  if (id == null || id === '' || id === ackedID.value) return
  const requestID = String(id)
  ackedID.value = requestID
  await nextTick()
  try {
    await ackSubagentUI(props.sessionId, props.runId, requestID)
  } catch {
    // 与父 agent 一致：ack 失败静默，对话框仍可作答。
  }
}, { immediate: true })

async function respond(payload) {
  const current = dialog.value
  if (!current || submitting.value) return
  submitting.value = true
  submitError.value = ''
  try {
    let response = { id: current.id, ...payload }
    if (payload?.browserProfile) {
      const form = payload.browserProfile
      const profile = await saveBrowserProfile({
        key: form.key,
        targetUrl: form.targetUrl,
        loginUrl: form.targetUrl,
        authMode: 'manual'
      })
      response = { id: current.id, value: profile.id }
    }
    await respondSubagentUI(props.sessionId, props.runId, response)
    submittedID.value = current.id
    emit('responded', response)
  } catch (error) {
    submitError.value = String(error?.message || error)
    emit('error', submitError.value)
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <div v-if="visible" class="subagent-interaction" :class="{ 'is-compact': compact }">
    <ChatPlanPanel :items="items" :dialog="dialog" :t="t" @respond="respond" />
  </div>
</template>

<style scoped>
.subagent-interaction { min-width: 0; }
.subagent-interaction :deep(.plan-panel) { width: 100%; max-height: min(42vh, 360px); margin: 0; border-radius: 10px; box-shadow: none; }
.subagent-interaction.is-compact :deep(.plan-panel) { max-height: 210px; border-right: 0; border-bottom: 0; border-left: 0; border-radius: 0; }
.subagent-interaction.is-compact :deep(.plan-proposal) { padding: 9px 12px 5px; }
.subagent-interaction.is-compact :deep(.plan-proposal__list) { max-height: 92px; }
.subagent-interaction.is-compact :deep(.extension-prompt) { padding: 9px 12px; }
</style>
