<script setup>
import { computed, ref } from 'vue'
import { Bot, GitBranch, Plus, RefreshCw, Settings, Trash2 } from 'lucide-vue-next'
import { useAppContext } from '../../composables/appContext'
import { agentAvatar } from '../../composables/appContext'
import { collectAgentExtensions } from '../../agentExtensions'
import { extensionIcon } from '../../extensionIcons'

const { t, bootstrap, config, agentNotice, piInstallBusy, piInstallError, installPiNow, createAgent, agentList, openAgentConfig, agentDeleteBusy, requestDeleteAgent, setDefaultAgent, extensionSnapshot, extensionLoading, refreshExtensions, newAgentId } = useAppContext()

// 智能体列表上方的两个 Tab：智能体（当前列表）/ 协作流（类似环境中的 Git tab）
const agentsTopTab = ref('agents')

// 直接依据统一扩展快照推导每个智能体的扩展。packages 来自 Pi 自己的
// settings.json，因此第三方扩展安装后无需再修改这张列表。
function agentExtensions(agent) {
  return collectAgentExtensions(agent, extensionSnapshot.value)
    .map(item => ({
      ...item,
      name: item.key === 'document' ? t.value.documentTool : item.name,
      icon: extensionIcon(item.key),
    }))
}

function enabledExtensions(agent) {
  return agentExtensions(agent).filter(item => item.active)
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
      <div class="agent-avatar">
        <span v-if="agentAvatar(agent)" class="agent-avatar__emoji">{{ agentAvatar(agent) }}</span>
        <Bot v-else :size="18" />
      </div>
      <div class="agent-copy">
        <div class="agent-name">
          <strong>{{ agent.name }}</strong>
          <small v-if="agent.id === config.activeAgentId">{{ t.defaultAgentBadge }}</small>
        </div>
        <p>{{ agent.description || t.agentNoDescription }}</p>
        <div v-if="extensionsReady" class="agent-exts">
          <span v-if="enabledExtensions(agent).length" class="agent-tags">
            <span
              v-for="ext in enabledExtensions(agent)"
              :key="ext.key"
              class="agent-ext-tag"
              :class="{ 'agent-ext-tag--missing': !ext.installed }"
              :title="ext.name + (ext.installed ? '' : '（' + t.notInstalledTag + '）')"
            >
              <component :is="ext.icon" :size="12" />
              <span class="agent-ext-tag__name">{{ ext.name }}</span>
              <em v-if="!ext.installed">{{ t.notInstalledTag }}</em>
            </span>
          </span>
          <span v-else class="agent-no-ext">{{ t.noEnabledExtensions }}</span>
        </div>
        <div v-else class="agent-loading" role="status" aria-label="检查中…">
          <RefreshCw class="spin" :size="13" />
          <span>{{ t.checking }}</span>
        </div>
      </div>
      <div class="agent-actions">
        <label class="agent-default-toggle" :title="t.agentDefaultHint">
          <span>{{ t.defaultAgentBadge }}</span>
          <span class="switch">
            <input
              type="checkbox"
              :checked="agent.id === config.activeAgentId"
              :disabled="agent.id === config.activeAgentId"
              @change="setDefaultAgent(agent, $event.target.checked)"
            />
            <span class="switch__track"></span>
          </span>
        </label>
        <button class="secondary-button" @click="openAgentConfig(agent)">
          <Settings :size="14" />{{ t.openEditor }}
        </button>
        <button class="danger-button" :disabled="agent.id === config.activeAgentId || !canDelete || agentDeleteBusy" @click="requestDeleteAgent(agent)">
          <Trash2 :size="14" />{{ t.delete }}
        </button>
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
</section>
</template>
