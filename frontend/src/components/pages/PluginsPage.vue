<script setup>
import { computed } from 'vue'
import { Drama, ExternalLink, Globe2, Image, RefreshCw, Settings, Zap } from 'lucide-vue-next'
import { useAppContext } from '../../composables/appContext'

const { t, refreshExtensions, extensionSnapshot, extensionBusy, extensionAction, figma, showFigmaConfig, figmaAction } = useAppContext()
const rtkRuntime = computed(() => (extensionSnapshot.value?.tools || []).find(tool => tool.key === 'rtk') || null)
const browserRuntime = computed(() => (extensionSnapshot.value?.tools || []).find(tool => tool.key === 'agent-browser') || null)
const playwrightRuntime = computed(() => (extensionSnapshot.value?.tools || []).find(tool => tool.key === 'playwright') || null)
</script>

<template>
  <section class="content-page">
    <div class="page-heading">
      <div><h2>{{ t.pluginsTitle }}</h2><p>{{ t.pluginsIntro }}</p></div>
      <button class="icon-button page-refresh" :title="t.refresh" @click="refreshExtensions"><RefreshCw :size="17" /></button>
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
          <code v-if="rtkRuntime?.version">{{ rtkRuntime.version }}</code>
        </div>
        <div class="plugin-actions">
          <a href="https://github.com/rtk-ai/rtk" target="_blank" rel="noreferrer" :title="t.homepage"><ExternalLink :size="14" /></a>
          <button v-if="!rtkRuntime?.installed" class="primary-button" :disabled="extensionBusy === 'rtk'" @click="extensionAction(rtkRuntime || { key: 'rtk', name: 'RTK' }, 'install')">
            <RefreshCw v-if="extensionBusy === 'rtk'" class="spin" :size="13" />{{ t.runInstall }}
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
          <code v-if="playwrightRuntime?.version">{{ playwrightRuntime.version }}</code>
        </div>
        <div class="plugin-actions">
          <a href="https://playwright.dev/" target="_blank" rel="noreferrer" :title="t.homepage"><ExternalLink :size="14" /></a>
          <button :class="playwrightRuntime?.installed ? 'secondary-button' : 'primary-button'" :disabled="extensionBusy === 'playwright'" @click="extensionAction(playwrightRuntime || { key: 'playwright', name: 'Playwright' }, 'install')">
            <RefreshCw v-if="playwrightRuntime?.installed || extensionBusy === 'playwright'" :class="{ spin: extensionBusy === 'playwright' }" :size="13" />{{ playwrightRuntime?.installed ? t.update : t.runInstall }}
          </button>
        </div>
      </article>
      <article class="plugin-row">
        <div class="plugin-icon"><Image :size="19" /></div>
        <div class="plugin-copy">
          <div class="plugin-name">
            <strong>{{ t.figma }}</strong>
            <span class="status-dot" :class="{ active: figma.installed && figma.hasToken, missing: !figma.installed || !figma.hasToken }"></span>
            <small>{{ !figma.installed ? t.notInstalled : (figma.hasToken ? t.figmaAuthorized : t.figmaNotAuthorized) }}</small>
          </div>
          <p>{{ t.figmaDescription }}</p>
          <code v-if="figma.activeAuthorizationName">{{ figma.activeAuthorizationName }} · {{ figma.authorizationCount }}</code>
          <code v-if="figma.version">{{ figma.version }}</code>
        </div>
        <div class="plugin-actions">
          <a href="https://www.figma.com" target="_blank" rel="noreferrer" :title="t.homepage"><ExternalLink :size="14" /></a>
          <button v-if="!figma.installed" class="primary-button" :disabled="extensionBusy === 'figma-install'" @click="figmaAction('install')">
            <RefreshCw v-if="extensionBusy === 'figma-install'" class="spin" :size="13" />{{ t.runInstall }}
          </button>
          <button v-else class="secondary-button" @click="showFigmaConfig = true"><Settings :size="13" />{{ t.configure }}</button>
        </div>
      </article>
    </div>
  </section>
</template>
