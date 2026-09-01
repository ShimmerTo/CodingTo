import { reactive, ref } from 'vue'

import { getDBAuditLogs, testDBConnection } from '../../backend.js'
import { localizeError, safeClone, withTimeout } from '../../utils/appHelpers.js'

const DB_TEST_TIMEOUT_MS = 30000

// Owns database connection drafts, persistence, testing, and workspace assignments.
export function useDatabaseConnections({
  config,
  getWorkspaceDraft,
  persist,
  persistWorkspaceChange,
  pushToast,
  t
}) {
  const dbDraft = ref(null)
  const editingNewDb = ref(false)
  const newDbId = ref('')
  const dbEditorOpen = ref(false)
  const pendingDeleteDb = ref(null)
  const dbBusy = ref(false)
  const dbTestStates = reactive({})
  const dbAuditRows = ref([])
  const dbAuditLoading = ref(false)

  function defaultDbConnection() {
    return {
      id: `db-${crypto.randomUUID().slice(0, 8)}`,
      name: `Database ${config.extensions.db.connections.length + 1}`,
      kind: 'mysql',
      host: '', port: 3306, database: '', path: '', username: '', password: '', sslMode: '', sshConfigId: '',
      policy: { preset: 'safe', overrides: [] },
      queryTimeoutSeconds: 0, maxRows: 0
    }
  }

  function normalizeDbConnection(connection) {
    connection.id ||= `db-${crypto.randomUUID().slice(0, 8)}`
    connection.name ||= ''
    connection.kind ||= 'mysql'
    connection.host ||= ''
    connection.port = Number(connection.port) || (connection.kind === 'postgres' ? 5432 : 3306)
    connection.database ||= ''
    connection.path ||= ''
    connection.username ||= ''
    connection.password ||= ''
    connection.sslMode ||= ''
    connection.sshConfigId ||= ''
    connection.policy ||= { preset: 'safe', overrides: [] }
    connection.policy.preset ||= 'safe'
    connection.policy.overrides ||= []
    return connection
  }

  async function loadDbAudit(connectionId) {
    dbAuditLoading.value = true
    try {
      dbAuditRows.value = (await getDBAuditLogs(connectionId, 20)) || []
    } catch {
      dbAuditRows.value = []
    } finally {
      dbAuditLoading.value = false
    }
  }

  function openDbEditor(connection) {
    dbDraft.value = connection ? safeClone(normalizeDbConnection(connection)) : defaultDbConnection()
    editingNewDb.value = !connection
    newDbId.value = connection ? '' : dbDraft.value.id
    dbAuditRows.value = []
    dbEditorOpen.value = true
    if (connection) void loadDbAudit(connection.id)
  }

  function closeDbEditor() {
    if (editingNewDb.value) {
      dbDraft.value = null
      newDbId.value = ''
      editingNewDb.value = false
    }
    dbEditorOpen.value = false
  }

  function persistDbChange() {
    if (newDbId.value) return
    if (dbDraft.value) {
      const connections = config.extensions.db.connections
      const index = connections.findIndex(connection => connection.id === dbDraft.value.id)
      if (index >= 0) connections[index] = safeClone(dbDraft.value)
    }
    void persist()
  }

  async function saveNewDb() {
    const connection = dbDraft.value
    if (!connection || dbBusy.value) return
    if (!connection.name.trim()) { pushToast('error', t.value.dbNameRequired); return }
    if (connection.kind === 'sqlite') {
      if (!connection.path.trim()) { pushToast('error', t.value.dbPathRequired); return }
    } else {
      if (!connection.host.trim()) { pushToast('error', t.value.dbHostRequired); return }
      const port = Number(connection.port)
      if (!Number.isInteger(port) || port < 1 || port > 65535) { pushToast('error', t.value.dbPortRequired); return }
      connection.port = port
    }

    dbBusy.value = true
    config.extensions.db.connections.push(connection)
    const ok = await persist()
    if (ok) {
      newDbId.value = ''
      dbDraft.value = null
      editingNewDb.value = false
      dbEditorOpen.value = false
      pushToast('success', t.value.dbCreated)
    } else {
      config.extensions.db.connections = config.extensions.db.connections.filter(item => item.id !== connection.id)
      pushToast('error', t.value.dbCreateFailed)
    }
    dbBusy.value = false
  }

  function requestDeleteDb(connection) {
    if (!dbBusy.value) pendingDeleteDb.value = connection
  }

  async function confirmDeleteDb() {
    const requested = pendingDeleteDb.value
    if (!requested || dbBusy.value) return
    const connections = config.extensions.db.connections
    const index = connections.findIndex(connection => connection.id === requested.id)
    if (index < 0) {
      pendingDeleteDb.value = null
      return
    }

    dbBusy.value = true
    const [connection] = connections.splice(index, 1)
    const previousChecks = config.environments.map(workspace => [...(workspace.dbConnections || [])])
    const workspaceDraft = getWorkspaceDraft()
    const previousDraftChecks = workspaceDraft ? [...(workspaceDraft.dbConnections || [])] : null
    for (const workspace of config.environments) {
      workspace.dbConnections = (workspace.dbConnections || []).filter(id => id !== connection.id)
    }
    if (workspaceDraft) {
      workspaceDraft.dbConnections = (workspaceDraft.dbConnections || []).filter(id => id !== connection.id)
    }

    const ok = await persist()
    if (ok) {
      pendingDeleteDb.value = null
      pushToast('success', t.value.dbDeleted)
    } else {
      connections.splice(index, 0, connection)
      config.environments.forEach((workspace, workspaceIndex) => {
        workspace.dbConnections = previousChecks[workspaceIndex]
      })
      if (workspaceDraft && previousDraftChecks) workspaceDraft.dbConnections = previousDraftChecks
      pushToast('error', t.value.dbCreateFailed)
    }
    dbBusy.value = false
  }

  async function testDb(connection) {
    if (!connection) return
    if (!dbTestStates[connection.id]) dbTestStates[connection.id] = { busy: false, ok: null, message: '' }
    const state = dbTestStates[connection.id]
    if (state.busy) return
    state.busy = true
    state.message = ''
    try {
      const result = await withTimeout(testDBConnection(safeClone(connection)), DB_TEST_TIMEOUT_MS, t.value.dbTestTimeout)
      state.ok = !!result?.ok
      state.message = String(result?.message || (result?.ok ? t.value.dbTestPassed : t.value.dbTestFailed))
    } catch (error) {
      state.ok = false
      state.message = localizeError(String(error))
    } finally {
      state.busy = false
    }
  }

  function toggleWorkspaceDb(connectionId, checked) {
    const workspaceDraft = getWorkspaceDraft()
    if (!workspaceDraft) return
    const selected = new Set(workspaceDraft.dbConnections || [])
    if (checked) selected.add(connectionId)
    else selected.delete(connectionId)
    workspaceDraft.dbConnections = [...selected]
    persistWorkspaceChange()
  }

  function workspaceDbConnections(workspace) {
    const connections = config.extensions?.db?.connections || []
    return (workspace?.dbConnections || [])
      .map(id => connections.find(connection => connection.id === id))
      .filter(Boolean)
  }

  return {
    closeDbEditor,
    confirmDeleteDb,
    normalizeDbConnection,
    dbAuditLoading,
    dbAuditRows,
    dbBusy,
    dbDraft,
    dbEditorOpen,
    dbTestStates,
    editingNewDb,
    loadDbAudit,
    newDbId,
    openDbEditor,
    pendingDeleteDb,
    persistDbChange,
    requestDeleteDb,
    saveNewDb,
    testDb,
    toggleWorkspaceDb,
    workspaceDbConnections
  }
}
