<script setup>
import { computed, ref, watch } from 'vue'
import { ArrowLeft, Award, Binary, Blocks, Bot, Check, FileText, Network, Package, Pencil, RefreshCw, Settings, Shield, Sparkles, Trash2, X, Zap } from 'lucide-vue-next'
import SkillCard from '../SkillCard.vue'
import { useAppContext, agentAvatar, isImageAvatar } from '../../composables/appContext'
import { extensionIcon } from '../../extensionIcons'
import { listBrowserProfiles, deleteBrowserProfile, renameBrowserProfile } from '../../backend'
import InstallDialog from '../../components/InstallDialog.vue'
import ConfirmDeleteDialog from '../ConfirmDeleteDialog.vue'

const { t, bootstrap, selectedAgent, activeAgentId, defaultAgentId, agentList, modelOptions, extensionSnapshot, refreshExtensions, refreshAgentExtensions, extensionBusy, extensionDeleteBusy, extensionLoading, figma, toggleAgentExtension, setDefaultAgent, persistAgentChange, restartAgent, pickAgentDataDir, openAgentConfig, agentConfigInitialTab, backToAgentList, readAgentFile, writeAgentFile, pushToast, installAgentMcp, removeAgentMcpServer, installAgentExtension, uninstallAgentExtension, requestDeleteExtension, deleteSkill, skills, refreshSkills, skillsLoading } = useAppContext()

const activeTab = ref(agentConfigInitialTab.value || 'basics')
const availableSubagents = computed(() =>
  (agentList.value || []).filter(agent => agent.id !== selectedAgent.value?.id)
)
const agentSkills = computed(() =>
  (skills.value || [])
    .filter(skill => (skill.agents || []).some(agent => agent.id === selectedAgent.value?.id))
    .slice()
    .sort((a, b) => String(a.name).localeCompare(String(b.name)))
)
watch(activeTab, (tab) => {
  if (tab === 'skills') refreshSkills()
})
const avatarPresets = ['🤖', '🧠', '💡', '🚀', '🛠️', '📚', '🎯', '🔧', '⚡', '🌐', '🐙', '🦊', '🐳', '🐝', '🌟', '🔥']
function selectAvatar(value) {
  const agent = selectedAgent.value
  if (!agent) return
  agent.avatar = (value || '').trim().slice(0, 2)
  persistAgentChange(agent)
}
const agentAvatarInput = ref(null)
function uploadAgentAvatar(event) {
  const file = event.target.files?.[0]
  event.target.value = ''
  if (!file || !selectedAgent.value) return
  if (!file.type.startsWith('image/')) {
    pushToast('error', t.value.agentAvatarUploadType)
    return
  }
  const reader = new FileReader()
  reader.onload = () => {
    const agent = selectedAgent.value
    if (!agent) return
    agent.avatar = String(reader.result)
    persistAgentChange(agent)
  }
  reader.onerror = () => pushToast('error', t.value.agentAvatarUploadFailed)
  reader.readAsDataURL(file)
}
const selectedDefaultModel = computed({
  get: () => {
    const agent = selectedAgent.value
    return agent ? `${agent.defaultProvider || ''}/${agent.defaultModel || ''}` : ''
  },
  set: (value) => {
    const agent = selectedAgent.value
    const index = String(value).indexOf('/')
    if (!agent || index < 0) return
    agent.defaultProvider = value.slice(0, index)
    agent.defaultModel = value.slice(index + 1)
  },
})

async function saveAgentRuntimeConfig() {
  if (!selectedAgent.value) return
  const saved = await persistAgentChange(selectedAgent.value)
  if (saved && selectedAgent.value.id === activeAgentId.value) await restartAgent()
}

function subagentEnabled(id) {
  return selectedAgent.value?.subagents?.includes(id)
}

async function toggleSubagent(id) {
  if (!selectedAgent.value) return
  const current = new Set(selectedAgent.value.subagents || [])
  if (current.has(id)) current.delete(id)
  else current.add(id)
  selectedAgent.value.subagents = [...current]
  await saveAgentRuntimeConfig()
}
function subagentTooltip(agent) {
  const lines = []
  if (agent.description) lines.push(agent.description)
  lines.push(`${t.value.agentSubagentsModel || '模型'}: ${agent.defaultProvider || '—'}/${agent.defaultModel || '—'}`)
  lines.push(`${t.value.agentSubagentsId || 'ID'}: ${agent.id}`)
  return lines.join('\n')
}

async function togglePiTool(key) {
  if (!selectedAgent.value) return
  selectedAgent.value.piTools ||= { read: true, bash: true, edit: true, write: true }
  selectedAgent.value.piTools[key] = selectedAgent.value.piTools[key] === false
  await saveAgentRuntimeConfig()
}
const RECOMMENDED_BROWSER_PROFILE_POLICY = {
  existingProfileMode: 'headless',
  interactiveLoginMode: 'headed',
  authenticatedTaskMode: 'headless',
}
const showBrowserPolicyModal = ref(false)
const browserPolicySaving = ref(false)
const browserPolicyDraft = ref({ ...RECOMMENDED_BROWSER_PROFILE_POLICY })

function normalizedBrowserPolicy(policy) {
  return {
    existingProfileMode: ['headed', 'headless'].includes(policy?.existingProfileMode)
      ? policy.existingProfileMode
      : RECOMMENDED_BROWSER_PROFILE_POLICY.existingProfileMode,
    interactiveLoginMode: 'headed',
    authenticatedTaskMode: ['headed', 'headless'].includes(policy?.authenticatedTaskMode)
      ? policy.authenticatedTaskMode
      : RECOMMENDED_BROWSER_PROFILE_POLICY.authenticatedTaskMode,
  }
}

const browserPolicySummary = computed(() => {
  const policy = normalizedBrowserPolicy(selectedAgent.value?.browserProfilePolicy)
  const label = mode => mode === 'headed' ? t.value.browserModeHeaded : t.value.browserModeHeadless
  return [
    `${t.value.browserPolicyExistingShort}: ${label(policy.existingProfileMode)}`,
    `${t.value.browserPolicyLoginShort}: ${label(policy.interactiveLoginMode)}`,
    `${t.value.browserPolicyTaskShort}: ${label(policy.authenticatedTaskMode)}`,
  ].join(' · ')
})

function openBrowserPolicyModal() {
  browserPolicyDraft.value = normalizedBrowserPolicy(selectedAgent.value?.browserProfilePolicy)
  showBrowserPolicyModal.value = true
  loadManagedProfiles()
}

function useRecommendedBrowserPolicy() {
  browserPolicyDraft.value = { ...RECOMMENDED_BROWSER_PROFILE_POLICY }
}

async function saveBrowserPolicy() {
  if (!selectedAgent.value || browserPolicySaving.value) return
  browserPolicySaving.value = true
  const agentId = selectedAgent.value.id
  const previous = selectedAgent.value.browserProfilePolicy
  selectedAgent.value.browserProfilePolicy = normalizedBrowserPolicy(browserPolicyDraft.value)
  try {
    const saved = await persistAgentChange(selectedAgent.value)
    if (saved) {
      showBrowserPolicyModal.value = false
      pushToast('success', t.value.browserPolicySaved)
      if (agentId === activeAgentId.value) {
        try {
          await restartAgent()
        } catch (error) {
          pushToast('error', t.value.toastExtensionError.replace('{error}', String(error)))
        }
      }
    } else {
      selectedAgent.value.browserProfilePolicy = previous
      pushToast('error', t.value.browserPolicySaveFailed)
    }
  } finally {
    browserPolicySaving.value = false
  }
}

const builtinBusy = ref('')
const builtinStatuses = computed(() => {
  const agentId = selectedAgent.value?.id || ''
  const statuses = extensionSnapshot.value?.builtins?.[agentId] || []
  return statuses.length ? statuses : (extensionSnapshot.value?.builtinCatalog || [])
})
const rtkStatus = computed(() => {
  const agentId = selectedAgent.value?.id || ''
  return (extensionSnapshot.value?.recommended?.[agentId] || []).find(tool => tool.key === 'rtk') || null
})
const dcgStatus = computed(() => {
  const agentId = selectedAgent.value?.id || ''
  return (extensionSnapshot.value?.recommended?.[agentId] || []).find(tool => tool.key === 'dcg') || null
})
const recommendedExtensions = computed(() => {
  const agentId = selectedAgent.value?.id || ''
  return extensionSnapshot.value?.recommended?.[agentId] || []
})
const piPluginsStatus = computed(() => {
  return recommendedExtensions.value.find(tool => tool.key === 'pi-plugins') || null
})
const piPluginsWindowsUnsupported = computed(() => bootstrap.value?.os === 'windows')
const piPluginsInstallDisabled = computed(() =>
  !piPluginsStatus.value?.installed && piPluginsWindowsUnsupported.value
)
const installedPackages = computed(() => {
  const agentId = selectedAgent.value?.id || ''
  const managedPaths = new Set(
    recommendedExtensions.value
      .map(status => status.sourcePath)
      .filter(Boolean),
  )
  return (extensionSnapshot.value?.packages?.[agentId] || [])
    .filter(status => !status.sourcePath || !managedPaths.has(status.sourcePath))
})
// 未纳管扩展：物理存在于 agent extensions/ 目录、但不属于内置工具/系统扩展/
// 推荐扩展的条目（如手动拷入的 ask-user）。Pi 会自动加载它们，这里展示出来
// 供用户审查并删除。
const unmanagedExtensions = computed(() => {
  const agentId = selectedAgent.value?.id || ''
  return extensionSnapshot.value?.directory?.[agentId] || []
})
const browserStatus = computed(() => {
  return recommendedExtensions.value.find(tool => tool.key === 'browser-native') || null
})
const browserRuntimeStatus = computed(() => {
  return (extensionSnapshot.value?.tools || []).find(tool => tool.key === 'agent-browser') || null
})
// Playwright 是全局插件：安装/更新在主菜单“插件”页完成，这里只读取版本用于展示。
const playwrightStatus = computed(() => {
  return (extensionSnapshot.value?.tools || []).find(tool => tool.key === 'playwright') || null
})
const piFigmaStatus = computed(() => {
  return recommendedExtensions.value.find(tool => tool.key === 'figma') || null
})
const agentMcpServers = computed(() => {
  const agentId = selectedAgent.value?.id || ''
  return (extensionSnapshot.value?.mcp?.[agentId] || []).filter(server => server.key !== 'figma')
})
async function installBrowserNative() {
  if (!selectedAgent.value || extensionBusy.value === 'browser-install') return
  if (!browserRuntimeStatus.value?.installed) {
    pushToast('error', t.value.browserRuntimeMissing)
    return
  }
  await installAgentExtension(selectedAgent.value.id, 'pi install npm:pi-agent-browser-native')
}
async function uninstallBrowserNative() {
  if (!selectedAgent.value || extensionBusy.value === 'browser-install') return
  await uninstallAgentExtension(selectedAgent.value.id, 'browser-native')
}
// 推荐/内置扩展按钮：未装入时直接安装；已装入时弹出二次确认再移除
function onExtensionToggle(group, key, installed, name) {
  if (installed) requestDeleteExtension({ type: 'toggle', group, key, name })
  else toggleAgentExtension(group, key)
}
// 浏览器原生按钮：未安装时直接安装；已安装时弹出二次确认再移除
function onBrowserToggle(installed) {
  if (installed) requestDeleteExtension({ type: 'browser', name: browserStatus.value?.name || t.value.browserNative })
  else installBrowserNative()
}

// 返回某个已安装 npm 包内包含的 skills（按包名/key 匹配）
function packageSkills(extension) {
  return (skills.value || []).filter(s => s.source === extension.key)
}

// 单独删除某个 skill（仅移除该 skill 自身，不影响同包其它 skill），删除前二次确认。
const skillDeleteTarget = ref(null)
const skillDeleting = ref(false)
function requestRemoveSkill(skill) {
  if (skillsLoading.value || skillDeleting.value) return
  skillDeleteTarget.value = skill
}
async function confirmRemoveSkill() {
  const skill = skillDeleteTarget.value
  if (!skill || skillDeleting.value) return
  const agentId = selectedAgent.value?.id
  if (!agentId) {
    // 没有当前 agent 时绝不能调用（后端 agentID 为空会退化为“从所有 agent 删除”）。
    console.error('refuse to delete skill without current agent id')
    skillDeleteTarget.value = null
    return
  }
  skillDeleting.value = true
  try {
    await deleteSkill(skill.id, agentId)
    await refreshSkills()
  } catch (e) {
    console.error('failed to delete skill', e)
  } finally {
    skillDeleting.value = false
    skillDeleteTarget.value = null
  }
}
async function installPiPlugins() {
  if (!selectedAgent.value || piPluginsInstallDisabled.value || extensionBusy.value === 'pi-plugins-install') return
  const result = await installAgentExtension(
    selectedAgent.value.id,
    'pi install npm:@nklisch/pi-plugins',
    { busyKey: 'pi-plugins-install', name: t.value.piPlugins },
  )
  if (result?.success === true && !selectedAgent.value.recommended?.['pi-plugins']) {
    await toggleAgentExtension('recommended', 'pi-plugins', true)
  }
}
function onPiPluginsToggle(installed) {
  if (installed) {
    requestDeleteExtension({
      type: 'recommended-package',
      group: 'recommended',
      key: 'pi-plugins',
      packageKey: 'pi-plugins',
      name: t.value.piPlugins,
    })
  } else {
    installPiPlugins()
  }
}
async function installPiFigma() {
  if (!selectedAgent.value || extensionBusy.value === 'figma-agent-install') return
  if (!figma.value.installed) {
    pushToast('error', t.value.piFigmaGlobalMissing)
    return
  }
  if (!figma.value.hasToken) {
    pushToast('error', t.value.piFigmaAuthorizationMissing)
    return
  }
  const result = await installAgentExtension(
    selectedAgent.value.id,
    'pi install npm:pi-mcp-adapter',
    { busyKey: 'figma-agent-install', name: t.value.piFigma },
  )
  if (result?.success === true) await toggleAgentExtension('recommended', 'figma', true)
}
function onPiFigmaToggle(installed) {
  if (installed) requestDeleteExtension({ type: 'toggle', group: 'recommended', key: 'figma', name: t.value.figma, category: 'mcp' })
  else installPiFigma()
}

// 自定义安装统一只接收 npm 包名。界面展示生成后的平台命令，后端再做
// 一次严格校验后执行，避免把这里变成任意 shell 命令入口。
const showInstallModal = ref(false)
const installKind = ref('extension')
const installPackageName = ref('')
const installing = ref(false)
const installTitle = computed(() => installKind.value === 'mcp' ? t.value.installAgentMcp : t.value.installExtension)
const installHint = computed(() => installKind.value === 'mcp' ? t.value.installAgentMcpHint : t.value.installExtensionHint)
const installPreviewCommand = computed(() => {
  const packageName = installPackageName.value.trim() || '<package>'
  if (installKind.value === 'mcp') {
    return `npm install -g ${packageName}\npi install npm:pi-mcp-adapter`
  }
  return `pi install npm:${packageName}`
})
function openInstallModal() {
  installKind.value = 'extension'
  installPackageName.value = ''
  showInstallModal.value = true
}
function openMcpInstallModal() {
  installKind.value = 'mcp'
  installPackageName.value = ''
  showInstallModal.value = true
}
async function runInstallCommand() {
  if (!selectedAgent.value || !installPackageName.value.trim()) return
  installing.value = true
  try {
    if (installKind.value === 'mcp') {
      await installAgentMcp(selectedAgent.value.id, installPackageName.value.trim())
      showInstallModal.value = false
      return
    }
    const command = `pi install npm:${installPackageName.value.trim()}`
    const res = await installAgentExtension(selectedAgent.value.id, command)
    if (res?.success) showInstallModal.value = false
  } catch {
    // The shared action layer already reports the concrete backend error.
  } finally {
    installing.value = false
  }
}
async function updateBuiltinTool(key) {
  if (!selectedAgent.value || builtinBusy.value) return
  builtinBusy.value = key
  try {
    await persistAgentChange(selectedAgent.value)
    await refreshAgentExtensions(selectedAgent.value.id)
  } finally {
    builtinBusy.value = ''
  }
}
const extensionsBusy = ref(false)
async function reloadExtensions() {
  if (extensionsBusy.value) return
  extensionsBusy.value = true
  try {
    await refreshAgentExtensions(selectedAgent.value?.id)
  } finally {
    extensionsBusy.value = false
  }
}


// --- Prompt tabs ---
// 会话启动提示词 -> AGENTS.md（Pi 启动时加载）
// 强制提示词   -> PROMPT_FORCE.md（追加到每次用户问题末尾，按模型启用）
// 压缩提示词   -> PROMPT_COMPRESS.md
const PROMPT_FILES = { startup: 'AGENTS.md', forced: 'PROMPT_FORCE.md', compress: 'PROMPT_COMPRESS.md' }
const promptStartup = ref('')
const promptForce = ref('')
const promptCompress = ref('')
const promptBusy = ref(false)
const promptLoading = ref(false)
const activePromptTab = ref('startup') // 'startup' | 'forced' | 'compress'
let promptLoadRequest = 0

const activePromptFile = computed(() => PROMPT_FILES[activePromptTab.value])
const activePromptContent = computed({
  get: () => {
    const tab = activePromptTab.value
    if (tab === 'forced') return promptForce.value
    if (tab === 'compress') return promptCompress.value
    return promptStartup.value
  },
  set: (v) => {
    const tab = activePromptTab.value
    if (tab === 'forced') promptForce.value = v
    else if (tab === 'compress') promptCompress.value = v
    else promptStartup.value = v
  }
})

async function loadPrompt(agentId = selectedAgent.value?.id) {
  if (!agentId) {
    promptStartup.value = ''
    promptForce.value = ''
    promptCompress.value = ''
    return
  }
  const tab = activePromptTab.value
  const file = PROMPT_FILES[tab]
  const request = ++promptLoadRequest
  promptLoading.value = true
  try {
    const content = await readAgentFile(agentId, file)
    if (request === promptLoadRequest && selectedAgent.value?.id === agentId) {
      if (tab === 'forced') promptForce.value = content
      else if (tab === 'compress') promptCompress.value = content
      else promptStartup.value = content
    }
  } catch (err) {
    if (request === promptLoadRequest && selectedAgent.value?.id === agentId) {
      if (tab === 'forced') promptForce.value = ''
      else if (tab === 'compress') promptCompress.value = ''
      else promptStartup.value = ''
    }
  } finally {
    if (request === promptLoadRequest) promptLoading.value = false
  }
}
function onTabChange(tab) {
  activeTab.value = tab
  if (tab === 'prompt') loadPrompt()
}
function onPromptTabChange(tab) {
  activePromptTab.value = tab
  loadPrompt()
}
async function savePrompt() {
  if (!selectedAgent.value || promptBusy.value) return
  const tab = activePromptTab.value
  const content = tab === 'forced' ? promptForce.value : tab === 'compress' ? promptCompress.value : promptStartup.value
  promptBusy.value = true
  try {
    await writeAgentFile(selectedAgent.value.id, PROMPT_FILES[tab], content)
    pushToast('success', t.value.agentPromptSaved)
  } catch (err) {
    pushToast('error', t.value.agentPromptSaveFailed.replace('{error}', String(err)))
  } finally {
    promptBusy.value = false
  }
}
// 强制提示词：选择对哪些模型启用（key 与 modelOptions 的 value 保持一致：provider/model）
function toggleForcedModel(key) {
  const agent = selectedAgent.value
  if (!agent) return
  if (!agent.forcedPromptModels) agent.forcedPromptModels = {}
  agent.forcedPromptModels[key] = !agent.forcedPromptModels[key]
  persistAgentChange(agent)
}

// 管理已有 Browser Profile 连接：列出当前 Agent 的持久浏览器连接，可重命名/删除。
// 该列表直接展示在「配置」弹窗（browserPolicyModal）内，不再单独弹窗。
const profileManageLoading = ref(false)
const managedProfiles = ref([])
const profileDeleteTarget = ref(null)
const profileDeleting = ref(false)

async function loadManagedProfiles() {
  if (!selectedAgent.value) return
  profileManageLoading.value = true
  try {
    const list = await listBrowserProfiles('')
    managedProfiles.value = list || []
  } catch (e) {
    managedProfiles.value = []
    pushToast('error', t.browserProfileManageLoadFailed.replace('{error}', String(e)))
  } finally {
    profileManageLoading.value = false
  }
}

async function confirmDeleteBrowserProfile() {
  const target = profileDeleteTarget.value
  if (!target || profileDeleting.value || !selectedAgent.value) return
  profileDeleting.value = true
  try {
    await deleteBrowserProfile(target.id)
    managedProfiles.value = managedProfiles.value.filter(p => p.id !== target.id)
    profileDeleteTarget.value = null
    pushToast('success', t.browserProfileDeleteSuccess)
  } catch (e) {
    pushToast('error', t.browserProfileDeleteFailed.replace('{error}', String(e)))
  } finally {
    profileDeleting.value = false
  }
}

// 重命名 Browser Profile 连接的显示名称
const profileRenameTarget = ref(null)
const renameName = ref('')
const profileRenaming = ref(false)

function openRenameDialog(profile) {
  profileRenameTarget.value = profile
  renameName.value = profile.name || profile.id || ''
}
function closeRenameDialog() {
  profileRenameTarget.value = null
  renameName.value = ''
}
async function confirmRenameBrowserProfile() {
  const target = profileRenameTarget.value
  const newName = renameName.value.trim()
  if (!target || !newName || profileRenaming.value || !selectedAgent.value) return
  profileRenaming.value = true
  try {
    await renameBrowserProfile(target.id, newName)
    const idx = managedProfiles.value.findIndex(p => p.id === target.id)
    if (idx !== -1) {
      managedProfiles.value[idx] = { ...managedProfiles.value[idx], name: newName, updatedAt: new Date().toISOString() }
    }
    pushToast('success', t.browserProfileRenameSuccess)
    closeRenameDialog()
  } catch (e) {
    pushToast('error', t.browserProfileRenameFailed.replace('{error}', String(e)))
  } finally {
    profileRenaming.value = false
  }
}

// 顶部下拉切换正在配置的 Agent：复用 openAgentConfig 设置 editingAgentId 并切到配置页
function switchAgent(agentId) {
  const agent = agentList.value.find(a => a.id === agentId)
  if (agent) openAgentConfig(agent)
}

// 切换正在配置的 Agent 时，刷新所有 tab 的数据（扩展/技能/提示词），
// 否则切换后整页仍停留在上一个智能体的配置。
watch(
  () => selectedAgent.value?.id,
  (id) => {
    showBrowserPolicyModal.value = false
    activeTab.value = agentConfigInitialTab.value || 'basics'
    if (!id) return
    void refreshAgentExtensions(id)
    if (activeTab.value === 'prompt') loadPrompt(id)
    else if (activeTab.value === 'skills') refreshSkills()
  },
  { immediate: true }
)
</script>

<template>
<section class="content-page agent-config-page">
  <div class="page-heading">
      <div class="page-heading__back">
        <button class="icon-button" :title="t.back" @click="backToAgentList"><ArrowLeft :size="16" /></button>
        <div class="agent-config-avatar">
          <img v-if="isImageAvatar(agentAvatar(selectedAgent))" :src="agentAvatar(selectedAgent)" class="agent-config-avatar__img" alt="" />
          <span v-else-if="agentAvatar(selectedAgent)" class="agent-config-avatar__emoji">{{ agentAvatar(selectedAgent) }}</span>
          <Bot v-else :size="20" />
        </div>
        <div class="agent-config-title">
          <select
            class="agent-config-switcher"
            :value="selectedAgent ? selectedAgent.id : ''"
            :title="t.agentSwitchTitle || '切换 Agent'"
            @change="switchAgent($event.target.value)"
          >
            <option v-for="agent in agentList" :key="agent.id" :value="agent.id">{{ agent.name }}</option>
          </select>
          <p>{{ t.agentConfigIntro }}</p>
        </div>
      </div>
    <button class="icon-button page-refresh" :title="t.refresh" :disabled="extensionsBusy" @click="reloadExtensions">
      <RefreshCw v-if="extensionsBusy" class="spin" :size="15" /><RefreshCw v-else :size="15" />
    </button>
  </div>

  <div v-if="!selectedAgent" class="agent-runtime-state">
    <Bot :size="28" />
    <strong>{{ t.agentNotFound }}</strong>
    <button class="secondary-button" @click="backToAgentList"><ArrowLeft :size="14" />{{ t.back }}</button>
  </div>

  <div v-else class="agent-view agent-view--tabs">
    <nav class="agent-config-tabs" aria-label="agent config sections">
      <button :class="{ active: activeTab === 'basics' }" @click="onTabChange('basics')">
        <Settings :size="15" />{{ t.agentTabBasics }}
      </button>
      <button :class="{ active: activeTab === 'extensions' }" @click="onTabChange('extensions')">
        <Zap :size="15" />{{ t.agentTabExtensions }}
      </button>
      <button :class="{ active: activeTab === 'mcp' }" @click="onTabChange('mcp')">
        <Network :size="15" />{{ t.agentTabMcp }}
      </button>
      <button :class="{ active: activeTab === 'prompt' }" @click="onTabChange('prompt')">
        <FileText :size="15" />{{ t.agentTabPrompt }}
      </button>
      <button :class="{ active: activeTab === 'skills' }" @click="activeTab = 'skills'">
        <Sparkles :size="15" />{{ t.agentTabSkills }}
      </button>
    </nav>

    <div class="agent-config-panel">
      <section v-if="activeTab === 'basics'" class="agent-view__meta-block">
        <div class="agent-view__meta-row">
          <label>{{ t.agentName }}</label>
          <input v-model="selectedAgent.name" @change="persistAgentChange(selectedAgent)" />
        </div>
        <div class="agent-view__meta-row">
          <label>{{ t.agentDefault }}</label>
          <label class="agent-basics-default-toggle">
            <span class="switch">
              <input
                type="checkbox"
                :checked="selectedAgent.id === defaultAgentId"
                :disabled="selectedAgent.id === defaultAgentId"
                @change="setDefaultAgent(selectedAgent, $event.target.checked)"
              />
              <span class="switch__track"></span>
            </span>
            <span>{{ selectedAgent.id === defaultAgentId ? t.agentDefaultEnabled : t.agentDefaultHint }}</span>
          </label>
        </div>
        <div class="agent-view__meta-row">
          <label>{{ t.agentDescription }}</label>
          <input v-model="selectedAgent.description" @change="persistAgentChange(selectedAgent)" :placeholder="t.agentNoDescription" />
        </div>
        <div class="agent-view__meta-row agent-view__meta-row--avatar">
          <label>{{ t.agentAvatar }}</label>
          <div class="agent-avatar-picker">
            <div class="agent-avatar-picker__presets">
              <button
                v-for="emoji in avatarPresets"
                :key="emoji"
                type="button"
                class="agent-avatar-picker__preset"
                :class="{ 'agent-avatar-picker__preset--active': selectedAgent.avatar === emoji }"
                :title="emoji"
                @click="selectAvatar(emoji)"
              >{{ emoji }}</button>
              <button
                type="button"
                class="agent-avatar-picker__preset agent-avatar-picker__preset--clear"
                :title="t.agentAvatarClear"
                @click="selectAvatar('')"
              >{{ selectedAgent.avatar ? '×' : '—' }}</button>
            </div>
            <div class="agent-avatar-picker__row">
              <button
                type="button"
                class="agent-avatar-picker__upload"
                @click="agentAvatarInput?.click()"
              ><Upload :size="15" />{{ t.agentAvatarUpload }}</button>
              <input
                v-if="!isImageAvatar(selectedAgent.avatar)"
                class="agent-avatar-picker__custom"
                :value="selectedAgent.avatar"
                maxlength="2"
                :placeholder="t.agentAvatarClear"
                @input="selectAvatar($event.target.value)"
              />
            </div>
            <div v-if="isImageAvatar(selectedAgent.avatar)" class="agent-avatar-picker__image">
              <img :src="selectedAgent.avatar" alt="" />
              <button type="button" class="secondary-button compact" @click="selectAvatar('')">{{ t.agentAvatarClear }}</button>
            </div>
            <small>{{ t.agentAvatarHint }}</small>
            <input
              ref="agentAvatarInput"
              type="file"
              accept="image/*"
              class="hidden-file-input"
              @change="uploadAgentAvatar"
            />
          </div>
        </div>
        <div class="agent-view__meta-row">
          <label>{{ t.agentDefaultModel }}</label>
          <select v-model="selectedDefaultModel" @change="saveAgentRuntimeConfig">
            <option v-for="option in modelOptions" :key="option.value" :value="option.value">{{ option.label }}</option>
          </select>
          <small>{{ t.agentDefaultModelHint }}</small>
        </div>
        <div class="agent-view__meta-row">
          <label>{{ t.agentSubagents }}</label>
          <div v-if="availableSubagents.length" class="agent-subagent-list">
            <label v-for="agent in availableSubagents" :key="agent.id" class="agent-subagent-option" :title="subagentTooltip(agent)">
              <input type="checkbox" :checked="subagentEnabled(agent.id)" @change="toggleSubagent(agent.id)" />
              <span class="agent-subagent-option__avatar">
                <img v-if="isImageAvatar(agentAvatar(agent))" :src="agentAvatar(agent)" class="agent-subagent-option__img" alt="" />
                <span v-else-if="agentAvatar(agent)" class="agent-subagent-option__emoji">{{ agentAvatar(agent) }}</span>
                <Bot v-else :size="14" />
              </span>
              <span class="agent-subagent-option__name">{{ agent.name }}</span>
            </label>
          </div>
          <small v-else>{{ t.agentSubagentsEmpty }}</small>
        </div>
        <div class="agent-view__meta-row agent-view__meta-row--top">
          <label>{{ t.agentPiTools }}</label>
          <div class="agent-pi-tool-list">
            <label v-for="tool in ['read', 'bash', 'edit', 'write']" :key="tool" class="agent-pi-tool-option">
              <input type="checkbox" :checked="selectedAgent?.piTools?.[tool] !== false" @change="togglePiTool(tool)" />
              <span>
                <strong>{{ tool }}</strong>
                <small>{{ t.piDefaultTool }}</small>
              </span>
            </label>
          </div>
        </div>
        <div class="agent-view__meta-row">
          <label>{{ t.agentDataDir }}</label>
          <div class="agent-view__dir-pick">
            <code>{{ selectedAgent.dataDir || t.agentDataDirDefault }}</code>
            <button class="secondary-button compact" @click="pickAgentDataDir">{{ t.choose }}</button>
          </div>
        </div>
      </section>

      <div v-else-if="activeTab === 'extensions'" class="agent-extensions">
        <div v-if="extensionLoading" class="agent-extensions__loading" role="status" aria-label="检查中…">
          <RefreshCw class="spin" :size="20" />
          <span>{{ t.checking }}</span>
        </div>
        <template v-else>
        <div class="plugin-section recommended">
          <div class="plugin-section__title">
            <span class="plugin-section__title-left">
              <span>{{ t.recommendedExtensions }}</span>
              <small>{{ t.recommendedExtensionsHint }}</small>
            </span>
            <button class="secondary-button compact install-ext-trigger" @click="openInstallModal">+ {{ t.installExtension }}</button>
          </div>
          <div class="agent-ext-grid">
            <article class="agent-ext-row">
              <span class="agent-ext-row__icon"><component :is="extensionIcon('pi-plugins')" /></span>
              <div class="agent-ext-row__body">
                <header class="agent-ext-row__head">
                  <h3>{{ t.piPlugins }}</h3>
                </header>
                <p class="agent-ext-row__description">{{ t.piPluginsDescription }}</p>
                <div class="agent-ext-row__meta">
                  <span class="agent-ext-row__version" :class="{ 'plugin-error': piPluginsStatus && !piPluginsStatus.installed }">
                    {{ !piPluginsStatus || !piPluginsStatus.installed ? t.notInstalled : (piPluginsStatus.version || t.installed) }}
                  </span>
                </div>
              </div>
              <div
                class="agent-ext-row__actions"
                :title="piPluginsInstallDisabled ? t.piPluginsWindowsUnsupported : ''"
              >
                <button
                  class="btn-install"
                  :class="{ 'is-installed': piPluginsStatus?.installed }"
                  :disabled="piPluginsInstallDisabled || extensionBusy === 'pi-plugins-install'"
                  @click="onPiPluginsToggle(piPluginsStatus?.installed)"
                >
                  <RefreshCw v-if="extensionBusy === 'pi-plugins-install'" class="spin" :size="13" />
                  <span class="btn-install__install">{{ t.runInstall }}</span>
                  <span class="btn-install__delete">{{ t.delete }}</span>
                </button>
              </div>
            </article>

            <article class="agent-ext-row">
              <span class="agent-ext-row__icon"><component :is="extensionIcon('dcg')" /></span>
              <div class="agent-ext-row__body">
                <header class="agent-ext-row__head">
                  <h3>DCG</h3>
                </header>
                <p class="agent-ext-row__description">{{ t.dcgDescription }}</p>
                <div class="agent-ext-row__meta">
                  <span class="agent-ext-row__version" :class="{ 'plugin-error': dcgStatus && !dcgStatus.installed }">{{ !dcgStatus || !dcgStatus.installed ? t.dcgBinaryMissing : (dcgStatus.version || t.installed) }}</span>
                </div>
              </div>
              <div class="agent-ext-row__actions">
                <button class="btn-install" :class="{ 'is-installed': selectedAgent?.recommended?.dcg }" :disabled="!dcgStatus?.installed && !selectedAgent?.recommended?.dcg" @click="onExtensionToggle('recommended', 'dcg', selectedAgent?.recommended?.dcg, 'DCG')"><span class="btn-install__install">{{ t.enable }}</span><span class="btn-install__delete">{{ t.delete }}</span></button>
              </div>
            </article>

            <article class="agent-ext-row">
              <span class="agent-ext-row__icon"><component :is="extensionIcon('rtk')" /></span>
              <div class="agent-ext-row__body">
                <header class="agent-ext-row__head">
                  <h3>RTK</h3>
                </header>
                <p class="agent-ext-row__description">{{ t.rtkDescription }}</p>
                <div class="agent-ext-row__meta">
                  <span class="agent-ext-row__version" :class="{ 'plugin-error': rtkStatus && !rtkStatus.installed }">{{ !rtkStatus || !rtkStatus.installed ? t.rtkBinaryMissing : (rtkStatus.version || t.installed) }}</span>
                </div>
              </div>
              <div class="agent-ext-row__actions">
                <button class="btn-install" :class="{ 'is-installed': selectedAgent?.recommended?.rtk }" :disabled="!rtkStatus?.installed && !selectedAgent?.recommended?.rtk" @click="onExtensionToggle('recommended', 'rtk', selectedAgent?.recommended?.rtk, 'RTK')"><span class="btn-install__install">{{ t.enable }}</span><span class="btn-install__delete">{{ t.delete }}</span></button>
              </div>
            </article>

            <article class="agent-ext-row">
              <span class="agent-ext-row__icon"><component :is="extensionIcon('browser-native')" /></span>
              <div class="agent-ext-row__body">
                <header class="agent-ext-row__head">
                  <h3>{{ t.browserNative }}</h3>
                </header>
                <p class="agent-ext-row__description">{{ t.browserNativeDescription }}</p>
                <div class="agent-ext-row__meta">
                  <span class="agent-ext-row__version" :class="{ 'plugin-error': browserStatus && !browserStatus.installed }">{{ !browserStatus || !browserStatus.installed ? t.notInstalled : (browserStatus.version || t.installed) }}</span>
                  <span class="agent-ext-row__version" :class="{ 'plugin-error': !browserRuntimeStatus?.installed }">{{ t.globalBrowserRuntime }}：{{ browserRuntimeStatus?.installed ? (browserRuntimeStatus.version || t.installed) : t.notInstalled }}</span>
                  <span class="agent-ext-row__version" :class="{ 'plugin-error': !playwrightStatus?.installed }">Playwright：{{ playwrightStatus?.installed ? (playwrightStatus.version || t.installed) : t.notInstalled }}</span>
                </div>
              </div>
              <div class="agent-ext-row__actions">
                <template v-if="extensionLoading">
                  <span class="pw-loading"><RefreshCw class="spin" :size="13" />{{ t.checking }}</span>
                </template>
                <template v-else>
                  <button class="btn-install" :class="{ 'is-installed': browserStatus && browserStatus.installed }" :disabled="extensionBusy === 'browser-install'" @click="onBrowserToggle(browserStatus && browserStatus.installed)">
                    <RefreshCw v-if="extensionBusy === 'browser-install'" class="spin" :size="13" />
                    <span class="btn-install__install">{{ t.runInstall }}</span>
                    <span class="btn-install__delete">{{ t.delete }}</span>
                  </button>
                </template>
              </div>
            </article>

          </div>
        </div>

        <div v-if="installedPackages.length" class="plugin-section installed">
          <div class="plugin-section__title"><span>{{ t.installedExtensions }}</span></div>
          <div class="agent-ext-grid">
            <article v-for="extension in installedPackages" :key="extension.key" class="agent-ext-row">
              <span class="agent-ext-row__icon"><component :is="extensionIcon(extension.key)" /></span>
              <div class="agent-ext-row__body">
                <header class="agent-ext-row__head">
                  <h3>{{ extension.name || extension.key }}</h3>
                </header>
                <p class="agent-ext-row__description">{{ extension.description || extension.key }}</p>
                <div class="agent-ext-row__meta">
                  <span class="agent-ext-row__version" :class="{ 'plugin-error': !extension.installed }">
                    {{ extension.installed ? (extension.version || t.installed) : t.notInstalled }}
                  </span>
                </div>
                <div v-if="packageSkills(extension).length" class="agent-ext-skills">
                  <div v-for="sk in packageSkills(extension)" :key="sk.id" class="agent-ext-skill">
                    <div class="agent-ext-skill__meta">
                      <span class="agent-ext-skill__name">{{ sk.name }}</span>
                      <span class="agent-ext-skill__desc">{{ sk.description }}</span>
                    </div>
                    <span class="agent-ext-skill__mode">{{ sk.loadMode === 'skills_list' ? (t.agentSkillListMode || '按需加载') : (t.agentSkillStartup || '随启动加载') }}</span>
                    <button class="agent-ext-skill__del" :title="t.delete" :disabled="skillsLoading" @click.stop="requestRemoveSkill(sk)">
                      <svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 6h18M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2m3 0v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6"/><path d="M10 11v6M14 11v6"/></svg>
                    </button>
                  </div>
                </div>
              </div>
              <div class="agent-ext-row__actions">
                <button
                  class="btn-install is-installed"
                  :disabled="extensionDeleteBusy"
                  @click="requestDeleteExtension({ type: 'package', key: extension.key, name: extension.name || extension.key })"
                >
                  <span class="btn-install__delete">{{ t.delete }}</span>
                </button>
              </div>
            </article>
          </div>
        </div>

        <div v-if="unmanagedExtensions.length" class="plugin-section installed">
          <div class="plugin-section__title">
            <span class="plugin-section__title-left">
              <span>{{ t.unmanagedExtensions }}</span>
              <small>{{ t.unmanagedExtensionsHint }}</small>
            </span>
          </div>
          <div class="agent-ext-grid">
            <article v-for="extension in unmanagedExtensions" :key="extension.key" class="agent-ext-row">
              <span class="agent-ext-row__icon"><component :is="extensionIcon(extension.key)" /></span>
              <div class="agent-ext-row__body">
                <header class="agent-ext-row__head">
                  <h3>{{ extension.name || extension.key }}</h3>
                  <code v-if="extension.name && extension.name !== extension.key" class="agent-ext-row__dir" :title="t.unmanagedExtensionsDirTitle">{{ extension.key }}</code>
                </header>
                <p class="agent-ext-row__description">{{ extension.description || extension.key }}</p>
                <div class="agent-ext-row__meta">
                  <span class="agent-ext-row__version">{{ extension.version || t.installed }}</span>
                </div>
              </div>
              <div class="agent-ext-row__actions">
                <button
                  class="btn-install is-installed"
                  :disabled="extensionDeleteBusy"
                  @click="requestDeleteExtension({ type: 'directory', key: extension.key, name: extension.name || extension.key })"
                >
                  <span class="btn-install__delete">{{ t.delete }}</span>
                </button>
              </div>
            </article>
          </div>
        </div>

        <div class="plugin-section builtins">
          <div class="plugin-section__title"><span>{{ t.builtinTools }}</span><small>{{ t.extensionGroupHint }}</small></div>
          <div class="agent-ext-grid">
            <article v-for="status in builtinStatuses" :key="status.key" class="agent-ext-row">
              <span class="agent-ext-row__icon"><component :is="extensionIcon(status.key)" /></span>
              <div class="agent-ext-row__body">
                <header class="agent-ext-row__head">
                  <h3>{{ status.name || status.key }}</h3>
                </header>
                <p class="agent-ext-row__description">{{ status.description || status.key }}</p>
                <p v-if="status.key === 'browser-profile'" class="browser-policy-summary">{{ browserPolicySummary }}</p>
                <div class="agent-ext-row__meta">
                  <span v-if="status.required" class="agent-ext-row__version agent-ext-row__version--latest">{{ t.requiredBuiltin }}</span>
                  <span v-if="status.installed && status.installedVersion" class="agent-ext-row__version"><span class="agent-ext-row__version-label">{{ t.currentVersionLabel }}</span>v{{ status.installedVersion }}</span>
                  <button v-if="status.installed && status.installedVersion && status.installedVersion !== status.currentVersion" class="primary-button compact" :disabled="builtinBusy === status.key" @click="updateBuiltinTool(status.key)"><RefreshCw v-if="builtinBusy === status.key" :size="13" />{{ t.updateBuiltin }}</button>
                </div>
              </div>
              <div class="agent-ext-row__actions">
                <button v-if="status.key === 'browser-profile'" class="secondary-button compact browser-policy-button" :title="t.browserPolicyTitle" @click="openBrowserPolicyModal">
                  <Settings :size="13" />{{ t.configure }}
                </button>
                <button v-if="status.required" class="btn-install is-installed" disabled>
                  <span class="btn-install__delete">{{ t.requiredBuiltin }}</span>
                </button>
                <button v-else class="btn-install" :class="{ 'is-installed': selectedAgent?.builtin?.[status.key] }" @click="onExtensionToggle('builtin', status.key, selectedAgent?.builtin?.[status.key], status.name || status.key)">
                  <span class="btn-install__install">{{ t.runInstall }}</span>
                  <span class="btn-install__delete">{{ t.delete }}</span>
                </button>
              </div>
            </article>
          </div>
        </div>
        </template>
      </div>

      <section v-else-if="activeTab === 'mcp'" class="agent-extensions agent-mcp">
        <div v-if="extensionLoading" class="agent-extensions__loading" role="status" aria-label="检查中…">
          <RefreshCw class="spin" :size="20" />
          <span>{{ t.checking }}</span>
        </div>
        <div v-else class="plugin-section recommended">
          <div class="plugin-section__title">
            <span class="plugin-section__title-left">
              <span>{{ t.agentMcpServers }}</span>
              <small>{{ t.agentMcpServersHint }}</small>
            </span>
            <button class="secondary-button compact install-ext-trigger" @click="openMcpInstallModal">+ {{ t.installAgentMcp }}</button>
          </div>
          <div class="agent-ext-grid">
            <article class="agent-ext-row">
              <span class="agent-ext-row__icon"><component :is="extensionIcon('figma')" /></span>
              <div class="agent-ext-row__body">
                <header class="agent-ext-row__head">
                  <h3>{{ t.figma }}</h3>
                </header>
                <p class="agent-ext-row__description">{{ t.piFigmaDescription }}</p>
                <div class="agent-ext-row__meta">
                  <span class="agent-ext-row__version" :class="{ 'plugin-error': !figma.installed }">
                    {{ t.agentMcpGlobalServer }}：{{ figma.installed ? (figma.version || t.installed) : t.notInstalled }}
                  </span>
                  <span class="agent-ext-row__version" :class="{ 'plugin-error': !figma.hasToken }">
                    {{ t.agentMcpAuthorization }}：{{ figma.hasToken ? t.figmaAuthorized : t.figmaNotAuthorized }}
                  </span>
                  <span class="agent-ext-row__version" :class="{ 'plugin-error': !piFigmaStatus?.installed }">
                    {{ t.agentMcpConnection }}：{{ piFigmaStatus?.installed ? (piFigmaStatus.version || t.enabled) : t.notInstalledForAgent }}
                  </span>
                </div>
              </div>
              <div class="agent-ext-row__actions">
                <button
                  class="btn-install"
                  :class="{ 'is-installed': piFigmaStatus?.installed }"
                  :disabled="extensionBusy === 'figma-agent-install'"
                  @click="onPiFigmaToggle(!!piFigmaStatus?.installed)"
                >
                  <RefreshCw v-if="extensionBusy === 'figma-agent-install'" class="spin" :size="13" />
                  <span class="btn-install__install">{{ t.enable }}</span>
                  <span class="btn-install__delete">{{ t.delete }}</span>
                </button>
              </div>
            </article>
            <article v-for="server in agentMcpServers" :key="server.key" class="agent-ext-row">
              <span class="agent-ext-row__icon"><Network /></span>
              <div class="agent-ext-row__body">
                <header class="agent-ext-row__head">
                  <h3>{{ server.name || server.key }}</h3>
                </header>
                <p class="agent-ext-row__description">{{ server.description || server.key }}</p>
                <div class="agent-ext-row__meta">
                  <span class="agent-ext-row__version">{{ server.installed ? t.installedForAgent : t.notInstalledForAgent }}</span>
                  <span v-if="server.version" class="agent-ext-row__version">{{ server.version }}</span>
                </div>
              </div>
              <div class="agent-ext-row__actions">
                <button
                  class="btn-install is-installed"
                  :disabled="extensionBusy === 'agent-mcp-remove'"
                  @click="removeAgentMcpServer(selectedAgent.id, server.key)"
                >
                  <RefreshCw v-if="extensionBusy === 'agent-mcp-remove'" class="spin" :size="13" />
                  <span class="btn-install__delete">{{ t.removeMcp }}</span>
                </button>
              </div>
            </article>
          </div>
        </div>
      </section>

      <InstallDialog
        v-if="showInstallModal"
        mode="command"
        :title="installTitle"
        :hint="installHint"
        :command="installPackageName"
        :preview-command="installPreviewCommand"
        :command-placeholder="t.npmPackagePlaceholder"
        :running="installing"
        :run-text="t.runInstall"
        @update:command="installPackageName = $event"
        @run="runInstallCommand"
        @close="showInstallModal = false"
      />

      <section v-else-if="activeTab === 'prompt'" class="agent-prompt">
        <nav class="agent-prompt__tabs" aria-label="prompt types">
          <button :class="{ active: activePromptTab === 'startup' }" @click="onPromptTabChange('startup')">
            <FileText :size="15" />{{ t.agentPromptTabStartup }}
          </button>
          <button :class="{ active: activePromptTab === 'forced' }" @click="onPromptTabChange('forced')">
            <Shield :size="15" />{{ t.agentPromptTabForced }}
          </button>
          <button :class="{ active: activePromptTab === 'compress' }" @click="onPromptTabChange('compress')">
            <Binary :size="15" />{{ t.agentPromptTabCompress }}
          </button>
        </nav>

        <template v-if="activePromptTab !== 'forced'">
          <div class="agent-prompt__head">
            <div>
              <h2>{{ activePromptTab === 'compress' ? t.agentPromptTitleCompress : t.agentPromptTitleStartup }}</h2>
              <p>{{ activePromptTab === 'compress' ? t.agentPromptIntroCompress : t.agentPromptIntroStartup }}</p>
            </div>
            <code class="agent-prompt__file">{{ activePromptFile }}</code>
          </div>
          <div v-if="promptLoading" class="agent-prompt__loading">{{ t.agentPromptLoading }}</div>
          <textarea
            v-else
            v-model="activePromptContent"
            class="agent-prompt__editor"
            :placeholder="t.agentPromptPlaceholder"
            spellcheck="false"
          ></textarea>
          <div class="agent-prompt__actions">
            <button class="primary-button" :disabled="promptBusy || promptLoading" @click="savePrompt">
              <RefreshCw v-if="promptBusy" class="spin" :size="14" />{{ t.agentPromptSave }}
            </button>
          </div>
        </template>

        <div v-else class="agent-prompt__forced">
          <div class="agent-prompt__head">
            <div>
              <h2>{{ t.agentPromptTitleForce }}</h2>
              <p>{{ t.agentPromptIntroForce }}</p>
            </div>
            <code class="agent-prompt__file">{{ activePromptFile }}</code>
          </div>
          <div v-if="promptLoading" class="agent-prompt__loading">{{ t.agentPromptLoading }}</div>
          <textarea
            v-else
            v-model="activePromptContent"
            class="agent-prompt__editor"
            :placeholder="t.agentPromptPlaceholder"
            spellcheck="false"
          ></textarea>
          <div class="agent-prompt__actions">
            <button class="primary-button" :disabled="promptBusy || promptLoading" @click="savePrompt">
              <RefreshCw v-if="promptBusy" class="spin" :size="14" />{{ t.agentPromptSave }}
            </button>
          </div>
          <div class="agent-prompt__models">
            <h3>{{ t.agentPromptForcedModelsTitle }}</h3>
            <p class="agent-prompt__models-intro">{{ t.agentPromptForcedModelsIntro }}</p>
            <div class="agent-prompt__model-list">
              <label v-for="m in modelOptions" :key="m.value" class="agent-prompt__model-option">
                <input
                  type="checkbox"
                  :checked="!!(selectedAgent && selectedAgent.forcedPromptModels && selectedAgent.forcedPromptModels[m.value])"
                  @change="toggleForcedModel(m.value)"
                />
                <span class="agent-prompt__model-label">{{ m.label }}</span>
              </label>
            </div>
            <p v-if="!modelOptions.length" class="agent-prompt__models-empty">{{ t.agentPromptForcedNoModels }}</p>
          </div>
        </div>
      </section>

      <section v-else-if="activeTab === 'skills'" class="agent-skills-tab">
        <div class="agent-skills-tab__head">
          <div>
            <h2>{{ t.agentTabSkills }}</h2>
            <p>{{ t.agentSkillsIntro }}</p>
          </div>
          <button class="secondary-button compact" :disabled="skillsLoading" @click="refreshSkills">
            <RefreshCw :size="14" :class="{ spin: skillsLoading }" />{{ t.refresh }}
          </button>
        </div>
        <div v-if="!agentSkills.length" class="agent-skills-empty">
          <Sparkles :size="26" />
          <strong>{{ t.agentSkillsEmpty || '该 Agent 还没有分配 Skill' }}</strong>
          <p>{{ t.agentSkillsEmptyHint || '在「Skill 管理」页面安装并分配给本 Agent 后，会在此显示。' }}</p>
        </div>
        <ul v-else class="agent-skills-list">
          <li v-for="skill in agentSkills" :key="skill.id" class="agent-skill-item">
            <div class="agent-skill-item__icon"><Sparkles :size="16" /></div>
            <SkillCard :skill="skill" />
            <div class="agent-skill-item__meta">
              <span class="agent-skill-badge" :class="skill.loadMode === 'skills_list' ? 'badge--list' : 'badge--startup'">{{ skill.loadMode === 'skills_list' ? (t.agentSkillListMode || '按需加载') : (t.agentSkillStartup || '随启动加载') }}</span>
              <button class="danger-button" :disabled="skillsLoading" @click.stop="requestRemoveSkill(skill)"><Trash2 :size="14" />{{ t.delete }}</button>
            </div>
          </li>
        </ul>
      </section>

      <div v-if="showBrowserPolicyModal" class="modal-backdrop" @click.self="showBrowserPolicyModal = false">
        <div class="agent-editor-dialog browser-policy-dialog">
          <header class="agent-editor-dialog__head">
            <h2>{{ t.browserPolicyTitle }}</h2>
            <button class="icon-button" :title="t.closeDialog" @click="showBrowserPolicyModal = false"><X :size="16" /></button>
          </header>
          <div class="agent-editor-dialog__body">
            <p class="browser-policy-intro">{{ t.browserPolicyIntro }}</p>
            <div class="browser-policy-list">
              <label class="browser-policy-row">
                <span>
                  <strong>{{ t.browserPolicyExisting }}</strong>
                  <small>{{ t.browserPolicyExistingHint }}</small>
                </span>
                <select v-model="browserPolicyDraft.existingProfileMode">
                  <option value="headless">{{ t.browserModeHeadless }}（{{ t.browserPolicyRecommended }}）</option>
                  <option value="headed">{{ t.browserModeHeaded }}</option>
                </select>
              </label>
              <label class="browser-policy-row">
                <span>
                  <strong>{{ t.browserPolicyLogin }}</strong>
                  <small>{{ t.browserPolicyLoginHint }}</small>
                </span>
                <select v-model="browserPolicyDraft.interactiveLoginMode" disabled>
                  <option value="headed">{{ t.browserModeHeaded }}（{{ t.browserPolicyRequired }}）</option>
                </select>
              </label>
              <label class="browser-policy-row">
                <span>
                  <strong>{{ t.browserPolicyTask }}</strong>
                  <small>{{ t.browserPolicyTaskHint }}</small>
                </span>
                <select v-model="browserPolicyDraft.authenticatedTaskMode">
                  <option value="headless">{{ t.browserModeHeadless }}（{{ t.browserPolicyRecommended }}）</option>
                  <option value="headed">{{ t.browserModeHeaded }}</option>
                </select>
              </label>
            </div>
            <div class="browser-profile-manage-inline">
              <h3 class="browser-profile-manage-inline__title">{{ t.browserProfileManageTitle }}</h3>
              <p v-if="profileManageLoading" class="browser-profile-manage-empty">{{ t.browserProfileManageLoading }}</p>
              <p v-else-if="!managedProfiles.length" class="browser-profile-manage-empty">{{ t.browserProfileManageEmpty }}</p>
              <div v-else class="agent-ext-grid browser-profile-manage-list">
                <article v-for="profile in managedProfiles" :key="profile.id" class="agent-ext-row">
                  <span class="agent-ext-row__icon"><Globe2 /></span>
                  <div class="agent-ext-row__body">
                    <header class="agent-ext-row__head">
                      <h3>{{ profile.name || profile.id }}</h3>
                      <small v-if="profile.loginUrl || (profile.origins && profile.origins.length)">{{ profile.loginUrl || profile.origins.join(', ') }}</small>
                    </header>
                    <div v-if="profile.credentialConfigured" class="agent-ext-row__meta">
                      <span class="browser-profile-tag">{{ t.browserProfileSavedCredential }}</span>
                    </div>
                  </div>
                  <div class="agent-ext-row__actions">
                    <button class="secondary-button" :disabled="profileRenaming" @click="openRenameDialog(profile)"><Pencil :size="14" />{{ t.rename }}</button>
                    <button class="danger-button" :disabled="profileDeleting || profileRenaming" @click="profileDeleteTarget = profile"><Trash2 :size="14" />{{ t.delete }}</button>
                  </div>
                </article>
              </div>
            </div>
          </div>
          <footer class="agent-editor-dialog__footer browser-policy-footer">
            <button class="secondary-button" :disabled="browserPolicySaving" @click="useRecommendedBrowserPolicy">{{ t.browserPolicyUseRecommended }}</button>
            <span class="browser-policy-footer__spacer"></span>
            <button class="secondary-button" :disabled="browserPolicySaving" @click="showBrowserPolicyModal = false">{{ t.cancel }}</button>
            <button class="primary-button" :disabled="browserPolicySaving" @click="saveBrowserPolicy">
              <RefreshCw v-if="browserPolicySaving" class="spin" :size="13" />{{ t.saveItem }}
            </button>
          </footer>
        </div>
      </div>

      <ConfirmDeleteDialog
        :model-value="!!profileDeleteTarget"
        :title="t.browserProfileManageTitle"
        :description="t.browserProfileDeleteConfirm.replace('{name}', (profileDeleteTarget?.name || profileDeleteTarget?.id || ''))"
        :busy="profileDeleting"
        :confirm-label="t.delete"
        :confirm-busy-label="t.browserProfileDeleteBusy"
        @cancel="profileDeleteTarget = null"
        @confirm="confirmDeleteBrowserProfile"
      />

      <ConfirmDeleteDialog
        :model-value="!!skillDeleteTarget"
        :title="t.skillsRemoveFromAgentTitle"
        :description="t.skillsRemoveFromAgentConfirm.replace('{name}', skillDeleteTarget?.name || '').replace('{agent}', selectedAgent?.name || '')"
        :busy="skillDeleting"
        :confirm-label="t.delete"
        :confirm-busy-label="t.deletingSkill"
        @cancel="skillDeleteTarget = null"
        @confirm="confirmRemoveSkill"
      />

      <div v-if="profileRenameTarget" class="modal-backdrop" @click.self="closeRenameDialog">
        <div class="agent-editor-dialog browser-profile-rename-dialog">
          <header class="agent-editor-dialog__head">
            <h2>{{ t.browserProfileRenameTitle }}</h2>
            <button class="icon-button" :title="t.closeDialog" @click="closeRenameDialog"><X :size="16" /></button>
          </header>
          <div class="agent-editor-dialog__body">
            <form class="browser-profile-form" @submit.prevent="confirmRenameBrowserProfile">
              <label>
                {{ t.browserProfileRenameName }}
                <input v-model="renameName" :placeholder="profileRenameTarget?.name || profileRenameTarget?.id" maxlength="64" autofocus />
              </label>
            </form>
          </div>
          <footer class="agent-editor-dialog__footer">
            <button class="secondary-button" :disabled="profileRenaming" @click="closeRenameDialog">{{ t.cancel }}</button>
            <button class="primary-button" :disabled="profileRenaming || !renameName.trim()" @click="confirmRenameBrowserProfile">
              <RefreshCw v-if="profileRenaming" class="spin" :size="13" />{{ profileRenaming ? t.browserProfileRenameBusy : t.rename }}
            </button>
          </footer>
        </div>
      </div>

    </div>
  </div>
</section>
</template>
