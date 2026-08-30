<script setup>
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ChevronDown, LoaderCircle, Plus, Server, SquareTerminal, X } from 'lucide-vue-next'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import {
  closeSessionTerminal,
  createSessionTerminal,
  getSessionTerminalWorkspace,
  onEvent,
  resizeSessionTerminal,
  writeSessionTerminal,
} from '../../backend'

const props = defineProps({
  open: { type: Boolean, default: false },
  sessionId: { type: Number, required: true },
  t: { type: Object, required: true },
})

const emit = defineEmits(['close', 'error'])
const PANEL_HEIGHT_KEY = 'codingto:terminal-panel-height'
const PANEL_MIN_HEIGHT = 170
const PANEL_MAX_RATIO = 0.72
const DEFAULT_HEIGHT = 300

const panel = ref(null)
const panelHeight = ref(readPanelHeight())
const panelResizing = ref(false)
const loading = ref(false)
const workspaceKey = ref('')
const workspaceRoot = ref('')
const profiles = ref([])
const terminals = ref([])
const activeTerminalId = ref('')
const profileMenuOpen = ref(false)
const creatingProfileId = ref('')
const closingTerminalId = ref('')

const terminalViews = new Map()
const terminalContainers = new Map()
const pendingChunks = new Map()
const inputQueues = new Map()
let resizeObserver = null
let themeObserver = null
let offData = null
let offExit = null
let loadRequest = 0
let resizeTimer = null
let stopPanelResize = null

const activeTerminal = computed(() => terminals.value.find(item => item.id === activeTerminalId.value) || null)
const activeProfile = computed(() => profiles.value.find(item => item.id === activeTerminal.value?.profileId) || profiles.value[0] || null)

function readPanelHeight() {
  try {
    const value = Number(localStorage.getItem(PANEL_HEIGHT_KEY))
    if (Number.isFinite(value) && value >= PANEL_MIN_HEIGHT) {
      return Math.min(value, Math.max(PANEL_MIN_HEIGHT, window.innerHeight * PANEL_MAX_RATIO))
    }
  } catch { /* local storage can be unavailable */ }
  return DEFAULT_HEIGHT
}

function persistPanelHeight() {
  try { localStorage.setItem(PANEL_HEIGHT_KEY, String(Math.round(panelHeight.value))) } catch { /* ignore */ }
}

function maxPanelHeight() {
  return Math.max(PANEL_MIN_HEIGHT, (panel.value?.parentElement?.clientHeight || window.innerHeight) * PANEL_MAX_RATIO)
}

function clampPanelHeight(value) {
  return Math.min(maxPanelHeight(), Math.max(PANEL_MIN_HEIGHT, value))
}

function decodeBase64(value) {
  if (!value) return new Uint8Array()
  const decoded = atob(value)
  const bytes = new Uint8Array(decoded.length)
  for (let index = 0; index < decoded.length; index += 1) bytes[index] = decoded.charCodeAt(index)
  return bytes
}

function selectedStorageKey(key = workspaceKey.value) {
  return key ? `codingto:terminal-active:${key}` : ''
}

function readSelectedTerminal(key) {
  try { return localStorage.getItem(selectedStorageKey(key)) || '' } catch { return '' }
}

function persistSelectedTerminal() {
  const key = selectedStorageKey()
  if (!key || !activeTerminalId.value) return
  try { localStorage.setItem(key, activeTerminalId.value) } catch { /* ignore */ }
}

function terminalTheme(element) {
  const styles = getComputedStyle(element)
  const value = (name, fallback) => styles.getPropertyValue(name).trim() || fallback
  const baseTheme = {
    background: value('--terminal-bg', '#161616'),
    foreground: value('--text', '#e7e7e7'),
    // --accent is intentionally the inverse foreground in both themes, while
    // --accent-fg is close to the terminal background and makes a bar vanish.
    cursor: value('--accent', '#d9a441'),
    cursorAccent: value('--terminal-bg', '#161616'),
    selectionBackground: 'rgba(120, 150, 210, 0.32)',
  }
  if (document.documentElement.dataset.theme !== 'dark') {
    return {
      ...baseTheme,
      black: '#1f1f1f', red: '#b42318', green: '#237a3b', yellow: '#7a5d00',
      blue: '#2457a7', magenta: '#8250b5', cyan: '#0e7490', white: '#5f6368',
      brightBlack: '#6b7280', brightRed: '#d92d20', brightGreen: '#15803d', brightYellow: '#8a6500',
      brightBlue: '#2563eb', brightMagenta: '#9333ea', brightCyan: '#0891b2', brightWhite: '#242422',
    }
  }
  return {
    ...baseTheme,
    black: '#1f1f1f', red: '#e06c75', green: '#98c379', yellow: '#e5c07b',
    blue: '#61afef', magenta: '#c678dd', cyan: '#56b6c2', white: '#d7dae0',
    brightBlack: '#5c6370', brightRed: '#ef7a85', brightGreen: '#b0d889', brightYellow: '#f0d38a',
    brightBlue: '#75bfff', brightMagenta: '#d895eb', brightCyan: '#6dced8', brightWhite: '#ffffff',
  }
}

function refreshTerminalThemes() {
  for (const [id, view] of terminalViews) {
    const element = terminalContainers.get(id)
    if (element) view.instance.options.theme = terminalTheme(element)
  }
}

function setTerminalContainer(id, element) {
  if (element) {
    terminalContainers.set(id, element)
    ensureTerminalView(terminals.value.find(item => item.id === id))
  } else {
    terminalContainers.delete(id)
  }
}

function ensureTerminalView(snapshot) {
  if (!snapshot || terminalViews.has(snapshot.id)) return terminalViews.get(snapshot?.id)
  const element = terminalContainers.get(snapshot.id)
  if (!element) return null
  const fitAddon = new FitAddon()
  const instance = new Terminal({
    allowProposedApi: false,
    convertEol: false,
    cursorBlink: snapshot.running,
    cursorStyle: 'bar',
    cursorWidth: 3,
    cursorInactiveStyle: 'bar',
    fontFamily: 'Cascadia Mono, Consolas, SFMono-Regular, Menlo, monospace',
    fontSize: 13,
    lineHeight: 1.16,
    scrollback: 10000,
    theme: terminalTheme(element),
  })
  instance.loadAddon(fitAddon)
  instance.open(element)
  instance.onData(data => queueTerminalInput(snapshot.id, data))
  instance.attachCustomKeyEventHandler(event => handleClipboardShortcut(instance, event))
  terminalViews.set(snapshot.id, { instance, fitAddon })
  if (snapshot.replayBase64) instance.write(decodeBase64(snapshot.replayBase64))
  for (const chunk of pendingChunks.get(snapshot.id) || []) instance.write(decodeBase64(chunk))
  pendingChunks.delete(snapshot.id)
  if (!snapshot.running) instance.options.cursorBlink = false
  if (snapshot.id === activeTerminalId.value) nextTick(() => fitActiveTerminal(true))
  return terminalViews.get(snapshot.id)
}

function handleClipboardShortcut(instance, event) {
  if (event.type !== 'keydown' || !event.ctrlKey || !event.shiftKey) return true
  const key = event.key.toLowerCase()
  if (key === 'c' && instance.hasSelection()) {
    navigator.clipboard?.writeText(instance.getSelection()).catch(() => {})
    return false
  }
  if (key === 'v') {
    navigator.clipboard?.readText().then(text => instance.paste(text)).catch(() => {})
    return false
  }
  return true
}

function queueTerminalInput(terminalId, data) {
  if (!data) return
  const sessionId = props.sessionId
  const previous = inputQueues.get(terminalId) || Promise.resolve()
  const next = previous
    .then(() => writeSessionTerminal({ sessionId, terminalId, data }))
    .catch(error => emit('error', error))
    .finally(() => {
      if (inputQueues.get(terminalId) === next) inputQueues.delete(terminalId)
    })
  inputQueues.set(terminalId, next)
}

function disposeTerminalView(id) {
  const view = terminalViews.get(id)
  if (view) view.instance.dispose()
  terminalViews.delete(id)
  terminalContainers.delete(id)
  pendingChunks.delete(id)
  inputQueues.delete(id)
}

function disposeAllTerminalViews() {
  for (const id of [...terminalViews.keys()]) disposeTerminalView(id)
}

function fitActiveTerminal(immediate = false) {
  if (resizeTimer) window.clearTimeout(resizeTimer)
  const fit = () => {
    const view = terminalViews.get(activeTerminalId.value)
    const terminal = activeTerminal.value
    if (!view || !terminal) return
    try {
      view.fitAddon.fit()
      view.instance.focus()
      if (terminal.running) {
        resizeSessionTerminal({
          sessionId: props.sessionId,
          terminalId: terminal.id,
          columns: view.instance.cols,
          rows: view.instance.rows,
        }).catch(error => emit('error', error))
      }
    } catch { /* hidden or detached terminal containers cannot be fitted */ }
  }
  if (immediate) fit()
  else resizeTimer = window.setTimeout(fit, 60)
}

async function loadWorkspace() {
  const request = ++loadRequest
  if (!props.open) return
  loading.value = true
  profileMenuOpen.value = false
  try {
    const result = await getSessionTerminalWorkspace(props.sessionId)
    if (request !== loadRequest) return
    const nextKey = result?.workspaceKey || ''
    if (workspaceKey.value && workspaceKey.value !== nextKey) disposeAllTerminalViews()
    workspaceKey.value = nextKey
    workspaceRoot.value = result?.root || ''
    profiles.value = Array.isArray(result?.profiles) ? result.profiles : []
    const snapshots = Array.isArray(result?.terminals) ? result.terminals : []
    const liveIds = new Set(snapshots.map(item => item.id))
    for (const id of [...terminalViews.keys()]) {
      if (!liveIds.has(id)) disposeTerminalView(id)
    }
    terminals.value = snapshots
    const remembered = readSelectedTerminal(nextKey)
    if (!liveIds.has(activeTerminalId.value)) {
      activeTerminalId.value = liveIds.has(remembered) ? remembered : (snapshots[0]?.id || '')
    }
    if (!snapshots.length && profiles.value.length) {
      await createTerminal(profiles.value[0].id)
    } else {
      await nextTick()
      for (const terminal of terminals.value) ensureTerminalView(terminal)
      fitActiveTerminal(true)
    }
  } catch (error) {
    if (request === loadRequest) emit('error', error)
  } finally {
    if (request === loadRequest) loading.value = false
  }
}

async function createTerminal(profileId = activeProfile.value?.id || profiles.value[0]?.id) {
  if (!profileId || creatingProfileId.value) return
  creatingProfileId.value = profileId
  profileMenuOpen.value = false
  try {
    const currentView = terminalViews.get(activeTerminalId.value)
    const snapshot = await createSessionTerminal({
      sessionId: props.sessionId,
      profileId,
      columns: currentView?.instance.cols || 100,
      rows: currentView?.instance.rows || 24,
    })
    terminals.value = [...terminals.value, snapshot]
    activeTerminalId.value = snapshot.id
    persistSelectedTerminal()
    await nextTick()
    ensureTerminalView(snapshot)
    fitActiveTerminal(true)
  } catch (error) {
    emit('error', error)
  } finally {
    creatingProfileId.value = ''
  }
}

async function closeTerminal(terminalId) {
  if (!terminalId || closingTerminalId.value) return
  closingTerminalId.value = terminalId
  const index = terminals.value.findIndex(item => item.id === terminalId)
  try {
    await closeSessionTerminal({ sessionId: props.sessionId, terminalId })
    disposeTerminalView(terminalId)
    terminals.value = terminals.value.filter(item => item.id !== terminalId)
    if (activeTerminalId.value === terminalId) {
      activeTerminalId.value = terminals.value[Math.min(index, terminals.value.length - 1)]?.id || ''
      if (activeTerminalId.value) {
        persistSelectedTerminal()
        await nextTick()
        fitActiveTerminal(true)
      }
    }
  } catch (error) {
    emit('error', error)
  } finally {
    closingTerminalId.value = ''
  }
}

function selectTerminal(terminalId) {
  activeTerminalId.value = terminalId
  persistSelectedTerminal()
  nextTick(() => fitActiveTerminal(true))
}

function handleTerminalData(event) {
  if (!event || event.workspaceKey !== workspaceKey.value || !event.terminalId || !event.dataBase64) return
  const view = terminalViews.get(event.terminalId)
  if (view) {
    view.instance.write(decodeBase64(event.dataBase64))
  } else {
    const chunks = pendingChunks.get(event.terminalId) || []
    chunks.push(event.dataBase64)
    pendingChunks.set(event.terminalId, chunks)
  }
}

function handleTerminalExit(event) {
  if (!event || event.workspaceKey !== workspaceKey.value || !event.terminalId) return
  terminals.value = terminals.value.map(item => item.id === event.terminalId
    ? { ...item, running: false, exitCode: Number(event.exitCode) }
    : item)
  const view = terminalViews.get(event.terminalId)
  if (view) {
    view.instance.options.cursorBlink = false
    view.instance.writeln(`\r\n\x1b[90m${props.t.terminalExited.replace('{code}', String(event.exitCode))}\x1b[0m`)
  }
}

function startPanelResize(event) {
  event.preventDefault()
  if (stopPanelResize) stopPanelResize(false)
  panelResizing.value = true
  const startY = event.clientY
  const startHeight = panelHeight.value
  const move = moveEvent => {
    panelHeight.value = clampPanelHeight(startHeight + startY - moveEvent.clientY)
    fitActiveTerminal()
  }
  const finish = (persist = true) => {
    if (!panelResizing.value) return
    panelResizing.value = false
    document.removeEventListener('pointermove', move)
    document.removeEventListener('pointerup', up)
    document.removeEventListener('pointercancel', cancel)
    document.body.style.cursor = ''
    document.body.style.userSelect = ''
    stopPanelResize = null
    if (persist) persistPanelHeight()
    fitActiveTerminal(true)
  }
  const up = () => finish(true)
  const cancel = () => finish(false)
  stopPanelResize = finish
  document.body.style.cursor = 'row-resize'
  document.body.style.userSelect = 'none'
  document.addEventListener('pointermove', move)
  document.addEventListener('pointerup', up, { once: true })
  document.addEventListener('pointercancel', cancel, { once: true })
}

function handleViewportResize() {
  const nextHeight = clampPanelHeight(panelHeight.value)
  if (nextHeight !== panelHeight.value) {
    panelHeight.value = nextHeight
    persistPanelHeight()
  }
  fitActiveTerminal()
}

function closeProfileMenu(event) {
  if (profileMenuOpen.value && !event.target.closest('.terminal-panel__create-wrap')) profileMenuOpen.value = false
}

watch(() => [props.sessionId, props.open], () => {
  if (props.open) void loadWorkspace()
}, { immediate: true })

watch(activeTerminalId, () => nextTick(() => fitActiveTerminal(true)))

onMounted(() => {
  panelHeight.value = clampPanelHeight(panelHeight.value)
  offData = onEvent('terminal:data', handleTerminalData)
  offExit = onEvent('terminal:exit', handleTerminalExit)
  resizeObserver = new ResizeObserver(() => fitActiveTerminal())
  if (panel.value) resizeObserver.observe(panel.value)
  themeObserver = new MutationObserver(refreshTerminalThemes)
  themeObserver.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
  document.addEventListener('pointerdown', closeProfileMenu)
  window.addEventListener('resize', handleViewportResize)
})

onBeforeUnmount(() => {
  loadRequest += 1
  if (offData) offData()
  if (offExit) offExit()
  if (resizeObserver) resizeObserver.disconnect()
  if (themeObserver) themeObserver.disconnect()
  if (resizeTimer) window.clearTimeout(resizeTimer)
  if (stopPanelResize) stopPanelResize(false)
  document.removeEventListener('pointerdown', closeProfileMenu)
  window.removeEventListener('resize', handleViewportResize)
  disposeAllTerminalViews()
})
</script>

<template>
  <section
    v-show="open"
    ref="panel"
    class="terminal-panel"
    :class="{ 'terminal-panel--resizing': panelResizing }"
    :style="{ height: `${panelHeight}px`, '--terminal-panel-height': `${panelHeight}px` }"
    :aria-label="t.terminalTitle"
  >
    <div class="terminal-panel__resize" @pointerdown="startPanelResize"></div>
    <header class="terminal-panel__header">
      <div class="terminal-panel__tabs" role="tablist" :aria-label="t.terminalTabs">
        <button
          v-for="terminal in terminals"
          :key="terminal.id"
          class="terminal-panel__tab"
          :class="{ active: terminal.id === activeTerminalId }"
          type="button"
          role="tab"
          :aria-selected="terminal.id === activeTerminalId"
          @click="selectTerminal(terminal.id)"
        >
          <Server v-if="terminal.kind === 'ssh'" :size="13" />
          <SquareTerminal v-else :size="13" />
          <span class="terminal-panel__tab-title">{{ terminal.title }}</span>
          <span class="terminal-panel__status" :class="{ running: terminal.running }" :title="terminal.running ? t.terminalRunning : t.terminalStopped"></span>
          <span
            class="terminal-panel__tab-close"
            role="button"
            :aria-label="t.terminalClose"
            :title="t.terminalClose"
            @click.stop="closeTerminal(terminal.id)"
          >
            <LoaderCircle v-if="closingTerminalId === terminal.id" class="spin" :size="12" />
            <X v-else :size="12" />
          </span>
        </button>
      </div>

      <div class="terminal-panel__actions">
        <div class="terminal-panel__create-wrap">
          <button
            class="terminal-panel__action terminal-panel__new"
            type="button"
            :disabled="!profiles.length || !!creatingProfileId"
            :title="t.terminalNew"
            @click="createTerminal()"
          >
            <LoaderCircle v-if="creatingProfileId" class="spin" :size="14" />
            <Plus v-else :size="15" />
          </button>
          <button
            class="terminal-panel__action terminal-panel__profiles"
            type="button"
            :disabled="!profiles.length || !!creatingProfileId"
            :title="t.terminalSelectProfile"
            :aria-expanded="profileMenuOpen"
            @click.stop="profileMenuOpen = !profileMenuOpen"
          >
            <ChevronDown :size="13" />
          </button>
          <div v-if="profileMenuOpen" class="terminal-panel__profile-menu">
            <button v-for="profile in profiles" :key="profile.id" type="button" @click="createTerminal(profile.id)">
              <Server v-if="profile.kind === 'ssh'" :size="15" />
              <SquareTerminal v-else :size="15" />
              <span>
                <strong>{{ profile.title }}</strong>
                <small v-if="profile.detail">{{ profile.detail }}</small>
              </span>
            </button>
          </div>
        </div>
        <button class="terminal-panel__action" type="button" :title="t.terminalHide" :aria-label="t.terminalHide" @click="emit('close')">
          <X :size="15" />
        </button>
      </div>
    </header>

    <div class="terminal-panel__content">
      <div v-if="loading && !terminals.length" class="terminal-panel__empty">
        <LoaderCircle class="spin" :size="18" />
        <span>{{ t.terminalLoading }}</span>
      </div>
      <div v-else-if="!terminals.length" class="terminal-panel__empty">
        <SquareTerminal :size="22" />
        <span>{{ profiles.length ? t.terminalEmpty : t.terminalUnavailable }}</span>
        <button v-if="profiles.length" type="button" @click="createTerminal(profiles[0].id)">{{ t.terminalNew }}</button>
      </div>
      <div
        v-for="terminal in terminals"
        v-show="terminal.id === activeTerminalId"
        :key="terminal.id"
        :ref="element => setTerminalContainer(terminal.id, element)"
        class="terminal-panel__viewport"
        role="tabpanel"
      ></div>
    </div>
  </section>
</template>

<style scoped src="../../styles/chat/terminal.css"></style>
