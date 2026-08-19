<script setup>
// 连接级权限策略编辑器：预设 + Override 规则，仅作用于当前连接。
import { useAppContext } from '../composables/appContext'

const props = defineProps({
  policy: { type: Object, required: true }
})
const emit = defineEmits(['persist'])

const { t } = useAppContext()

const presets = ['safe', 'development', 'full', 'custom']
const effects = ['allow', 'confirm', 'deny']
// 动作按树形前缀匹配，这里给出常用类别供选择，也允许手动输入子动作。
const actionOptions = [
  'database.read',
  'database.write',
  'database.write.insert',
  'database.write.update',
  'database.write.delete',
  'database.schema',
  'database.admin',
  'database.transaction',
  'database.external',
  'database.unknown'
]

function setPreset(preset) {
  props.policy.preset = preset
  emit('persist')
}

function addOverride() {
  props.policy.overrides ||= []
  props.policy.overrides.push({
    id: `override-${Date.now().toString(36)}`,
    action: 'database.write',
    effect: 'confirm',
    reason: '',
    conditions: { requireWhere: false, requireLimit: false, maxRows: 0 },
    resources: []
  })
  emit('persist')
}

function removeOverride(index) {
  props.policy.overrides.splice(index, 1)
  emit('persist')
}

// resources 以 "schema.table"（或 "table"）逗号分隔的文本形式编辑。
function resourcesText(rule) {
  return (rule.resources || [])
    .map(pattern => (pattern.schema ? `${pattern.schema}.${pattern.table || '*'}` : pattern.table || '*'))
    .join(', ')
}
function parseResources(rule, event) {
  const text = String(event.target.value || '')
  rule.resources = text
    .split(',')
    .map(item => item.trim())
    .filter(Boolean)
    .map(item => {
      const dot = item.indexOf('.')
      if (dot > 0) return { schema: item.slice(0, dot), table: item.slice(dot + 1) }
      return { table: item }
    })
  emit('persist')
}

function persistChange() {
  emit('persist')
}
</script>

<template>
  <div class="db-policy-editor">
    <div class="db-policy-editor__presets">
      <label v-for="preset in presets" :key="preset" class="db-preset-option" :class="{ active: policy.preset === preset }">
        <input type="radio" name="db-policy-preset" :checked="policy.preset === preset" @change="setPreset(preset)" />
        <span><strong>{{ t['dbPreset_' + preset] }}</strong><small>{{ t['dbPreset_' + preset + 'Desc'] }}</small></span>
      </label>
    </div>

    <div class="db-overrides">
      <div class="db-overrides__head">
        <strong>{{ t.dbOverrides }}</strong>
        <button type="button" class="secondary-button compact" @click="addOverride"><span>+</span>{{ t.dbAddOverride }}</button>
      </div>
      <small class="db-overrides__hint">{{ t.dbOverridesHint }}</small>
      <p v-if="!(policy.overrides || []).length" class="db-overrides__empty">{{ t.dbOverridesEmpty }}</p>
      <div v-for="(rule, index) in policy.overrides" :key="rule.id || index" class="db-override-rule">
        <div class="db-override-rule__row">
          <label><span>{{ t.dbRuleAction }}</span>
            <input :value="rule.action" list="db-action-options" @change="rule.action = $event.target.value.trim(); persistChange()" />
          </label>
          <label><span>{{ t.dbRuleEffect }}</span>
            <select v-model="rule.effect" @change="persistChange">
              <option v-for="effect in effects" :key="effect" :value="effect">{{ t['dbEffect_' + effect] }}</option>
            </select>
          </label>
          <button type="button" class="icon-button danger" :aria-label="t.deleteItem" @click="removeOverride(index)"><span>✕</span></button>
        </div>
        <div class="db-override-rule__row">
          <label class="db-override-rule__wide"><span>{{ t.dbRuleResources }}</span>
            <input :value="resourcesText(rule)" placeholder="orders, archive.*" @change="parseResources(rule, $event)" />
          </label>
          <label><span>{{ t.dbRuleMaxRows }}</span>
            <input type="number" min="0" :value="rule.conditions?.maxRows || 0" @change="rule.conditions ||= {}; rule.conditions.maxRows = Number($event.target.value) || 0; persistChange()" />
          </label>
        </div>
        <div class="db-override-rule__row db-override-rule__conds">
          <label class="db-cond-check"><input type="checkbox" :checked="!!rule.conditions?.requireWhere" @change="rule.conditions ||= {}; rule.conditions.requireWhere = $event.target.checked; persistChange()" /><span>{{ t.dbRequireWhere }}</span></label>
          <label class="db-cond-check"><input type="checkbox" :checked="!!rule.conditions?.requireLimit" @change="rule.conditions ||= {}; rule.conditions.requireLimit = $event.target.checked; persistChange()" /><span>{{ t.dbRequireLimit }}</span></label>
          <label class="db-override-rule__reason"><span>{{ t.dbRuleReason }}</span><input :value="rule.reason || ''" @change="rule.reason = $event.target.value; persistChange()" /></label>
        </div>
      </div>
      <datalist id="db-action-options">
        <option v-for="action in actionOptions" :key="action" :value="action" />
      </datalist>
    </div>
  </div>
</template>
