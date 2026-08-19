<script setup>
// 精简对话模式下的「折叠摘要块」：连续的思考过程与工具调用归纳为
// 「思考 x 次 · 工具调用 x 次 · 总执行时长」一行，点击箭头展开后逐条展示原文。
import { computed, ref } from 'vue'
import { ChevronDown, LoaderCircle } from 'lucide-vue-next'
import { formatDuration } from './chatFormatters.js'
import { toolLineChanges, toolStatus } from './chatToolPresentation.js'
import { conciseBlockDuration } from './conciseChat.js'
import ChatMessageItem from './ChatMessageItem.vue'

const props = defineProps({
  // 折叠块内的条目：{ kind: 'thinking' | 'tool', message }
  items: { type: Array, required: true },
  sessionId: { type: Number, default: 0 },
  agents: { type: Array, default: () => [] },
  now: { type: Number, default: 0 },
  t: { type: Object, required: true },
  collapseToolsByDefault: { type: Boolean, default: false },
  showIdentity: { type: Boolean, default: true },
  // 子 Agent 快捷导航定位：子代理消息 id → 导航序号，用于块内条目打上 data-subagent-index。
  subagentIndexById: { type: Object, default: () => new Map() }
})

const emit = defineEmits(['update-thinking-open', 'artifact-error', 'open-change-file', 'open-git-diff', 'open-subagent-details', 'preview-image'])

// 懒渲染：摘要块默认折叠，展开后才渲染内部条目（长对话下避免一次性渲染大量工具 DOM）。
const blockOpen = ref(false)
function onToggle(event) {
  blockOpen.value = Boolean(event.currentTarget.open)
}

const thinkingCount = computed(() => props.items.filter(item => item.kind === 'thinking').length)
const toolCount = computed(() => props.items.filter(item => item.kind === 'tool').length)
const lineChanges = computed(() => {
  let added = 0
  let deleted = 0
  for (const item of props.items) {
    if (item.kind !== 'tool') continue
    const changes = toolLineChanges(item.message)
    added += Number(changes?.added || 0)
    deleted += Number(changes?.deleted || 0)
  }
  return added || deleted ? { added, deleted } : null
})

// 折叠块内仅持续更新仍在运行的条目；折叠状态下不渲染条目 DOM。
function itemLive(item) {
  if (item.kind === 'thinking') return Boolean(item.message.live) && !item.message.content
  return toolStatus(item.message) !== 'done'
}

function onThinkingOpen(item, open) {
  emit('update-thinking-open', { id: item.message.id, open })
}

// 块内是否仍有条目在执行：驱动摘要行背景呼吸 + 左侧转圈。
const running = computed(() => props.items.some(item => itemLive(item)))

const summaryText = computed(() => {
  const parts = []
  if (thinkingCount.value) parts.push(props.t.conciseThinkingCount.replace('{count}', String(thinkingCount.value)))
  if (toolCount.value) parts.push(props.t.conciseToolCount.replace('{count}', String(toolCount.value)))
  const duration = conciseBlockDuration(props.items, props.now)
  parts.push(props.t.conciseTotalDuration.replace('{duration}', formatDuration(duration)))
  return parts.join(props.t.conciseSep)
})
</script>

<template>
  <div class="concise-block-row" :class="{ 'concise-block-row--indented': showIdentity }">
    <details class="concise-block" @toggle="onToggle">
      <summary :class="{ 'concise-block--running': running }">
        <LoaderCircle v-if="running" class="concise-block__spinner spin" :size="12" />
        <span class="concise-block__summary">{{ summaryText }}<template v-if="lineChanges">{{ t.conciseSep }}</template></span>
        <span
          v-if="lineChanges"
          class="concise-block__changes"
          :aria-label="`+${lineChanges.added} ${t.lineUnit}, -${lineChanges.deleted} ${t.lineUnit}`"
        >
          <b class="is-added">+{{ lineChanges.added }}</b>
          <b class="is-deleted">-{{ lineChanges.deleted }}</b>
        </span>
        <ChevronDown class="details-chevron" :size="13" />
      </summary>
      <div v-if="blockOpen" class="concise-block__items">
        <ChatMessageItem
          v-for="item in items"
          :key="item.message.id"
          :message="item.message"
          :session-id="sessionId"
          :agents="agents"
          :now="itemLive(item) ? now : 0"
          :t="t"
          :show-identity="false"
          :hide-content="true"
          :collapse-tools-by-default="collapseToolsByDefault"
          :data-subagent-index="subagentIndexById.get(item.message.id)"
          @update-thinking-open="onThinkingOpen(item, $event)"
          @artifact-error="emit('artifact-error', $event)"
          @open-change-file="emit('open-change-file', $event)"
          @open-git-diff="emit('open-git-diff', $event)"
          @open-subagent-details="emit('open-subagent-details', $event)"
          @preview-image="emit('preview-image', $event)"
        />
      </div>
    </details>
  </div>
</template>

<style scoped src="../../styles/chat/fold-block.css"></style>
