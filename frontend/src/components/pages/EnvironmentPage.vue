<script setup>
import { Box, Check, Folder, FolderKanban, GitBranch, Globe2, KeyRound, Plus, Settings, Trash2 } from 'lucide-vue-next'
import { useAppContext } from '../../composables/appContext'

const { t, environmentTab, config, openWsEditor, openSshEditor, selectedWorkspace, selectedWorkspaceId, wsDraft, newWsId, wsBusy, requestDeleteWs, workspaceSsh, workspaceRemote, setActiveWorkspace, requestDeleteSsh } = useAppContext()
</script>

<template>
<section class="content-page environment-page">
          <div class="page-heading">
            <div><h2>{{ t.environmentTitle }}</h2><p>{{ t.environmentIntro }}</p></div>
            <button v-if="environmentTab === 'workspace'" class="primary-button" @click="openWsEditor(null)"><Plus :size="15" />{{ t.addWs }}</button>
          </div>

          <div class="environment-tabs" role="tablist" :aria-label="t.environmentTitle">
            <button role="tab" :aria-selected="environmentTab === 'workspace'" :class="{ active: environmentTab === 'workspace' }" @click="environmentTab = 'workspace'">
              <FolderKanban :size="16" />{{ t.workspaceTab }}
              <span>{{ config.environments.length }}</span>
            </button>
            <button role="tab" :aria-selected="environmentTab === 'ssh'" :class="{ active: environmentTab === 'ssh' }" @click="environmentTab = 'ssh'">
              <KeyRound :size="16" />{{ t.sshTab }}
              <span class="tab-dev">{{ t.devBadge }}</span>
              <span>{{ config.sshConfigs.length }}</span>
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
                    <button class="secondary-button" @click="openWsEditor(selectedWorkspace)"><Settings :size="14" />{{ t.openEditor }}</button>
                    <button class="danger-button" :disabled="config.environments.length <= 1 || wsBusy" @click="requestDeleteWs(selectedWorkspace)"><Trash2 :size="14" />{{ t.deleteItem }}</button>
                  </div>
                </div>

                <div class="workspace-composition" :class="{ 'workspace-composition--remote': workspaceSsh(selectedWorkspace) }">
                  <article>
                    <span class="workspace-step">1</span>
                    <div class="workspace-composition__icon"><Folder :size="18" /></div>
                    <div><small>{{ t.wsLocalDirectory }}</small><strong>{{ selectedWorkspace.path || t.notConfigured }}</strong></div>
                  </article>
                  <article>
                    <span class="workspace-step">2</span>
                    <div class="workspace-composition__icon"><KeyRound :size="18" /></div>
                    <div><small>{{ t.wsSshConfig }}</small><strong>{{ workspaceSsh(selectedWorkspace)?.name || t.notConfigured }}</strong></div>
                  </article>
                  <article v-if="workspaceSsh(selectedWorkspace)">
                    <span class="workspace-step">3</span>
                    <div class="workspace-composition__icon"><Globe2 :size="18" /></div>
                    <div><small>{{ t.wsRemoteDirectory }}</small><strong>{{ workspaceRemote(selectedWorkspace)?.remotePath || t.notConfigured }}</strong></div>
                  </article>
                </div>

                <div class="workspace-statusbar">
                  <span v-if="selectedWorkspace.id === config.activeEnvId" class="active-pill"><Check :size="12" />{{ t.wsActive }}</span>
                  <button v-else class="secondary-button" :disabled="wsBusy" @click="setActiveWorkspace(selectedWorkspace)">{{ t.wsSetActive }}</button>
                  <p>{{ t.workspaceRule }}</p>
                </div>
              </div>
            </div>
          </div>

          <div v-else-if="environmentTab === 'ssh'" class="environment-tab-panel" role="tabpanel">
            <div v-if="!config.sshConfigs.length" class="agent-runtime-state">
              <KeyRound :size="28" />
              <strong>{{ t.createFirst }} {{ t.sshTab }}</strong>
              <p>{{ t.sshEmptyHint }}</p>
              <button class="primary-button" :disabled="true" @click="openSshEditor(null)"><Plus :size="14" />{{ t.addSsh }}</button>
            </div>
            <div v-else class="item-grid">
              <article v-for="ssh in config.sshConfigs" :key="ssh.id" class="item-card">
                <div class="item-card__head"><span class="item-card__icon"><KeyRound :size="17" /></span><strong>{{ ssh.name || t.sshName }}</strong>
                  <button class="icon-button" :aria-label="t.openEditor" @click="openSshEditor(ssh)"><Settings :size="14" /></button>
                  <button class="icon-button danger" :aria-label="t.deleteItem" @click="requestDeleteSsh(ssh)"><Trash2 :size="14" /></button>
                </div>
                <dl class="item-card__body">
                  <div><dt>{{ t.sshAddress }}</dt><dd>{{ ssh.address || '—' }}</dd></div>
                  <div><dt>{{ t.sshPort }}</dt><dd>{{ ssh.port || 22 }}</dd></div>
                  <div><dt>{{ t.sshUsername }}</dt><dd>{{ ssh.username || '—' }}</dd></div>
                  <div><dt>{{ t.sshAuthMode }}</dt><dd>{{ t.sshPasswordMode }}</dd></div>
                  <div><dt>{{ t.sshRemark }}</dt><dd>{{ ssh.remark || '—' }}</dd></div>
                </dl>
              </article>
            </div>
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
