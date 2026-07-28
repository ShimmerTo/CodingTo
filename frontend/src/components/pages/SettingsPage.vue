<script setup>
import { ref, watch, computed, onMounted, onBeforeUnmount } from 'vue'
import { Download, FolderOpen, Globe2, Moon, RefreshCw, Settings, Sun } from 'lucide-vue-next'
import { Call, Events } from '@wailsio/runtime'
import { useAppContext } from '../../composables/appContext'
import InstallDialog from '../../components/InstallDialog.vue'
import { renderMarkdown } from '../../components/chat/chatFormatters.js'

const { t, config, bootstrap, pickSessionDirectory, persist } = useAppContext()

const piVersion = ref('')
const checkingUpdate = ref(false)
const updating = ref(false)
const dialog = ref({ open: false, mode: 'info', title: '', message: '', latest: '', installed: '', log: [], done: false, success: false })
let eventOffs = []

const statusText = computed(() => (updating.value ? t.value.piUpdating : (dialog.value.success ? t.value.piUpdateSuccess : t.value.piUpdateFailed)))
const dialogModeHint = computed(() => {
  if (dialog.value.mode === 'confirm') return t.value.piUpdateConfirm.replace('{latest}', dialog.value.latest).replace('{installed}', dialog.value.installed)
  if (dialog.value.mode === 'info') return dialog.value.message
  return ''
})

async function fetchPiVersion() {
  try {
    piVersion.value = await Call.ByName('codingto/internal/app.App.GetPiVersion')
  } catch (e) {
    piVersion.value = ''
  }
}

onMounted(fetchPiVersion)

function openInfo(title, message) {
  dialog.value = { open: true, mode: 'info', title, message, latest: '', installed: '', log: [], done: false, success: false }
}

const updateLog = ref({ open: false, content: '' })
const renderedLog = computed(() => renderMarkdown(updateLog.value.content))
async function loadUpdateLog() {
  try {
    const content = await Call.ByName('codingto/internal/app.App.GetUpdateLog')
    updateLog.value = { open: true, content: (content && content.trim()) ? content : t.value.update_log_empty }
  } catch (e) {
    updateLog.value = { open: true, content: String(e) }
  }
}

async function checkForUpdate() {
  if (checkingUpdate.value || updating.value) return
  checkingUpdate.value = true
  try {
    const res = await Call.ByName('codingto/internal/app.App.CheckPiUpdate')
    if (res.error) {
      openInfo(t.value.piUpdateFailed, res.error)
      return
    }
    if (res.available) {
      dialog.value = { open: true, mode: 'confirm', title: t.value.checkPiUpdate, message: '', latest: res.latest, installed: res.installed, log: [], done: false, success: false }
    } else {
      openInfo(t.value.piUpToDate.replace('{version}', res.installed), '')
    }
  } catch (e) {
    openInfo(t.value.piUpdateFailed, String(e))
  } finally {
    checkingUpdate.value = false
  }
}

// ---- 客户端自身更新（GitHub Release）----
const appChecking = ref(false)
const appDownloading = ref(false)
const appStatus = ref(null) // AppUpdateStatus | null

async function checkAppUpdate() {
  if (appChecking.value) return
  appChecking.value = true
  appStatus.value = null
  try {
    const res = await Call.ByName('codingto/internal/app.App.CheckAppUpdate')
    appStatus.value = res || {}
  } catch (e) {
    appStatus.value = { error: String(e) }
  } finally {
    appChecking.value = false
  }
}

async function downloadAndInstallApp() {
  if (!appStatus.value?.downloadUrl || appDownloading.value) return
  appDownloading.value = true
  try {
    await Call.ByName('codingto/internal/app.App.DownloadAndInstallApp', appStatus.value.downloadUrl)
  } catch (e) {
    openInfo(t.value.app_update_failed, String(e))
  } finally {
    appDownloading.value = false
  }
}

function cleanupEvents() {
  eventOffs.forEach((off) => { try { off() } catch (_) {} })
  eventOffs = []
}

function onConfirm() {
  startUpdate()
}

async function startUpdate() {
  dialog.value.mode = 'progress'
  dialog.value.log = []
  dialog.value.done = false
  updating.value = true
  const offLog = Events.On('piagent:log', (event) => { dialog.value.log.push(event.data.line) })
  const offDone = Events.On('piagent:done', async (event) => {
    dialog.value.done = true
    dialog.value.success = !!event.data.success
    updating.value = false
    await fetchPiVersion()
    cleanupEvents()
  })
  eventOffs = [offLog, offDone]
  try {
    await Call.ByName('codingto/internal/app.App.UpdatePi')
  } catch (e) {
    dialog.value.done = true
    dialog.value.success = false
    dialog.value.log.push(String(e))
    updating.value = false
    cleanupEvents()
  }
}

function closeDialog() {
  dialog.value.open = false
  cleanupEvents()
}

onBeforeUnmount(cleanupEvents)

const accentInput = ref(config.preferences.accentColor)
watch(() => config.preferences.accentColor, (v) => { accentInput.value = v })
// 设置页任意改动即时保存，避免刷新后丢失（主题、accent、目录都是 config 的一部分）。
function setTheme(value) {
  config.preferences.theme = value
  persist()
}
function applyAccent() {
  const s = accentInput.value.trim()
  const ok = /^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$/.test(s)
  config.preferences.accentColor = ok ? s : config.preferences.accentColor
  accentInput.value = config.preferences.accentColor
  persist()
}
async function pickDir() {
  await pickSessionDirectory()
  persist()
}
</script>

<template>
<section class="content-page settings-page">
          <div class="page-heading"><div><h2>{{ t.settingsTitle }}</h2><p>Preferences, providers and Pi runtime</p></div><Settings :size="28" /></div>

          <div class="settings-section">
            <h2>{{ t.appearance }}</h2>
            <div class="setting-row">
              <div><label>{{ t.theme }}</label></div>
              <div class="segmented">
                <button :class="{ active: config.preferences.theme === 'system' }" @click="setTheme('system')"><Globe2 :size="14" />{{ t.system }}</button>
                <button :class="{ active: config.preferences.theme === 'light' }" @click="setTheme('light')"><Sun :size="14" />{{ t.light }}</button>
                <button :class="{ active: config.preferences.theme === 'dark' }" @click="setTheme('dark')"><Moon :size="14" />{{ t.dark }}</button>
              </div>
            </div>
            <!-- 中英文切换栏 temporarily disabled
            <div class="setting-row">
              <div><label>{{ t.language }}</label></div>
              <div class="segmented">
                <button :class="{ active: config.preferences.language === 'zh-CN' }" @click="config.preferences.language = 'zh-CN'"><Languages :size="14" />中文</button>
                <button :class="{ active: config.preferences.language === 'en-US' }" @click="config.preferences.language = 'en-US'"><Languages :size="14" />English</button>
              </div>
            </div>
            -->
            <div class="setting-row">
              <div><label>{{ t.accentColor }}</label></div>
              <div class="color-field">
                <input type="color" v-model="config.preferences.accentColor" @change="persist()" />
                <input class="color-field__hex" v-model="accentInput" @change="applyAccent" @blur="applyAccent" placeholder="#10b981" spellcheck="false" />
              </div>
            </div>
          </div>

          <div class="settings-section">
            <h2>{{ t.sessionStorage }}</h2>
            <div class="setting-row setting-row--directory">
              <div>
                <label>{{ t.sessionStorageDir }}</label>
                <small>{{ t.sessionStorageHint }}</small>
              </div>
              <div class="directory-field">
                <input v-model.trim="config.sessionDir" :placeholder="t.sessionStorageDefault" @change="savePrefs()" />
                <button class="secondary-button" type="button" @click="pickDir">
                  <FolderOpen :size="14" />{{ t.choose }}
                </button>
              </div>
            </div>
          </div>

          <div class="settings-section runtime-info">
            <h2>{{ t.runtime }}</h2>
            <div class="runtime-row--env"><span>Pi</span><strong :class="{ bad: !bootstrap?.piInstalled }">{{ bootstrap?.piInstalled ? bootstrap.piPath : t.piMissing }}<span v-if="bootstrap?.piInstalled && piVersion" class="pi-version">v{{ piVersion }}</span></strong><button v-if="bootstrap?.piInstalled" class="secondary-button" :disabled="checkingUpdate || updating" @click="checkForUpdate"><RefreshCw :size="15" :class="{ spinning: checkingUpdate }" />{{ checkingUpdate ? t.checkingPiUpdate : t.checkPiUpdate }}</button></div>
            <div><span>{{ t.configDir }}</span><strong>{{ bootstrap?.configDir }}</strong></div>
            <div><span>{{ t.version }}</span><strong>{{ bootstrap?.version }}</strong></div>
          </div>

          <div class="settings-section">
            <h2>{{ t.version }}</h2>
            <div class="setting-row">
              <div>
                <label>{{ t.current_version }} v{{ bootstrap?.version }}</label>
              </div>
              <button class="secondary-button" type="button" :disabled="appChecking" @click="checkAppUpdate">
                <RefreshCw :size="15" :class="{ spinning: appChecking }" />{{ appChecking ? t.checking_new_version : t.check_new_version }}
              </button>
            </div>

            <div v-if="appStatus" class="app-update-line">
              <template v-if="appStatus.available">
                <button class="primary-button" type="button" :disabled="appDownloading" @click="downloadAndInstallApp">
                  <Download :size="15" />{{ appDownloading ? t.downloading : t.download_and_install }}
                </button>
                <strong class="app-update-new">{{ t.new_version_available.replace('{latest}', appStatus.latest) }}</strong>
              </template>
              <span v-else-if="appStatus.error" class="app-update-error">{{ appStatus.error }}</span>
              <span v-else-if="appStatus.hasNewer" class="app-update-note">{{ t.no_asset_for_platform.replace('{latest}', appStatus.latest).replace('{platform}', appStatus.platform) }}</span>
              <span v-else-if="appStatus.latest" class="app-update-note">{{ t.already_latest.replace('{version}', appStatus.current) }}</span>
              <span v-else class="app-update-note">{{ t.no_release }}</span>
            </div>

            <div class="setting-row">
              <div>
                <label>{{ t.update_log }}</label>
              </div>
              <button class="secondary-button" type="button" @click="loadUpdateLog">{{ t.view }}</button>
            </div>
          </div>

          <InstallDialog
            v-if="dialog.open"
            :mode="dialog.mode === 'progress' ? 'progress' : 'command'"
            :title="dialog.title"
            :hint="dialogModeHint"
            :running="updating"
            :log="dialog.log"
            :status-text="statusText"
            :log-empty-text="t.installLogEmpty"
            :run-text="t.piUpdateNow"
            @update:command="onConfirm"
            @run="startUpdate"
            @close="closeDialog"
          />

          <div v-if="updateLog.open" class="modal-backdrop" @click.self="updateLog.open = false">
            <div class="install-dialog">
              <h3>{{ t.update_log }}</h3>
              <div class="message-markdown update-log-md" v-html="renderedLog"></div>
              <div class="install-dialog__actions">
                <button class="primary-button" type="button" @click="updateLog.open = false">{{ t.close }}</button>
              </div>
            </div>
          </div>

          </section>
</template>

<style scoped>
.update-log-md {
  max-height: 60vh;
  overflow: auto;
  padding-right: 4px;
  text-align: left;
  line-height: 1.65;
}
.update-log-md :deep(> :first-child) { margin-top: 0; }
.update-log-md :deep(> :last-child) { margin-bottom: 0; }
.update-log-md :deep(p) { margin: 0 0 9px; }
.update-log-md :deep(h1),
.update-log-md :deep(h2),
.update-log-md :deep(h3),
.update-log-md :deep(h4) { margin: 16px 0 8px; color: var(--text); line-height: 1.35; }
.update-log-md :deep(h1) { font-size: 18px; }
.update-log-md :deep(h2) { font-size: 16px; }
.update-log-md :deep(h3),
.update-log-md :deep(h4) { font-size: 14px; }
.update-log-md :deep(ul),
.update-log-md :deep(ol) { margin: 7px 0 10px; padding-left: 22px; }
.update-log-md :deep(li + li) { margin-top: 3px; }
.update-log-md :deep(a) { color: var(--accent); text-decoration: underline; text-underline-offset: 2px; }
.update-log-md :deep(strong) { font-weight: 700; }
.update-log-md :deep(blockquote) { margin: 9px 0; padding: 2px 0 2px 11px; border-left: 3px solid var(--border); color: var(--muted); }
.update-log-md :deep(hr) { margin: 14px 0; border: 0; border-top: 1px solid var(--border-soft); }
.update-log-md :deep(code) { padding: 1px 4px; border-radius: 4px; background: var(--surface-2); font: 12px/1.5 "SFMono-Regular", Consolas, monospace; }
.update-log-md :deep(pre) { max-width: 100%; margin: 9px 0; padding: 10px 11px; overflow-x: auto; border: 1px solid var(--border-soft); border-radius: 7px; background: var(--surface-2); white-space: pre; }
.update-log-md :deep(pre code) { padding: 0; background: transparent; font-size: 11px; }
.update-log-md :deep(table) { display: block; max-width: 100%; margin: 10px 0; overflow-x: auto; border-collapse: collapse; }
.update-log-md :deep(th),
.update-log-md :deep(td) { min-width: 90px; padding: 6px 9px; border: 1px solid var(--border); text-align: left; vertical-align: top; }
.update-log-md :deep(th) { background: var(--surface-2); font-weight: 650; }

.app-update-line {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
  margin: 4px 0 12px;
}
.app-update-new { color: var(--accent); font-weight: 650; }
.app-update-note { color: var(--muted); }
.app-update-error { color: #ef4444; }
</style>
