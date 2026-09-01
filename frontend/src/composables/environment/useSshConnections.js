import { reactive, ref } from 'vue'

import { chooseSSHKeyFile, testSSHConnection } from '../../backend.js'
import { localizeError, safeClone, withTimeout } from '../../utils/appHelpers.js'
import { reconcileSshConfigResult } from '../../utils/sshConfig.js'

const SSH_TEST_TIMEOUT_MS = 30000

// Owns SSH connection drafts, persistence, testing, deletion, and masked save reconciliation.
export function useSshConnections({ config, getWorkspaceDraft, normalizeWorkspace, persist, pushToast, t }) {
  const sshDraft = ref(null)
  const editingNewSsh = ref(false)
  const newSshId = ref('')
  const sshEditorOpen = ref(false)
  const pendingDeleteSsh = ref(null)
  const sshBusy = ref(false)
  const sshTestStates = reactive({})
  let sshEditRevision = 0

  function defaultSsh() {
    return {
      id: `ssh-${crypto.randomUUID().slice(0, 8)}`,
      name: `SSH ${config.sshConfigs.length + 1}`,
      address: '', port: 22, username: '', authMode: 'password', password: '',
      privateKey: '', privateKeyPassphrase: '', hostKeyFingerprint: '', remark: '',
      policy: { preset: 'safe', overrides: [] }, customCapabilities: []
    }
  }

  function normalizeSsh(ssh) {
    ssh.id ||= `ssh-${crypto.randomUUID().slice(0, 8)}`
    ssh.name ||= ''
    ssh.address ||= ''
    ssh.port = Number(ssh.port) || 22
    ssh.username ||= ''
    ssh.authMode = ssh.authMode === 'key' ? 'key' : 'password'
    ssh.password ||= ''
    ssh.privateKey ||= ''
    ssh.privateKeyPassphrase ||= ''
    ssh.hostKeyFingerprint ||= ''
    ssh.remark ||= ''
    ssh.policy ||= { preset: 'safe', overrides: [] }
    ssh.policy.preset ||= 'safe'
    ssh.policy.overrides ||= []
    ssh.customCapabilities ||= []
    return ssh
  }

  function openSshEditor(ssh) {
    sshDraft.value = ssh ? normalizeSsh(ssh) : defaultSsh()
    sshEditRevision = 0
    editingNewSsh.value = !ssh
    newSshId.value = ssh ? '' : sshDraft.value.id
    sshEditorOpen.value = true
  }

  function closeSshEditor() {
    if (editingNewSsh.value) {
      newSshId.value = ''
      editingNewSsh.value = false
    }
    sshEditRevision++
    sshDraft.value = null
    sshEditorOpen.value = false
  }

  function persistSshChange() {
    sshEditRevision++
    if (!newSshId.value) void persist()
  }

  async function saveNewSsh() {
    const ssh = sshDraft.value
    if (!ssh || sshBusy.value) return
    if (!ssh.address.trim()) { pushToast('error', t.value.sshAddressRequired); return }
    if (!Number.isInteger(Number(ssh.port)) || Number(ssh.port) < 1 || Number(ssh.port) > 65535) {
      pushToast('error', t.value.sshPortRequired)
      return
    }
    ssh.port = Number(ssh.port)
    if (!ssh.username.trim()) { pushToast('error', t.value.sshUsernameRequired); return }
    if (ssh.authMode === 'key') {
      if (!ssh.privateKey.trim()) { pushToast('error', t.value.sshPrivateKeyRequired); return }
    } else if (!ssh.password) {
      pushToast('error', t.value.sshPasswordRequired)
      return
    }

    sshBusy.value = true
    config.sshConfigs.push(ssh)
    const ok = await persist()
    if (ok) {
      newSshId.value = ''
      sshDraft.value = null
      editingNewSsh.value = false
      sshEditorOpen.value = false
      pushToast('success', t.value.sshCreated)
    } else {
      config.sshConfigs = config.sshConfigs.filter(item => item.id !== ssh.id)
      pushToast('error', t.value.sshCreateFailed)
    }
    sshBusy.value = false
  }

  function requestDeleteSsh(ssh) {
    if (!sshBusy.value) pendingDeleteSsh.value = ssh
  }

  async function confirmDeleteSsh() {
    const requested = pendingDeleteSsh.value
    if (!requested || sshBusy.value) return
    const index = config.sshConfigs.findIndex(ssh => ssh.id === requested.id)
    if (index < 0) {
      pendingDeleteSsh.value = null
      return
    }

    sshBusy.value = true
    const [ssh] = config.sshConfigs.splice(index, 1)
    const previousRemotes = config.environments.map(workspace => safeClone(workspace.remotes || []))
    const workspaceDraft = getWorkspaceDraft()
    const previousDraftRemotes = workspaceDraft ? safeClone(workspaceDraft.remotes || []) : null
    for (const workspace of config.environments) {
      workspace.remotes = (workspace.remotes || []).filter(remote => remote.sshConfigId !== ssh.id)
      normalizeWorkspace(workspace)
    }
    if (workspaceDraft) {
      workspaceDraft.remotes = (workspaceDraft.remotes || []).filter(remote => remote.sshConfigId !== ssh.id)
      normalizeWorkspace(workspaceDraft)
    }

    const ok = await persist()
    if (ok) {
      pendingDeleteSsh.value = null
      pushToast('success', t.value.sshDeleted)
    } else {
      config.sshConfigs.splice(index, 0, ssh)
      config.environments.forEach((workspace, workspaceIndex) => {
        workspace.remotes = previousRemotes[workspaceIndex]
      })
      if (workspaceDraft && previousDraftRemotes) workspaceDraft.remotes = previousDraftRemotes
      pushToast('error', t.value.sshCreateFailed)
    }
    sshBusy.value = false
  }

  async function testSsh(ssh) {
    if (!ssh) return
    if (!sshTestStates[ssh.id]) sshTestStates[ssh.id] = { busy: false, ok: null, message: '' }
    const state = sshTestStates[ssh.id]
    if (state.busy) return
    state.busy = true
    state.message = ''
    try {
      const result = await withTimeout(testSSHConnection(safeClone(ssh)), SSH_TEST_TIMEOUT_MS, t.value.sshTestTimeout)
      state.ok = !!result?.ok
      state.message = String(result?.message || (result?.ok ? t.value.sshTestPassed : t.value.sshTestFailed))
    } catch (error) {
      state.ok = false
      state.message = localizeError(String(error))
    } finally {
      state.busy = false
    }
  }

  async function pickSshKeyFile() {
    if (!sshDraft.value || sshBusy.value) return
    try {
      const result = await chooseSSHKeyFile()
      if (!result || !result.content) return
      sshDraft.value.privateKey = result.content
      persistSshChange()
      pushToast('success', t.value.sshKeyFileLoaded)
    } catch (error) {
      pushToast('error', localizeError(String(error)))
    }
  }

  function captureSshSaveState() {
    return { activeDraft: sshEditorOpen.value ? sshDraft.value : null, revision: sshEditRevision }
  }

  function reconcileSshSaveResult(serverConfigs, saveState) {
    const currentDraft = sshEditorOpen.value ? sshDraft.value : null
    return reconcileSshConfigResult(
      serverConfigs,
      currentDraft,
      saveState.activeDraft,
      saveState.revision,
      sshEditRevision,
      normalizeSsh
    )
  }

  return {
    captureSshSaveState,
    closeSshEditor,
    confirmDeleteSsh,
    editingNewSsh,
    newSshId,
    normalizeSsh,
    openSshEditor,
    pendingDeleteSsh,
    persistSshChange,
    pickSshKeyFile,
    reconcileSshSaveResult,
    requestDeleteSsh,
    saveNewSsh,
    sshBusy,
    sshDraft,
    sshEditorOpen,
    sshTestStates,
    testSsh
  }
}
