<template>
  <div class="pi-gate">
    <div class="pi-gate__card">
      <div class="pi-gate__icon">
        <Package :size="34" />
      </div>
      <h1 class="pi-gate__title">{{ t.pi_gate_title }}</h1>
      <p class="pi-gate__desc">{{ t.pi_gate_desc }}</p>
      <button class="pi-gate__btn" :disabled="busy" @click="onInstall">
        <span v-if="busy" class="pi-gate__spinner" aria-hidden="true"></span>
        {{ busy ? t.pi_gate_installing : t.pi_gate_install }}
      </button>
      <p v-if="error" class="pi-gate__error">{{ error }}</p>
      <p class="pi-gate__hint">{{ t.pi_gate_hint }}</p>
    </div>
  </div>
</template>

<script setup>
import { Package } from 'lucide-vue-next'
import { computed } from 'vue'
import { buildT } from '../i18n'

const props = defineProps({
  onInstall: { type: Function, required: true },
  busy: { type: Boolean, default: false },
  error: { type: String, default: '' }
})

const lang = 'zh-CN'
const t = computed(() => buildT(lang))
</script>

<style scoped>
.pi-gate {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--surface);
  z-index: 20;
}
.pi-gate__card {
  width: min(460px, 90vw);
  padding: 38px 34px;
  text-align: center;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: 16px;
  box-shadow: 0 20px 60px rgba(0, 0, 0, 0.45);
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 14px;
}
.pi-gate__icon {
  width: 64px;
  height: 64px;
  border-radius: 16px;
  display: flex;
  align-items: center;
  justify-content: center;
  color: #fff;
  background: linear-gradient(135deg, #6d5efc, #8b7bff);
}
.pi-gate__title {
  margin: 0;
  font-size: 20px;
  font-weight: 700;
  color: var(--text);
}
.pi-gate__desc {
  margin: 0;
  font-size: 14px;
  line-height: 1.6;
  color: var(--muted);
}
.pi-gate__btn {
  margin-top: 6px;
  min-width: 200px;
  padding: 12px 22px;
  font-size: 15px;
  font-weight: 600;
  color: #fff;
  background: var(--accent, #6d5efc);
  border: none;
  border-radius: 10px;
  cursor: pointer;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  transition: opacity 0.15s ease, transform 0.05s ease;
}
.pi-gate__btn:hover:not(:disabled) {
  opacity: 0.92;
}
.pi-gate__btn:active:not(:disabled) {
  transform: translateY(1px);
}
.pi-gate__btn:disabled {
  opacity: 0.6;
  cursor: default;
}
.pi-gate__spinner {
  width: 15px;
  height: 15px;
  border: 2px solid rgba(255, 255, 255, 0.4);
  border-top-color: #fff;
  border-radius: 50%;
  animation: pi-gate-spin 0.8s linear infinite;
}
.pi-gate__error {
  margin: 0;
  font-size: 13px;
  color: #f85149;
}
.pi-gate__hint {
  margin: 0;
  font-size: 12px;
  color: var(--muted);
}
@keyframes pi-gate-spin {
  to {
    transform: rotate(360deg);
  }
}
</style>
