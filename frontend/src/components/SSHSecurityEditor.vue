<script setup>
import { reactive } from 'vue'
import { Trash2 } from 'lucide-vue-next'
import { useAppContext } from '../composables/appContext'

const props = defineProps({
  ssh: { type: Object, required: true }
})
const emit = defineEmits(['persist'])
const { t } = useAppContext()
const jsonErrors = reactive({})
const presets = ['safe', 'development', 'full', 'custom']
const effects = ['allow', 'ask', 'deny']
const capabilityOptions = [
  'system', 'system.journal', 'service', 'service.restart', 'service.stop',
  'git', 'git.fetch', 'git.pull', 'docker', 'docker.restart', 'docker.stop',
  'docker.rm', 'docker.exec', 'shell.raw', 'custom'
]
const builtinGroups = [
  { name: 'system', items: [['system.uname', 'allow'], ['system.uptime', 'allow'], ['system.disk_usage', 'allow'], ['system.memory', 'allow'], ['system.processes', 'allow'], ['system.journal', 'allow']] },
  { name: 'git', items: [['git.status', 'allow'], ['git.log', 'allow'], ['git.diff', 'allow'], ['git.show', 'allow'], ['git.branches', 'allow'], ['git.tags', 'allow'], ['git.remotes', 'allow'], ['git.fetch', 'ask'], ['git.pull', 'ask']] },
  { name: 'docker', items: [['docker.ps', 'allow'], ['docker.images', 'allow'], ['docker.logs', 'allow'], ['docker.inspect', 'allow'], ['docker.stats', 'allow'], ['docker.restart', 'ask'], ['docker.stop', 'ask'], ['docker.rm', 'deny'], ['docker.exec', 'deny']] },
  { name: 'service', items: [['service.status', 'allow'], ['service.restart', 'ask'], ['service.stop', 'ask']] },
  { name: 'advanced', items: [['shell.raw', 'deny']] },
]

function persist() {
  emit('persist')
}

function setPreset(preset) {
  props.ssh.policy ||= { preset: 'safe', overrides: [] }
  props.ssh.policy.preset = preset
  persist()
}

function addOverride() {
  props.ssh.policy ||= { preset: 'safe', overrides: [] }
  props.ssh.policy.overrides ||= []
  props.ssh.policy.overrides.push({ id: `override-${Date.now().toString(36)}`, capability: 'docker.restart', effect: 'ask', reason: '' })
  persist()
}

function removeOverride(index) {
  props.ssh.policy.overrides.splice(index, 1)
  persist()
}

function addCapability() {
  props.ssh.customCapabilities ||= []
  props.ssh.customCapabilities.push({
    name: `custom.status_${Date.now().toString(36)}`,
    group: 'custom',
    description: '',
    executable: 'systemctl',
    args: ['status', '{service}', '--no-pager'],
    params: { service: { type: 'service_name', required: true } },
    permission: 'allow',
    timeoutSeconds: 30,
  })
  persist()
}

function removeCapability(index) {
  props.ssh.customCapabilities.splice(index, 1)
  persist()
}

function argsText(capability) {
  return (capability.args || []).join('\n')
}

function updateArgs(capability, event) {
  capability.args = String(event.target.value || '').split(/\r?\n/).map(item => item.trim()).filter(Boolean)
  persist()
}

function paramsText(capability) {
  return JSON.stringify(capability.params || {}, null, 2)
}

function updateParams(capability, index, event) {
  try {
    const value = JSON.parse(String(event.target.value || '{}'))
    if (!value || Array.isArray(value) || typeof value !== 'object') throw new Error('object required')
    capability.params = value
    jsonErrors[index] = false
    persist()
  } catch {
    jsonErrors[index] = true
  }
}
</script>

<template>
  <section class="db-form-section ssh-security-section">
    <h3>{{ t.sshSecurityPolicy }}</h3>
    <p class="db-policy-hint">{{ t.sshSecurityHint }}</p>
    <div class="db-policy-editor">
      <div class="db-policy-editor__presets">
        <label v-for="preset in presets" :key="preset" class="db-preset-option" :class="{ active: ssh.policy?.preset === preset }">
          <input type="radio" name="ssh-policy-preset" :checked="ssh.policy?.preset === preset" @change="setPreset(preset)" />
          <span><strong>{{ t['sshPreset_' + preset] }}</strong><small>{{ t['sshPreset_' + preset + 'Desc'] }}</small></span>
        </label>
      </div>

      <div class="ssh-builtin-catalog">
        <strong>{{ t.sshBuiltinCatalog }}</strong>
        <small>{{ t.sshBuiltinCatalogHint }}</small>
        <div class="ssh-builtin-groups">
          <section v-for="group in builtinGroups" :key="group.name">
            <h4>{{ group.name }}</h4>
            <div><span v-for="item in group.items" :key="item[0]" class="ssh-capability-chip"><code>{{ item[0] }}</code><em :data-effect="item[1]">{{ item[1].toUpperCase() }}</em></span></div>
          </section>
        </div>
      </div>

      <div class="db-overrides">
        <div class="db-overrides__head">
          <strong>{{ t.sshOverrides }}</strong>
          <button type="button" class="secondary-button compact" @click="addOverride"><span>+</span>{{ t.sshAddOverride }}</button>
        </div>
        <small class="db-overrides__hint">{{ t.sshOverridesHint }}</small>
        <p v-if="!(ssh.policy?.overrides || []).length" class="db-overrides__empty">{{ t.sshOverridesEmpty }}</p>
        <div v-for="(rule, index) in (ssh.policy?.overrides || [])" :key="rule.id || index" class="db-override-rule">
          <div class="db-override-rule__row">
            <label><span>{{ t.sshRuleCapability }}</span><input v-model.trim="rule.capability" list="ssh-capability-options" @change="persist" /></label>
            <label><span>{{ t.sshRuleEffect }}</span><select v-model="rule.effect" @change="persist"><option v-for="effect in effects" :key="effect" :value="effect">{{ t['sshEffect_' + effect] }}</option></select></label>
            <button type="button" class="icon-button danger" :aria-label="t.deleteItem" @click="removeOverride(index)"><Trash2 :size="14" /></button>
          </div>
          <label class="ssh-security-reason"><span>{{ t.sshRuleReason }}</span><input v-model="rule.reason" @change="persist" /></label>
        </div>
        <datalist id="ssh-capability-options"><option v-for="name in capabilityOptions" :key="name" :value="name" /></datalist>
      </div>

      <div class="db-overrides ssh-custom-capabilities">
        <div class="db-overrides__head">
          <strong>{{ t.sshCustomCapabilities }}</strong>
          <button type="button" class="secondary-button compact" @click="addCapability"><span>+</span>{{ t.sshAddCapability }}</button>
        </div>
        <small class="db-overrides__hint">{{ t.sshCustomHint }}</small>
        <p v-if="!(ssh.customCapabilities || []).length" class="db-overrides__empty">{{ t.sshCustomEmpty }}</p>
        <div v-for="(capability, index) in (ssh.customCapabilities || [])" :key="capability.name + index" class="db-override-rule ssh-custom-capability">
          <div class="ssh-custom-capability__head">
            <strong>{{ capability.name || t.sshCustomCapability }}</strong>
            <button type="button" class="icon-button danger" :aria-label="t.deleteItem" @click="removeCapability(index)"><Trash2 :size="14" /></button>
          </div>
          <div class="form-grid">
            <label><span>{{ t.sshCapabilityName }}</span><input v-model.trim="capability.name" placeholder="nginx.status" @change="persist" /></label>
            <label><span>{{ t.sshCapabilityGroup }}</span><input v-model.trim="capability.group" placeholder="custom" @change="persist" /></label>
            <label><span>{{ t.sshExecutable }}</span><input v-model.trim="capability.executable" placeholder="systemctl" @change="persist" /></label>
            <label><span>{{ t.sshPermission }}</span><select v-model="capability.permission" @change="persist"><option v-for="effect in effects" :key="effect" :value="effect">{{ t['sshEffect_' + effect] }}</option></select></label>
            <label><span>{{ t.sshTimeoutSeconds }}</span><input v-model.number="capability.timeoutSeconds" type="number" min="1" max="300" @change="persist" /></label>
            <label><span>{{ t.sshCapabilityDescription }}</span><input v-model="capability.description" @change="persist" /></label>
            <label class="db-form-wide"><span>{{ t.sshArgsTemplate }}</span><textarea :value="argsText(capability)" rows="4" spellcheck="false" @change="updateArgs(capability, $event)"></textarea><small class="field-hint">{{ t.sshArgsTemplateHint }}</small></label>
            <label class="db-form-wide"><span>{{ t.sshTypedParams }}</span><textarea :value="paramsText(capability)" rows="7" spellcheck="false" @change="updateParams(capability, index, $event)"></textarea><small class="field-hint" :class="{ 'db-test-result--fail': jsonErrors[index] }">{{ jsonErrors[index] ? t.sshParamsJsonInvalid : t.sshTypedParamsHint }}</small></label>
          </div>
        </div>
      </div>
    </div>
  </section>
</template>
