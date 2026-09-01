import { onBeforeUnmount, ref } from 'vue'

const SIDEBAR_MIN = 160
const SIDEBAR_MAX = 420
const SIDEBAR_WIDTH_KEY = 'codingto:left-sidebar-width'

function loadSidebarWidth() {
  try {
    const raw = Number(localStorage.getItem(SIDEBAR_WIDTH_KEY))
    if (Number.isFinite(raw) && raw >= SIDEBAR_MIN && raw <= SIDEBAR_MAX) return raw
  } catch {
    // Storage can be unavailable in restricted browser contexts.
  }
  return 224
}

function persistSidebarWidth(value) {
  try {
    localStorage.setItem(SIDEBAR_WIDTH_KEY, String(value))
  } catch {
    // A persisted width is optional UI state.
  }
}

// Owns sidebar width persistence and pointer-resize lifecycle.
export function useSidebarLayout() {
  const sidebarOpen = ref(true)
  const sidebarWidth = ref(loadSidebarWidth())
  const sidebarResizing = ref(false)
  let onMove = null
  let onUp = null

  function stopSidebarResize({ persist = false } = {}) {
    if (onMove) document.removeEventListener('pointermove', onMove)
    if (onUp) document.removeEventListener('pointerup', onUp)
    onMove = null
    onUp = null
    document.body.style.cursor = ''
    document.body.style.userSelect = ''
    sidebarResizing.value = false
    if (persist) persistSidebarWidth(sidebarWidth.value)
  }

  function startSidebarResize(event) {
    if (!sidebarOpen.value) return
    event.preventDefault()
    stopSidebarResize()
    sidebarResizing.value = true
    const startX = event.clientX
    const startWidth = sidebarWidth.value
    onMove = moveEvent => {
      sidebarWidth.value = Math.min(SIDEBAR_MAX, Math.max(SIDEBAR_MIN, startWidth + (moveEvent.clientX - startX)))
    }
    onUp = () => stopSidebarResize({ persist: true })
    document.addEventListener('pointermove', onMove)
    document.addEventListener('pointerup', onUp)
    document.body.style.cursor = 'col-resize'
    document.body.style.userSelect = 'none'
  }

  onBeforeUnmount(() => stopSidebarResize())

  return {
    sidebarOpen,
    sidebarWidth,
    sidebarResizing,
    startSidebarResize
  }
}
