<script setup>
import { computed, ref } from 'vue'
import { Archive, Check, ExternalLink, Pencil, RefreshCw, Sparkles, Trash2, Upload, X } from 'lucide-vue-next'
import SkillCard from '../SkillCard.vue'
import { useAppContext } from '../../composables/appContext'

const { t, skills, skillsLoading, agentList, refreshSkills, installSkills, previewSkillArchive, previewSkillUrl, editSkill, deleteSkill, updateSkill, pushToast } = useAppContext()

const installOpen = ref(false)
const installMethod = ref('pi')
const command = ref('pi install ')
const url = ref('')
const archive = ref(null)
const preview = ref(null)
const selectedAgents = ref([])
const loadMode = ref('startup')
const busy = ref(false)
const notice = ref('')
const archiveInput = ref(null)
const updateInput = ref(null)
const editTarget = ref(null)
const editAgents = ref([])
const editMode = ref('startup')
const updateTarget = ref(null)
const updateURL = ref('')
const updateArchive = ref(null)

const allSelected = computed(() => selectedAgents.value.length === agentList.value.length && agentList.value.length > 0)
function agentName(id) { return agentList.value.find(agent => agent.id === id)?.name || id }
function toggleAll() {
  selectedAgents.value = allSelected.value ? [] : agentList.value.map(agent => agent.id)
}
function openInstall(method) {
  installMethod.value = method
  command.value = method === 'pi' ? 'pi install ' : ''
  url.value = ''
  archive.value = null
  preview.value = null
  selectedAgents.value = []
  loadMode.value = method === 'pi' ? 'startup' : 'skills_list'
  notice.value = ''
  installOpen.value = true
}
function closeInstall() {
  if (busy.value) return
  installOpen.value = false
}
function fileToBase64(file) {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onerror = () => reject(reader.error || new Error('无法读取文件'))
    reader.onload = () => {
      const bytes = new Uint8Array(reader.result)
      let binary = ''
      const chunk = 0x8000
      for (let i = 0; i < bytes.length; i += chunk) binary += String.fromCharCode(...bytes.subarray(i, i + chunk))
      resolve(btoa(binary))
    }
    reader.readAsArrayBuffer(file)
  })
}
async function chooseArchive(event, update = false) {
  const file = event.target.files?.[0]
  event.target.value = ''
  if (!file) return
  try {
    const data = await fileToBase64(file)
    const value = { name: file.name, data }
    if (update) {
      updateArchive.value = value
      return
    }
    archive.value = value
    preview.value = await previewSkillArchive(value)
    notice.value = ''
  } catch (err) {
    notice.value = String(err)
  }
}
async function validateURL() {
  notice.value = ''
  preview.value = null
  try {
    preview.value = await previewSkillUrl(url.value.trim())
  } catch (err) {
    notice.value = String(err)
  }
}
async function install() {
  if (busy.value) return
  if (!selectedAgents.value.length) { notice.value = t.skillsChooseAgent || '至少选择一个 Agent'; return }
  busy.value = true
  notice.value = ''
  try {
    await installSkills({
      method: installMethod.value,
      command: command.value,
      url: url.value.trim(),
      archiveName: archive.value?.name || '',
      archiveData: archive.value?.data || '',
      agentIds: selectedAgents.value,
      loadMode: installMethod.value === 'pi' ? 'startup' : loadMode.value
    })
    await refreshSkills()
    installOpen.value = false
    pushToast('success', t.skillsInstalled || 'Skill 已安装')
  } catch (err) {
    notice.value = String(err)
  } finally {
    busy.value = false
  }
}
function openEdit(skill) {
  editTarget.value = skill
  editAgents.value = skill.agents.map(agent => agent.id)
  editMode.value = skill.loadMode || 'startup'
}
function closeEdit() { if (!busy.value) editTarget.value = null }
async function saveEdit() {
  if (!editAgents.value.length) { notice.value = t.skillsChooseAgent || '至少选择一个 Agent'; return }
  busy.value = true
  notice.value = ''
  try {
    await editSkill({ skillId: editTarget.value.id, agentIds: editAgents.value, loadMode: editMode.value })
    await refreshSkills()
    editTarget.value = null
  } catch (err) { notice.value = String(err) } finally { busy.value = false }
}
function openUpdate(skill) {
  updateTarget.value = skill
  updateURL.value = ''
  updateArchive.value = null
  notice.value = ''
}
async function update() {
  if (busy.value || (!updateURL.value.trim() && !updateArchive.value)) return
  busy.value = true
  notice.value = ''
  try {
    await updateSkill({ skillId: updateTarget.value.id, url: updateURL.value.trim(), archiveName: updateArchive.value?.name || '', archiveData: updateArchive.value?.data || '' })
    await refreshSkills()
    updateTarget.value = null
    pushToast('success', t.skillsUpdated || 'Skill 已更新')
  } catch (err) { notice.value = String(err) } finally { busy.value = false }
}
async function remove(skill) {
  if (busy.value) return
  if (!window.confirm((t.skillsDeleteConfirm || '确定删除 Skill “{name}”？').replace('{name}', skill.name))) return
  busy.value = true
  try { await deleteSkill(skill.id); await refreshSkills() } catch (err) { pushToast('error', String(err)) } finally { busy.value = false }
}
</script>

<template>
  <section class="content-page skills-page">
    <div class="page-heading">
      <div><h2>{{ t.skillsTitle }}</h2><p>{{ t.skillsIntro }}</p></div>
      <Sparkles :size="28" />
    </div>

    <div class="skills-actions">
      <button class="primary-button" @click="openInstall('pi')"><Sparkles :size="14" />{{ t.skillsInstallPi || '通过 pi 安装' }}</button>
      <button class="secondary-button" @click="openInstall('archive')"><Upload :size="14" />{{ t.skillsUploadZip || '上传 ZIP' }}</button>
      <button class="secondary-button" @click="openInstall('url')"><ExternalLink :size="14" />{{ t.skillsInstallUrl || '通过 URL 安装' }}</button>
      <button class="icon-button" :title="t.refresh || '刷新'" @click="refreshSkills"><RefreshCw :size="15" :class="{ spin: skillsLoading }" /></button>
    </div>

    <div v-if="!skills.length && !skillsLoading" class="empty-integration skills-empty">
      <Sparkles :size="28" /><strong>{{ t.skillsEmpty || '还没有安装 Skill' }}</strong><p>{{ t.skillsEmptyHint || 'Skill 只会对被分配的 Agent 可见。' }}</p>
    </div>
    <div v-else class="skills-list">
      <article v-for="skill in skills" :key="skill.id" class="skill-card">
        <div class="skill-card__icon"><Sparkles :size="19" /></div>
            <SkillCard :skill="skill">
              <div class="skill-agents"><span>{{ t.skillsAgents || '安装 Agent' }}</span><b v-for="agent in skill.agents" :key="agent.id">{{ agent.name }}</b></div>
            </SkillCard>
        <div class="skill-card__actions">
          <button class="secondary-button compact" @click="openEdit(skill)"><Pencil :size="13" />{{ t.edit }}</button>
          <button v-if="skill.sourceType === 'pi'" class="secondary-button compact" @click="openUpdate(skill)"><RefreshCw :size="13" />{{ t.skillsUpgrade || '升级' }}</button>
          <button v-else class="secondary-button compact" @click="openUpdate(skill)"><RefreshCw :size="13" />{{ t.skillsUpdate || '更新' }}</button>
          <button class="danger-button compact" @click="remove(skill)"><Trash2 :size="13" />{{ t.delete }}</button>
        </div>
      </article>
    </div>

    <div v-if="installOpen" class="modal-backdrop" @click.self="closeInstall">
      <section class="agent-editor-dialog skills-dialog" role="dialog" aria-modal="true">
        <header class="agent-editor-dialog__head"><h2>{{ installMethod === 'pi' ? (t.skillsInstallPi || '通过 pi 安装 Skill') : installMethod === 'archive' ? (t.skillsUploadZip || '上传 Skill ZIP') : (t.skillsInstallUrl || '通过 URL 安装 Skill') }}</h2><button class="icon-button" @click="closeInstall"><X :size="16" /></button></header>
        <div class="agent-editor-dialog__body">
          <label v-if="installMethod === 'pi'" class="skill-field"><span>pi install 命令</span><input v-model="command" placeholder="pi install git:github.com/user/repo" /></label>
          <template v-else-if="installMethod === 'url'">
            <label class="skill-field"><span>ZIP URL</span><input v-model="url" placeholder="https://example.com/skill.zip" @change="validateURL" /></label>
            <button class="secondary-button" :disabled="!url.trim() || busy" @click="validateURL"><Check :size="14" />{{ t.skillsValidate || '下载并校验' }}</button>
          </template>
          <template v-else>
            <input ref="archiveInput" type="file" accept=".zip,application/zip" hidden @change="chooseArchive" />
            <button class="secondary-button" :disabled="busy" @click="archiveInput?.click()"><Archive :size="14" />{{ archive?.name || (t.skillsChooseZip || '选择 ZIP 文件') }}</button>
          </template>
          <div v-if="preview" class="skill-preview"><strong>{{ preview.name }}<span v-if="preview.count > 1"> · {{ preview.count }} skills</span></strong><p>{{ preview.description }}</p></div>
          <fieldset v-if="installMethod === 'pi' || preview" class="skills-agent-picker"><legend>{{ t.skillsChooseAgent || '选择 Agent（至少一个）' }}</legend><label class="skill-agent-option skill-agent-option--all"><input type="checkbox" :checked="allSelected" @change="toggleAll" /><span>{{ t.all || '全部 Agent' }}</span></label><label v-for="agent in agentList" :key="agent.id" class="skill-agent-option"><input v-model="selectedAgents" type="checkbox" :value="agent.id" /><span>{{ agent.name }}</span></label></fieldset>
          <fieldset v-if="installMethod !== 'pi' && preview" class="skills-agent-picker"><legend>{{ t.skillsLoadMode || '安装模式' }}</legend><label class="skill-agent-option"><input v-model="loadMode" type="radio" value="startup" /><span><strong>{{ t.skillsStartup || '随 pi 启动时加载' }}</strong><small>{{ t.skillsStartupHint || '会加大上下文占用，安装到 skills 目录' }}</small></span></label><label class="skill-agent-option"><input v-model="loadMode" type="radio" value="skills_list" /><span><strong>{{ t.skillsListMode || '通过 skills_list 工具发现' }}</strong><small>{{ t.skillsListModeHint || '使用时加载，安装到 skills_list 目录' }}</small></span></label></fieldset>
          <p v-if="installMethod === 'pi'" class="skills-warning">{{ t.skillsStartupWarning || '安装后随 pi 启动加载，会加大上下文占用。' }}</p>
          <p v-if="notice" class="skills-notice">{{ notice }}</p>
        </div>
        <footer class="agent-editor-dialog__footer"><button class="secondary-button" :disabled="busy" @click="closeInstall">{{ t.cancel }}</button><button class="primary-button" :disabled="busy || !selectedAgents.length || (installMethod === 'pi' ? !command.trim() : (!preview || (installMethod === 'url' ? !url.trim() : !archive)))" @click="install"><RefreshCw v-if="busy" class="spin" :size="14" /><Check v-else :size="14" />{{ busy ? (t.skillsInstalling || '安装中…') : (t.skillsInstall || '安装') }}</button></footer>
      </section>
    </div>

    <div v-if="editTarget" class="modal-backdrop" @click.self="closeEdit"><section class="agent-editor-dialog skills-dialog" role="dialog" aria-modal="true"><header class="agent-editor-dialog__head"><h2>{{ t.skillsEdit || '编辑 Skill' }}</h2><button class="icon-button" @click="closeEdit"><X :size="16" /></button></header><div class="agent-editor-dialog__body"><fieldset class="skills-agent-picker"><legend>{{ t.skillsChooseAgent || '选择 Agent（至少一个）' }}</legend><label v-for="agent in agentList" :key="agent.id" class="skill-agent-option"><input v-model="editAgents" type="checkbox" :value="agent.id" /><span>{{ agent.name }}</span></label></fieldset><fieldset v-if="editTarget.sourceType !== 'pi'" class="skills-agent-picker"><legend>{{ t.skillsLoadMode || '安装模式' }}</legend><label class="skill-agent-option"><input v-model="editMode" type="radio" value="startup" /><span>{{ t.skillsStartup || '随 pi 启动时加载' }}</span></label><label class="skill-agent-option"><input v-model="editMode" type="radio" value="skills_list" /><span>{{ t.skillsListMode || '通过 skills_list 工具发现' }}</span></label></fieldset><p v-if="notice" class="skills-notice">{{ notice }}</p></div><footer class="agent-editor-dialog__footer"><button class="secondary-button" @click="closeEdit">{{ t.cancel }}</button><button class="primary-button" :disabled="busy || !editAgents.length" @click="saveEdit"><Check :size="14" />{{ t.save }}</button></footer></section></div>

    <div v-if="updateTarget" class="modal-backdrop" @click.self="updateTarget = null"><section class="agent-editor-dialog skills-dialog" role="dialog" aria-modal="true"><header class="agent-editor-dialog__head"><h2>{{ updateTarget.sourceType === 'pi' ? (t.skillsUpgrade || '升级 Skill') : (t.skillsUpdate || '更新 Skill') }}</h2><button class="icon-button" @click="updateTarget = null"><X :size="16" /></button></header><div class="agent-editor-dialog__body"><template v-if="updateTarget.sourceType === 'pi'"><p class="field-hint">{{ updateTarget.source }}</p></template><template v-else><label class="skill-field"><span>URL（可选）</span><input v-model="updateURL" placeholder="https://example.com/skill.zip" /></label><span class="skill-or">{{ t.or || '或' }}</span><input ref="updateInput" type="file" accept=".zip,application/zip" hidden @change="event => chooseArchive(event, true)" /><button class="secondary-button" @click="updateInput?.click()"><Upload :size="14" />{{ updateArchive?.name || (t.skillsChooseZip || '选择 ZIP 文件') }}</button></template><p v-if="notice" class="skills-notice">{{ notice }}</p></div><footer class="agent-editor-dialog__footer"><button class="secondary-button" :disabled="busy" @click="updateTarget = null">{{ t.cancel }}</button><button class="primary-button" :disabled="busy || (updateTarget.sourceType !== 'pi' && !updateURL.trim() && !updateArchive)" @click="update"><RefreshCw v-if="busy" class="spin" :size="14" /><Check v-else :size="14" />{{ busy ? (t.updating || '更新中…') : (t.save || '更新') }}</button></footer></section></div>
  </section>
</template>
