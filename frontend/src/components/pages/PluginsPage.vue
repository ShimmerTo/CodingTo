<script setup>
import { computed, ref } from 'vue'
import { Drama, ExternalLink, Globe2, PackagePlus, RefreshCw, Trash2, Zap } from 'lucide-vue-next'
import { useAppContext } from '../../composables/appContext'
import InstallDialog from '../InstallDialog.vue'

const { t, refreshExtensions, extensionSnapshot, extensionBusy, extensionAction, installGlobalPackage, removeGlobalPackage } = useAppContext()
const rtkRuntime = computed(() => (extensionSnapshot.value?.tools || []).find(tool => tool.key === 'rtk') || null)
const browserRuntime = computed(() => (extensionSnapshot.value?.tools || []).find(tool => tool.key === 'agent-browser') || null)
const playwrightRuntime = computed(() => (extensionSnapshot.value?.tools || []).find(tool => tool.key === 'playwright') || null)
const globalPlugins = computed(() => extensionSnapshot.value?.globalPlugins || [])
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
    await installGlobalPackage('plugin', packageName.value.trim())
    showInstallModal.value = false
  } catch {}
}

function confirmRemove(name) {
  if (!window.confirm(t.confirmRemoveGlobalPlugin.replace('{name}', name))) return
  removeGlobalPackage('plugin', name)
}
</script>

<template>
  <section class="content-page">
    <div class="page-heading">
      <div><h2>{{ t.pluginsTitle }}</h2><p>{{ t.pluginsIntro }}</p></div>
      <div class="page-heading__actions">
        <button class="icon-button page-refresh" :title="t.refresh" @click="refreshExtensions"><RefreshCw :size="17" /></button>
      </div>
    </div>

    <div class="plugin-section">
      <div class="plugin-section__title">
        <span>{{ t.globalExtensions }}</span>
        <small>{{ t.globalExtensionsHint }}</small>
      </div>
      <article class="plugin-row">
        <div class="plugin-icon"><Zap :size="19" /></div>
        <div class="plugin-copy">
          <div class="plugin-name">
            <strong>RTK</strong>
            <span class="status-dot" :class="{ active: rtkRuntime?.installed, missing: !rtkRuntime?.installed }"></span>
            <small>{{ rtkRuntime?.installed ? t.installed : t.notInstalled }}</small>
          </div>
          <p>{{ t.globalRtkDescription }}</p>
          <code v-if="rtkRuntime?.installHint">{{ rtkRuntime.installHint }}</code>
          <code v-if="rtkRuntime?.version">{{ rtkRuntime.version }}</code>
        </div>
        <div class="plugin-actions">
          <a href="https://github.com/rtk-ai/rtk" target="_blank" rel="noreferrer" :title="t.homepage"><ExternalLink :size="14" /></a>
          <button :class="rtkRuntime?.installed ? 'secondary-button' : 'primary-button'" :disabled="extensionBusy === 'rtk'" @click="extensionAction(rtkRuntime || { key: 'rtk', name: 'RTK' }, 'install')">
            <RefreshCw v-if="rtkRuntime?.installed || extensionBusy === 'rtk'" :class="{ spin: extensionBusy === 'rtk' }" :size="13" />{{ rtkRuntime?.installed ? t.update : t.runInstall }}
          </button>
        </div>
      </article>
      <article class="plugin-row">
        <div class="plugin-icon"><Globe2 :size="19" /></div>
        <div class="plugin-copy">
          <div class="plugin-name">
            <strong>{{ t.globalBrowserRuntime }}</strong>
            <span class="status-dot" :class="{ active: browserRuntime?.installed, missing: !browserRuntime?.installed }"></span>
            <small>{{ browserRuntime?.installed ? t.installed : t.notInstalled }}</small>
          </div>
          <p>{{ t.globalBrowserRuntimeDescription }}</p>
          <code>{{ browserRuntime?.installHint || 'npm install -g agent-browser' }}</code>
          <code v-if="browserRuntime?.version">{{ browserRuntime.version }}</code>
        </div>
        <div class="plugin-actions">
          <a href="https://agent-browser.dev/" target="_blank" rel="noreferrer" :title="t.homepage"><ExternalLink :size="14" /></a>
          <button :class="browserRuntime?.installed ? 'secondary-button' : 'primary-button'" :disabled="extensionBusy === 'agent-browser'" @click="extensionAction(browserRuntime || { key: 'agent-browser', name: t.globalBrowserRuntime }, 'install')">
            <RefreshCw v-if="browserRuntime?.installed || extensionBusy === 'agent-browser'" :class="{ spin: extensionBusy === 'agent-browser' }" :size="13" />{{ browserRuntime?.installed ? t.update : t.runInstall }}
          </button>
        </div>
      </article>
      <article class="plugin-row">
        <div class="plugin-icon"><Drama :size="19" /></div>
        <div class="plugin-copy">
          <div class="plugin-name">
            <strong>Playwright</strong>
            <span class="status-dot" :class="{ active: playwrightRuntime?.installed, missing: !playwrightRuntime?.installed }"></span>
            <small>{{ playwrightRuntime?.installed ? t.installed : t.notInstalled }}</small>
          </div>
          <p>{{ t.globalPlaywrightDescription }}</p>
          <code>{{ playwrightRuntime?.installHint || 'npm install -g playwright && playwright install chromium' }}</code>
          <code v-if="playwrightRuntime?.version">{{ playwrightRuntime.version }}</code>
        </div>
        <div class="plugin-actions">
          <a href="https://playwright.dev/" target="_blank" rel="noreferrer" :title="t.homepage"><ExternalLink :size="14" /></a>
          <button :class="playwrightRuntime?.installed ? 'secondary-button' : 'primary-button'" :disabled="extensionBusy === 'playwright'" @click="extensionAction(playwrightRuntime || { key: 'playwright', name: 'Playwright' }, 'install')">
            <RefreshCw v-if="playwrightRuntime?.installed || extensionBusy === 'playwright'" :class="{ spin: extensionBusy === 'playwright' }" :size="13" />{{ playwrightRuntime?.installed ? t.update : t.runInstall }}
          </button>
        </div>
      </article>
    </div>

    <div v-if="globalPlugins.length" class="plugin-section">
      <div class="plugin-section__title"><span>{{ t.otherGlobalPlugins }}</span></div>
      <article v-for="plugin in globalPlugins" :key="plugin.key" class="plugin-row">
        <div class="plugin-icon"><PackagePlus :size="19" /></div>
        <div class="plugin-copy">
          <div class="plugin-name">
            <strong>{{ plugin.name || plugin.key }}</strong>
            <span class="status-dot" :class="{ active: plugin.installed, missing: !plugin.installed }"></span>
            <small>{{ plugin.installed ? t.installed : t.notInstalled }}</small>
          </div>
          <p>{{ plugin.description || plugin.key }}</p>
          <code>{{ plugin.installHint || `npm install -g ${plugin.key}` }}</code>
          <code v-if="plugin.version">{{ plugin.version }}</code>
        </div>
        <div class="plugin-actions">
          <button :class="plugin.installed ? 'secondary-button' : 'primary-button'" :disabled="extensionBusy === 'global-plugin-install'" @click="openInstallModal(plugin.key)">
            <RefreshCw v-if="plugin.installed" :size="13" />{{ plugin.installed ? t.update : t.runInstall }}
          </button>
          <button class="icon-button danger" :disabled="extensionBusy === 'global-plugin-remove'" :title="t.delete" @click="confirmRemove(plugin.key)">
            <Trash2 :size="14" />
          </button>
        </div>
      </article>
    </div>

    <InstallDialog
      v-if="showInstallModal"
      mode="command"
      :title="t.installGlobalPlugin"
      :hint="t.installGlobalPluginHint"
      :command="packageName"
      :preview-command="previewCommand"
      :command-placeholder="t.npmPackagePlaceholder"
      :running="extensionBusy === 'global-plugin-install'"
      :run-text="t.runInstall"
      @update:command="packageName = $event"
      @run="runInstall"
      @close="showInstallModal = false"
    />
  </section>
</template>

<style scoped>
.icon-button.danger {
  color: #ef4444;
}
.icon-button.danger:hover:not(:disabled) {
  background: rgba(239, 68, 68, 0.12);
}
</style>
