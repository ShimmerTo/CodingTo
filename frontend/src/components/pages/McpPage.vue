<script setup>
import { Network, Plus } from 'lucide-vue-next'
import { useAppContext } from '../../composables/appContext'

const { t } = useAppContext()
</script>

<template>
<section class="content-page">
          <div class="page-heading"><div><h2>{{ t.mcpTitle }}</h2><p>{{ t.mcpIntro }}</p></div><Network :size="28" /></div>
          <div class="integration-panel">
            <article class="integration-hero">
              <div class="integration-hero__icon"><Network :size="24" /></div>
              <div><h2>{{ t.mcpServers }}</h2><p>{{ t.mcpServersIntro }}</p></div>
              <button class="secondary-button" disabled><Plus :size="14" />{{ t.addServer }}</button>
            </article>
            <div class="empty-integration">
              <Network :size="28" />
              <strong>{{ t.noMcpServers }}</strong>
              <p>{{ t.noMcpServersIntro }}</p>
            </div>
          </div>
        </section>
</template>
