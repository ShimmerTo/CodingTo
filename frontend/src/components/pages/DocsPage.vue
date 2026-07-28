<script setup>
import { BookOpen } from 'lucide-vue-next'
import { useAppContext } from '../../composables/appContext'

const { t, bootstrap } = useAppContext()
</script>

<template>
<section class="content-page docs-page">
          <div class="page-heading"><div><h2>{{ t.docsTitle }}</h2><p>{{ t.docsIntro }}</p></div><BookOpen :size="28" /></div>
          <article class="doc-card">
            <h2>运行环境</h2>
            <p>{{ t.installPi }}</p>
            <code>{{ bootstrap?.os === 'windows' ? 'npm.cmd install -g --ignore-scripts @earendil-works/pi-coding-agent' : 'npm install -g --ignore-scripts @earendil-works/pi-coding-agent' }}</code>
          </article>
          <article class="doc-card">
            <h2>Cross-platform runtime</h2>
            <p>Windows WebView2 · macOS WKWebView · Go {{ bootstrap?.version }}</p>
          </article>
        </section>
</template>
