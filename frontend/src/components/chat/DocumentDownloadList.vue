<script setup>
import { reactive } from 'vue'
import { Download } from 'lucide-vue-next'
import { downloadSessionArtifact } from '../../backend.js'

const props = defineProps({
  files: { type: Array, required: true },
  sessionId: { type: Number, required: true },
  t: { type: Object, required: true }
})

const status = reactive({})
const errors = reactive({})

function formatSize(bytes) {
  if (bytes === undefined || bytes === null) return ''
  const units = ['B', 'KB', 'MB', 'GB']
  let i = 0
  let n = Number(bytes) || 0
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024
    i += 1
  }
  return `${i === 0 ? n : n.toFixed(1)} ${units[i]}`
}

async function onDownload(file) {
  const key = file.path
  status[key] = 'loading'
  errors[key] = ''
  try {
    const dest = await downloadSessionArtifact(file.path, props.sessionId)
    if (dest) {
      status[key] = 'done'
    } else {
      status[key] = 'idle'
    }
  } catch (err) {
    status[key] = 'error'
    errors[key] = String(err?.message || err)
  }
}

function buttonLabel(file) {
  const state = status[file.path]
  if (state === 'loading') return props.t.downloadListSaving
  if (state === 'done') return props.t.downloadListSaved
  return props.t.downloadListSave
}
</script>

<template>
  <div class="doc-download-list">
    <div
      v-for="file in files"
      :key="file.path"
      class="doc-download-row"
    >
      <Download class="doc-download-icon" :size="13" />
      <div class="doc-download-meta">
        <span class="doc-download-name">{{ file.name }}</span>
        <span v-if="formatSize(file.size)" class="doc-download-size">{{ formatSize(file.size) }}</span>
      </div>
      <span v-if="status[file.path] === 'done'" class="doc-download-saved">{{ props.t.downloadListSaved }}</span>
      <span v-if="status[file.path] === 'error'" class="doc-download-error" :title="errors[file.path]">{{ props.t.downloadListFailed }}</span>
      <button
        type="button"
        class="doc-download-btn"
        :disabled="status[file.path] === 'loading'"
        :title="`${props.t.downloadListSave} ${file.name}`"
        @click="onDownload(file)"
      >
        <Download :size="14" />
        <span>{{ buttonLabel(file) }}</span>
      </button>
    </div>
  </div>
</template>

<style scoped>
.doc-download-list {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin: 6px 0 2px;
  padding: 8px 10px;
  border: 1px solid var(--border-soft, #2a2a33);
  border-radius: 8px;
  background: var(--surface-raised, rgba(255, 255, 255, 0.02));
}
.doc-download-row {
  display: flex;
  align-items: center;
  gap: 10px;
  min-height: 28px;
}
.doc-download-icon {
  flex: none;
  color: #8c6426;
}
.doc-download-meta {
  display: flex;
  flex-direction: column;
  min-width: 0;
  flex: 1 1 auto;
}
.doc-download-name {
  font-size: 12.5px;
  color: var(--text, #e7e7ea);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.doc-download-size {
  font-size: 11px;
  color: var(--text-muted, #9a9aa3);
}
.doc-download-saved {
  font-size: 11px;
  color: var(--success, #4ec07a);
  white-space: nowrap;
}
.doc-download-error {
  font-size: 11px;
  color: var(--danger, #e06c75);
  white-space: nowrap;
  cursor: help;
}
.doc-download-btn {
  flex: none;
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 4px 10px;
  font-size: 12px;
  color: var(--text, #e7e7ea);
  background: var(--surface, #1c1c22);
  border: 1px solid var(--border, #33333d);
  border-radius: 6px;
  cursor: pointer;
  transition: background 0.15s ease, border-color 0.15s ease;
}
.doc-download-btn:hover:not(:disabled) {
  background: var(--surface-hover, #26262e);
  border-color: var(--accent, #d9a441);
}
.doc-download-btn:disabled {
  opacity: 0.6;
  cursor: default;
}
</style>
