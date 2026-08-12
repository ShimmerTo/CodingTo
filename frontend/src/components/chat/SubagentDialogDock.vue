<script setup>
import { KeyRound, ListTodo, MessageSquareText, ShieldCheck } from 'lucide-vue-next'
import ChatPlanPanel from './ChatPlanPanel.vue'
import { isBrowserProfileDialog, isDCGConfirmationDialog, isPlanConfirmationDialog } from './extensionDialog.js'
import { planItemsFromLines } from './subagentRuntime.js'

// 子 Agent 的权限申请 / 计划确认弹窗统一提升到主对话弹窗区渲染（与主 Agent
// 的 extensionDialog 同位置同样式），每个弹窗标注来源子 Agent 名称，避免把
// 交互挤在狭小的子 Agent 卡片里。响应/ack 由父级按 item.sessionId/runId 路由。
const props = defineProps({
  dialogs: { type: Array, default: () => [] },
  t: { type: Object, required: true }
})

const emit = defineEmits(['respond', 'ack'])
const dialogIcons = { plan: ListTodo, profile: KeyRound, permission: ShieldCheck, interaction: MessageSquareText }
const dialogLabelKeys = {
  plan: 'subagentPlanRequest',
  profile: 'subagentProfileRequest',
  permission: 'subagentPermissionRequest',
  interaction: 'subagentInteractionRequest'
}

function itemKey(item) {
  return `${item.runId || item.messageId || ''}:${item.dialog?.id || ''}`
}

function dialogKind(dialog) {
  if (isPlanConfirmationDialog(dialog)) return 'plan'
  if (isBrowserProfileDialog(dialog)) return 'profile'
  if (isDCGConfirmationDialog(dialog)) return 'permission'
  return 'interaction'
}

function dialogIcon(dialog) {
  return dialogIcons[dialogKind(dialog)]
}

function dialogHeader(item) {
  const key = dialogLabelKeys[dialogKind(item.dialog)]
  const template = props.t[key] || props.t.subagentInteractionRequest
  return template.replace('{agent}', item.agentName)
}

// 计划确认弹窗附带子 Agent 已提交的完整计划步骤（App.vue 从 setWidget 的
// plan-todos/plan-execution 中提取），随确认框一并展示完整计划，不再是
// 只有标题与步数的空洞提示。
function planItems(item) {
  return planItemsFromLines(Array.isArray(item.planLines) ? item.planLines : [])
}
</script>

<template>
  <div v-if="dialogs.length" class="subagent-dialog-dock">
    <section
      v-for="item in dialogs"
      :key="itemKey(item)"
      class="subagent-dialog-dock__item"
    >
      <header class="subagent-dialog-dock__head" :class="`subagent-dialog-dock__head--${dialogKind(item.dialog)}`">
        <span class="subagent-dialog-dock__icon"><component :is="dialogIcon(item.dialog)" :size="14" /></span>
        <span class="subagent-dialog-dock__label">
          {{ dialogHeader(item) }}
        </span>
      </header>
      <ChatPlanPanel
        :items="planItems(item)"
        :dialog="item.dialog"
        :t="t"
        @respond="payload => emit('respond', { item, payload })"
        @ack="ack => emit('ack', { item, id: ack.id })"
      />
    </section>
  </div>
</template>

<style scoped>
.subagent-dialog-dock { display: flex; flex-direction: column; gap: 8px; }
.subagent-dialog-dock__item { min-width: 0; overflow: hidden; border: 1px solid var(--border); border-radius: 12px; background: var(--surface-2); box-shadow: 0 8px 24px rgba(20,20,18,.1); }
.subagent-dialog-dock__item > :deep(.plan-panel) { width: 100%; max-width: 100%; margin: 0; border: 0; border-radius: 0; box-shadow: none; }
.subagent-dialog-dock__item > :deep(.plan-panel > .extension-prompt:first-child) { border-top: 0; }
.subagent-dialog-dock__head { min-height: 34px; display: flex; align-items: center; gap: 8px; padding: 6px 11px; border-bottom: 1px solid var(--border-soft); background: color-mix(in srgb, var(--amber) 8%, var(--surface)); }
.subagent-dialog-dock__icon { width: 22px; height: 22px; flex: 0 0 22px; display: grid; place-items: center; border-radius: 7px; color: var(--amber); background: color-mix(in srgb, var(--amber) 13%, transparent); }
.subagent-dialog-dock__label { min-width: 0; overflow: hidden; color: var(--text); font-size: var(--fs-12); font-weight: 650; text-overflow: ellipsis; white-space: nowrap; }
</style>
