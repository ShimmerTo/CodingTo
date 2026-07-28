<script setup>
import { computed, ref, watch } from 'vue'
import { CheckCircle2, KeyRound, ListTodo } from 'lucide-vue-next'
import { extensionDialogTitle } from './extensionDialog.js'

const props = defineProps({
  items: { type: Array, default: () => [] },
  dialog: { type: Object, default: null },
  t: { type: Object, required: true }
})

const emit = defineEmits(['respond'])
const responseValue = ref('')
const browserProfile = ref({
  key: '', targetUrl: ''
})
const isSecureBrowserProfile = computed(() => props.dialog?.title === '__CODINGTO_SECURE_BROWSER_PROFILE__')
const visibleDialogTitle = computed(() => extensionDialogTitle(props.dialog))

watch(() => props.dialog?.id, () => {
  responseValue.value = props.dialog?.prefill || ''
  if (props.dialog?.title === '__CODINGTO_SECURE_BROWSER_PROFILE__') {
    let setup = {}
    try { setup = JSON.parse(props.dialog?.placeholder || '{}') } catch { /* invalid setup becomes empty */ }
    browserProfile.value = {
      key: '',
      targetUrl: setup.targetUrl || ''
    }
  }
}, { immediate: true })

function submitBrowserProfile() {
  if (!browserProfile.value.key.trim()) return
  emit('respond', { browserProfile: { ...browserProfile.value } })
}
</script>

<template>
  <section class="plan-panel">
    <div v-if="items.length" class="plan-proposal">
      <div class="plan-proposal__head">
        <ListTodo :size="14" />
        <span>{{ t.planProposalTitle || '执行计划' }}</span>
        <small>{{ items.length }} {{ t.planStepsUnit || '步' }}</small>
      </div>
      <ol class="plan-proposal__list">
        <li
          v-for="item in items"
          :key="item.step"
          class="plan-proposal__item"
          :class="{ 'plan-proposal__item--done': item.completed }"
        >
          <span class="plan-proposal__mark">
            <CheckCircle2 v-if="item.completed" :size="14" />
            <span v-else class="plan-proposal__num">{{ item.step }}</span>
          </span>
          <span class="plan-proposal__text">{{ item.text }}</span>
        </li>
      </ol>
    </div>
    <div v-if="dialog" class="extension-prompt" :class="{ 'extension-prompt--credential': isSecureBrowserProfile }">
      <template v-if="isSecureBrowserProfile">
        <strong><KeyRound :size="14" />{{ t.browserProfileCreateTitle }}</strong>
        <p>{{ t.browserProfileSecureHint }}</p>
        <p v-if="dialog.error" class="extension-prompt__error">{{ dialog.error }}</p>
        <div class="browser-profile-form">
          <label><span>{{ t.browserProfileKey }}</span><input v-model="browserProfile.key" :placeholder="t.browserProfileKeyPlaceholder" autocomplete="off" autocapitalize="none" spellcheck="false" /></label>
        </div>
        <div class="extension-prompt__actions">
          <button class="primary" :disabled="dialog.saving || !browserProfile.key.trim()" @click="submitBrowserProfile">{{ dialog.saving ? t.savingItem : t.browserProfileCreate }}</button>
          <button :disabled="dialog.saving" @click="emit('respond', { cancelled: true })">{{ t.cancel }}</button>
        </div>
      </template>
      <template v-else>
      <strong>{{ visibleDialogTitle }}</strong>
      <p v-if="dialog.message">{{ dialog.message }}</p>
      <div v-if="dialog.method === 'select'" class="extension-prompt__actions">
        <button
          v-for="option in dialog.options"
          :key="option"
          :class="{ primary: option.toLowerCase().startsWith('execute') }"
          @click="emit('respond', { value: option })"
        >{{ option }}</button>
        <button @click="emit('respond', { cancelled: true })">{{ t.cancel }}</button>
      </div>
      <div v-else-if="dialog.method === 'confirm'" class="extension-prompt__actions">
        <button class="primary" @click="emit('respond', { confirmed: true })">{{ t.confirm || 'Confirm' }}</button>
        <button @click="emit('respond', { confirmed: false })">{{ t.cancel }}</button>
      </div>
      <template v-else>
        <textarea v-model="responseValue" :placeholder="dialog.placeholder || ''" rows="4"></textarea>
        <div class="extension-prompt__actions">
          <button class="primary" :disabled="!responseValue.trim()" @click="emit('respond', { value: responseValue })">{{ t.saveItem }}</button>
          <button @click="emit('respond', { cancelled: true })">{{ t.cancel }}</button>
        </div>
      </template>
      </template>
    </div>
  </section>
</template>

<style scoped src="../../styles/chat/plan-panel.css"></style>
