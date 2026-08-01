<script setup>
import { computed } from 'vue'
import { Bot, Brain, Check, Eye, EyeOff, Folder, Image, KeyRound, Plus, RefreshCw, Trash2, Wrench, X, Zap } from 'lucide-vue-next'
import { useAppContext } from '../composables/appContext'
import ConfirmDeleteDialog from './ConfirmDeleteDialog.vue'

const { t, pendingDeleteAgent, agentDeleteBusy, confirmDeleteAgent, pendingDeleteProvider, saving, confirmDeleteProvider, agentEditorOpen, closeAgentEditor, editingNewAgent, selectedAgent, agentList, modelOptions, newAgentId, defaultAgentId, setDefaultAgent, persistAgentChange, pickAgentDataDir, cancelNewAgent, saveNewAgent, providerEditorOpen, closeProviderEditor, providerDraft, editingNewProvider, showProviderApiKey, apiKeyVisibilityLabel, piCompatBooleanFields, formatCompat, updateCompatJson, addModel, modelRequestRoute, toggleImageInput, confirmAddProvider, confirmSaveProvider, pendingDeleteSsh, sshBusy, confirmDeleteSsh, pendingExtensionDelete, extensionDeleteBusy, confirmDeleteExtension, pendingDeleteWs, wsBusy, confirmDeleteWs, sshEditorOpen, closeSshEditor, editingNewSsh, sshDraft, persistSshChange, saveNewSsh, wsEditorOpen, closeWsEditor, editingNewWs, wsDraft, persistWsChange, saveNewWs, pickWorkspacePath, config, handleWorkspaceSshChange, handleWsPathChange, toasts, extensionBusy, showFigmaConfig, figmaAuthorizationsDraft, figmaActiveAuthorizationIdDraft, addFigmaAuthorization, removeFigmaAuthorization, persistFigma } = useAppContext()

const piToolOptions = ['read', 'bash', 'edit', 'write']
const availableSubagents = computed(() =>
  (agentList.value || []).filter(agent => agent.id !== selectedAgent.value?.id)
)
const extensionDeleteTitle = computed(() =>
  pendingExtensionDelete.value?.category === 'mcp' ? t.value.disconnectMcpTitle : t.value.deleteExtensionTitle
)
const extensionDeleteConfirm = computed(() => {
  const template = pendingExtensionDelete.value?.category === 'mcp'
    ? t.value.disconnectMcpConfirm
    : t.value.deleteExtensionConfirm
  return template.replace('{name}', pendingExtensionDelete.value?.name || '')
})
const selectedAgentDefaultModel = computed({
  get: () => {
    const agent = selectedAgent.value
    return agent ? `${agent.defaultProvider || ''}/${agent.defaultModel || ''}` : ''
  },
  set: (value) => {
    const agent = selectedAgent.value
    const separator = String(value).indexOf('/')
    if (!agent || separator < 0) return
    agent.defaultProvider = value.slice(0, separator)
    agent.defaultModel = value.slice(separator + 1)
  },
})
</script>

<template>
  <div class="app-dialogs">
    <ConfirmDeleteDialog
      :model-value="!!pendingDeleteAgent"
      :title="t.deleteAgentTitle"
      :description="t.deleteAgentConfirm.replace('{name}', pendingDeleteAgent?.name || '')"
      :busy="agentDeleteBusy"
      :confirm-label="t.confirmDelete"
      :confirm-busy-label="t.deletingAgent"
      @cancel="pendingDeleteAgent = null"
      @confirm="confirmDeleteAgent"
    />

    <ConfirmDeleteDialog
      :model-value="!!pendingDeleteProvider"
      :title="`${t.delete} ${pendingDeleteProvider?.label || pendingDeleteProvider?.name || ''}?`"
      :description="t.confirmDeleteItem"
      :busy="saving"
      :confirm-label="t.delete"
      @cancel="pendingDeleteProvider = null"
      @confirm="confirmDeleteProvider"
    />

    <div v-if="agentEditorOpen" class="modal-backdrop" @pointerdown.self="closeAgentEditor">
      <section class="agent-editor-dialog" role="dialog" aria-modal="true" :aria-labelledby="'agent-editor-title'">
        <header class="agent-editor-dialog__head">
          <h2 id="agent-editor-title">{{ editingNewAgent ? t.createAgent : t.editAgent }}</h2>
          <button class="icon-button" :aria-label="t.closeDialog" @click="closeAgentEditor"><X :size="16" /></button>
        </header>
        <div v-if="selectedAgent" class="agent-editor-dialog__body">
          <div class="form-grid agent-create-form">
            <label class="agent-create-form__wide"><span>{{ t.agentName }}</span><input v-model="selectedAgent.name" @change="persistAgentChange(selectedAgent)" /></label>
          </div>
          <details class="agent-advanced-settings">
            <summary>{{ t.agentAdvancedSettings }}</summary>
            <div class="agent-advanced-settings__body form-grid">
              <label class="agent-create-form__wide"><span>{{ t.agentDescription }}</span><input v-model="selectedAgent.description" @change="persistAgentChange(selectedAgent)" /></label>
              <label class="agent-create-form__wide"><span>{{ t.agentDefaultModel }}</span>
                <select v-model="selectedAgentDefaultModel" @change="persistAgentChange(selectedAgent)">
                  <option v-for="option in modelOptions" :key="option.value" :value="option.value">{{ option.label }}</option>
                </select>
              </label>
              <fieldset class="agent-create-options agent-create-form__wide">
                <legend>{{ t.agentDefault }}</legend>
                <label class="agent-create-option agent-create-option--default">
                  <input
                    type="checkbox"
                    :checked="selectedAgent.id === defaultAgentId"
                    :disabled="selectedAgent.id === defaultAgentId && selectedAgent.id !== newAgentId"
                    @change="setDefaultAgent(selectedAgent, $event.target.checked)"
                  />
                  <span><strong>{{ t.agentDefaultEnabled }}</strong><small>{{ t.agentDefaultHint }}</small></span>
                </label>
              </fieldset>
              <label class="agent-data-dir agent-create-form__wide"><span>{{ t.agentDataDir }}</span><div><input v-model="selectedAgent.dataDir" :placeholder="t.agentDataDirDefault" @change="persistAgentChange(selectedAgent)" /><button class="secondary-button" @click="pickAgentDataDir"><Folder :size="14" />{{ t.choose }}</button></div><small>{{ t.agentDataDirHint }}</small></label>
              <fieldset class="agent-create-options agent-create-form__wide">
                <legend>{{ t.agentSubagents }}</legend>
                <div v-if="availableSubagents.length" class="agent-create-options__grid">
                  <label v-for="agent in availableSubagents" :key="agent.id" class="agent-create-option">
                    <input v-model="selectedAgent.subagents" type="checkbox" :value="agent.id" @change="persistAgentChange(selectedAgent)" />
                    <span><strong>{{ agent.name }}</strong><small>{{ agent.defaultProvider }}/{{ agent.defaultModel }}</small></span>
                  </label>
                </div>
                <small v-else class="agent-create-options__empty">{{ t.agentSubagentsEmpty }}</small>
              </fieldset>
              <fieldset class="agent-create-options agent-create-form__wide">
                <legend>{{ t.agentPiTools }}</legend>
                <div class="agent-create-options__grid agent-create-options__grid--tools">
                  <label v-for="tool in piToolOptions" :key="tool" class="agent-create-option">
                    <input v-model="selectedAgent.piTools[tool]" type="checkbox" @change="persistAgentChange(selectedAgent)" />
                    <span><strong>{{ tool }}</strong><small>{{ t.piDefaultTool }}</small></span>
                  </label>
                </div>
              </fieldset>
            </div>
          </details>
        </div>
        <footer class="agent-editor-dialog__footer">
          <div class="agent-draft-actions">
            <button v-if="editingNewAgent" class="secondary-button" :disabled="saving" @click="cancelNewAgent">{{ t.cancel }}</button>
            <button v-if="editingNewAgent" class="primary-button" :disabled="saving" @click="saveNewAgent">
              <RefreshCw v-if="saving" class="spin" :size="14" />
              <Check v-else :size="14" />
              {{ saving ? t.savingAgent : t.saveAgent }}
            </button>
            <button v-else class="primary-button" @click="closeAgentEditor">{{ t.closeDialog }}</button>
          </div>
        </footer>
      </section>
    </div>

    <div v-if="providerEditorOpen" class="modal-backdrop" @pointerdown.self="closeProviderEditor">
      <section class="agent-editor-dialog provider-editor-dialog" role="dialog" aria-modal="true" :aria-labelledby="'provider-editor-title'">
        <header class="agent-editor-dialog__head">
          <h2 id="provider-editor-title">{{ editingNewProvider ? t.newProviderTitle : t.editProvider }}</h2>
          <button class="icon-button" :aria-label="t.closeDialog" @click="closeProviderEditor"><X :size="16" /></button>
        </header>
        <div v-if="providerDraft" class="agent-editor-dialog__body">
          <div class="form-grid provider-config-grid">
            <label><span>{{ t.providerLabel }}</span><input v-model="providerDraft.label" :placeholder="t.providerLabel" /></label>
            <label><span>{{ t.providerName }}</span><input v-model="providerDraft.name" /></label>
            <label class="provider-config-grid__wide"><span>{{ t.providerBaseUrl }}</span>
              <input v-model="providerDraft.baseUrl" placeholder="https://api.example.com" />
              <small class="field-hint">{{ t.providerBaseUrlHint }}</small>
            </label>
            <label><span>{{ t.apiKey }}</span>
              <span class="password-field">
                <input
                  v-model="providerDraft.apiKey"
                  :type="showProviderApiKey ? 'text' : 'password'"
                  placeholder="$OPENAI_API_KEY / !command / literal"
                  autocomplete="off"
                />
                <button type="button" :aria-label="apiKeyVisibilityLabel" :title="apiKeyVisibilityLabel" @click="showProviderApiKey = !showProviderApiKey">
                  <Eye v-if="showProviderApiKey" :size="15" />
                  <EyeOff v-else :size="15" />
                </button>
              </span>
            </label>
            <label class="provider-enabled-field"><span>{{ t.enabled }}</span>
              <span class="switch">
                <input type="checkbox" v-model="providerDraft.enabled" />
                <span class="switch__track"></span>
              </span>
            </label>
          </div>
          <details class="compat-section">
            <summary>高级参数 (compat)</summary>
            <div class="compat-section__body">
              <div class="compat-bools">
                <label v-for="item in piCompatBooleanFields" :key="item.key" class="capability-option">
                  <input type="checkbox" v-model="providerDraft.compat[item.key]" />
                  <span>{{ t[item.hint] }}</span>
                </label>
              </div>
              <div class="compat-json">
                <label>原始 JSON</label>
                <textarea :value="formatCompat(providerDraft)" spellcheck="false" @blur="updateCompatJson(providerDraft, $event)"></textarea>
              </div>
            </div>
          </details>
        </div>
        <footer class="agent-editor-dialog__footer">
          <div class="agent-draft-actions">
            <button class="secondary-button" @click="closeProviderEditor">{{ t.cancel }}</button>
            <button v-if="editingNewProvider" class="primary-button" @click="confirmAddProvider"><Check :size="14" />{{ t.confirmAddProvider }}</button>
            <button v-else class="primary-button" @click="confirmSaveProvider"><Check :size="14" />{{ t.save }}</button>
          </div>
        </footer>
      </section>
    </div>

    <ConfirmDeleteDialog
      :model-value="!!pendingDeleteSsh"
      :title="t.deleteItem"
      :description="t.confirmDeleteItem"
      :busy="sshBusy"
      :confirm-label="t.deleteItem"
      @cancel="pendingDeleteSsh = null"
      @confirm="confirmDeleteSsh"
    />

    <ConfirmDeleteDialog
      :model-value="!!pendingDeleteWs"
      :title="t.deleteItem"
      :description="t.confirmDeleteItem"
      :busy="wsBusy"
      :confirm-label="t.deleteItem"
      @cancel="pendingDeleteWs = null"
      @confirm="confirmDeleteWs"
    />

    <div v-if="sshEditorOpen" class="modal-backdrop" @pointerdown.self="closeSshEditor">
      <section class="agent-editor-dialog" role="dialog" aria-modal="true">
        <header class="agent-editor-dialog__head">
          <h2>{{ editingNewSsh ? t.createSsh : t.openEditor }}</h2>
          <button class="icon-button" :aria-label="t.closeDialog" @click="closeSshEditor"><X :size="16" /></button>
        </header>
        <div v-if="sshDraft" class="agent-editor-dialog__body">
          <div class="form-grid">
            <label><span>{{ t.sshName }}</span><input v-model="sshDraft.name" @change="persistSshChange" /></label>
            <label><span>{{ t.sshAddress }}</span><input v-model="sshDraft.address" @change="persistSshChange" placeholder="192.168.1.10" /></label>
            <label><span>{{ t.sshPort }}</span><input v-model.number="sshDraft.port" type="number" min="1" max="65535" step="1" @change="persistSshChange" placeholder="22" /></label>
            <label><span>{{ t.sshUsername }}</span><input v-model="sshDraft.username" autocomplete="username" @change="persistSshChange" placeholder="root" /></label>
            <label><span>{{ t.sshPassword }}</span><input v-model="sshDraft.password" type="password" autocomplete="current-password" @change="persistSshChange" /></label>
            <label><span>{{ t.sshRemark }}</span><input v-model="sshDraft.remark" @change="persistSshChange" /></label>
          </div>
          <p class="ssh-auth-hint"><KeyRound :size="14" />{{ t.sshPasswordModeHint }}</p>
        </div>
        <footer class="agent-editor-dialog__footer">
          <div class="agent-draft-actions">
            <button v-if="editingNewSsh" class="secondary-button" :disabled="sshBusy" @click="closeSshEditor">{{ t.cancel }}</button>
            <button v-if="editingNewSsh" class="primary-button" :disabled="sshBusy" @click="saveNewSsh">
              <RefreshCw v-if="sshBusy" class="spin" :size="14" /><Check v-else :size="14" />{{ sshBusy ? t.savingItem : t.saveSsh }}
            </button>
            <button v-else class="primary-button" @click="closeSshEditor">{{ t.closeDialog }}</button>
          </div>
        </footer>
      </section>
    </div>

    <div v-if="wsEditorOpen" class="modal-backdrop" @pointerdown.self="closeWsEditor">
      <section class="agent-editor-dialog workspace-editor-dialog" role="dialog" aria-modal="true">
        <header class="agent-editor-dialog__head">
          <h2>{{ editingNewWs ? t.createWs : t.openEditor }}</h2>
          <button class="icon-button" :aria-label="t.closeDialog" @click="closeWsEditor"><X :size="16" /></button>
        </header>
        <div v-if="wsDraft" class="agent-editor-dialog__body">
          <div class="form-grid">
            <label><span>{{ t.wsName }}</span><input v-model="wsDraft.name" @change="persistWsChange" /></label>
            <label><span>{{ t.wsDescription }}</span><input v-model="wsDraft.description" @change="persistWsChange" /></label>
            <label class="workspace-editor-field workspace-editor-field--local">
              <span><b>1</b>{{ t.wsLocalDirectory }}</span>
              <div><input v-model="wsDraft.path" :placeholder="t.chooseWorkspace" @change="handleWsPathChange" @paste="handleWsPathChange" /><button class="secondary-button" @click="pickWorkspacePath"><Folder :size="14" />{{ t.choose }}</button></div>
            </label>
            <label class="workspace-editor-field">
              <span><b>2</b>{{ t.wsSshConfigOptional }}</span>
              <select v-model="wsDraft.remotes[0].sshConfigId" @change="handleWorkspaceSshChange">
                <option value="">{{ config.sshConfigs.length ? t.envNoSsh : t.wsCreateSshFirst }}</option>
                <option v-for="ssh in config.sshConfigs" :key="ssh.id" :value="ssh.id">{{ ssh.name }}</option>
              </select>
            </label>
            <label v-if="wsDraft.remotes[0].sshConfigId" class="workspace-editor-field">
              <span><b>3</b>{{ t.wsRemoteDirectory }}</span>
              <input v-model="wsDraft.remotes[0].remotePath" :placeholder="t.wsRemotePlaceholder" @change="persistWsChange" />
            </label>
          </div>
          <p class="workspace-editor-hint"><KeyRound :size="14" />{{ t.workspaceRule }}</p>
        </div>
        <footer class="agent-editor-dialog__footer">
          <div class="agent-draft-actions">
            <button v-if="editingNewWs" class="secondary-button" :disabled="wsBusy" @click="closeWsEditor">{{ t.cancel }}</button>
            <button v-if="editingNewWs" class="primary-button" :disabled="wsBusy" @click="saveNewWs">
              <RefreshCw v-if="wsBusy" class="spin" :size="14" /><Check v-else :size="14" />{{ wsBusy ? t.savingItem : t.saveWs }}
            </button>
            <button v-else class="primary-button" @click="closeWsEditor">{{ t.closeDialog }}</button>
          </div>
        </footer>
      </section>
    </div>

    <div class="toast-stack" aria-live="polite">
      <div v-for="toast in toasts" :key="toast.id" class="toast" :class="`toast--${toast.type}`" role="status">
        <component :is="toast.type === 'success' ? Check : toast.type === 'error' ? X : Zap" :size="15" />
        <span>{{ toast.text }}</span>
      </div>
    </div>

    <div v-if="showFigmaConfig" class="modal-backdrop" @pointerdown.self="showFigmaConfig = false">
      <section class="agent-editor-dialog" role="dialog" aria-modal="true">
        <header class="agent-editor-dialog__head">
          <h2>{{ t.figma }} · {{ t.figmaAuthorizations }}</h2>
          <button class="icon-button" :aria-label="t.closeDialog" @click="showFigmaConfig = false"><X :size="16" /></button>
        </header>
        <div class="agent-editor-dialog__body">
          <div class="figma-auth-guide">
            <strong>{{ t.figmaPatGuideTitle }}</strong>
            <ol>
              <li>{{ t.figmaPatGuideOpen }}</li>
              <li>{{ t.figmaPatGuideScopes }}</li>
              <li>{{ t.figmaPatGuidePaste }}</li>
            </ol>
          </div>
          <div class="section-heading"><span>{{ t.figmaRunHint }}</span><button class="secondary-button compact" type="button" @click="addFigmaAuthorization"><Plus :size="13" />{{ t.figmaAddAuthorization }}</button></div>
          <p v-if="!figmaAuthorizationsDraft.length" class="figma-empty">{{ t.figmaNoAuthorizations }}</p>
          <div v-for="authorization in figmaAuthorizationsDraft" :key="authorization.id" class="figma-authorization">
            <label class="figma-active">
              <input v-model="figmaActiveAuthorizationIdDraft" type="radio" name="active-figma-authorization" :value="authorization.id" />
              <span>{{ t.figmaActiveAuthorization }}</span>
            </label>
            <div class="form-grid figma-authorization__fields">
              <label><span>{{ t.figmaAuthorizationName }}</span><input v-model="authorization.name" :placeholder="t.figmaAuthorizationNamePlaceholder" /></label>
              <label><span>{{ t.figmaAuthorizationType }}</span><select v-model="authorization.tokenType"><option value="pat">{{ t.figmaPatRecommended }}</option><option value="oauth">{{ t.figmaExistingOAuthToken }}</option></select></label>
              <label class="figma-token"><span>{{ t.figmaToken }}</span><input v-model="authorization.token" type="password" :placeholder="t.figmaTokenPlaceholder" /></label>
              <button class="danger-button figma-remove" type="button" @click="removeFigmaAuthorization(authorization.id)"><Trash2 :size="14" />{{ t.remove }}</button>
            </div>
          </div>
        </div>
        <footer class="agent-editor-dialog__footer">
          <a class="secondary-button" href="https://www.figma.com/settings" target="_blank" rel="noreferrer"><KeyRound :size="14" />{{ t.figmaOpenAuthorization }}</a>
          <button class="primary-button" :disabled="extensionBusy === 'figma-config'" @click="persistFigma"><RefreshCw v-if="extensionBusy === 'figma-config'" class="spin" :size="14" />{{ extensionBusy === 'figma-config' ? t.figmaValidating : t.figmaAuthorize }}</button>
        </footer>
      </section>
    </div>

    <ConfirmDeleteDialog
      :model-value="!!pendingExtensionDelete"
      :title="extensionDeleteTitle"
      :description="extensionDeleteConfirm"
      :busy="extensionDeleteBusy"
      :confirm-label="t.delete"
      :confirm-busy-label="t.deletingExtension"
      @cancel="pendingExtensionDelete = null"
      @confirm="confirmDeleteExtension"
    />

  </div>
</template>
