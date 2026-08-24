<script setup>
import { ref, watch, computed, onMounted, onBeforeUnmount, nextTick } from 'vue'
import { Download, FolderOpen, Globe2, Moon, RefreshCw, Sun, Upload, User } from 'lucide-vue-next'
import { Call, Events } from '@wailsio/runtime'
import { useAppContext } from '../../composables/appContext'
import { conciseToggleFocus } from '../../utils/settingsNav'
import InstallDialog from '../../components/InstallDialog.vue'
import { renderMarkdown } from '../../components/chat/chatFormatters.js'

const { t, config, bootstrap, pickSessionDirectory, persist, appUpdateAvailable, openGlobalPromptConfig } = useAppContext()
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
const appStatus = ref(null) // AppUpdateStatus | null

async function checkAppUpdate() {
  if (appChecking.value) return
  appChecking.value = true
  appStatus.value = null
  try {
    const res = await Call.ByName('codingto/internal/app.App.CheckAppUpdate')
    appStatus.value = res || {}
    // 探测到新版本时点亮侧边栏设置菜单红点；无论来源于启动静默检查还是手动点检。
    if (res && res.available) appUpdateAvailable.value = true
  } catch (e) {
    appStatus.value = { error: String(e) }
  } finally {
    appChecking.value = false
  }
}

// 进入设置页即刷新一次更新状态：既为展示最新结果，也在启动静默检查未命中时
// 补齐侧边栏红点。红点不再因"打开设置"而被清除，仅在用户真正去下载时消除，
// 保证"检测到新版 → 菜单红点常驻提醒"这一预期行为。
onMounted(() => {
  if (!appStatus.value) void checkAppUpdate()
  // 聊天区「精简模式」提示跳转：定位并高亮「精简对话」开关。
  if (conciseToggleFocus.value) {
    conciseToggleFocus.value = false
    nextTick(() => requestAnimationFrame(scrollToConciseToggle))
  }
})

// 「精简对话」开关定位：滚动到开关所在行并短暂高亮，提示用户点击位置。
const conciseRowEl = ref(null)
const conciseRowHighlight = ref(false)
let conciseHighlightTimer = 0
function scrollToConciseToggle() {
  const el = conciseRowEl.value
  if (!el) return
  el.scrollIntoView({ block: 'center', behavior: 'smooth' })
  conciseRowHighlight.value = true
  clearTimeout(conciseHighlightTimer)
  conciseHighlightTimer = window.setTimeout(() => { conciseRowHighlight.value = false }, 2600)
}
onBeforeUnmount(() => { clearTimeout(conciseHighlightTimer) })

function downloadAndInstallApp() {
  if (!appStatus.value?.downloadUrl) return
  // 用户已着手处理更新，消除侧边栏红点（再次进入设置页若仍有新版会重新点亮）。
  appUpdateAvailable.value = false
  // Open the GitHub release page in the browser so the user can download manually.
  window.open(appStatus.value.downloadUrl, '_blank', 'noopener')
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
// 界面字号档位：小（默认，12/13/14px）、中（+1）、大（+2），由 App.vue applyTheme() 改写 --fs-* 变量生效。
function setFontSize(value) {
  config.preferences.fontSize = value
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

// 对话展示形式：'side' 左右、'left' 靠左（默认）。
function setChatLayout(value) {
  config.preferences.chatLayout = value
  persist()
}
// 代码对比方式：'unified' 上下（默认）、'split' 左右，作用于所有代码对比视图。
function setDiffMode(value) {
  config.preferences.diffMode = value
  persist()
}
// 头像昵称展示：默认开启，关闭后对话详情不再显示 agent / 用户头像与昵称。
function setShowIdentity(value) {
  config.preferences.showIdentity = value
  persist()
}
// 精简对话：默认关闭，开启后对话详情中的思考与工具调用折叠为精简摘要块。
function setConciseChat(value) {
  config.preferences.conciseChat = !!value
  persist()
}
function setSubagentConcurrency(value) {
  config.subagentConcurrency = Math.min(4, Math.max(1, Number(value) || 4))
  persist()
}
// 工具调用超时：限制单次工具（bash）执行时长，默认 10 分钟，最大 1 小时（60 分钟）。
function setToolExecutionTimeout(value) {
  config.toolExecutionTimeoutMinutes = Math.min(60, Math.max(1, Number(value) || 10))
  persist()
}
// 会话数据自动清理：启动时按保留天数清理过期会话（DB 记录 + 磁盘会话目录）。
function setSessionCleanupEnabled(value) {
  config.sessionCleanupEnabled = !!value
  persist()
}
function setSessionCleanupDays(value) {
  config.sessionCleanupDays = Math.min(100, Math.max(1, Number(value) || 60))
  persist()
}
// 计划审批 / 任务完成时是否发送系统通知（默认开启）。
function setSystemNotification(value) {
  config.systemNotificationEnabled = !!value
  persist()
}

// 个人信息：头像上传（压缩为 data URL 存入 config）+ 昵称
const avatarInput = ref(null)
function onAvatarPicked(event) {
  const input = event.target
  const file = input.files && input.files[0]
  input.value = ''
  if (!file) return
  resizeImageFile(file, 128).then((dataUrl) => {
    config.userProfile.avatar = dataUrl
    persist()
  }).catch(() => {})
}
function clearAvatar() {
  config.userProfile.avatar = ''
  persist()
}
function onProfileNameChange() {
  persist()
}
function resizeImageFile(file, maxSize) {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onerror = () => reject(reader.error)
    reader.onload = () => {
      const img = new Image()
      img.onerror = () => reject(new Error('image load failed'))
      img.onload = () => {
        const scale = Math.min(1, maxSize / Math.max(img.width, img.height))
        const w = Math.max(1, Math.round(img.width * scale))
        const h = Math.max(1, Math.round(img.height * scale))
        const canvas = document.createElement('canvas')
        canvas.width = w
        canvas.height = h
        const ctx = canvas.getContext('2d')
        ctx.drawImage(img, 0, 0, w, h)
        const type = file.type === 'image/png' ? 'image/png' : 'image/jpeg'
        resolve(canvas.toDataURL(type, 0.85))
      }
      img.src = reader.result
    }
    reader.readAsDataURL(file)
  })
}
</script>

<template>
<section class="content-page settings-page">
          <div class="page-heading"><div><h2>{{ t.settingsTitle }}</h2><p>{{ t.settingsIntro }}</p></div></div>

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
            <div class="setting-row">
              <div><label>{{ t.fontSize }}</label></div>
              <div class="segmented">
                <button :class="{ active: config.preferences.fontSize !== 'medium' && config.preferences.fontSize !== 'large' }" @click="setFontSize('small')">{{ t.fontSizeSmall }}</button>
                <button :class="{ active: config.preferences.fontSize === 'medium' }" @click="setFontSize('medium')">{{ t.fontSizeMedium }}</button>
                <button :class="{ active: config.preferences.fontSize === 'large' }" @click="setFontSize('large')">{{ t.fontSizeLarge }}</button>
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
            <h2>{{ t.conversation }}</h2>
            <div class="setting-row">
              <div><label>{{ t.chatLayout }}</label></div>
              <div class="segmented">
                <button :class="{ active: config.preferences.chatLayout === 'side' }" @click="setChatLayout('side')">{{ t.chatLayoutSide }}</button>
                <button :class="{ active: config.preferences.chatLayout === 'left' }" @click="setChatLayout('left')">{{ t.chatLayoutLeft }}</button>
              </div>
            </div>
            <div ref="conciseRowEl" class="setting-row setting-row--toggle" :class="{ 'is-focused': conciseRowHighlight }">
              <div>
                <label>{{ t.conciseChat }}</label>
                <small>{{ t.conciseChatHint }}</small>
              </div>
              <label class="switch" :title="t.conciseChat">
                <input type="checkbox" :checked="config.preferences.conciseChat === true" @change="setConciseChat($event.target.checked)" />
                <span class="switch__track"></span>
              </label>
            </div>
            <div class="setting-row">
              <div><label>{{ t.diffMode }}</label></div>
              <div class="segmented">
                <button :class="{ active: config.preferences.diffMode !== 'split' }" @click="setDiffMode('unified')">{{ t.diffModeUnified }}</button>
                <button :class="{ active: config.preferences.diffMode === 'split' }" @click="setDiffMode('split')">{{ t.diffModeSplit }}</button>
              </div>
            </div>
            <div class="setting-row">
              <div>
                <label>{{ t.showIdentity }}</label>
                <small>{{ t.showIdentityHint }}</small>
              </div>
              <div class="segmented">
                <button :class="{ active: config.preferences.showIdentity !== false }" @click="setShowIdentity(true)">{{ t.on }}</button>
                <button :class="{ active: config.preferences.showIdentity === false }" @click="setShowIdentity(false)">{{ t.off }}</button>
              </div>
            </div>
          </div>

          <div class="settings-section">
            <h2>{{ t.agentRuntimeSettings }}</h2>
            <div class="setting-row">
              <div>
                <label>{{ t.subagentConcurrency }}</label>
                <small>{{ t.subagentConcurrencyHint }}</small>
              </div>
              <div class="segmented" role="group" :aria-label="t.subagentConcurrency">
                <button
                  v-for="count in 4"
                  :key="count"
                  :class="{ active: config.subagentConcurrency === count }"
                  @click="setSubagentConcurrency(count)"
                >{{ count }}</button>
              </div>
            </div>
            <div class="setting-row">
              <div>
                <label>{{ t.toolExecutionTimeout }}</label>
                <small>{{ t.toolExecutionTimeoutHint }}</small>
              </div>
              <input
                type="number"
                min="1"
                max="60"
                class="tool-timeout-input"
                :value="config.toolExecutionTimeoutMinutes || 10"
                @change="setToolExecutionTimeout($event.target.value)"
              />
            </div>
            <div class="setting-row">
              <div>
                <label>{{ t.systemNotification }}</label>
                <small>{{ t.systemNotificationHint }}</small>
              </div>
              <div class="segmented">
                <button :class="{ active: config.systemNotificationEnabled !== false }" @click="setSystemNotification(true)">{{ t.on }}</button>
                <button :class="{ active: config.systemNotificationEnabled === false }" @click="setSystemNotification(false)">{{ t.off }}</button>
              </div>
            </div>
            <div class="setting-row">
              <div>
                <label>{{ t.globalPromptConfig }}</label>
                <small>{{ t.globalPromptConfigHint }}</small>
              </div>
              <button class="secondary-button" type="button" @click="openGlobalPromptConfig">{{ t.globalPromptConfigAction }}</button>
            </div>
          </div>

          <div class="settings-section">
            <h2>{{ t.profile }}</h2>
            <div class="setting-row setting-row--avatar">
              <div>
                <label>{{ t.profile_avatar }}</label>
                <small>{{ t.profile_avatar_hint }}</small>
              </div>
              <div class="avatar-field">
                <div class="avatar-preview">
                  <img v-if="config.userProfile.avatar" :src="config.userProfile.avatar" alt="" />
                  <User v-else :size="22" />
                </div>
                <div class="avatar-actions">
                  <button class="secondary-button" type="button" @click="avatarInput?.click()">
                    <Upload :size="14" />{{ t.profile_avatar_upload }}
                  </button>
                  <button v-if="config.userProfile.avatar" class="secondary-button" type="button" @click="clearAvatar">
                    {{ t.profile_avatar_clear }}
                  </button>
                  <input ref="avatarInput" type="file" accept="image/*" hidden @change="onAvatarPicked" />
                </div>
              </div>
            </div>
            <div class="setting-row">
              <div><label>{{ t.profile_name }}</label></div>
              <div class="name-field">
                <input v-model.trim="config.userProfile.name" :placeholder="t.profile_name_placeholder" @change="onProfileNameChange" @blur="onProfileNameChange" />
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
                <input v-model.trim="config.sessionDir" :placeholder="t.sessionStorageDefault" @change="persist()" />
                <button class="secondary-button" type="button" @click="pickDir">
                  <FolderOpen :size="14" />{{ t.choose }}
                </button>
              </div>
            </div>
            <div class="setting-row setting-row--toggle">
              <div>
                <label>{{ t.sessionCleanup }}</label>
                <small>{{ t.sessionCleanupHint }}</small>
              </div>
              <label class="switch" :title="t.sessionCleanup">
                <input type="checkbox" :checked="config.sessionCleanupEnabled === true" @change="setSessionCleanupEnabled($event.target.checked)" />
                <span class="switch__track"></span>
              </label>
            </div>
            <div class="setting-row">
              <div>
                <label>{{ t.sessionCleanupDays }}</label>
                <small>{{ t.sessionCleanupRange }}。{{ t.sessionCleanupNextStart }}</small>
              </div>
              <div class="cleanup-days-field">
                <input
                  type="number"
                  min="1"
                  max="100"
                  class="tool-timeout-input"
                  :value="config.sessionCleanupDays || 60"
                  @change="setSessionCleanupDays($event.target.value)"
                />
                <span class="cleanup-days-unit">{{ t.sessionCleanupDaysUnit }}</span>
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
              <div class="app-update-actions">
                <template v-if="appStatus && appStatus.available">
                  <strong class="app-update-new">{{ t.new_version_available.replace('{latest}', appStatus.latest) }}</strong>
                  <button class="link-button" type="button" @click="downloadAndInstallApp">
                    <Download :size="14" />{{ t.download_and_install }}
                  </button>
                </template>
                <span v-else-if="appStatus && appStatus.error" class="app-update-error">{{ appStatus.error }}</span>
                <span v-else-if="appStatus && appStatus.latest" class="app-update-note">{{ t.already_latest.replace('{version}', appStatus.current) }}</span>
                <span v-else-if="appStatus" class="app-update-note">{{ t.no_release }}</span>
                <button class="secondary-button" type="button" :disabled="appChecking" @click="checkAppUpdate">
                  <RefreshCw :size="15" :class="{ spinning: appChecking }" />{{ appChecking ? t.checking_new_version : t.check_new_version }}
                </button>
              </div>
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
.settings-note {
  margin: 12px 0 0;
  color: var(--muted);
  font-size: var(--fs-12);
  line-height: 1.55;
}
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
.update-log-md :deep(h1) { font-size: var(--fs-14); }
.update-log-md :deep(h2) { font-size: var(--fs-14); }
.update-log-md :deep(h3),
.update-log-md :deep(h4) { font-size: var(--fs-14); }
.update-log-md :deep(ul),
.update-log-md :deep(ol) { margin: 7px 0 10px; padding-left: 22px; }
.update-log-md :deep(li + li) { margin-top: 3px; }
.update-log-md :deep(a) { color: var(--accent); text-decoration: underline; text-underline-offset: 2px; }
.update-log-md :deep(strong) { font-weight: 700; }
.update-log-md :deep(blockquote) { margin: 9px 0; padding: 2px 0 2px 11px; border-left: 3px solid var(--border); color: var(--muted); }
.update-log-md :deep(hr) { margin: 14px 0; border: 0; border-top: 1px solid var(--border-soft); }
.update-log-md :deep(code) { padding: 1px 4px; border-radius: 4px; background: var(--surface-2); font: var(--fs-12)/1.5 "SFMono-Regular", Consolas, monospace; }
.update-log-md :deep(pre) { max-width: 100%; margin: 9px 0; padding: 10px 11px; overflow-x: auto; border: 1px solid var(--border-soft); border-radius: 7px; background: var(--surface-2); white-space: pre; }
.update-log-md :deep(pre code) { padding: 0; background: transparent; font-size: var(--fs-12); }
.update-log-md :deep(table) { display: block; max-width: 100%; margin: 10px 0; overflow-x: auto; border-collapse: collapse; }
.update-log-md :deep(th),
.update-log-md :deep(td) { min-width: 90px; padding: 6px 9px; border: 1px solid var(--border); text-align: left; vertical-align: top; }
.update-log-md :deep(th) { background: var(--surface-2); font-weight: 650; }

.app-update-actions {
  display: inline-flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}
.link-button {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 0;
  border: 0;
  background: transparent;
  color: var(--accent);
  font-size: var(--fs-13);
  cursor: pointer;
  text-decoration: underline;
  text-underline-offset: 2px;
}
.link-button:hover { opacity: .8; }
.app-update-new { color: var(--accent); font-weight: 650; }
.app-update-note { color: var(--muted); }
.app-update-error { color: #ef4444; }
.tool-timeout-input {
  width: 76px;
  padding: 5px 8px;
  border: 1px solid var(--border);
  border-radius: 6px;
  background: var(--surface-2);
  color: var(--text);
  font-size: var(--fs-13);
  text-align: center;
}
.tool-timeout-input:focus {
  outline: none;
  border-color: var(--accent);
}
.tool-timeout-input::-webkit-outer-spin-button,
.tool-timeout-input::-webkit-inner-spin-button {
  opacity: 1;
}
.cleanup-days-field {
  display: flex;
  align-items: center;
  gap: 7px;
}
.cleanup-days-unit {
  color: var(--muted);
  font-size: var(--fs-13);
}
</style>
