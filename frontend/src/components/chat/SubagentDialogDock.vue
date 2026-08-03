<script setup>
import { Bot, ShieldCheck } from 'lucide-vue-next'
import ChatPlanPanel from './ChatPlanPanel.vue'
import { planItemsFromLines } from './subagentRuntime.js'

// 子 Agent 的权限申请 / 计划确认弹窗统一提升到主对话弹窗区渲染（与主 Agent
// 的 extensionDialog 同位置同样式），每个弹窗标注来源子 Agent 名称，避免把
// 交互挤在狭小的子 Agent 卡片里。响应/ack 由父级按 item.sessionId/runId 路由。
const props = defineProps({
  dialogs: { type: Array, default: () => [] },
  t: { type: Object, required: true }
})

const emit = defineEmits(['respond', 'ack'])

function itemKey(item) {
  return `${item.runId || item.messageId || ''}:${item.dialog?.id || ''}`
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
      <header class="subagent-dialog-dock__head">
        <ShieldCheck :size="13" />
        <Bot :size="13" class="subagent-dialog-dock__agent-icon" />
        <span class="subagent-dialog-dock__label">
          {{ t.subagentPermissionRequest.replace('{agent}', item.agentName) }}
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
.subagent-dialog-dock__item { min-width: 0; }
.subagent-dialog-dock__item > :deep(.plan-panel) { width: 100%; max-width: 100%; margin: 0; }
.subagent-dialog-dock__head { display: flex; align-items: center; gap: 6px; padding: 5px 10px; border: 1px solid var(--border); border-bottom: 0; border-radius: 10px 10px 0 0; background: color-mix(in srgb, var(--amber) 10%, var(--surface-2)); }
.subagent-dialog-dock__head svg { flex: 0 0 auto; color: var(--amber); }
.subagent-dialog-dock__agent-icon { color: var(--faint); }
.subagent-dialog-dock__label { min-width: 0; overflow: hidden; color: var(--muted); font-size: var(--fs-12); font-weight: 600; text-overflow: ellipsis; white-space: nowrap; }
</style>
