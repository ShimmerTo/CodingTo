<script setup>
import { computed } from 'vue'
import { Bot, X } from 'lucide-vue-next'
import { useAppContext, agentAvatar, isImageAvatar } from '../composables/appContext'

const props = defineProps({
  modelValue: { type: Boolean, default: false },
  toolKey: { type: String, required: true },
  toolName: { type: String, required: true },
})
const emit = defineEmits(['update:modelValue'])

const { t, agentList, newAgentId, extensionSnapshot, extensionBusy, assignAgentExtension } = useAppContext()

// 草稿中的新智能体尚未保存，不可参与分配。
const agents = computed(() =>
  (agentList.value || []).filter(agent => agent.id !== newAgentId.value)
)

function agentInstalled(agent) {
  const status = (extensionSnapshot.value?.recommended?.[agent.id] || []).find(tool => tool.key === props.toolKey)
  if (!status) return false
  return props.toolKey === 'figma' ? !!status.installed : !!status.enabled
}

function agentBusy(agent) {
  return extensionBusy.value === `assign:${props.toolKey}:${agent.id}`
}

function onToggle(agent) {
  assignAgentExtension(agent.id, props.toolKey, !agentInstalled(agent))
}

function requestClose() {
  emit('update:modelValue', false)
}
</script>

<template>
  <div v-if="modelValue" class="modal-backdrop" @pointerdown.self="requestClose">
    <section class="agent-editor-dialog assign-agent-dialog" role="dialog" aria-modal="true" aria-labelledby="assign-agent-title">
      <header class="agent-editor-dialog__head">
        <h2 id="assign-agent-title">{{ t.assignAgentTitle.replace('{name}', toolName) }}</h2>
        <button class="icon-button" :aria-label="t.closeDialog" @click="requestClose"><X :size="16" /></button>
      </header>
      <div class="agent-editor-dialog__body">
        <p class="assign-agent-dialog__hint">{{ t.assignAgentHint }}</p>
        <fieldset class="assign-agent-picker">
          <legend>{{ t.assignedAgents }}</legend>
          <label v-for="agent in agents" :key="agent.id" class="assign-agent-option">
            <input type="checkbox" :checked="agentInstalled(agent)" :disabled="agentBusy(agent)" @change="onToggle(agent)" />
            <span class="assign-agent-option__avatar">
              <img v-if="isImageAvatar(agentAvatar(agent))" :src="agentAvatar(agent)" alt="" />
              <template v-else>{{ agentAvatar(agent) || '🤖' }}</template>
            </span>
            <span class="assign-agent-option__copy">
              <strong>{{ agent.name }}</strong>
              <small><span class="status-dot" :class="agentInstalled(agent) ? 'active' : 'missing'"></span>{{ agentInstalled(agent) ? t.installed : t.notInstalled }}</small>
            </span>
          </label>
        </fieldset>
        <p v-if="!agents.length" class="assign-agent-list__empty">
          <Bot :size="18" />
          <span>{{ t.noAgentsToAssign }}</span>
        </p>
      </div>
    </section>
  </div>
</template>

<style scoped>
.assign-agent-dialog { width: min(560px, 100%); }
.assign-agent-dialog__hint { margin: 0 0 14px; color: var(--muted); font-size: var(--fs-13); line-height: 1.6; }
.assign-agent-picker { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 7px; margin: 0; padding: 11px; border: 1px solid var(--border); border-radius: 8px; }
.assign-agent-picker legend { grid-column: 1 / -1; padding: 0 4px; color: var(--muted); font-size: var(--fs-12); font-weight: 650; }
.assign-agent-option { display: flex; align-items: center; gap: 8px; min-height: 36px; padding: 7px 9px; border: 1px solid var(--border); border-radius: 8px; color: var(--text); background: var(--surface); cursor: pointer; font-size: var(--fs-12); }
.assign-agent-option:hover { background: var(--hover); }
.assign-agent-option input { width: 15px; height: 15px; flex: 0 0 15px; margin: 0; padding: 0; accent-color: var(--accent); }
.assign-agent-option__avatar { width: 22px; height: 22px; flex: 0 0 22px; display: grid; place-items: center; border-radius: 50%; overflow: hidden; font-size: var(--fs-12); background: color-mix(in srgb, var(--accent) 14%, transparent); }
.assign-agent-option__avatar img { width: 100%; height: 100%; border-radius: 50%; object-fit: cover; }
.assign-agent-option__copy { display: flex; flex-direction: column; gap: 1px; min-width: 0; }
.assign-agent-option__copy strong { font-size: var(--fs-12); font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.assign-agent-option__copy small { display: flex; align-items: center; gap: 5px; color: var(--muted); font-size: var(--fs-11); }
.assign-agent-list__empty { display: flex; align-items: center; justify-content: center; gap: 8px; padding: 22px 12px; color: var(--faint); font-size: var(--fs-13); }
@media (max-width: 700px) {
  .assign-agent-picker { grid-template-columns: 1fr; }
}
</style>
