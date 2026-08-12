<script setup>
import { computed } from 'vue'
import { Clock3 } from 'lucide-vue-next'
import { formatDuration } from './chatFormatters.js'

const props = defineProps({
  title: { type: String, default: '' },
  sessionId: { type: Number, default: 0 },
  createdAt: { type: Number, default: 0 },
  connected: { type: Boolean, default: false },
  executionElapsedMs: { type: Number, default: 0 },
  executionRunning: { type: Boolean, default: false },
  t: { type: Object, required: true }
})

const createdAtLabel = computed(() => {
  if (!props.createdAt) return ''
  const d = new Date(props.createdAt)
  if (Number.isNaN(d.getTime())) return ''
  const pad = n => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
})
const createdAtTooltip = computed(() => createdAtLabel.value ? `${props.t.chatSessionCreated} ${createdAtLabel.value}` : '')
</script>

<template>
  <header class="chat-main__head">
    <div class="chat-main__title" :title="createdAtTooltip">
      <small v-if="title && sessionId > 0">#{{ sessionId }}</small>
      <span>{{ title || t.chatSelectOrCreate }}</span>
    </div>
    <div class="chat-main__head-actions">
      <span v-if="title" class="execution-time" :class="{ 'execution-time--running': executionRunning }">
        <Clock3 :size="13" />{{ t.execute }} {{ formatDuration(executionElapsedMs) }}
      </span>
      <span class="chat-status" :class="{ 'chat-status--on': connected }">
        <span class="chat-status__dot" />{{ connected ? t.chatConnected : t.chatDisconnected }}
      </span>
    </div>
  </header>
</template>

<style scoped src="../../styles/chat/header.css"></style>
