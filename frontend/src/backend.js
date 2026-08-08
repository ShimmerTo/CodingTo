import { Events, Call, Browser } from '@wailsio/runtime'
import { App } from '../bindings/codingto/internal/app'
import { localFileURL } from './localFileUrl.js'
import { DEFAULT_STEWARD_PERSONA } from './stewardState.js'

const fallback = {
  config: {
    configVersion: 5,
    preferences: { theme: 'system', language: 'zh-CN', accentColor: '#d9a441', chatLayout: 'left', showIdentity: true, diffMode: 'unified' },
    defaultProvider: 'openai',
    defaultModel: 'gpt-5.6-terra',
    lastEnvironment: '',
    sessionDir: 'C:\\Users\\asus\\.codingto\\sessions',
    providers: [
      {
        name: 'openai',
        label: 'OpenAI',
        vendor: 'openai',
        api: 'openai-responses',
        baseUrl: 'https://api.openai.com/v1',
        apiKey: '$OPENAI_API_KEY',
        enabled: true,
        models: [{ id: 'gpt-5.6-terra', name: 'GPT-5.6 Terra', contextWindow: 272000, maxTokens: 128000, reasoning: true, defaultThinkingLevel: 'medium', input: ['text', 'image'], capabilities: { toolCall: true } }]
      },
      {
        name: 'anthropic',
        label: 'Anthropic',
        vendor: 'anthropic',
        api: 'anthropic-messages',
        baseUrl: 'https://api.anthropic.com',
        apiKey: '$ANTHROPIC_API_KEY',
        enabled: true,
        models: [{ id: 'claude-opus-4-8', name: 'Claude Opus 4.8', contextWindow: 1000000, maxTokens: 128000, reasoning: true, defaultThinkingLevel: 'medium', input: ['text', 'image'], capabilities: { toolCall: true } }]
      }
    ],
    extensions: {
      figma: { enabled: false, activeAuthorizationId: '', authorizations: [] }
    },
    activeAgentId: 'default',
    agents: [{ id: 'default', name: 'Default Agent', description: 'General-purpose coding agent', dataDir: '', avatar: '', builtin: { 'browser-profile': true, document: true, plan: true, 'skills-list': true, subagent: true }, recommended: {}, subagents: [], piTools: { read: true, bash: true, edit: true, write: true }, defaultProvider: 'openai', defaultModel: 'gpt-5.6-terra', browserProfilePolicy: { existingProfileMode: 'headless', interactiveLoginMode: 'headed', authenticatedTaskMode: 'headless' } }],
    environments: [],
    activeEnvId: '',
    sshConfigs: [],
    userProfile: { name: '', avatar: '' }
  },
  providerPresets: [],
  os: 'browser',
  piInstalled: false,
  piPath: '',
  configDir: '',
  version: '0.1.0'
}

function isWails() {
  if (typeof window === 'undefined') return false
  return Boolean(
    window._wails?.environment?.OS ||
    window.chrome?.webview?.postMessage ||
    window.webkit?.messageHandlers?.external?.postMessage ||
    window.wails?.invoke
  )
}

// The fallback backend used outside Wails (plain browser dev) has no real storage
// like the SQLite-backed Wails runtime, so config changes would be lost on every
// page refresh. Persist the config to localStorage so dev refreshes keep user
// changes (avatar, nickname, providers, agents, ...).
const DEV_CONFIG_KEY = 'codingto.dev.config.v1'

function loadDevConfig() {
  try {
    if (typeof localStorage === 'undefined') return
    const raw = localStorage.getItem(DEV_CONFIG_KEY)
    if (!raw) return
    const saved = JSON.parse(raw)
    if (saved && typeof saved === 'object') {
      fallback.config = { ...structuredClone(fallback.config), ...saved }
    }
  } catch (_) {
    // ignore corrupt or unavailable storage
  }
}

function saveDevConfig() {
  try {
    if (typeof localStorage === 'undefined') return
    localStorage.setItem(DEV_CONFIG_KEY, JSON.stringify(fallback.config))
  } catch (_) {
    // ignore quota or serialization errors
  }
}

loadDevConfig()

export async function getBootstrap() {
  return isWails() ? App.GetBootstrap() : structuredClone(fallback)
}

export async function installPi() {
  if (isWails()) return App.InstallPi()
  fallback.piInstalled = true
  fallback.piPath = 'pi'
  if (!fallback.config.agents.length) {
    fallback.config.agents.push({ id: 'default', name: 'Default Agent', description: 'General-purpose coding agent', dataDir: '', avatar: '', builtin: { 'browser-profile': true, document: true, plan: true, 'skills-list': true, subagent: true }, recommended: {}, subagents: [], piTools: { read: true, bash: true, edit: true, write: true }, defaultProvider: 'openai', defaultModel: 'gpt-5.6-terra', browserProfilePolicy: { existingProfileMode: 'headless', interactiveLoginMode: 'headed', authenticatedTaskMode: 'headless' } })
    fallback.config.activeAgentId = 'default'
  }
  if (!fallback.config.environments) fallback.config.environments = []
  if (!fallback.config.sshConfigs) fallback.config.sshConfigs = []
  saveDevConfig()
  return structuredClone(fallback)
}

export async function saveConfig(config) {
  if (isWails()) return App.SaveConfig(config)
  const next = structuredClone(config)
  for (const agent of next.agents || []) {
    if (agent.id !== 'default' && !String(agent.dataDir || '').trim()) {
      agent.dataDir = `agent_${crypto.randomUUID().replaceAll('-', '')}`
    }
  }
  // 与后端 SaveConfig 语义一致：提交内容即权威，缺失的 agent 不从旧配置补回
  //（删除只能走 deleteAgent），避免浏览器 dev 模式下被删 agent 被静默复活。
  fallback.config = next
  saveDevConfig()
  return structuredClone(fallback.config)
}

const mockBrowserProfiles = new Map()

// Save a persistent browser session through CodingTo's direct backend channel. The
// password is never sent as an extension_ui_response and therefore never
// enters the Pi/LLM transcript.
export async function saveBrowserProfile(request) {
  if (isWails()) return App.SaveBrowserProfile(request)
  const key = String(request.key || request.name || '').trim()
  if (!/^[A-Za-z0-9](?:[A-Za-z0-9._-]{0,62}[A-Za-z0-9])?$/.test(key) || /^(con|prn|aux|nul|com[1-9]|lpt[1-9])(?:\..*)?$/i.test(key)) {
    throw new Error('Browser Profile Key 必须为 1 到 64 位，只能包含字母、数字、点、下划线或连字符，并且必须以字母或数字开头和结尾')
  }
  const id = request.profileId || key
  if (!request.profileId && [...mockBrowserProfiles.keys()].some(item => item.toLowerCase() === key.toLowerCase())) {
    throw new Error(`Browser Profile Key "${key}" 已存在，请使用其他 Key`)
  }
  const profile = {
    id,
    name: key,
    origins: [new URL(request.targetUrl).origin, new URL(request.loginUrl || request.targetUrl).origin],
    browserState: { kind: 'persistent-profile', path: 'user-data' },
    credentialRef: request.authMode === 'saved-credential' ? id : '',
    credentialConfigured: request.authMode === 'saved-credential',
    loginRecipe: {
      loginUrl: request.loginUrl || request.targetUrl,
      usernameSelector: request.usernameSelector || '',
      passwordSelector: request.passwordSelector || '',
      submitSelector: request.submitSelector || ''
    },
    createdAt: new Date().toISOString(),
    updatedAt: new Date().toISOString()
  }
  mockBrowserProfiles.set(id, profile)
  return structuredClone(profile)
}

export async function listBrowserProfiles(targetUrl = '') {
  if (isWails()) return App.ListBrowserProfiles(targetUrl)
  return [...mockBrowserProfiles.entries()]
    .filter(([, profile]) => !targetUrl || profile.origins.includes(new URL(targetUrl).origin))
    .map(([, profile]) => structuredClone(profile))
}

export async function deleteBrowserProfile(profileId) {
  if (isWails()) return App.DeleteBrowserProfile(profileId)
  mockBrowserProfiles.delete(profileId)
}

export async function renameBrowserProfile(profileId, newName) {
  if (isWails()) return App.RenameBrowserProfile(profileId, newName)
  const profile = mockBrowserProfiles.get(profileId)
  if (!profile) throw new Error('Browser Profile 不存在')
  const trimmed = String(newName || '').trim()
  if (!trimmed) throw new Error('名称不能为空')
  profile.name = trimmed
  profile.updatedAt = new Date().toISOString()
  return structuredClone(profile)
}

export async function deleteAgent(id) {
  if (isWails()) return App.DeleteAgent(id)
  if (fallback.config.activeAgentId === id) throw new Error('the default agent cannot be deleted')
  const index = fallback.config.agents.findIndex(agent => agent.id === id)
  if (index < 0) throw new Error(`agent not found: ${id}`)
  fallback.config.agents.splice(index, 1)
  if (fallback.config.activeAgentId === id) fallback.config.activeAgentId = fallback.config.agents[0]?.id || ''
  saveDevConfig()
  return structuredClone(fallback.config)
}

export async function chooseWorkspace() {
  return isWails() ? App.ChooseWorkspace() : ''
}

export async function chooseSessionDir() {
  return isWails() ? App.ChooseSessionDir() : ''
}

export async function startPrompt(request) {
  if (isWails()) return App.StartPrompt(request)
  const sessionId = request.sessionId
  clearMockPromptTimers(sessionId)
  const emitSessionEvent = payload => emitMock('agent:event', { ...payload, codingToSessionId: sessionId })
  emitMock('agent:state', { running: true, processRunning: true, codingToSessionId: sessionId })
  if (request.mode === 'plan') {
    // 模拟 plan 扩展链路（与 internal/piagent/default_tools/plan/index.ts 一致）：
    // 先 setWidget('plan-todos') 写入完整步骤，再弹确认框。如需手动验证乱序
    // 竞态修复（confirm 先到、setWidget 后到），把下面两个延时互换即可。
    scheduleMockPromptEvent(sessionId, () => emitSessionEvent({
      type: 'extension_ui_request', id: 'repro-plan-todos', method: 'setWidget',
      widgetKey: 'plan-todos', widgetPlacement: 'aboveEditor',
      widgetLines: ['☐ 步骤一：检查现状', '☐ 步骤二：实施改动', '☐ 步骤三：验证', '☐ 步骤四：运行测试', '☐ 步骤五：更新文档', '☐ 步骤六：归档总结'],
    }), 200)
    scheduleMockPromptEvent(sessionId, () => emitSessionEvent({
      type: 'extension_ui_request', id: 'repro-plan-confirm', method: 'confirm',
      title: '__CODINGTO_PLAN_CONFIRM__:确认执行以上计划？',
      message: '计划：复现测试 共 6 步，请于底部计划面板核对后确认。',
    }), 400)
    return
  }
  const text = request.mode === 'plan'
    ? 'Plan:\n1. Inspect the current implementation\n2. Apply the required changes\n3. Run verification'
    : 'Browser preview is ready. Connect the Wails backend to run the agent.'
  const assistant = { role: 'assistant', content: [{ type: 'text', text }], usage: { input: 1240, output: 312, cacheRead: 860, cacheWrite: 0 } }
  scheduleMockPromptEvent(sessionId, () => emitSessionEvent({ type: 'message_update', message: assistant, assistantMessageEvent: { type: 'text_delta', delta: text, partial: assistant } }), 400)
  scheduleMockPromptEvent(sessionId, () => emitSessionEvent({ type: 'turn_end', message: assistant }), 700)
  scheduleMockPromptEvent(sessionId, () => emitSessionEvent({ type: 'agent_end', messages: [assistant] }), 760)
  scheduleMockPromptEvent(sessionId, () => emitSessionEvent({ type: 'agent_settled' }), 780)
  scheduleMockPromptEvent(sessionId, () => emitMock('agent:state', {
    running: false,
    processRunning: true,
    codingToSessionId: sessionId
  }), 790)
  if (request.mode === 'plan') {
    scheduleMockPromptEvent(sessionId, () => emitSessionEvent({
      type: 'extension_ui_request',
      id: 'preview-plan-choice',
      method: 'select',
      title: 'Plan mode - what next?',
      options: ['Execute the plan (track progress)', 'Stay in plan mode', 'Refine the plan']
    }), 800)
  }
}

export async function sendAgentCommand(sessionId, command) {
  if (isWails()) return App.StartPrompt({ sessionId, command })
  const emitSessionEvent = payload => emitMock('agent:event', { ...payload, codingToSessionId: sessionId })
  if (command.type === 'get_session_stats') {
    setTimeout(() => emitSessionEvent({
      id: command.id,
      type: 'response',
      command: 'get_session_stats',
      success: true,
      data: {
        tokens: { input: 1240, output: 312, cacheRead: 860, cacheWrite: 0, total: 2412 },
        contextUsage: { tokens: 1552, contextWindow: 272000, percent: 1 }
      }
    }), 40)
  } else if (command.type === 'compact') {
    setTimeout(() => emitSessionEvent({ type: 'compaction_start', reason: 'manual' }), 40)
    setTimeout(() => emitSessionEvent({
      type: 'compaction_end',
      reason: 'manual',
      result: { tokensBefore: 1552, estimatedTokensAfter: 420 },
      aborted: false
    }), 180)
  } else if (command.type === 'extension_ui_response' && String(command.value || '').startsWith('Execute')) {
    setTimeout(() => emitSessionEvent({
      type: 'extension_ui_request',
      id: 'preview-plan-widget',
      method: 'setWidget',
      widgetKey: 'plan-todos',
      widgetLines: ['☑ Inspect the current implementation', '☐ Apply the required changes', '☐ Run verification'],
      widgetPlacement: 'aboveEditor'
    }), 40)
    setTimeout(() => emitSessionEvent({ type: 'agent_end', messages: [] }), 120)
  }
}

export async function abortPrompt(sessionId) {
  if (isWails()) {
    return App.StartPrompt({
      sessionId,
      command: { id: 'codingto-abort', type: 'abort' }
    })
  }
  clearMockPromptTimers(sessionId)
  emitMock('agent:event', { type: 'agent_settled', codingToSessionId: sessionId })
  emitMock('agent:state', { running: false, codingToSessionId: sessionId })
}

export async function restartAgent() {
  if (isWails()) return App.RestartAgent()
}

export async function listSessions() {
  return isWails() ? App.ListSessions() : structuredClone(mockSessions)
}

export async function createSession(request) {
  if (isWails()) return App.CreateSession(request)
  const now = Date.now()
  const session = {
    id: now,
    agentId: request.agentId,
    environmentId: request.environmentId,
    title: request.title || 'New session',
    sessionDir: `${fallback.config.sessionDir}\\s${now}`,
    sessionPath: '',
    provider: request.provider,
    model: request.model,
    status: 'active',
    execDurationMs: 0,
    createdAt: now,
    updatedAt: now
  }
  mockSessions.unshift(session)
  mockSessionMessages.set(session.id, [])
  return structuredClone(session)
}

export async function getSessionHistory(id) {
  if (isWails()) return App.GetSessionHistory(id)
  return {
    messages: structuredClone(mockSessionMessages.get(id) || []),
    tokenStats: { input: 1240, cached: 860, cacheWrite: 0, output: 312, total: 2412 },
    contextUsage: { tokens: 1552, contextWindow: 272000, percent: 1 },
  }
}

export async function getSessionChanges(id) {
  if (isWails()) return App.GetSessionChanges(id)
  const root = fallback.config.lastEnvironment || 'C:/workspace/codingto'
  return {
    root,
    nodes: [],
    files: [],
    added: 0,
    deleted: 0,
  }
}

export async function getSessionGitSnapshot(id, baseBranch = '') {
  if (isWails()) return App.GetSessionGitSnapshot(id, baseBranch)
  const root = fallback.config.lastEnvironment || 'C:/workspace/codingto'
  const git = {
    isRepository: true,
    root,
    worktreePath: root,
    currentBranch: 'feature/git-sidebar',
    baseBranch: 'origin/main',
    baseBranches: ['main', 'develop', 'origin/main', 'origin/develop'],
    ahead: 3,
    behind: 0,
    worktree: {
      added: 86,
      deleted: 14,
      files: [
        { path: 'frontend/src/components/chat/ChatRightSidebar.vue', status: 'modified', unstaged: true, added: 64, deleted: 8 },
        { path: 'internal/app/git_changes.go', status: 'added', staged: true, added: 22, deleted: 0 },
        { path: 'notes/sidebar.md', status: 'untracked', untracked: true, added: 0, deleted: 0 },
        { path: 'assets/sidebar-preview.png', status: 'modified', unstaged: true, added: 0, deleted: 0, binary: true },
        { path: 'bin/codingto.exe', status: 'modified', unstaged: true, added: 0, deleted: 0, binary: true },
      ],
    },
    branch: {
      added: 126,
      deleted: 31,
      files: [
        { path: 'frontend/src/App.vue', status: 'modified', added: 42, deleted: 9 },
        { path: 'internal/app/change_tracker.go', status: 'modified', added: 84, deleted: 22 },
      ],
    },
  }
  if (baseBranch && git.baseBranches.includes(baseBranch)) git.baseBranch = baseBranch
  return git
}

export async function applyGitFileOperation(sessionId, op, path) {
  if (isWails()) return App.ApplyGitFileOperation({ sessionId, op, path })
  // 浏览器 fallback：模拟成功，避免无后端时按钮报错。
  await new Promise(resolve => setTimeout(resolve, 120))
}

export async function getSessionGitFileDetail(id, scope, path, baseBranch = '') {
  if (isWails()) return App.GetSessionGitFileDetail(id, scope, path, baseBranch)
  const isImage = /\.(png|jpe?g|gif|webp)$/i.test(path)
  const isBinary = /\.(zip|exe|pdf|woff2?)$/i.test(path)
  const common = {
    path,
    scope,
    status: path.includes('new.') ? 'added' : 'modified',
    mimeType: isImage ? 'image/png' : isBinary ? 'application/octet-stream' : 'text/plain',
    added: 18,
    deleted: 7,
  }
  if (isImage) {
    return {
      ...common,
      kind: 'image',
      before: { exists: true, size: 182420, permissions: '-rw-r--r--', width: 1280, height: 720, mimeType: 'image/png' },
      after: { exists: true, size: 194832, permissions: '-rw-r--r--', width: 1280, height: 720, mimeType: 'image/png' },
      hunks: [],
    }
  }
  if (isBinary) {
    return {
      ...common,
      kind: 'binary',
      before: { exists: true, size: 182420, permissions: '-rw-r--r--', createdAt: Date.now() - 86400000, modifiedAt: Date.now() - 3600000 },
      after: { exists: true, size: 194832, permissions: '-rw-r--r--', createdAt: Date.now() - 86400000, modifiedAt: Date.now() },
      hunks: [],
    }
  }
  return {
    ...common,
    kind: 'text',
    before: { exists: true, size: 1420, permissions: '-rw-r--r--', lineCount: 48 },
    after: { exists: true, size: 1680, permissions: '-rw-r--r--', lineCount: 59 },
    hunks: [{
      header: '@@ -12,4 +12,6 @@',
      lines: [
        { kind: 'context', text: 'const activeTab = ref(\"artifacts\")', oldNumber: 12, newNumber: 12 },
        { kind: 'deleted', text: 'const baseBranch = \"main\"', oldNumber: 13 },
        { kind: 'added', text: 'const baseBranch = ref(\"origin/main\")', newNumber: 13 },
        { kind: 'added', text: 'const baseBranches = ref([])', newNumber: 14 },
      ],
    }],
  }
}

export async function deleteSession(id) {
  if (isWails()) return App.DeleteSession(id)
  const index = mockSessions.findIndex(item => item.id === id)
  if (index >= 0) mockSessions.splice(index, 1)
  mockSessionMessages.delete(id)
}

// ---- 管家（Steward）----

export async function listBotChannels() {
  return isWails() ? App.ListBotChannels() : []
}

export async function saveBotChannel(request) {
  return isWails() ? App.SaveBotChannel(request) : { ...request, id: Date.now(), status: 'disconnected' }
}

export async function deleteBotChannel(id) {
  return isWails() ? App.DeleteBotChannel(id) : null
}

export async function toggleBotChannel(id, enabled) {
  return isWails() ? App.ToggleBotChannel(id, enabled) : null
}

export async function testBotChannel(id) {
  return isWails() ? App.TestBotChannel(id) : null
}

export async function injectBotMessage(channelId, text) {
  return isWails() ? App.InjectBotMessage(channelId, text) : null
}

export async function getStewardProfile() {
  return isWails() ? App.GetStewardProfile() : {
    ...DEFAULT_STEWARD_PERSONA, residentSessionId: 0
  }
}

export async function saveStewardProfile(profile) {
  return isWails() ? App.SaveStewardProfile(profile) : profile
}

export async function listBotTasks() {
  return isWails() ? App.ListBotTasks() : []
}

export async function listStewardPermissions() {
  return isWails() ? App.ListStewardPermissions() : []
}

export async function respondStewardPermission(requestId, answer) {
  return isWails() ? App.RespondStewardPermission(requestId, answer) : null
}

export async function stewardStopSession(sessionId) {
  return isWails() ? App.StewardStopSession(sessionId) : null
}

export async function stewardDeleteSession(sessionId) {
  return isWails() ? App.StewardDeleteSession(sessionId) : null
}

const mockBuiltinCatalog = [
  { key: 'browser-profile', name: 'Browser Profile', description: 'Manage reusable authenticated browser profiles for this agent.', required: false, currentVersion: '6.0.0' },
  { key: 'document', name: 'Document', description: 'Inspect, search, create, and distribute local documents.', required: false, currentVersion: '1.0.0' },
  { key: 'plan', name: 'Plan Mode', description: 'Present and track an execution plan before making changes.', required: false, currentVersion: '1.0.1' },
  { key: 'skills-list', name: 'Skills List', description: 'List every skill available to the current isolated agent.', required: true, currentVersion: '1.0.0' },
  { key: 'subagent', name: 'Subagent', description: 'Run authorized CodingTo agents in the background and receive their results automatically.', required: false, currentVersion: '1.1.0' },
]

const mockExtensions = {
  tools: [
    { key: 'rtk', name: 'RTK', installed: false, enabled: false, version: '', installHint: 'winget install --id rtk-ai.rtk --exact' },
    { key: 'agent-browser', name: 'Agent Browser', installed: false, enabled: false, version: '', installHint: 'npm install -g agent-browser' }
  ],
  figma: { installed: false, enabled: false, running: false, pid: 0, hasToken: false, version: '' },
  globalMcp: [],
  globalPlugins: [],
  builtinCatalog: mockBuiltinCatalog,
  builtins: {},
  recommended: {},
  packages: {},
  directory: {},
  mcp: {}
}

function mockPiPluginsStatus(agentId) {
  const installed = (mockExtensions.packages[agentId] || [])
    .find(item => item.key === 'npm:@nklisch/pi-plugins' || item.name === '@nklisch/pi-plugins')
  return {
    key: 'pi-plugins',
    name: 'Pi Plugins',
    description: 'Adds marketplace discovery and plugin lifecycle management for the current agent.',
    installHint: 'pi install npm:@nklisch/pi-plugins',
    installed: !!installed?.installed,
    enabled: !!installed?.enabled,
    version: installed?.version || '',
    sourcePath: installed?.sourcePath || '',
  }
}

export async function getSubagentTranscript(sessionId, runId) {
  if (isWails()) return App.GetSubagentTranscript(sessionId, runId)
  return { messages: [], tokenStats: {}, contextUsage: {}, subagentUi: { widgets: {} }, subagent: null }
}

export async function respondSubagentUI(sessionId, runId, response) {
  if (isWails()) return App.RespondSubagentUI(sessionId, runId, response)
}

export async function ackSubagentUI(sessionId, runId, requestId) {
  if (isWails()) return App.AckSubagentUI(sessionId, runId, requestId)
}

export async function abortSubagent(sessionId, runId) {
  if (isWails()) return App.AbortSubagent(sessionId, runId)
  // Keep the browser preview contract aligned with the real backend: a
  // successful abort is a request and does not claim a terminal run status.
  return { runId, status: 'running', abortRequested: true }
}

export async function getExtensions() {
  if (isWails()) return App.GetExtensions()
  const snapshot = structuredClone(mockExtensions)
  for (const agent of fallback.config.agents || []) {
    snapshot.builtins[agent.id] = mockBuiltinCatalog.map(tool => ({
      ...tool,
      installed: !!agent.builtin?.[tool.key],
      installedVersion: agent.builtin?.[tool.key] ? tool.currentVersion : '',
    }))
    snapshot.packages[agent.id] ||= []
    snapshot.directory[agent.id] ||= []
    snapshot.mcp[agent.id] ||= []
    snapshot.recommended[agent.id] = [
      mockPiPluginsStatus(agent.id),
      { key: 'rtk', name: 'RTK', installed: !!agent.recommended?.rtk, enabled: !!agent.recommended?.rtk, version: '' },
      { key: 'browser-native', name: 'Pi Agent Browser Native', installed: false, enabled: false, version: '' },
      { key: 'figma', name: 'Pi Figma', installed: !!agent.recommended?.figma, enabled: !!agent.recommended?.figma, version: 'preview' }
    ]
  }
  return snapshot
}

export async function getAgentExtensions(agentId) {
  if (isWails()) return App.GetAgentExtensions(agentId)
  const agent = (fallback.config.agents || []).find(a => a.id === agentId)
  return {
    builtins: mockBuiltinCatalog.map(tool => ({
      ...tool,
      installed: !!agent?.builtin?.[tool.key],
      installedVersion: agent?.builtin?.[tool.key] ? tool.currentVersion : '',
    })),
    recommended: [
      mockPiPluginsStatus(agentId),
      { key: 'rtk', name: 'RTK', installed: !!agent?.recommended?.rtk, enabled: !!agent?.recommended?.rtk, version: '' },
      { key: 'browser-native', name: 'Pi Agent Browser Native', installed: false, enabled: false, version: '' },
      { key: 'figma', name: 'Pi Figma', installed: !!agent?.recommended?.figma, enabled: !!agent?.recommended?.figma, version: 'preview' }
    ],
    packages: structuredClone(mockExtensions.packages[agentId] || []),
    directory: structuredClone(mockExtensions.directory[agentId] || []),
    mcp: structuredClone(mockExtensions.mcp[agentId] || [])
  }
}

export async function listSkills() {
  if (isWails()) return App.ListSkills()
  return []
}

export async function previewSkillArchive(input) {
  if (isWails()) return App.PreviewSkillArchive(input)
  const raw = String(input?.name || 'skill.zip')
  if (!raw.toLowerCase().endsWith('.zip')) throw new Error('Skill 文件必须是 ZIP')
  return { name: raw.replace(/\.zip$/i, ''), description: 'Browser preview skill', count: 1 }
}

export async function previewSkillUrl(url) {
  if (isWails()) return App.PreviewSkillURL(url)
  if (!/^https?:\/\//i.test(String(url || '').trim())) throw new Error('Skill URL 必须使用 http:// 或 https://')
  return { name: String(url).split('/').filter(Boolean).pop() || 'remote-skill', description: 'Browser preview skill', count: 1 }
}

export async function installSkills(request) {
  if (isWails()) return App.InstallSkills(request)
  return []
}

export async function editSkill(request) {
  if (isWails()) return App.EditSkill(request)
  return []
}

export async function deleteSkill(skillId, agentId) {
  if (isWails()) return App.DeleteSkill(skillId, agentId || '')
  return []
}

export async function updateSkill(request) {
  if (isWails()) return App.UpdateSkill(request)
  return []
}

export async function manageExtension(request) {
  if (isWails()) return App.ManageExtension(request)
  if (request.key === 'figma' && request.action === 'install') {
    mockExtensions.figma.installed = true
    mockExtensions.figma.version = '0.13.2'
  }
  const tool = mockExtensions.tools.find(item => item.key === request.key)
  if (tool && request.action === 'install') {
    tool.installed = true
    tool.enabled = true
    if (tool.key === 'agent-browser') tool.version = 'agent-browser preview'
    if (tool.key === 'rtk') tool.version = 'rtk preview'
  }
  if (tool && ['enable', 'start'].includes(request.action)) tool.enabled = true
  if (tool && ['disable', 'stop'].includes(request.action)) tool.enabled = false
  return { message: 'Browser preview only', command: tool?.installHint || '', output: '' }
}

function mockGlobalPackage(packageName) {
  const name = String(packageName || '').trim()
  return {
    key: name,
    name,
    description: name,
    installed: true,
    enabled: true,
    version: 'preview',
    installHint: `npm install -g ${name}`,
    sourcePath: `preview:global:${name}`,
  }
}

// Install an npm package into one of CodingTo's shared inventories. The backend
// validates the package name before invoking npm, which keeps the generated
// command safe to display and execute without accepting arbitrary shell input.
export async function installGlobalPackage(scope, packageName) {
  if (isWails()) {
    return Call.ByName('codingto/internal/app.App.InstallGlobalPackage', { scope, package: packageName })
  }
  const target = scope === 'mcp' ? mockExtensions.globalMcp : mockExtensions.globalPlugins
  const item = mockGlobalPackage(packageName)
  const index = target.findIndex(entry => entry.key === item.key)
  if (index >= 0) target[index] = item
  else target.push(item)
  return { message: '浏览器预览模式：已模拟全局安装', command: item.installHint, output: '' }
}

// Remove an npm package from one of CodingTo's shared inventories. The backend
// runs npm uninstall -g and drops the registration, so the package is gone from
// the global scope and the UI list.
export async function removeGlobalPackage(scope, packageName) {
  if (isWails()) {
    return Call.ByName('codingto/internal/app.App.RemoveGlobalPackage', { scope, package: packageName })
  }
  const target = scope === 'mcp' ? mockExtensions.globalMcp : mockExtensions.globalPlugins
  const index = target.findIndex(entry => entry.key === packageName)
  if (index >= 0) target.splice(index, 1)
  return { message: '浏览器预览模式：已模拟移除全局插件', command: `npm uninstall -g ${packageName}`, output: '' }
}

// Install and register an MCP package for one isolated agent.
export async function installAgentMcp(agentId, packageName) {
  if (isWails()) {
    return Call.ByName('codingto/internal/app.App.InstallAgentMCP', { agentId, package: packageName })
  }
  mockExtensions.mcp[agentId] ||= []
  const item = mockGlobalPackage(packageName)
  const index = mockExtensions.mcp[agentId].findIndex(entry => entry.key === item.key)
  if (index >= 0) mockExtensions.mcp[agentId][index] = item
  else mockExtensions.mcp[agentId].push(item)
  return {
    message: '浏览器预览模式：已模拟 Agent MCP 安装',
    command: `npm install -g ${packageName}\npi install npm:pi-mcp-adapter`,
    output: '',
  }
}

// Add a manually configured MCP server to one or more agents.
export async function addManualMCP(payload) {
  if (isWails()) {
    return Call.ByName('codingto/internal/app.App.AddManualMCP', payload)
  }
  const { key, command, args, url, agentIds } = payload || {}
  const description = url || [command, ...(args || [])].filter(Boolean).join(' ')
  for (const agentId of (agentIds || [])) {
    mockExtensions.mcp[agentId] ||= []
    const item = { key, name: key, description, installed: true, enabled: true, version: 'manual' }
    const index = mockExtensions.mcp[agentId].findIndex(entry => entry.key === key)
    if (index >= 0) mockExtensions.mcp[agentId][index] = item
    else mockExtensions.mcp[agentId].push(item)
  }
  return { message: `浏览器预览模式：已模拟手动添加 MCP "${key}"`, command: '', output: '' }
}

// Remove one MCP server entry from a single agent's mcp.json.
export async function removeAgentMcpServer(agentId, key) {
  if (isWails()) {
    return Call.ByName('codingto/internal/app.App.RemoveAgentMCPServer', { agentId, key })
  }
  const list = mockExtensions.mcp[agentId] || []
  const index = list.findIndex(entry => entry.key === key)
  if (index >= 0) list.splice(index, 1)
  return { message: `浏览器预览模式：已模拟移除 MCP "${key}"`, command: '', output: '' }
}

// Install a Pi extension into the current agent's data directory by running a
// full install command (for example `pi install npm:pi-agent-browser-native`).
export async function installAgentExtension(agentId, command) {
  if (!isWails()) {
    const source = String(command || '').trim().match(/^pi\s+install\s+(.+)$/i)?.[1]?.trim()
    if (source) {
      mockExtensions.packages[agentId] ||= []
      if (!mockExtensions.packages[agentId].some(item => item.key === source)) {
        const npmName = source.startsWith('npm:')
          ? source.slice(4).replace(/^(@[^/]+\/[^@]+|[^@]+)(?:@.*)?$/, '$1')
          : source.split(/[\\/]/).filter(Boolean).at(-1) || source
        mockExtensions.packages[agentId].push({
          key: source,
          name: npmName,
          description: source,
          installed: true,
          enabled: true,
          version: 'preview',
          sourcePath: `preview:${agentId}:${source}`,
        })
      }
    }
    return { success: true, message: '浏览器预览模式：已模拟安装', command, output: '' }
  }
  return App.InstallAgentExtension({ agentId, command })
}

// Remove a Pi extension previously installed into the agent's data directory.
export async function uninstallAgentExtension(agentId, key) {
  if (!isWails()) {
    const packages = mockExtensions.packages[agentId] || []
    const index = packages.findIndex(item => item.key === key || (key === 'browser-native' && item.name === 'pi-agent-browser-native'))
    if (index >= 0) packages.splice(index, 1)
    return { success: true, message: '浏览器预览模式：已模拟卸载', command: key, output: '' }
  }
  return App.UninstallAgentExtension({ agentId, key })
}

// Remove an unmanaged extension directory (one that lives under the agent's
// extensions/ folder but is not owned by CodingTo, e.g. a manually copied
// ask-user). Managed extensions are rejected by the backend.
export async function deleteAgentExtensionDir(agentId, key) {
  if (!isWails()) {
    const directory = mockExtensions.directory[agentId] || []
    const index = directory.findIndex(item => item.key === key)
    if (index >= 0) directory.splice(index, 1)
    return { success: true, message: '浏览器预览模式：已模拟删除', command: key, output: '' }
  }
  return App.DeleteAgentExtensionDir({ agentId, key })
}

// Subscribe to the start of a streamed install. The handler receives a payload
// with { agentId, title }. Returns an unsubscribe function.
export function onInstallStart(handler) {
  if (!isWails()) return () => {}
  return Events.On('install:start', event => handler(event?.data))
}

// Subscribe to live install log lines emitted by the backend during long-running
// installs (e.g. Playwright browser downloads). The handler receives a payload
// with { agentId, line }. Returns an unsubscribe function.
export function onInstallLog(handler) {
  if (!isWails()) return () => {}
  return Events.On('install:log', event => handler(event?.data))
}

// Subscribe to the final outcome of a streamed install. The handler receives a
// payload with { agentId, success }. Returns an unsubscribe function.
export function onInstallDone(handler) {
  if (!isWails()) return () => {}
  return Events.On('install:done', event => handler(event?.data))
}


export async function saveFigmaConfig(config) {
  if (isWails()) return App.SaveFigmaConfig(config)
  fallback.config.extensions.figma = structuredClone(config)
  saveDevConfig()
  const active = config.authorizations?.find(item => item.id === config.activeAuthorizationId)
  mockExtensions.figma = {
    ...(mockExtensions.figma || {}),
    enabled: !!config.enabled,
    hasToken: !!active?.token,
    authorizationCount: config.authorizations?.length || 0,
    activeAuthorizationName: active?.name || ''
  }
  return structuredClone(mockExtensions)
}

export async function testModel(request) {
  if (isWails()) return App.TestModel({ provider: request.provider, model: request.model })
  return new Promise(resolve => setTimeout(() => resolve({ ok: true, output: 'OK', latencyMs: 412 }), 800))
}

// ReadAgentFile returns the contents of a whitelisted file inside an agent's
// data directory (currently AGENTS.md). A missing file yields an empty string.
export async function readAgentFile(agentId, filename) {
  if (isWails()) return App.ReadAgentFile(agentId, filename)
  return ''
}

// WriteAgentFile writes content to a whitelisted file inside an agent's data
// directory (currently AGENTS.md).
export async function writeAgentFile(agentId, filename, content) {
  if (isWails()) return App.WriteAgentFile(agentId, filename, content)
  return undefined
}

export function minimise() {
  if (isWails()) return App.WindowMinimise()
}

export function toggleMaximise() {
  if (isWails()) return App.WindowToggleMaximise()
}

export function closeWindow() {
  if (isWails()) return App.WindowClose()
}

export function openExternal(url) {
  if (!url) return
  if (isWails()) return Browser.OpenURL(url)
  window.open(url, '_blank', 'noopener')
}

export function openSessionArtifact(path) {
  if (!path) return
  if (isWails()) return App.OpenSessionArtifact(path)
  const url = localFileURL(path)
  if (url) return openExternal(url)
}

// Save the file into the OS Downloads folder and open it. The backend derives
// trusted workspace and artifact roots from sessionId.
export async function downloadSessionArtifact(path, sessionId) {
  if (!path) return null
  if (isWails()) return App.SaveSessionArtifact(path, Number(sessionId) || 0)
  const url = localFileURL(path)
  if (url) return openExternal(url)
  return null
}

const mockListeners = new Map()
const mockSessions = []
const mockSessionMessages = new Map()
const mockPromptTimers = new Map()

function scheduleMockPromptEvent(sessionId, callback, delay) {
  const key = String(sessionId)
  if (!mockPromptTimers.has(key)) mockPromptTimers.set(key, new Set())
  const timers = mockPromptTimers.get(key)
  const timer = setTimeout(() => {
    timers.delete(timer)
    if (!timers.size) mockPromptTimers.delete(key)
    callback()
  }, delay)
  timers.add(timer)
}

function clearMockPromptTimers(sessionId) {
  const key = String(sessionId)
  const timers = mockPromptTimers.get(key)
  if (!timers) return
  for (const timer of timers) clearTimeout(timer)
  mockPromptTimers.delete(key)
}

function emitMock(name, payload) {
  for (const callback of mockListeners.get(name) || []) callback(payload)
}

export function onEvent(name, callback) {
  if (isWails()) {
    return Events.On(name, event => callback(event.data))
  }
  const listeners = mockListeners.get(name) || []
  listeners.push(callback)
  mockListeners.set(name, listeners)
  return () => mockListeners.set(name, listeners.filter(item => item !== callback))
}
