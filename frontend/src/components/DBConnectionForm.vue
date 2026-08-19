<script setup>
// DB 连接编辑弹窗内容：基础信息 + 连接级权限策略 + 最近审计记录。
// 密码永不回显（后端已脱敏），空密码保存 = 不修改。
import { ref } from 'vue'
import { RefreshCw } from 'lucide-vue-next'
import { useAppContext } from '../composables/appContext'
import { testDBConnection } from '../backend'
import DBPolicyEditor from './DBPolicyEditor.vue'

const props = defineProps({
  conn: { type: Object, required: true },
  isNew: { type: Boolean, default: false }
})
const emit = defineEmits(['persist'])

const { t, config, dbAuditRows, dbAuditLoading } = useAppContext()

const kinds = ['mysql', 'postgres', 'sqlite']
const testing = ref(false)
const testResult = ref(null)

function persistChange() {
  emit('persist')
}

function onKindChange(event) {
  props.conn.kind = event.target.value
  if (props.conn.kind === 'postgres' && (!props.conn.port || props.conn.port === 3306)) props.conn.port = 5432
  if (props.conn.kind === 'mysql' && (!props.conn.port || props.conn.port === 5432)) props.conn.port = 3306
  persistChange()
}

// 弹窗内测试走草稿值（含未保存的新密码）；后端对空密码会沿用已存密码。
async function testConnection() {
  if (testing.value) return
  testing.value = true
  testResult.value = null
  try {
    const result = await testDBConnection(JSON.parse(JSON.stringify(props.conn)))
    testResult.value = { ok: !!result?.ok, message: String(result?.message || '') }
  } catch (err) {
    testResult.value = { ok: false, message: String(err) }
  } finally {
    testing.value = false
  }
}

function auditTime(row) {
  const value = row?.time || row?.Time
  if (!value) return '—'
  try {
    return new Date(value).toLocaleString()
  } catch {
    return String(value)
  }
}
function auditSql(row) {
  const sql = String(row?.sql || row?.SQL || '').trim()
  return sql.length > 120 ? `${sql.slice(0, 120)}…` : sql || '—'
}
</script>

<template>
  <div class="db-connection-form">
    <section class="db-form-section">
      <h3>{{ t.dbBasicInfo }}</h3>
      <div class="form-grid">
        <label><span>{{ t.dbName }}</span><input :value="conn.name" @change="conn.name = $event.target.value; persistChange()" /></label>
        <label><span>{{ t.dbKind }}</span>
          <select :value="conn.kind" @change="onKindChange">
            <option v-for="kind in kinds" :key="kind" :value="kind">{{ kind === 'postgres' ? 'PostgreSQL' : kind === 'sqlite' ? 'SQLite' : 'MySQL' }}</option>
          </select>
        </label>
        <template v-if="conn.kind === 'sqlite'">
          <label class="db-form-wide"><span>{{ t.dbFile }}</span><input :value="conn.path" placeholder="/path/to/app.db" @change="conn.path = $event.target.value; persistChange()" /></label>
        </template>
        <template v-else>
          <label><span>{{ t.dbHost }}</span><input :value="conn.host" placeholder="127.0.0.1" @change="conn.host = $event.target.value; persistChange()" /></label>
          <label><span>{{ t.dbPort }}</span><input type="number" min="1" max="65535" :value="conn.port" @change="conn.port = Number($event.target.value) || 0; persistChange()" /></label>
          <label><span>{{ t.dbDatabase }}</span><input :value="conn.database" @change="conn.database = $event.target.value; persistChange()" /></label>
          <label><span>{{ t.dbUsername }}</span><input :value="conn.username" autocomplete="off" @change="conn.username = $event.target.value; persistChange()" /></label>
          <label><span>{{ t.dbPassword }}</span><input type="password" :value="conn.password" autocomplete="new-password" :placeholder="isNew ? '' : t.dbPasswordUnchanged" @change="conn.password = $event.target.value; persistChange()" /></label>
          <label v-if="conn.kind === 'postgres'"><span>{{ t.dbSslMode }}</span>
            <select :value="conn.sslMode || 'disable'" @change="conn.sslMode = $event.target.value; persistChange()">
              <option value="disable">{{ t.dbSslDisable }}</option>
              <option value="require">{{ t.dbSslRequire }}</option>
              <option value="verify-full">{{ t.dbSslVerifyFull }}</option>
            </select>
            <small class="field-hint">{{ t.dbSslModeHint }}</small>
          </label>
          <label><span>{{ t.dbSshBridge }}</span>
            <select :value="conn.sshConfigId || ''" @change="conn.sshConfigId = $event.target.value || ''; persistChange()">
              <option value="">{{ t.dbSshNone }}</option>
              <option v-for="ssh in config.sshConfigs" :key="ssh.id" :value="ssh.id">{{ ssh.name || ssh.address }}</option>
            </select>
            <small class="field-hint">{{ t.dbSshBridgeHint }}</small>
          </label>
        </template>
        <label><span>{{ t.dbQueryTimeout }}</span><input type="number" min="0" max="600" :value="conn.queryTimeoutSeconds || 0" @change="conn.queryTimeoutSeconds = Number($event.target.value) || 0; persistChange()" /><small class="field-hint">{{ t.dbQueryTimeoutHint }}</small></label>
        <label><span>{{ t.dbMaxRows }}</span><input type="number" min="0" max="5000" :value="conn.maxRows || 0" @change="conn.maxRows = Number($event.target.value) || 0; persistChange()" /><small class="field-hint">{{ t.dbMaxRowsHint }}</small></label>
      </div>
      <div class="db-form-test">
        <button type="button" class="secondary-button" :disabled="testing" @click="testConnection">
          <RefreshCw v-if="testing" class="spin" :size="14" />{{ testing ? t.dbTesting : t.dbTest }}
        </button>
        <small v-if="testResult" :class="testResult.ok ? 'db-test-result--ok' : 'db-test-result--fail'">{{ testResult.message || (testResult.ok ? t.dbTestPassed : t.dbTestFailed) }}</small>
      </div>
      <p class="db-password-hint">{{ t.dbPasswordHint }}</p>
    </section>

    <section class="db-form-section">
      <h3>{{ t.dbPolicy }}</h3>
      <p class="db-policy-hint">{{ t.dbPolicyHint }}</p>
      <DBPolicyEditor :policy="conn.policy" @persist="persistChange" />
    </section>

    <section v-if="!isNew" class="db-form-section">
      <h3>{{ t.dbAudit }}</h3>
      <p v-if="dbAuditLoading" class="db-audit-empty">{{ t.dbAuditLoading }}</p>
      <p v-else-if="!dbAuditRows.length" class="db-audit-empty">{{ t.dbAuditEmpty }}</p>
      <div v-else class="db-audit-list">
        <div v-for="(row, index) in dbAuditRows" :key="index" class="db-audit-row">
          <span class="db-audit-row__time">{{ auditTime(row) }}</span>
          <span class="db-audit-row__decision" :data-decision="row.decision || ''">{{ row.decision || '—' }}</span>
          <span class="db-audit-row__action">{{ row.action || '—' }}</span>
          <span class="db-audit-row__sql">{{ auditSql(row) }}</span>
        </div>
      </div>
    </section>
  </div>
</template>
