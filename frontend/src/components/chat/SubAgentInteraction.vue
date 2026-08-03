<script setup>
import { computed } from 'vue'
import ChatPlanPanel from './ChatPlanPanel.vue'
import ChatExecutionPlan from './ChatExecutionPlan.vue'
import { planItemsFromLines } from './subagentRuntime.js'

// 子 Agent 卡片/详情内的计划进度展示。交互弹窗（权限申请、计划确认等）已
// 统一提升到主对话弹窗区渲染（见 App.vue 的 subagentDialogs / SubagentDialogDock），
// 此处只消费 setWidget 写入的只读计划步骤列表，不再处理 dialog。
// compact（卡片内）：复用对话底部计划条的紧凑形态——缩小居中、鼠标移入展开
// 完整计划列表；非 compact（详情弹窗内）：保留完整计划面板。
const props = defineProps({
  sessionId: { type: Number, required: true },
  runId: { type: String, required: true },
  agentKey: { type: String, required: true },
  uiState: { type: Object, default: () => ({ widgets: {} }) },
  compact: { type: Boolean, default: false },
  t: { type: Object, required: true }
})

const emit = defineEmits(['error'])

const planLines = computed(() => (
  props.uiState?.widgets?.['plan-execution']
  || props.uiState?.widgets?.['plan-todos']
  || []
))
const items = computed(() => planItemsFromLines(planLines.value))
const visible = computed(() => Boolean(items.value.length))
</script>

<template>
  <div v-if="visible" class="subagent-interaction" :class="{ 'is-compact': compact }">
    <ChatExecutionPlan v-if="compact" :items="items" :t="t" />
    <ChatPlanPanel v-else :items="items" :dialog="null" :t="t" />
  </div>
</template>

<style scoped>
.subagent-interaction { min-width: 0; }
/* 卡片内：执行条缩小居中、贴近对话区底部，hover 展开完整计划（复用 exec-bar）。 */
.subagent-interaction.is-compact :deep(.exec-bar) { max-width: 100%; margin: 0 auto 8px; }
.subagent-interaction.is-compact :deep(.exec-bar__head) { padding: 5px 9px; }
.subagent-interaction.is-compact :deep(.exec-bar__text) { max-width: 240px; }
.subagent-interaction.is-compact :deep(.exec-bar__flyout) { z-index: 30; }
.subagent-interaction :deep(.plan-panel) { width: 100%; max-height: min(42vh, 360px); margin: 0; border-radius: 10px; box-shadow: none; }
</style>
