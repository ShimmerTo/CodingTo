<script setup>
import { computed, ref } from 'vue'
import { ExternalLink, Image, Network, PackagePlus, RefreshCw, Settings } from 'lucide-vue-next'
import { useAppContext } from '../../composables/appContext'
import InstallDialog from '../InstallDialog.vue'

const { t, refreshExtensions, extensionSnapshot, extensionBusy, figma, showFigmaConfig, figmaAction, installGlobalPackage } = useAppContext()
const globalMcp = computed(() => extensionSnapshot.value?.globalMcp || [])
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
    await installGlobalPackage('mcp', packageName.value.trim())
    showInstallModal.value = false
  } catch {}
}
</script>

<template>
  <section class="content-page">
    <div class="page-heading">
      <div><h2>{{ t.mcpTitle }}</h2><p>{{ t.mcpIntro }}</p></div>
      <div class="page-heading__actions">
        <button class="secondary-button compact" @click="openInstallModal()"><PackagePlus :size="14" />{{ t.installGlobalMcp }}</button>
        <button class="icon-button page-refresh" :title="t.refresh" @click="refreshExtensions"><RefreshCw :size="17" /></button>
      </div>
    </div>

    <div class="plugin-section">
      <div class="plugin-section__title">
        <span>{{ t.mcpServers }}</span>
        <small>{{ t.mcpServersIntro }}</small>
      </div>
      <article class="plugin-row">
        <div class="plugin-icon"><Image :size="19" /></div>
        <div class="plugin-copy">
          <div class="plugin-name">
            <strong>{{ t.figma }}</strong>
            <span class="status-dot" :class="{ active: figma.installed && figma.hasToken, missing: !figma.installed || !figma.hasToken }"></span>
            <small>{{ !figma.installed ? t.notInstalled : (figma.hasToken ? t.figmaAuthorized : t.figmaNotAuthorized) }}</small>
          </div>
          <p>{{ t.figmaDescription }}</p>
          <code>{{ t.mcpTransportStdio }} · figma-developer-mcp</code>
          <code v-if="figma.activeAuthorizationName">{{ figma.activeAuthorizationName }} · {{ figma.authorizationCount }}</code>
          <code v-if="figma.version">{{ figma.version }}</code>
        </div>
        <div class="plugin-actions">
          <a href="https://www.figma.com" target="_blank" rel="noreferrer" :title="t.homepage"><ExternalLink :size="14" /></a>
          <button :class="figma.installed ? 'secondary-button' : 'primary-button'" :disabled="extensionBusy === 'figma-install'" @click="figmaAction('install')">
            <RefreshCw v-if="figma.installed || extensionBusy === 'figma-install'" :class="{ spin: extensionBusy === 'figma-install' }" :size="13" />{{ figma.installed ? t.update : t.runInstall }}
          </button>
          <button v-if="figma.installed" class="secondary-button" @click="showFigmaConfig = true"><Settings :size="13" />{{ t.configure }}</button>
        </div>
      </article>
    </div>

    <div v-if="globalMcp.length" class="plugin-section">
      <div class="plugin-section__title"><span>{{ t.otherMcpServers }}</span></div>
      <article v-for="server in globalMcp" :key="server.key" class="plugin-row">
        <div class="plugin-icon"><Network :size="19" /></div>
        <div class="plugin-copy">
          <div class="plugin-name">
            <strong>{{ server.name || server.key }}</strong>
            <span class="status-dot" :class="{ active: server.installed, missing: !server.installed }"></span>
            <small>{{ server.installed ? t.installed : t.notInstalled }}</small>
          </div>
          <p>{{ server.description || server.key }}</p>
          <code>{{ server.installHint || `npm install -g ${server.key}` }}</code>
          <code v-if="server.version">{{ server.version }}</code>
        </div>
        <div class="plugin-actions">
          <button :class="server.installed ? 'secondary-button' : 'primary-button'" :disabled="extensionBusy === 'global-mcp-install'" @click="openInstallModal(server.key)">
            <RefreshCw v-if="server.installed" :size="13" />{{ server.installed ? t.update : t.runInstall }}
          </button>
        </div>
      </article>
    </div>

    <InstallDialog
      v-if="showInstallModal"
      mode="command"
      :title="t.installGlobalMcp"
      :hint="t.installGlobalMcpHint"
      :command="packageName"
      :preview-command="previewCommand"
      :command-placeholder="t.npmPackagePlaceholder"
      :running="extensionBusy === 'global-mcp-install'"
      :run-text="t.runInstall"
      @update:command="packageName = $event"
      @run="runInstall"
      @close="showInstallModal = false"
    />
  </section>
</template>
