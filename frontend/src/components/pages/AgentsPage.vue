<script setup>
import { computed, ref } from 'vue'
import { Bot, GitBranch, Plus, RefreshCw, Settings, Trash2, X } from 'lucide-vue-next'
import { useAppContext } from '../../composables/appContext'
import { agentAvatar, isImageAvatar } from '../../composables/appContext'
import { collectAgentExtensions } from '../../agentExtensions'

const { t, bootstrap, config, agentNotice, piInstallBusy, piInstallError, installPiNow, createAgent, agentList, openAgentConfig, agentDeleteBusy, requestDeleteAgent, extensionSnapshot, extensionLoading, refreshExtensions, newAgentId } = useAppContext()

// 智能体列表上方的两个 Tab：智能体（当前列表）/ 协作流（类似环境中的 Git tab）
const agentsTopTab = ref('agents')

// 直接依据统一扩展快照推导每个智能体的扩展。packages 来自 Pi 自己的
// settings.json，因此第三方扩展安装后无需再修改这张列表。
function agentExtensions(agent) {
  return collectAgentExtensions(agent, extensionSnapshot.value)
    .map(item => ({
      ...item,
      name: item.key === 'document' ? t.value.documentTool : item.name,
    }))
}

function enabledExtensions(agent) {
  return agentExtensions(agent).filter(item => item.active)
}

// 行内标签最多展示 3 个已启用扩展，超出部分收进“+N”按钮。
const MAX_VISIBLE_EXTENSIONS = 3
function visibleExtensions(agent) {
  return enabledExtensions(agent).slice(0, MAX_VISIBLE_EXTENSIONS)
}

// “查看全部已安装扩展”弹窗：展示该智能体所有已安装扩展（含未启用的）。
const agentExtDialog = ref(null)
function openAgentExtDialog(agent) {
  agentExtDialog.value = agent
}
function installedExtensions(agent) {
  return agentExtensions(agent).filter(item => item.installed)
}

// 子智能体：展示该智能体配置中授权的其他智能体名称（过滤已删除的失效引用）。
const agentNameById = computed(() => {
  const map = {}
  for (const a of config.agents) map[a.id] = a.name
  return map
})
function subagentNames(agent) {
  return (agent.subagents || []).map(id => agentNameById.value[id]).filter(Boolean)
}

const extensionsBusy = ref(false)
async function reloadExtensions() {
  if (extensionsBusy.value) return
  extensionsBusy.value = true
  try {
    await refreshExtensions()
  } finally {
    extensionsBusy.value = false
  }
}

const canCreate = computed(() => !!bootstrap?.value?.piInstalled)
const canDelete = computed(() => config.agents.length > 1)

// 扩展快照尚未读取完成（仍在请求中，或 builtins/recommended 尚未填充）时，
// 列表卡片的“已安装扩展”区域改用懒加载占位元素。
const extensionsReady = computed(
  () => !extensionLoading.value
    && extensionSnapshot.value?.builtins != null
    && extensionSnapshot.value?.packages != null
)
</script>

<template>
<section class="content-page">
  <div class="page-heading">
    <div><h2>{{ t.agentsTitle }}</h2><p>{{ t.agentsIntro }}</p></div>
    <div class="page-heading__actions">
      <button class="icon-button page-refresh" :title="t.refresh" :disabled="extensionsBusy" @click="reloadExtensions">
        <RefreshCw v-if="extensionsBusy" class="spin" :size="15" /><RefreshCw v-else :size="15" />
      </button>
      <button class="primary-button" :disabled="!canCreate || piInstallBusy" @click="createAgent">
        <Plus :size="14" />{{ t.createAgent }}
      </button>
    </div>
  </div>

  <div class="agents-top-tabs" role="tablist" :aria-label="t.agentsTabsLabel || '智能体视图'">
    <button role="tab" :aria-selected="agentsTopTab === 'agents'" :class="{ active: agentsTopTab === 'agents' }" @click="agentsTopTab = 'agents'">
      <Bot :size="16" />{{ t.agentsTopTabAgents || '智能体' }}
      <span>{{ agentList.length }}</span>
    </button>
    <button role="tab" :aria-selected="agentsTopTab === 'flow'" :class="{ active: agentsTopTab === 'flow' }" @click="agentsTopTab = 'flow'">
      <GitBranch :size="16" />{{ t.agentsTopTabFlow || '协作流' }}
    </button>
  </div>

  <div v-if="agentsTopTab === 'agents'">
  <div v-if="agentNotice" class="agent-notice" :class="agentNotice.type" role="status">
    <span>{{ agentNotice.text }}</span>
    <button :aria-label="t.close" @click="agentNotice = null"><span class="agent-notice__close">×</span></button>
  </div>

  <div v-if="!bootstrap?.piInstalled" class="agent-runtime-state">
    <Bot :size="28" />
    <strong>{{ t.piMissing }}</strong>
    <p>{{ t.agentPiMissingHint }}</p>
    <button class="primary-button" :disabled="piInstallBusy" @click="installPiNow">
      <RefreshCw v-if="piInstallBusy" class="spin" :size="14" />
      <Zap v-else :size="14" />
      {{ piInstallBusy ? t.installingPi : t.installPiNow }}
    </button>
    <p v-if="piInstallError" class="agent-install-error">{{ piInstallError }}</p>
  </div>

  <div v-else-if="!config.agents.length" class="agent-runtime-state">
    <RefreshCw :size="28" />
    <strong>{{ t.agentInitFailed }}</strong>
    <p>{{ t.agentInitFailedHint }}</p>
    <button class="primary-button" :disabled="piInstallBusy" @click="installPiNow">
      <RefreshCw v-if="piInstallBusy" class="spin" :size="14" /><Zap v-else :size="14" />
      {{ piInstallBusy ? t.installingPi : t.installPiNow }}
    </button>
  </div>

  <div v-else class="agent-list-table">
    <article v-for="agent in agentList" :key="agent.id" class="agent-row" :class="{ 'agent-row--new': agent.id === newAgentId }">
      <div class="agent-actions">
        <button class="icon-button" :title="t.openEditor" :aria-label="t.openEditor" @click="openAgentConfig(agent)"><Settings :size="14" /></button>
        <button class="icon-button danger" :title="t.delete" :aria-label="t.delete" :disabled="!canDelete || agentDeleteBusy" @click="requestDeleteAgent(agent)"><Trash2 :size="14" /></button>
      </div>
      <div class="agent-avatar">
        <img v-if="isImageAvatar(agentAvatar(agent))" :src="agentAvatar(agent)" class="agent-avatar__img" alt="" />
        <span v-else-if="agentAvatar(agent)" class="agent-avatar__emoji">{{ agentAvatar(agent) }}</span>
        <Bot v-else :size="18" />
      </div>
      <div class="agent-copy">
        <div class="agent-name">
          <strong>{{ agent.name }}</strong>
        </div>
        <p>{{ agent.description || t.agentNoDescription }}</p>
        <div v-if="extensionsReady" class="agent-exts">
          <span class="agent-exts__label">{{ t.agentExtLabel }}</span>
          <span v-if="enabledExtensions(agent).length" class="agent-tags">
            <span
              v-for="ext in visibleExtensions(agent)"
              :key="ext.key"
              class="agent-ext-tag"
              :class="{ 'agent-ext-tag--missing': !ext.installed }"
              :title="ext.name + (ext.installed ? '' : '（' + t.notInstalledTag + '）')"
            >
              <span class="agent-ext-tag__name">{{ ext.name }}</span>
              <em v-if="!ext.installed">{{ t.notInstalledTag }}</em>
            </span>
            <button
              v-if="enabledExtensions(agent).length > MAX_VISIBLE_EXTENSIONS"
              class="agent-ext-more"
              :title="t.agentExtMoreHint.replace('{n}', String(enabledExtensions(agent).length))"
              @click="openAgentExtDialog(agent)"
            >+{{ enabledExtensions(agent).length - MAX_VISIBLE_EXTENSIONS }}</button>
          </span>
          <span v-else class="agent-no-ext">{{ t.noEnabledExtensions }}</span>
        </div>
        <div v-else class="agent-loading" role="status" aria-label="检查中…">
          <RefreshCw class="spin" :size="13" />
          <span>{{ t.checking }}</span>
        </div>
        <div v-if="subagentNames(agent).length" class="agent-subagents">
          <span class="agent-subagents__label">{{ t.agentSubagents }}</span>
          <span v-for="name in subagentNames(agent)" :key="name" class="agent-subagent-badge">{{ name }}</span>
        </div>
      </div>
    </article>
  </div>
  </div>

  <div v-else-if="agentsTopTab === 'flow'" class="agent-flow-panel">
    <div class="agent-runtime-state">
      <GitBranch :size="28" />
      <strong>{{ t.agentFlowTitle || '协作流' }}</strong>
      <p>{{ t.agentFlowComingSoon || '协作流功能正在开发中，敬请期待。' }}</p>
    </div>
  </div>

  <div v-if="agentExtDialog" class="modal-backdrop" @pointerdown.self="agentExtDialog = null">
    <section class="agent-editor-dialog agent-ext-dialog" role="dialog" aria-modal="true">
      <header class="agent-editor-dialog__head">
        <h2>{{ t.agentExtDialogTitle }} · {{ agentExtDialog.name }}</h2>
        <button class="icon-button" :aria-label="t.closeDialog" @click="agentExtDialog = null"><X :size="16" /></button>
      </header>
      <div class="agent-editor-dialog__body">
        <div v-if="installedExtensions(agentExtDialog).length" class="agent-ext-dialog-list">
          <div v-for="ext in installedExtensions(agentExtDialog)" :key="ext.key" class="agent-ext-dialog-row">
            <span class="agent-ext-dialog-row__name">{{ ext.name }}</span>
            <span class="agent-ext-dialog-row__state" :class="{ 'agent-ext-dialog-row__state--on': ext.active }">
              {{ ext.active ? t.agentExtEnabled : t.agentExtDisabled }}
            </span>
          </div>
        </div>
        <p v-else class="agent-ext-dialog-empty">{{ t.agentExtEmpty }}</p>
      </div>
    </section>
  </div>
</section>
</template>
