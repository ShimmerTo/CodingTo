<script setup>
import { computed, ref } from 'vue'
import { Network, PackagePlus, Plus, RefreshCw, Trash2, X } from 'lucide-vue-next'
import { useAppContext, agentAvatar, isImageAvatar } from '../../composables/appContext'
import InstallDialog from '../InstallDialog.vue'

const { t, refreshExtensions, extensionSnapshot, extensionBusy, installGlobalPackage, addManualMCP, agentList, installAgentMcp, removeAgentMcpServer } = useAppContext()
const globalMcp = computed(() => extensionSnapshot.value?.globalMcp || [])

// --- Agent filter ---
const filterAgentId = ref('')

// Aggregate per-agent MCP servers into a unique list keyed by server key,
// each entry carrying the list of agents it is installed on.
const agentMcpEntries = computed(() => {
  const mcpMap = extensionSnapshot.value?.mcp || {}
  const byKey = new Map()
  for (const [agentId, servers] of Object.entries(mcpMap)) {
    for (const server of (servers || [])) {
      if (server.key === 'figma') continue
      if (!byKey.has(server.key)) byKey.set(server.key, { ...server, agents: [] })
      const agent = (agentList.value || []).find(a => a.id === agentId)
      if (agent) byKey.get(server.key).agents.push(agent)
    }
  }
  return [...byKey.values()]
})
const filteredMcpEntries = computed(() => {
  const id = filterAgentId.value
  if (!id) return agentMcpEntries.value
  return agentMcpEntries.value.filter(entry => entry.agents.some(a => a.id === id))
})
function agentMcpCount(agentId) {
  return ((extensionSnapshot.value?.mcp || {})[agentId] || []).filter(s => s.key !== 'figma').length
}
const filteredAgent = computed(() => (agentList.value || []).find(a => a.id === filterAgentId.value) || null)

// --- Global MCP install dialog ---
const showInstallModal = ref(false)
const packageName = ref('')
const previewCommand = computed(() => packageName.value.trim() ? `npm install -g ${packageName.value.trim()}` : 'npm install -g <package>')

function openInstallModal(name = '') {
  packageName.value = name
  showInstallModal.value = true
}

async function runInstall() {
  if (!packageName.value.trim()) return
  try {
    await installGlobalPackage('mcp', packageName.value.trim())
    showInstallModal.value = false
  } catch {}
}

// --- Per-agent MCP install dialog ---
const showAgentInstallModal = ref(false)
const agentPackageName = ref('')
const agentPreviewCommand = computed(() => {
  const name = agentPackageName.value.trim() || '<package>'
  return `npm install -g ${name}\npi install npm:pi-mcp-adapter`
})
function openAgentInstallModal() {
  agentPackageName.value = ''
  showAgentInstallModal.value = true
}
async function runAgentInstall() {
  if (!filterAgentId.value || !agentPackageName.value.trim()) return
  try {
    await installAgentMcp(filterAgentId.value, agentPackageName.value.trim())
    showAgentInstallModal.value = false
  } catch {}
}

// --- Manual MCP dialog ---
const showManualModal = ref(false)
const manualKey = ref('')
const manualTransport = ref('stdio')
const manualCommand = ref('')
const manualArgs = ref('')
const manualEnv = ref('')
const manualUrl = ref('')
const manualAgentIds = ref([])
const showJsonImport = ref(false)
const manualJson = ref('')
const manualJsonError = ref('')
const manualJsonPlaceholder = '{\n  "mcpServers": {\n    "my-server": {\n      "command": "npx",\n      "args": ["-y", "some-mcp-server"],\n      "env": { "API_KEY": "xxx" }\n    }\n  }\n}'

function openManualModal() {
  manualKey.value = ''
  manualTransport.value = 'stdio'
  manualCommand.value = ''
  manualArgs.value = ''
  manualEnv.value = ''
  manualUrl.value = ''
  manualAgentIds.value = filterAgentId.value ? [filterAgentId.value] : (agentList.value || []).map(a => a.id)
  showManualModal.value = true
}

function openJsonImport() {
  manualJson.value = ''
  manualJsonError.value = ''
  showJsonImport.value = true
}

function toggleManualAgent(id) {
  const idx = manualAgentIds.value.indexOf(id)
  if (idx >= 0) manualAgentIds.value.splice(idx, 1)
  else manualAgentIds.value.push(id)
}

// Parse pasted JSON into a list of server entries. Accepts both the standard
// { "mcpServers": { ... } } wrapper and a bare { "name": { ... } } object.
function parseJsonEntries(raw) {
  if (!raw.trim()) return []
  let parsed
  try {
    parsed = JSON.parse(raw)
  } catch {
    return null
  }
  if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) return null
  const servers = (parsed.mcpServers && typeof parsed.mcpServers === 'object' && !Array.isArray(parsed.mcpServers))
    ? parsed.mcpServers
    : parsed
  const entries = []
  for (const [key, value] of Object.entries(servers)) {
    if (typeof value !== 'object' || value === null || Array.isArray(value)) continue
    if (!value.command && !value.url) continue
    entries.push({
      key,
      command: typeof value.command === 'string' ? value.command : '',
      args: Array.isArray(value.args) ? value.args.map(String) : [],
      url: typeof value.url === 'string' ? value.url : '',
      env: (value.env && typeof value.env === 'object' && !Array.isArray(value.env)) ? value.env : null,
    })
  }
  return entries
}

// Confirm JSON import: fill the first parsed entry into the form so the user
// can review and adjust every field before submitting.
function confirmJsonImport() {
  const entries = parseJsonEntries(manualJson.value)
  if (!Array.isArray(entries) || entries.length === 0) {
    manualJsonError.value = t.value.manualMcpJsonInvalid
    return
  }
  const entry = entries[0]
  manualKey.value = entry.key
  if (entry.url) {
    manualTransport.value = 'url'
    manualUrl.value = entry.url
    manualCommand.value = ''
    manualArgs.value = ''
    manualEnv.value = ''
  } else {
    manualTransport.value = 'stdio'
    manualCommand.value = entry.command
    manualArgs.value = entry.args.join(', ')
    manualEnv.value = entry.env ? Object.entries(entry.env).map(([k, v]) => `${k}=${v}`).join('\n') : ''
    manualUrl.value = ''
  }
  showJsonImport.value = false
}

const MCP_KEY_PATTERN = /^[a-zA-Z0-9_-]+$/
const manualKeyInvalid = computed(() => {
  const v = manualKey.value.trim()
  return v.length > 0 && !MCP_KEY_PATTERN.test(v)
})

const manualCanSubmit = computed(() => {
  if (manualAgentIds.value.length === 0) return false
  if (!manualKey.value.trim()) return false
  if (manualKeyInvalid.value) return false
  if (manualTransport.value === 'stdio') return !!manualCommand.value.trim()
  return !!manualUrl.value.trim()
})

async function submitManualMCP() {
  if (!manualCanSubmit.value) return
  const env = {}
  if (manualTransport.value === 'stdio' && manualEnv.value.trim()) {
    for (const line of manualEnv.value.split('\n')) {
      const eq = line.indexOf('=')
      if (eq > 0) env[line.slice(0, eq).trim()] = line.slice(eq + 1).trim()
    }
  }
  const payload = {
    key: manualKey.value.trim(),
    command: manualTransport.value === 'stdio' ? manualCommand.value.trim() : '',
    args: manualTransport.value === 'stdio' && manualArgs.value.trim() ? manualArgs.value.split(',').map(s => s.trim()).filter(Boolean) : [],
    url: manualTransport.value === 'url' ? manualUrl.value.trim() : '',
    env: Object.keys(env).length ? env : null,
    agentIds: manualAgentIds.value,
  }
  try {
    await addManualMCP(payload)
    showManualModal.value = false
  } catch {}
}
</script>

<template>
  <section class="content-page mcp-page">
    <div class="page-heading">
      <div><h2>{{ t.mcpTitle }}</h2><p>{{ t.mcpIntro }}</p></div>
      <div class="page-heading__actions">
        <button v-if="filterAgentId" class="primary-button compact" @click="openAgentInstallModal()"><Network :size="14" />{{ t.mcpInstallToAgent || '安装 MCP' }}</button>
        <button v-else class="secondary-button compact" @click="openInstallModal()"><PackagePlus :size="14" />{{ t.installGlobalMcp }}</button>
        <button class="secondary-button compact" @click="openManualModal()"><Plus :size="14" />{{ t.manualMcp }}</button>
        <button class="icon-button page-refresh" :title="t.refresh" @click="refreshExtensions"><RefreshCw :size="17" /></button>
      </div>
    </div>

    <div class="agent-filter-bar">
      <button class="agent-filter-chip" :class="{ active: !filterAgentId }" @click="filterAgentId = ''">{{ t.filterAllAgents || '全部 Agent' }}<span class="agent-filter-chip__count">{{ agentMcpEntries.length }}</span></button>
      <button v-for="agent in agentList" :key="agent.id" class="agent-filter-chip" :class="{ active: filterAgentId === agent.id }" @click="filterAgentId = filterAgentId === agent.id ? '' : agent.id">
        <span class="agent-filter-chip__avatar"><img v-if="isImageAvatar(agentAvatar(agent))" :src="agentAvatar(agent)" alt="" />{{ isImageAvatar(agentAvatar(agent)) ? '' : (agentAvatar(agent) || agent.name?.charAt(0) || '?') }}</span>
        {{ agent.name }}<span class="agent-filter-chip__count">{{ agentMcpCount(agent.id) }}</span>
      </button>
    </div>

    <!-- Per-agent MCP installations -->
    <div class="plugin-section">
      <div class="plugin-section__title">
        <span>{{ t.mcpAgentSection || 'Agent MCP 安装情况' }}</span>
        <small>{{ t.mcpAgentSectionIntro || '按 Agent 查看已接入的 MCP 服务，可单独安装或移除。' }}</small>
      </div>
      <div v-if="!filteredMcpEntries.length" class="empty-integration mcp-agent-empty">
        <Network :size="26" />
        <strong>{{ filterAgentId ? (t.mcpAgentEmpty || '该 Agent 还没有接入 MCP') : (t.mcpAllEmpty || '还没有 Agent 接入 MCP') }}</strong>
        <p>{{ filterAgentId ? (t.mcpAgentEmptyHint || '点击上方“安装 MCP”或“手动添加”为该 Agent 接入服务。') : (t.mcpAllEmptyHint || '点击“安装 MCP”或“手动添加”为 Agent 接入服务。') }}</p>
      </div>
      <template v-else>
        <article v-for="entry in filteredMcpEntries" :key="entry.key" class="plugin-row">
          <div class="plugin-icon"><Network :size="19" /></div>
          <div class="plugin-copy">
            <div class="plugin-name">
              <strong>{{ entry.name || entry.key }}</strong>
              <span class="status-dot" :class="{ active: entry.installed, missing: !entry.installed }"></span>
              <small>{{ entry.installed ? t.installed : t.notInstalled }}</small>
            </div>
            <p>{{ entry.description || entry.key }}</p>
            <code v-if="entry.version">{{ entry.version }}</code>
            <div class="mcp-entry-agents"><span>{{ t.mcpInstalledAgents || '已安装 Agent' }}</span><b v-for="agent in entry.agents" :key="agent.id">{{ agent.name }}</b></div>
          </div>
          <div class="plugin-actions">
            <button v-if="filterAgentId" class="danger-button compact" :disabled="extensionBusy === 'agent-mcp-remove'" @click="removeAgentMcpServer(filterAgentId, entry.key)">
              <Trash2 :size="13" />{{ t.removeMcp || '移除' }}
            </button>
          </div>
        </article>
      </template>
    </div>

    <div v-if="globalMcp.length" class="plugin-section">
      <div class="plugin-section__title"><span>{{ t.otherMcpServers }}</span></div>
      <article v-for="server in globalMcp" :key="server.key" class="plugin-row">
        <div class="plugin-icon"><Network :size="19" /></div>
        <div class="plugin-copy">
          <div class="plugin-name">
            <strong>{{ server.name || server.key }}</strong>
            <span class="status-dot" :class="{ active: server.installed, missing: !server.installed }"></span>
            <small>{{ server.installed ? t.installed : t.notInstalled }}</small>
          </div>
          <p>{{ server.description || server.key }}</p>
          <code>{{ server.installHint || `npm install -g ${server.key}` }}</code>
          <code v-if="server.version">{{ server.version }}</code>
        </div>
        <div class="plugin-actions">
          <button :class="server.installed ? 'secondary-button' : 'primary-button'" :disabled="extensionBusy === 'global-mcp-install'" @click="openInstallModal(server.key)">
            <RefreshCw v-if="server.installed" :size="13" />{{ server.installed ? t.update : t.runInstall }}
          </button>
        </div>
      </article>
    </div>

    <InstallDialog
      v-if="showInstallModal"
      mode="command"
      :title="t.installGlobalMcp"
      :hint="t.installGlobalMcpHint"
      :command="packageName"
      :preview-command="previewCommand"
      :command-placeholder="t.npmPackagePlaceholder"
      :running="extensionBusy === 'global-mcp-install'"
      :run-text="t.runInstall"
      @update:command="packageName = $event"
      @run="runInstall"
      @close="showInstallModal = false"
    />

    <InstallDialog
      v-if="showAgentInstallModal"
      mode="command"
      :title="(t.mcpInstallToAgent || '安装 MCP') + (filteredAgent ? ` → ${filteredAgent.name}` : '')"
      :hint="t.installAgentMcpHint"
      :command="agentPackageName"
      :preview-command="agentPreviewCommand"
      :command-placeholder="t.npmPackagePlaceholder"
      :running="extensionBusy === 'agent-mcp-install'"
      :run-text="t.runInstall"
      @update:command="agentPackageName = $event"
      @run="runAgentInstall"
      @close="showAgentInstallModal = false"
    />

    <!-- Manual MCP configuration dialog -->
    <Teleport to="body">
      <div v-if="showManualModal" class="modal-backdrop" @pointerdown.self="showManualModal = false">
        <div class="agent-editor-dialog manual-mcp-dialog" role="dialog" aria-modal="true">
          <header class="agent-editor-dialog__head">
            <h2>{{ t.manualMcp }}</h2>
            <button class="icon-button" @click="showManualModal = false"><X :size="16" /></button>
          </header>
          <div class="agent-editor-dialog__body manual-mcp-body">
            <p class="manual-mcp-hint">
              {{ t.manualMcpHint }}
              <button type="button" class="manual-mcp-json-link" @click="openJsonImport">{{ t.manualMcpImportJson }}</button>
            </p>

            <label class="manual-mcp-field">{{ t.manualMcpKey }}
              <input v-model="manualKey" class="text-input" :class="{ 'input-error': manualKeyInvalid }" placeholder="my-mcp-server" />
              <small v-if="manualKeyInvalid" class="manual-mcp-error">{{ t.manualMcpKeyInvalid }}</small>
            </label>

            <label class="manual-mcp-field">{{ t.manualMcpTransport }}
              <select v-model="manualTransport" class="text-input">
                <option value="stdio">stdio (command)</option>
                <option value="url">URL (SSE / Streamable HTTP)</option>
              </select>
            </label>

            <template v-if="manualTransport === 'stdio'">
              <label class="manual-mcp-field">{{ t.manualMcpCommand }}
                <input v-model="manualCommand" class="text-input" placeholder="npx -y @modelcontextprotocol/server-filesystem" />
              </label>
              <label class="manual-mcp-field">{{ t.manualMcpArgs }}
                <input v-model="manualArgs" class="text-input" placeholder="/path/to/dir, --read-only" />
              </label>
              <label class="manual-mcp-field">{{ t.manualMcpEnv }}
                <textarea v-model="manualEnv" class="text-input manual-mcp-env" rows="3" placeholder="API_KEY=xxx&#10;DEBUG=true"></textarea>
              </label>
            </template>
            <template v-else>
              <label class="manual-mcp-field">{{ t.manualMcpUrl }}
                <input v-model="manualUrl" class="text-input" placeholder="https://example.com/mcp" />
              </label>
            </template>

            <fieldset class="manual-mcp-agents">
              <legend>{{ t.manualMcpSelectAgents }}</legend>
              <label v-for="agent in agentList" :key="agent.id" class="manual-mcp-agent-row">
                <input type="checkbox" :checked="manualAgentIds.includes(agent.id)" @change="toggleManualAgent(agent.id)" />
                <span>{{ agent.name }}</span>
              </label>
            </fieldset>
          </div>
          <footer class="agent-editor-dialog__footer">
            <button class="secondary-button" @click="showManualModal = false">{{ t.cancel }}</button>
            <button class="primary-button" :disabled="!manualCanSubmit || extensionBusy === 'manual-mcp-add'" @click="submitManualMCP">
              <RefreshCw v-if="extensionBusy === 'manual-mcp-add'" class="spin" :size="13" />{{ t.confirm }}
            </button>
          </footer>
        </div>
      </div>
    </Teleport>

    <!-- JSON import sub-dialog -->
    <Teleport to="body">
      <div v-if="showJsonImport" class="modal-backdrop" @pointerdown.self="showJsonImport = false">
        <div class="agent-editor-dialog manual-mcp-dialog" role="dialog" aria-modal="true">
          <header class="agent-editor-dialog__head">
            <h2>{{ t.manualMcpImportJson }}</h2>
            <button class="icon-button" @click="showJsonImport = false"><X :size="16" /></button>
          </header>
          <div class="agent-editor-dialog__body manual-mcp-body">
            <label class="manual-mcp-field">{{ t.manualMcpJsonLabel }}
              <textarea
                v-model="manualJson"
                class="text-input manual-mcp-json"
                rows="12"
                spellcheck="false"
                :placeholder='manualJsonPlaceholder'
                @input="manualJsonError = ''"
              ></textarea>
            </label>
            <p v-if="manualJsonError" class="manual-mcp-error">{{ manualJsonError }}</p>
          </div>
          <footer class="agent-editor-dialog__footer">
            <button class="secondary-button" @click="showJsonImport = false">{{ t.cancel }}</button>
            <button class="primary-button" :disabled="!manualJson.trim()" @click="confirmJsonImport">{{ t.confirm }}</button>
          </footer>
        </div>
      </div>
    </Teleport>
  </section>
</template>

<style scoped>
.manual-mcp-dialog {
  width: min(480px, 100%);
}
.manual-mcp-body {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.manual-mcp-hint {
  font-size: var(--fs-12);
  opacity: 0.7;
  margin: 0;
}
.manual-mcp-field {
  display: flex;
  flex-direction: column;
  gap: 4px;
  font-size: var(--fs-13);
  font-weight: 500;
}
.manual-mcp-field input,
.manual-mcp-field select,
.manual-mcp-field textarea {
  min-width: 0;
  height: 34px;
  border: 1px solid var(--border);
  outline: 0;
  border-radius: 7px;
  padding: 0 10px;
  color: var(--text);
  background: var(--surface);
  font-size: var(--fs-13);
}
.manual-mcp-field textarea {
  height: auto;
  padding: 8px 10px;
  line-height: 1.5;
}
.manual-mcp-field input:focus,
.manual-mcp-field select:focus,
.manual-mcp-field textarea:focus {
  border-color: var(--faint);
  box-shadow: 0 0 0 2px rgba(113,113,109,.08);
}
.manual-mcp-env {
  resize: vertical;
  font-family: var(--font-mono, monospace);
  font-size: var(--fs-12);
}
.manual-mcp-json-link {
  display: inline;
  border: 0;
  padding: 0;
  background: none;
  color: var(--link);
  font-size: var(--fs-12);
  cursor: pointer;
  text-decoration: underline;
  text-underline-offset: 2px;
}
.manual-mcp-json-link:hover {
  opacity: .8;
}
.manual-mcp-json {
  resize: vertical;
  font-family: var(--font-mono, monospace);
  font-size: var(--fs-12);
  white-space: pre;
}
.manual-mcp-error {
  margin: 0;
  font-size: var(--fs-12);
  color: var(--danger);
}
.manual-mcp-field input.input-error {
  border-color: var(--danger);
}
.manual-mcp-agents {
  border: 1px solid var(--border, #ddd);
  border-radius: 8px;
  padding: 10px 12px;
  margin: 0;
}
.manual-mcp-agents legend {
  font-size: var(--fs-13);
  font-weight: 500;
  padding: 0 4px;
}
.manual-mcp-agent-row {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 0;
  font-size: var(--fs-13);
  cursor: pointer;
}
.mcp-entry-agents {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 5px;
  margin-top: 8px;
  color: var(--faint);
  font-size: var(--fs-12);
}
.mcp-entry-agents b {
  padding: 3px 7px;
  border-radius: 9px;
  color: var(--text);
  background: var(--surface-2);
  font-weight: 500;
}
.mcp-agent-empty {
  min-height: 160px;
}
</style>
