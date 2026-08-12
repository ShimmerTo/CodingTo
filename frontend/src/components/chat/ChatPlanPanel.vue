<script setup>
import { computed, nextTick, ref, watch } from 'vue'
import { CheckCircle2, ChevronDown, ChevronUp, KeyRound, ListTodo, ShieldAlert } from 'lucide-vue-next'
import {
  browserProfileCreateTarget,
  extensionDialogTitle,
  isBrowserProfileCreateOption,
  isDCGConfirmationDialog,
  isPlanConfirmationDialog,
  SECURE_BROWSER_PROFILE_DIALOG_TITLE
} from './extensionDialog.js'

const props = defineProps({
  items: { type: Array, default: () => [] },
  dialog: { type: Object, default: null },
  t: { type: Object, required: true }
})

const emit = defineEmits(['respond', 'ack'])
// 计划列表折叠：折叠后仅保留头部，点击右侧按钮展开/收起。
const planCollapsed = ref(false)
const responseValue = ref('')
const dialogEl = ref(null)
const profileKeyInput = ref(null)
const isInlineBrowserProfile = ref(false)
const browserProfile = ref({
  key: '', targetUrl: ''
})
const isSecureBrowserProfile = computed(() => props.dialog?.title === SECURE_BROWSER_PROFILE_DIALOG_TITLE)
const isBrowserProfileForm = computed(() => isSecureBrowserProfile.value || isInlineBrowserProfile.value)
const visibleDialogTitle = computed(() => extensionDialogTitle(props.dialog))
// 计划确认弹窗：用于在 plan-todos 未就绪时展示缺失提示区，并禁用批准按钮。
const isPlanDialog = computed(() => isPlanConfirmationDialog(props.dialog))
const isDCGDialog = computed(() => isDCGConfirmationDialog(props.dialog))

watch(() => props.dialog?.id, () => {
  responseValue.value = props.dialog?.prefill || ''
  isInlineBrowserProfile.value = false
  browserProfile.value = { key: '', targetUrl: '' }
  if (props.dialog?.title === SECURE_BROWSER_PROFILE_DIALOG_TITLE) {
    let setup = {}
    try { setup = JSON.parse(props.dialog?.placeholder || '{}') } catch { /* invalid setup becomes empty */ }
    browserProfile.value = {
      key: '',
      targetUrl: setup.targetUrl || ''
    }
  }
  // 弹窗成功渲染入 DOM 后向父组件发出 ack，由 App.vue 回传 extension_ui_ack
  // 解除后端看门狗。若渲染失败（dialogEl 为空）则不发 ack，后端超时后会自动
  // 代答取消，避免 agent 被阻断型扩展卡死。plan/browser-profile/ask-user 的
  // 弹窗都走这里，一视同仁。
  const dialogId = props.dialog?.id
  if (dialogId) {
    nextTick(() => {
      if (dialogEl.value) emit('ack', { id: dialogId })
    })
  }
}, { immediate: true })

function submitBrowserProfile() {
  if (props.dialog?.saving || !browserProfile.value.key.trim()) return
  emit('respond', { browserProfile: { ...browserProfile.value } })
}

function startBrowserProfileCreation(option) {
  isInlineBrowserProfile.value = true
  browserProfile.value = {
    key: '',
    targetUrl: browserProfileCreateTarget(option)
  }
  nextTick(() => profileKeyInput.value?.focus())
}

function leaveBrowserProfileCreation() {
  isInlineBrowserProfile.value = false
  browserProfile.value = { key: '', targetUrl: '' }
}

// select 弹窗的 options 既可能是纯字符串（计划扩展、浏览器身份扩展），也可能是
// {label, value, description} 对象（ask-user 扩展）。以下辅助函数统一归一化，
// 避免直接对对象调用字符串方法导致渲染崩溃、弹窗无法展示。
function isOptionObject(option) {
  return typeof option === 'object' && option !== null
}
function optionLabel(option) {
  return isOptionObject(option) ? String(option.label ?? option.value ?? '') : String(option)
}
function optionValue(option, index) {
  if (isOptionObject(option)) {
    const value = option.value ?? option.label
    return value == null ? String(index) : String(value)
  }
  return String(option)
}
function optionDescription(option) {
  return isOptionObject(option) ? String(option.description || '') : ''
}
function isPrimaryOption(option) {
  return optionLabel(option).toLowerCase().startsWith('execute')
}
function selectOption(option, index) {
  if (isBrowserProfileCreateOption(option)) {
    startBrowserProfileCreation(option)
    return
  }
  emit('respond', { value: optionValue(option, index) })
}
</script>

<template>
  <section class="plan-panel">
    <div v-if="items.length && !isDCGDialog" class="plan-proposal">
      <div class="plan-proposal__head">
        <ListTodo :size="14" />
        <span>{{ t.planProposalTitle || '执行计划' }}</span>
        <small>{{ items.length }} {{ t.planStepsUnit || '步' }}</small>
        <button
          class="plan-proposal__collapse"
          type="button"
          :title="planCollapsed ? t.planExpand : t.planCollapse"
          :aria-label="planCollapsed ? t.planExpand : t.planCollapse"
          :aria-expanded="String(!planCollapsed)"
          @click="planCollapsed = !planCollapsed"
        >
          <ChevronDown v-if="planCollapsed" :size="14" />
          <ChevronUp v-else :size="14" />
        </button>
      </div>
      <ol v-show="!planCollapsed" class="plan-proposal__list">
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
    <!-- 计划数据未就绪：确认弹窗先于 plan-todos 到达且已等待超时，禁用批准，
         避免用户在看不到完整计划时盲目确认；取消仍可用。 -->
    <div v-else-if="isPlanDialog && dialog.planMissing" class="plan-proposal plan-proposal--missing">
      <div class="plan-proposal__head">
        <ListTodo :size="14" />
        <span>{{ t.planProposalTitle || '执行计划' }}</span>
      </div>
      <p class="plan-proposal__missing">{{ t.planMissing || '计划数据未就绪，请取消后重试' }}</p>
    </div>
    <div v-if="dialog" ref="dialogEl" class="extension-prompt" :class="{ 'extension-prompt--credential': isBrowserProfileForm, 'extension-prompt--danger': isDCGDialog }">
      <template v-if="isBrowserProfileForm">
        <strong><KeyRound :size="14" />{{ t.browserProfileCreateTitle }}</strong>
        <p>{{ t.browserProfileSecureHint }}</p>
        <p v-if="dialog.error" class="extension-prompt__error">{{ dialog.error }}</p>
        <div class="browser-profile-form">
          <label><span>{{ t.browserProfileKey }}</span><input ref="profileKeyInput" v-model="browserProfile.key" :placeholder="t.browserProfileKeyPlaceholder" autocomplete="off" autocapitalize="none" spellcheck="false" @keydown.enter.prevent="submitBrowserProfile" /></label>
        </div>
        <div class="extension-prompt__actions">
          <button class="primary" :disabled="dialog.saving || !browserProfile.key.trim()" @click="submitBrowserProfile">{{ dialog.saving ? t.savingItem : t.browserProfileCreate }}</button>
          <button v-if="isInlineBrowserProfile" :disabled="dialog.saving" @click="leaveBrowserProfileCreation">{{ t.back }}</button>
          <button v-else :disabled="dialog.saving" @click="emit('respond', { cancelled: true })">{{ t.cancel }}</button>
        </div>
      </template>
      <template v-else>
      <strong><ShieldAlert v-if="isDCGDialog" :size="15" />{{ visibleDialogTitle }}</strong>
      <p v-if="dialog.message">{{ dialog.message }}</p>
      <div v-if="dialog.method === 'select'" class="extension-prompt__actions">
        <button
          v-for="(option, index) in dialog.options"
          :key="optionValue(option, index)"
          :class="{ primary: isPrimaryOption(option) }"
          :title="optionDescription(option)"
          @click="selectOption(option, index)"
        >{{ optionLabel(option) }}</button>
        <button @click="emit('respond', { cancelled: true })">{{ t.cancel }}</button>
      </div>
      <div v-else-if="dialog.method === 'confirm'" class="extension-prompt__actions">
        <button class="primary" :class="{ 'primary--danger': isDCGDialog }" :disabled="dialog.planMissing" @click="emit('respond', { confirmed: true })">{{ isDCGDialog ? t.dcgApproveCommand : (t.confirm || 'Confirm') }}</button>
        <button @click="emit('respond', { confirmed: false })">{{ isDCGDialog ? t.dcgRejectCommand : t.cancel }}</button>
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
