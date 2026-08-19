<script setup>
import { Bot, Box, Check, Database, Folder, FolderKanban, GitBranch, Globe2, KeyRound, LoaderCircle, Plus, Settings, Trash2 } from 'lucide-vue-next'
import IconPlay from '../icons/IconPlay.vue'
import { useAppContext } from '../../composables/appContext'

const { t, environmentTab, config, openWsEditor, openSshEditor, selectedWorkspace, selectedWorkspaceId, wsDraft, newWsId, wsBusy, requestDeleteWs, workspaceRemotes, remoteSsh, setActiveWorkspace, requestDeleteSsh, openDbEditor, requestDeleteDb, testDb, dbTestStates, dbBusy, workspaceDbConnections, testSsh, sshTestStates } = useAppContext()

function dbKindLabel(kind) {
  if (kind === 'postgres') return 'PostgreSQL'
  if (kind === 'sqlite') return 'SQLite'
  return 'MySQL'
}
function sshAuthLabel(ssh) {
  return ssh?.authMode === 'key' ? t.value.sshKeyMode : t.value.sshPasswordMode
}
function sshBridgeName(connectionId) {
  return config.sshConfigs.find(ssh => ssh.id === connectionId)?.name || connectionId || ''
}
function workspaceDefaultAgent(ws) {
  const id = ws?.defaultAgentId && config.agents.some(agent => agent.id === ws.defaultAgentId)
    ? ws.defaultAgentId
    : config.agents[0]?.id || ''
  return config.agents.find(agent => agent.id === id) || null
}
</script>

<template>
<section class="content-page environment-page">
          <div class="page-heading">
            <div><h2>{{ t.environmentTitle }}</h2><p>{{ t.environmentIntro }}</p></div>
            <button v-if="environmentTab === 'workspace'" class="primary-button" @click="openWsEditor(null)"><Plus :size="15" />{{ t.addWs }}</button>
            <button v-else-if="environmentTab === 'ssh'" class="primary-button" @click="openSshEditor(null)"><Plus :size="15" />{{ t.addSsh }}</button>
            <button v-else-if="environmentTab === 'db'" class="primary-button" @click="openDbEditor(null)"><Plus :size="15" />{{ t.addDb }}</button>
          </div>

          <div class="environment-tabs" role="tablist" :aria-label="t.environmentTitle">
            <button role="tab" :aria-selected="environmentTab === 'workspace'" :class="{ active: environmentTab === 'workspace' }" @click="environmentTab = 'workspace'">
              <FolderKanban :size="16" />{{ t.workspaceTab }}
              <span>{{ config.environments.length }}</span>
            </button>
            <button role="tab" :aria-selected="environmentTab === 'ssh'" :class="{ active: environmentTab === 'ssh' }" @click="environmentTab = 'ssh'">
              <KeyRound :size="16" />{{ t.sshTab }}
              <span>{{ config.sshConfigs.length }}</span>
            </button>
            <button role="tab" :aria-selected="environmentTab === 'db'" :class="{ active: environmentTab === 'db' }" @click="environmentTab = 'db'">
              <Database :size="16" />{{ t.dbTab }}
              <span>{{ (config.extensions?.db?.connections || []).length }}</span>
            </button>
            <button role="tab" :aria-selected="environmentTab === 'docker'" :class="{ active: environmentTab === 'docker' }" @click="environmentTab = 'docker'">
              <Box :size="16" />{{ t.dockerTab }}
              <span class="tab-dev">{{ t.devBadge }}</span>
            </button>
            <button role="tab" :aria-selected="environmentTab === 'git'" :class="{ active: environmentTab === 'git' }" @click="environmentTab = 'git'">
              <GitBranch :size="16" />{{ t.gitTab }}
              <span class="tab-dev">{{ t.devBadge }}</span>
            </button>
          </div>

          <div v-if="environmentTab === 'workspace'" class="environment-tab-panel" role="tabpanel">
            <div v-if="!config.environments.length" class="agent-runtime-state">
              <FolderKanban :size="28" />
              <strong>{{ t.createFirst }} {{ t.workspaceTab }}</strong>
              <p>{{ t.workspaceEmptyHint }}</p>
              <button class="primary-button" @click="openWsEditor(null)"><Plus :size="14" />{{ t.addWs }}</button>
            </div>
            <div v-else class="workspace-layout">
              <div class="workspace-list">
                <button v-for="ws in config.environments" :key="ws.id" :class="{ active: ws.id === selectedWorkspace?.id }" @click="selectedWorkspaceId = ws.id; wsDraft = null">
                  <span class="workspace-list__icon"><Folder :size="16" /></span>
                  <span class="workspace-list__copy">
                    <strong>{{ ws.name }} <em v-if="ws.id === newWsId">{{ t.unsavedItem }}</em></strong>
                    <small>{{ ws.path || t.noWorkspace }}</small>
                  </span>
                </button>
              </div>
              <div v-if="selectedWorkspace" class="workspace-view">
                <div class="workspace-view__head">
                  <div class="agent-view__title">
                    <Folder :size="20" />
                    <div><strong>{{ selectedWorkspace.name }}</strong><small>{{ selectedWorkspace.description || t.agentNoDescription }}</small></div>
                  </div>
                  <div class="workspace-view__actions">
                    <button class="icon-button" :title="t.openEditor" :aria-label="t.openEditor" @click="openWsEditor(selectedWorkspace)"><Settings :size="14" /></button>
                    <button class="icon-button danger" :title="t.deleteItem" :aria-label="t.deleteItem" :disabled="config.environments.length <= 1 || wsBusy" @click="requestDeleteWs(selectedWorkspace)"><Trash2 :size="14" /></button>
                  </div>
                </div>

                <div class="workspace-composition">
                  <article>
                    <span class="workspace-step">1</span>
                    <div class="workspace-composition__icon"><Folder :size="18" /></div>
                    <div><small>{{ t.wsLocalDirectory }}</small><strong>{{ selectedWorkspace.path || t.notConfigured }}</strong></div>
                  </article>
                  <article v-for="(remote, index) in workspaceRemotes(selectedWorkspace)" :key="remote.id">
                    <span class="workspace-step">{{ index + 2 }}</span>
                    <div class="workspace-composition__icon"><KeyRound :size="18" /></div>
                    <div><small>{{ remoteSsh(remote)?.name || t.wsSshConfig }}</small><strong>{{ remote.remotePath || t.notConfigured }}</strong></div>
                  </article>
                  <article v-if="!workspaceRemotes(selectedWorkspace).length">
                    <span class="workspace-step">2</span>
                    <div class="workspace-composition__icon"><Globe2 :size="18" /></div>
                    <div><small>{{ t.wsSshConfig }}</small><strong>{{ t.notConfigured }}</strong></div>
                  </article>
                  <article v-if="workspaceDbConnections(selectedWorkspace).length">
                    <span class="workspace-step">{{ Math.max(2, workspaceRemotes(selectedWorkspace).length + 1) + 1 }}</span>
                    <div class="workspace-composition__icon"><Database :size="18" /></div>
                    <div><small>{{ t.wsDbConnections }}</small><strong>{{ workspaceDbConnections(selectedWorkspace).map(conn => conn.name).join(', ') }}</strong></div>
                  </article>
                </div>

                <div class="workspace-statusbar">
                  <span v-if="workspaceDefaultAgent(selectedWorkspace)" class="workspace-default-agent" :title="t.wsDefaultAgentHint">
                    <Bot :size="12" />{{ t.wsDefaultAgent }}：{{ workspaceDefaultAgent(selectedWorkspace).name }}
                  </span>
                  <span v-if="selectedWorkspace.id === config.activeEnvId" class="active-pill"><Check :size="12" />{{ t.wsActive }}</span>
                  <button v-else class="secondary-button" :disabled="wsBusy" @click="setActiveWorkspace(selectedWorkspace)">{{ t.wsSetActive }}</button>
                </div>
              </div>
            </div>
          </div>

          <div v-else-if="environmentTab === 'ssh'" class="environment-tab-panel" role="tabpanel">
            <div v-if="!config.sshConfigs.length" class="agent-runtime-state">
              <KeyRound :size="28" />
              <strong>{{ t.createFirst }} {{ t.sshTab }}</strong>
              <p>{{ t.sshEmptyHint }}</p>
              <button class="primary-button" @click="openSshEditor(null)"><Plus :size="14" />{{ t.addSsh }}</button>
            </div>
            <div v-else class="item-grid">
              <article v-for="ssh in config.sshConfigs" :key="ssh.id" class="item-card">
                <div class="item-card__head"><span class="item-card__icon"><KeyRound :size="17" /></span><strong>{{ ssh.name || t.sshName }}</strong>
                  <button class="icon-button" :title="t.sshTest" :aria-label="t.sshTest" :disabled="sshTestStates[ssh.id]?.busy" @click="testSsh(ssh)">
                    <IconPlay v-if="!sshTestStates[ssh.id]?.busy" />
                    <LoaderCircle v-else class="spin" :size="14" />
                  </button>
                  <button class="icon-button" :aria-label="t.openEditor" @click="openSshEditor(ssh)"><Settings :size="14" /></button>
                  <button class="icon-button danger" :aria-label="t.deleteItem" @click="requestDeleteSsh(ssh)"><Trash2 :size="14" /></button>
                </div>
                <dl class="item-card__body">
                  <div><dt>{{ t.sshAddress }}</dt><dd>{{ ssh.address || '—' }}</dd></div>
                  <div><dt>{{ t.sshPort }}</dt><dd>{{ ssh.port || 22 }}</dd></div>
                  <div><dt>{{ t.sshUsername }}</dt><dd>{{ ssh.username || '—' }}</dd></div>
                  <div><dt>{{ t.sshAuthMode }}</dt><dd>{{ sshAuthLabel(ssh) }}</dd></div>
                  <div><dt>{{ t.sshRemark }}</dt><dd>{{ ssh.remark || '—' }}</dd></div>
                </dl>
                <div v-if="sshTestStates[ssh.id]?.message" class="ssh-card__foot">
                  <small :class="sshTestStates[ssh.id]?.ok ? 'db-test-result--ok' : 'db-test-result--fail'">{{ sshTestStates[ssh.id].message }}</small>
                </div>
              </article>
            </div>
          </div>

          <div v-else-if="environmentTab === 'db'" class="environment-tab-panel" role="tabpanel">
            <div v-if="!(config.extensions?.db?.connections || []).length" class="agent-runtime-state">
              <Database :size="28" />
              <strong>{{ t.createFirst }} {{ t.dbTab }}</strong>
              <p>{{ t.dbEmptyHint }}</p>
              <button class="primary-button" @click="openDbEditor(null)"><Plus :size="14" />{{ t.addDb }}</button>
            </div>
            <div v-else class="item-grid">
              <article v-for="conn in config.extensions.db.connections" :key="conn.id" class="item-card db-card">
                <div class="item-card__head"><span class="item-card__icon"><Database :size="17" /></span><strong>{{ conn.name || t.dbTab }}</strong>
                  <button class="icon-button" :title="t.dbTest" :aria-label="t.dbTest" :disabled="dbTestStates[conn.id]?.busy" @click="testDb(conn)">
                    <IconPlay v-if="!dbTestStates[conn.id]?.busy" />
                    <LoaderCircle v-else class="spin" :size="14" />
                  </button>
                  <button class="icon-button" :aria-label="t.openEditor" @click="openDbEditor(conn)"><Settings :size="14" /></button>
                  <button class="icon-button danger" :aria-label="t.deleteItem" :disabled="dbBusy" @click="requestDeleteDb(conn)"><Trash2 :size="14" /></button>
                </div>
                <dl class="item-card__body">
                  <div><dt>{{ t.dbKind }}</dt><dd><span class="db-kind-badge">{{ dbKindLabel(conn.kind) }}</span></dd></div>
                  <template v-if="conn.kind === 'sqlite'">
                    <div class="db-file-row"><dt>{{ t.dbFile }}</dt><dd>{{ conn.path || '—' }}</dd></div>
                  </template>
                  <template v-else>
                    <div><dt>{{ t.dbHost }}</dt><dd>{{ conn.host || '—' }}:{{ conn.port }}</dd></div>
                    <div><dt>{{ t.dbDatabase }}</dt><dd>{{ conn.database || '—' }}</dd></div>
                    <div><dt>{{ t.dbUsername }}</dt><dd>{{ conn.username || '—' }}</dd></div>
                    <div v-if="conn.sshConfigId"><dt>{{ t.dbSshBridge }}</dt><dd class="db-ssh-badge"><KeyRound :size="12" />{{ sshBridgeName(conn.sshConfigId) }}</dd></div>
                  </template>
                  <div><dt>{{ t.dbPolicy }}</dt><dd>
                    <span class="db-preset-badge" :data-preset="conn.policy?.preset || 'safe'">{{ t['dbPreset_' + (conn.policy?.preset || 'safe')] }}</span>
                  </dd></div>
                </dl>
                <div v-if="dbTestStates[conn.id]?.message" class="db-card__foot">
                  <small :class="dbTestStates[conn.id]?.ok ? 'db-test-result--ok' : 'db-test-result--fail'">{{ dbTestStates[conn.id].message }}</small>
                </div>
              </article>
            </div>
            <p class="db-panel-hint"><Database :size="14" />{{ t.dbPanelHint }}</p>
          </div>

          <div v-else-if="environmentTab === 'docker'" class="environment-tab-panel" role="tabpanel">
            <div class="agent-runtime-state">
              <Box :size="28" />
              <strong>{{ t.dockerTab }}</strong>
              <p>{{ t.devComingSoon }}</p>
            </div>
          </div>

          <div v-else-if="environmentTab === 'git'" class="environment-tab-panel" role="tabpanel">
            <div class="agent-runtime-state">
              <GitBranch :size="28" />
              <strong>{{ t.gitTab }}</strong>
              <p>{{ t.devComingSoon }}</p>
            </div>
          </div>
        </section>
</template>
