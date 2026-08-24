<script setup>
import { computed } from 'vue'
import { RefreshCw } from 'lucide-vue-next'
import { useAppContext } from '../composables/appContext'

const props = defineProps({
  modelValue: { type: Boolean, default: false },
  title: { type: String, required: true },
  description: { type: String, default: '' },
  busy: { type: Boolean, default: false },
  confirmLabel: { type: String, default: '' },
  confirmBusyLabel: { type: String, default: '' },
  cancelLabel: { type: String, default: '' },
  closeOnBackdrop: { type: Boolean, default: true },
})
const emit = defineEmits(['confirm', 'cancel', 'update:modelValue'])

const { t } = useAppContext()

const resolvedConfirm = computed(() => props.confirmLabel || t.value.confirm)
const resolvedConfirmBusy = computed(() => props.confirmBusyLabel || resolvedConfirm.value)
const resolvedCancel = computed(() => props.cancelLabel || t.value.cancel)

function requestClose() {
  if (props.busy) return
  emit('cancel')
  emit('update:modelValue', false)
}
function requestConfirm() {
  if (props.busy) return
  emit('confirm')
}
</script>

<template>
  <div v-if="modelValue" class="modal-backdrop" @pointerdown.self="closeOnBackdrop && requestClose()">
    <section class="confirm-dialog" role="dialog" aria-modal="true" aria-labelledby="confirm-dialog-title" :aria-describedby="description ? 'confirm-dialog-desc' : undefined">
      <h2 id="confirm-dialog-title">{{ title }}</h2>
      <p v-if="description" id="confirm-dialog-desc">{{ description }}</p>
      <slot />
      <div class="confirm-dialog__actions">
        <button type="button" class="secondary-button" :disabled="busy" @click="requestClose">{{ resolvedCancel }}</button>
        <button type="button" class="primary-button" :disabled="busy" @click="requestConfirm">
          <RefreshCw v-if="busy" class="spin" :size="14" />
          {{ busy ? resolvedConfirmBusy : resolvedConfirm }}
        </button>
      </div>
    </section>
  </div>
</template>
