<script setup>
import { computed, ref } from 'vue'
import { Bot, Download, Drama, ExternalLink, Globe2, Image, PackagePlus, RefreshCw, ShieldAlert, SlidersHorizontal, Trash2, Zap } from 'lucide-vue-next'
import { useAppContext } from '../../composables/appContext'
import AgentAssignDialog from '../AgentAssignDialog.vue'
import ConfirmDeleteDialog from '../ConfirmDeleteDialog.vue'
import DcgPolicyDialog from '../DcgPolicyDialog.vue'
import InstallDialog from '../InstallDialog.vue'

const { t, refreshExtensions, extensionSnapshot, extensionBusy, extensionAction, figma, figmaAction, showFigmaConfig, installGlobalPackage, removeGlobalPackage, agentList, newAgentId } = useAppContext()
const rtkRuntime = computed(() => (extensionSnapshot.value?.tools || []).find(tool => tool.key === 'rtk') || null)
const dcgRuntime = computed(() => (extensionSnapshot.value?.tools || []).find(tool => tool.key === 'dcg') || null)
const browserRuntime = computed(() => (extensionSnapshot.value?.tools || []).find(tool => tool.key === 'agent-browser') || null)
const playwrightRuntime = computed(() => (extensionSnapshot.value?.tools || []).find(tool => tool.key === 'playwright') || null)
const globalPlugins = computed(() => extensionSnapshot.value?.globalPlugins || [])
const showInstallModal = ref(false)
const packageName = ref('')
const pendingPluginDelete = ref(null)
const assignDialogTool = ref('')
const showDcgPolicy = ref(false)

// 汇总各推荐扩展的已分配智能体。RTK/DCG 的 per-agent 状态在 enabled 上
//（installed 表示全局运行时二进制），Figma 的 installed 已含 recommended 标记。
const toolAgents = computed(() => {
  const map = { rtk: [], dcg: [], figma: [] }
  for (const agent of agentList.value || []) {
    if (agent.id === newAgentId.value) continue
    const statuses = extensionSnapshot.value?.recommended?.[agent.id] || []
    for (const key of Object.keys(map)) {
      const status = statuses.find(tool => tool.key === key)
      const active = status ? (key === 'figma' ? !!status.installed : !!status.enabled) : false
      if (active) map[key].push(agent)
    }
  }
  return map
})
function openAssignDialog(toolKey) {
  assignDialogTool.value = toolKey
}
const previewCommand = computed(() => packageName.value.trim() ? `npm install -g ${packageName.value.trim()}` : 'npm install -g <package>')
const pluginDeleteBusy = computed(() => {
  const pending = pendingPluginDelete.value
  if (!pending) return false
  if (pending.type === 'package') return extensionBusy.value === 'global-plugin-remove'
  if (pending.useFigmaAction) return extensionBusy.value === 'figma-uninstall'
  return extensionBusy.value === pending.tool?.key
})
const pluginDeleteDescription = computed(() => {
  const pending = pendingPluginDelete.value
  if (!pending) return ''
  const key = pending.type === 'package' ? 'confirmRemoveGlobalPlugin' : 'confirmUninstallGlobalExtension'
  return t.value[key].replace('{name}', pending.name)
})

function openInstallModal(name = '') {
  packageName.value = name
  showInstallModal.value = true
}

async function runInstall() {
  if (!packageName.value.trim()) return
  try {
    await installGlobalPackage('plugin', packageName.value.trim())
    showInstallModal.value = false
  } catch {}
}

function requestRemove(name) {
  pendingPluginDelete.value = { type: 'package', name }
}

function requestUninstall(tool, useFigmaAction = false) {
  const name = tool?.name || tool?.key || t.value.figma
  pendingPluginDelete.value = { type: 'extension', name, tool, useFigmaAction }
}

async function confirmPluginDelete() {
  const pending = pendingPluginDelete.value
  if (!pending || pluginDeleteBusy.value) return
  try {
    if (pending.type === 'package') await removeGlobalPackage('plugin', pending.name)
    else if (pending.useFigmaAction) await figmaAction('uninstall')
    else await extensionAction(pending.tool, 'uninstall')
    pendingPluginDelete.value = null
  } catch {}
}
</script>

<template>
  <section class="content-page plugins-page">
    <div class="page-heading">
      <div><h2>{{ t.pluginsTitle }}</h2><p>{{ t.pluginsIntro }}</p></div>
      <div class="page-heading__actions">
        <button class="icon-button page-refresh" :title="t.refresh" @click="refreshExtensions"><RefreshCw :size="17" /></button>
      </div>
    </div>

    <div class="plugin-section">
      <div class="plugin-section__title">
        <span>{{ t.globalExtensions }}</span>
        <small>{{ t.globalExtensionsHint }}</small>
      </div>
      <article class="plugin-row">
        <div class="plugin-icon"><Zap :size="19" /></div>
        <div class="plugin-copy">
          <div class="plugin-name">
            <span class="plugin-name__main">
              <strong :class="{ 'plugin-name__label--installed': rtkRuntime?.installed }">RTK</strong>
              <span class="status-dot" :class="{ active: rtkRuntime?.installed, missing: !rtkRuntime?.installed }"></span>
              <small>{{ rtkRuntime?.installed ? t.installed : t.notInstalled }}</small>
            </span>
            <div class="plugin-actions">
              <a href="https://github.com/rtk-ai/rtk" target="_blank" rel="noreferrer" :title="t.homepage"><ExternalLink :size="14" /></a>
              <button class="icon-button" :disabled="extensionBusy !== ''" :title="t.assignAgent" @click="openAssignDialog('rtk')"><Bot :size="14" /></button>
              <button class="icon-button" :disabled="extensionBusy === 'rtk'" :title="rtkRuntime?.installed ? t.update : t.runInstall" @click="extensionAction(rtkRuntime || { key: 'rtk', name: 'RTK' }, 'install')">
                <RefreshCw v-if="extensionBusy === 'rtk' || rtkRuntime?.installed" :class="{ spin: extensionBusy === 'rtk' }" :size="14" /><Download v-else :size="14" />
              </button>
              <button v-if="rtkRuntime?.installed" class="icon-button danger" :disabled="extensionBusy === 'rtk'" :title="t.delete" @click="requestUninstall(rtkRuntime)"><Trash2 :size="14" /></button>
            </div>
          </div>
          <p>{{ t.globalRtkDescription }}</p>
          <code v-if="rtkRuntime?.installHint">{{ rtkRuntime.installHint }}</code>
          <code v-if="rtkRuntime?.version">{{ rtkRuntime.version }}</code>
          <div class="plugin-assigned">
            <span class="plugin-assigned__label">{{ t.assignedAgents }}</span>
            <template v-if="toolAgents.rtk.length">
              <span v-for="agent in toolAgents.rtk" :key="agent.id" class="plugin-assigned__chip">{{ agent.name }}</span>
            </template>
            <span v-else class="plugin-assigned__empty">{{ t.notAssignedToAnyAgent }}</span>
          </div>
        </div>
      </article>
      <article class="plugin-row">
        <div class="plugin-icon"><ShieldAlert :size="19" /></div>
        <div class="plugin-copy">
          <div class="plugin-name">
            <span class="plugin-name__main">
              <strong :class="{ 'plugin-name__label--installed': dcgRuntime?.installed }">DCG</strong>
              <span class="status-dot" :class="{ active: dcgRuntime?.installed, missing: !dcgRuntime?.installed }"></span>
              <small>{{ dcgRuntime?.installed ? t.installed : t.notInstalled }}</small>
            </span>
            <div class="plugin-actions">
              <a href="https://github.com/Dicklesworthstone/destructive_command_guard" target="_blank" rel="noreferrer" :title="t.homepage"><ExternalLink :size="14" /></a>
              <button class="icon-button" :disabled="extensionBusy !== ''" :title="t.assignAgent" @click="openAssignDialog('dcg')"><Bot :size="14" /></button>
              <button class="icon-button" :disabled="extensionBusy !== '' || !dcgRuntime?.installed" :title="dcgRuntime?.installed ? t.dcgPolicy : t.dcgBinaryMissing" @click="showDcgPolicy = true"><SlidersHorizontal :size="14" /></button>
              <button class="icon-button" :disabled="extensionBusy === 'dcg'" :title="dcgRuntime?.installed ? t.update : t.runInstall" @click="extensionAction(dcgRuntime || { key: 'dcg', name: 'DCG' }, 'install')">
                <RefreshCw v-if="extensionBusy === 'dcg' || dcgRuntime?.installed" :class="{ spin: extensionBusy === 'dcg' }" :size="14" /><Download v-else :size="14" />
              </button>
              <button v-if="dcgRuntime?.installed" class="icon-button danger" :disabled="extensionBusy === 'dcg'" :title="t.delete" @click="requestUninstall(dcgRuntime)"><Trash2 :size="14" /></button>
            </div>
          </div>
          <p>{{ t.globalDcgDescription }}</p>
          <p class="plugin-hint">{{ t.globalDcgAntivirusHint }}</p>
          <code v-if="dcgRuntime?.installHint">{{ dcgRuntime.installHint }}</code>
          <code v-if="dcgRuntime?.version">{{ dcgRuntime.version }}</code>
          <div class="plugin-assigned">
            <span class="plugin-assigned__label">{{ t.assignedAgents }}</span>
            <template v-if="toolAgents.dcg.length">
              <span v-for="agent in toolAgents.dcg" :key="agent.id" class="plugin-assigned__chip">{{ agent.name }}</span>
            </template>
            <span v-else class="plugin-assigned__empty">{{ t.notAssignedToAnyAgent }}</span>
          </div>
        </div>
      </article>
      <article class="plugin-row">
        <div class="plugin-icon"><Globe2 :size="19" /></div>
        <div class="plugin-copy">
          <div class="plugin-name">
            <span class="plugin-name__main">
              <strong :class="{ 'plugin-name__label--installed': browserRuntime?.installed }">{{ t.globalBrowserRuntime }}</strong>
              <span class="status-dot" :class="{ active: browserRuntime?.installed, missing: !browserRuntime?.installed }"></span>
              <small>{{ browserRuntime?.installed ? t.installed : t.notInstalled }}</small>
            </span>
            <div class="plugin-actions">
              <a href="https://agent-browser.dev/" target="_blank" rel="noreferrer" :title="t.homepage"><ExternalLink :size="14" /></a>
              <button class="icon-button" :disabled="extensionBusy === 'agent-browser'" :title="browserRuntime?.installed ? t.update : t.runInstall" @click="extensionAction(browserRuntime || { key: 'agent-browser', name: t.globalBrowserRuntime }, 'install')">
                <RefreshCw v-if="extensionBusy === 'agent-browser' || browserRuntime?.installed" :class="{ spin: extensionBusy === 'agent-browser' }" :size="14" /><Download v-else :size="14" />
              </button>
              <button v-if="browserRuntime?.installed" class="icon-button danger" :disabled="extensionBusy === 'agent-browser'" :title="t.delete" @click="requestUninstall(browserRuntime)"><Trash2 :size="14" /></button>
            </div>
          </div>
          <p>{{ t.globalBrowserRuntimeDescription }}</p>
          <code>{{ browserRuntime?.installHint || 'npm install -g agent-browser' }}</code>
          <code v-if="browserRuntime?.version">{{ browserRuntime.version }}</code>
        </div>
      </article>
      <article class="plugin-row">
        <div class="plugin-icon"><Drama :size="19" /></div>
        <div class="plugin-copy">
          <div class="plugin-name">
            <span class="plugin-name__main">
              <strong :class="{ 'plugin-name__label--installed': playwrightRuntime?.installed }">Playwright</strong>
              <span class="status-dot" :class="{ active: playwrightRuntime?.installed, missing: !playwrightRuntime?.installed }"></span>
              <small>{{ playwrightRuntime?.installed ? t.installed : t.notInstalled }}</small>
            </span>
            <div class="plugin-actions">
              <a href="https://playwright.dev/" target="_blank" rel="noreferrer" :title="t.homepage"><ExternalLink :size="14" /></a>
              <button class="icon-button" :disabled="extensionBusy === 'playwright'" :title="playwrightRuntime?.installed ? t.update : t.runInstall" @click="extensionAction(playwrightRuntime || { key: 'playwright', name: 'Playwright' }, 'install')">
                <RefreshCw v-if="extensionBusy === 'playwright' || playwrightRuntime?.installed" :class="{ spin: extensionBusy === 'playwright' }" :size="14" /><Download v-else :size="14" />
              </button>
              <button v-if="playwrightRuntime?.installed" class="icon-button danger" :disabled="extensionBusy === 'playwright'" :title="t.delete" @click="requestUninstall(playwrightRuntime)"><Trash2 :size="14" /></button>
            </div>
          </div>
          <p>{{ t.globalPlaywrightDescription }}</p>
          <code>{{ playwrightRuntime?.installHint || 'npm install -g playwright && playwright install chromium' }}</code>
          <code v-if="playwrightRuntime?.version">{{ playwrightRuntime.version }}</code>
        </div>
      </article>
      <article class="plugin-row">
        <div class="plugin-icon"><Image :size="19" /></div>
        <div class="plugin-copy">
          <div class="plugin-name">
            <span class="plugin-name__main">
              <strong :class="{ 'plugin-name__label--installed': figma.installed }">{{ t.figma }}</strong>
              <span class="status-dot" :class="{ active: figma.installed && figma.hasToken, missing: !figma.installed || !figma.hasToken }"></span>
              <small>{{ !figma.installed ? t.notInstalled : (figma.hasToken ? t.figmaAuthorized : t.figmaNotAuthorized) }}</small>
            </span>
            <div class="plugin-actions">
              <a href="https://www.figma.com" target="_blank" rel="noreferrer" :title="t.homepage"><ExternalLink :size="14" /></a>
              <button class="icon-button" :disabled="extensionBusy !== '' || !figma.hasToken" :title="figma.hasToken ? t.assignAgent : t.configureFigmaFirst" @click="openAssignDialog('figma')"><Bot :size="14" /></button>
              <button class="icon-button" :disabled="extensionBusy === 'figma-install'" :title="figma.installed ? t.update : t.runInstall" @click="figmaAction('install')">
                <RefreshCw v-if="extensionBusy === 'figma-install' || figma.installed" :class="{ spin: extensionBusy === 'figma-install' }" :size="14" /><Download v-else :size="14" />
              </button>
              <button v-if="figma.installed" class="icon-button" :title="t.configure" @click="showFigmaConfig = true"><Settings :size="14" /></button>
              <button v-if="figma.installed" class="icon-button danger" :disabled="extensionBusy === 'figma-uninstall'" :title="t.delete" @click="requestUninstall({ key: 'figma', name: t.figma }, true)"><Trash2 :size="14" /></button>
            </div>
          </div>
          <p>{{ t.figmaDescription }}</p>
          <code>figma-developer-mcp</code>
          <code v-if="figma.activeAuthorizationName">{{ figma.activeAuthorizationName }} · {{ figma.authorizationCount }}</code>
          <code v-if="figma.version">{{ figma.version }}</code>
          <div class="plugin-assigned">
            <span class="plugin-assigned__label">{{ t.assignedAgents }}</span>
            <template v-if="toolAgents.figma.length">
              <span v-for="agent in toolAgents.figma" :key="agent.id" class="plugin-assigned__chip">{{ agent.name }}</span>
            </template>
            <span v-else class="plugin-assigned__empty">{{ t.notAssignedToAnyAgent }}</span>
          </div>
        </div>
      </article>
    </div>

    <div v-if="globalPlugins.length" class="plugin-section">
      <div class="plugin-section__title"><span>{{ t.otherGlobalPlugins }}</span></div>
      <article v-for="plugin in globalPlugins" :key="plugin.key" class="plugin-row">
        <div class="plugin-icon"><PackagePlus :size="19" /></div>
        <div class="plugin-copy">
          <div class="plugin-name">
            <span class="plugin-name__main">
              <strong :class="{ 'plugin-name__label--installed': plugin.installed }">{{ plugin.name || plugin.key }}</strong>
              <span class="status-dot" :class="{ active: plugin.installed, missing: !plugin.installed }"></span>
              <small>{{ plugin.installed ? t.installed : t.notInstalled }}</small>
            </span>
            <div class="plugin-actions">
              <button class="icon-button" :disabled="extensionBusy === 'global-plugin-install'" :title="plugin.installed ? t.update : t.runInstall" @click="openInstallModal(plugin.key)">
                <RefreshCw v-if="plugin.installed" :size="14" /><Download v-else :size="14" />
              </button>
              <button class="icon-button danger" :disabled="extensionBusy === 'global-plugin-remove'" :title="t.delete" @click="requestRemove(plugin.key)"><Trash2 :size="14" /></button>
            </div>
          </div>
          <p>{{ plugin.description || plugin.key }}</p>
          <code>{{ plugin.installHint || `npm install -g ${plugin.key}` }}</code>
          <code v-if="plugin.version">{{ plugin.version }}</code>
        </div>
      </article>
    </div>

    <ConfirmDeleteDialog
      :model-value="!!pendingPluginDelete"
      :title="t.deleteExtensionTitle"
      :description="pluginDeleteDescription"
      :busy="pluginDeleteBusy"
      :confirm-label="t.delete"
      :confirm-busy-label="t.deletingExtension"
      @cancel="pendingPluginDelete = null"
      @confirm="confirmPluginDelete"
    />

    <InstallDialog
      v-if="showInstallModal"
      mode="command"
      :title="t.installGlobalPlugin"
      :hint="t.installGlobalPluginHint"
      :command="packageName"
      :preview-command="previewCommand"
      :command-placeholder="t.npmPackagePlaceholder"
      :running="extensionBusy === 'global-plugin-install'"
      :run-text="t.runInstall"
      @update:command="packageName = $event"
      @run="runInstall"
      @close="showInstallModal = false"
    />

    <AgentAssignDialog
      v-if="assignDialogTool"
      :model-value="!!assignDialogTool"
      :tool-key="assignDialogTool"
      :tool-name="({ rtk: 'RTK', dcg: 'DCG', figma: t.figma })[assignDialogTool]"
      @update:model-value="assignDialogTool = ''"
    />

    <DcgPolicyDialog v-model="showDcgPolicy" />

  </section>
</template>

<style scoped>
.plugin-copy .plugin-hint {
  margin: 8px 0 0;
  padding: 8px 10px;
  font-size: var(--fs-13);
  font-weight: 600;
  line-height: 1.5;
  color: #8a5a00;
  background: rgba(217, 164, 65, 0.14);
  border: 1px solid rgba(217, 164, 65, 0.5);
  border-radius: 7px;
}
:root[data-theme="dark"] .plugin-copy .plugin-hint {
  color: #e6b84e;
  background: rgba(217, 164, 65, 0.12);
  border-color: rgba(217, 164, 65, 0.35);
}
.plugin-assigned {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 5px;
  margin-top: 6px;
}
.plugin-assigned__label {
  color: var(--faint);
  font-size: var(--fs-12);
}
.plugin-assigned__chip {
  padding: 3px 7px;
  border-radius: 9px;
  font-size: var(--fs-12);
  color: var(--text);
  background: var(--surface);
  font-weight: 500;
}
.plugin-assigned__empty {
  color: var(--faint);
  font-size: var(--fs-12);
}
</style>
