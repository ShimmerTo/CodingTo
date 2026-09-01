<template>
  <div v-if="visible" class="ilm-overlay" @pointerdown.self="close">
    <div class="ilm-panel">
      <div class="ilm-header">
        <div class="ilm-title">
          <span v-if="running" class="ilm-spinner" aria-hidden="true"></span>
          <span class="ilm-status-dot" :class="{ ok: !running && success, fail: !running && !success }" v-else></span>
          <span>{{ title || t.installLogTitle }}</span>
        </div>
        <button class="ilm-close" :title="t.installLogClose" @click="close">×</button>
      </div>

      <div class="ilm-body">
        <pre ref="logBox" class="ilm-log">{{ displayText }}</pre>
      </div>

      <div class="ilm-footer">
        <span class="ilm-state" :class="{ running, ok: !running && success, fail: !running && !success }">
          {{ statusText }}
        </span>
        <span v-if="hintText" class="ilm-hint">{{ hintText }}</span>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, watch, onMounted, onBeforeUnmount, nextTick } from 'vue'
import { Events } from '@wailsio/runtime'
import { buildT } from '../i18n'
import { getBootstrap } from '../backend'

const lang = ref('zh-CN')
const t = computed(() => buildT(lang.value))

const visible = ref(false)
const running = ref(false)
const success = ref(false)
const title = ref('')
const installId = ref('')
const operation = ref('install')
const lines = ref([])
const logBox = ref(null)

const displayText = computed(() => (lines.value.length ? lines.value.join('\n') : t.value.installLogEmpty))
const statusText = computed(() => {
  if (operation.value === 'uninstall') {
    if (running.value) return t.value.uninstallLogRunning
    return success.value ? t.value.uninstallLogDone : t.value.uninstallLogFailed
  }
  if (running.value) return t.value.installLogRunning
  return success.value ? t.value.installLogDone : t.value.installLogFailed
})
const hintText = computed(() => {
  if (operation.value === 'uninstall') return success.value ? '' : t.value.uninstallLogHint
  return t.value.installLogHint
})

function open(payload) {
  installId.value = payload.installId || payload.agentId || ''
  title.value = payload.title || ''
  operation.value = payload.operation === 'uninstall' ? 'uninstall' : 'install'
  lines.value = []
  running.value = true
  success.value = false
  visible.value = true
}

function close() {
  // Keep the content visible but allow the user to dismiss the overlay.
  visible.value = false
}

watch(
  () => lines.value.length,
  async () => {
    await nextTick()
    if (logBox.value) logBox.value.scrollTop = logBox.value.scrollHeight
  }
)

let offStart, offLog, offDone

onMounted(async () => {
  // 独立挂载的弹窗拿不到主应用的 config，启动时拉取一次语言偏好。
  try {
    const boot = await getBootstrap()
    lang.value = boot?.config?.preferences?.language || 'zh-CN'
  } catch {}
  // Wails 事件回调收到的是 { name, data } 包装对象，真实负载在 data 上。
  offStart = Events.On('install:start', (event) => {
    const payload = event?.data
    if (payload?.installId || payload?.agentId) open(payload)
  })
  offLog = Events.On('install:log', (event) => {
    const payload = event?.data
    const payloadInstallId = payload?.installId || payload?.agentId || ''
    if (!visible.value || payloadInstallId !== installId.value) return
    if (typeof payload?.line === 'string' && payload.line !== '') {
      lines.value.push(payload.line)
    }
  })
  offDone = Events.On('install:done', (event) => {
    const payload = event?.data
    const payloadInstallId = payload?.installId || payload?.agentId || ''
    if (payloadInstallId !== installId.value) return
    if (payload?.operation === 'uninstall') operation.value = 'uninstall'
    running.value = false
    success.value = !!payload?.success
  })
})

onBeforeUnmount(() => {
  offStart?.()
  offLog?.()
  offDone?.()
})
</script>

<style scoped>
.ilm-overlay {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.55);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 9999;
}
.ilm-panel {
  width: min(760px, 92vw);
  max-height: 80vh;
  display: flex;
  flex-direction: column;
  background: var(--surface);
  color: var(--text);
  border: 1px solid var(--border);
  border-radius: 12px;
  box-shadow: 0 18px 60px rgba(0, 0, 0, 0.5);
  overflow: hidden;
}
.ilm-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 12px 16px;
  border-bottom: 1px solid var(--border);
  font-weight: 600;
}
.ilm-title {
  display: flex;
  align-items: center;
  gap: 8px;
}
.ilm-close {
  background: transparent;
  border: none;
  color: var(--muted);
  font-size: var(--fs-14);
  line-height: 1;
  cursor: pointer;
}
.ilm-close:hover {
  color: var(--text);
}
.ilm-body {
  flex: 1;
  overflow: auto;
  padding: 12px 16px;
}
.ilm-log {
  margin: 0;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: var(--fs-13);
  line-height: 1.55;
  white-space: pre-wrap;
  word-break: break-word;
  color: var(--text);
}
.ilm-footer {
  border-top: 1px solid var(--border);
  padding: 10px 16px;
  display: flex;
  flex-direction: column;
  gap: 4px;
  font-size: var(--fs-12);
}
.ilm-state {
  font-weight: 600;
}
.ilm-state.running {
  color: #f0b429;
}
.ilm-state.ok {
  color: #3fb950;
}
.ilm-state.fail {
  color: #f85149;
}
.ilm-hint {
  color: var(--muted);
}
.ilm-spinner {
  width: 14px;
  height: 14px;
  border: 2px solid rgba(240, 180, 41, 0.35);
  border-top-color: #f0b429;
  border-radius: 50%;
  animation: ilm-spin 0.8s linear infinite;
}
.ilm-status-dot {
  width: 10px;
  height: 10px;
  border-radius: 50%;
  display: inline-block;
}
.ilm-status-dot.ok {
  background: #3fb950;
}
.ilm-status-dot.fail {
  background: #f85149;
}
@keyframes ilm-spin {
  to {
    transform: rotate(360deg);
  }
}
</style>
