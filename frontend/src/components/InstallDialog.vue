<script setup>
import { RefreshCw } from 'lucide-vue-next'
import { useAppContext } from '../composables/appContext'

const { t } = useAppContext()

defineProps({
  // 'command'：输入安装命令并执行；'progress'：执行中显示实时日志
  mode: { type: String, default: 'command' },
  title: { type: String, default: '' },
  hint: { type: String, default: '' },
  command: { type: String, default: '' },
  previewCommand: { type: String, default: '' },
  commandPlaceholder: { type: String, default: '' },
  running: { type: Boolean, default: false },
  log: { type: Array, default: () => [] },
  statusText: { type: String, default: '' },
  logEmptyText: { type: String, default: '' },
  runText: { type: String, default: '' },
})

const emit = defineEmits(['update:command', 'run', 'close'])
</script>

<template>
  <div class="modal-backdrop" @click.self="emit('close')">
    <div class="install-dialog">
      <h3>{{ title }}</h3>
      <p v-if="hint" class="install-dialog__hint">{{ hint }}</p>

      <input
        v-if="mode === 'command'"
        :value="command"
        class="install-dialog__input"
        :placeholder="commandPlaceholder"
        :disabled="running"
        @input="emit('update:command', $event.target.value)"
        @keyup.enter="emit('run')"
      />
      <div v-if="mode === 'command' && previewCommand" class="install-dialog__preview">
        <span>{{ t.command }}</span>
        <code>{{ previewCommand }}</code>
      </div>

      <template v-else>
        <p v-if="statusText" class="install-dialog__hint">{{ statusText }}</p>
        <pre class="install-dialog__output">{{ log.length ? log.join('\n') : (logEmptyText || '') }}</pre>
      </template>

      <div class="install-dialog__actions">
        <button type="button" class="secondary-button" :disabled="running" @click="emit('close')">{{ t.cancel }}</button>
        <button
          v-if="mode === 'command'"
          type="button"
          class="primary-button"
          :disabled="!command.trim() || running"
          @click="emit('run')"
        >
          <RefreshCw v-if="running" :size="13" />{{ running ? t.executing : runText }}
        </button>
        <button v-else type="button" class="primary-button" :disabled="running" @click="emit('close')">{{ t.close }}</button>
      </div>
    </div>
  </div>
</template>
