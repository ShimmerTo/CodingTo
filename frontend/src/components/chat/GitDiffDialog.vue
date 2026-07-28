<script setup>
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import {
  ArrowLeft, ArrowRight, FileCode2, Image as ImageIcon,
  LoaderCircle, PackageOpen, X
} from 'lucide-vue-next'
import { getSessionGitFileDetail } from '../../backend.js'

const props = defineProps({
  open: { type: Boolean, default: false },
  sessionId: { type: Number, required: true },
  scope: { type: String, required: true },
  files: { type: Array, default: () => [] },
  index: { type: Number, default: 0 },
  baseBranch: { type: String, default: '' },
  t: { type: Object, required: true }
})

const emit = defineEmits(['close', 'update:index'])
const loading = ref(false)
const error = ref('')
const detail = ref(null)
let requestNonce = 0

const activeFile = computed(() => props.files[props.index] || null)
const positionLabel = computed(() => `${Math.min(props.index + 1, props.files.length)} / ${props.files.length}`)

watch(
  [() => props.open, () => props.index, () => activeFile.value?.path, () => props.baseBranch],
  loadDetail,
  { immediate: true }
)

async function loadDetail() {
  if (!props.open || !activeFile.value?.path) return
  const nonce = ++requestNonce
  loading.value = true
  error.value = ''
  detail.value = null
  try {
    const result = await getSessionGitFileDetail(
      props.sessionId,
      props.scope,
      activeFile.value.path,
      props.baseBranch
    )
    if (nonce === requestNonce) detail.value = result
  } catch (cause) {
    if (nonce === requestNonce) error.value = String(cause)
  } finally {
    if (nonce === requestNonce) loading.value = false
  }
}

function navigate(delta) {
  const next = props.index + delta
  if (next < 0 || next >= props.files.length) return
  emit('update:index', next)
}

function onKeydown(event) {
  if (!props.open) return
  if (event.key === 'Escape') emit('close')
  if (event.key === 'ArrowLeft') navigate(-1)
  if (event.key === 'ArrowRight') navigate(1)
}

onMounted(() => window.addEventListener('keydown', onKeydown))
onBeforeUnmount(() => window.removeEventListener('keydown', onKeydown))

function lineClass(kind) {
  return kind === 'added' ? 'is-added' : kind === 'deleted' ? 'is-deleted' : 'is-context'
}

function lineSign(kind) {
  return kind === 'added' ? '+' : kind === 'deleted' ? '-' : ' '
}

function formatSize(bytes) {
  const value = Number(bytes) || 0
  if (value < 1024) return `${value} B`
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KB`
  return `${(value / (1024 * 1024)).toFixed(1)} MB`
}

function formatDate(value) {
  if (!Number(value)) return '—'
  return new Date(Number(value)).toLocaleString()
}

function versionTitle(side) {
  return side === 'before' ? props.t.gitBeforeChange : props.t.gitAfterChange
}
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="git-diff-backdrop" @pointerdown.self="emit('close')">
      <section class="git-diff-dialog" role="dialog" aria-modal="true" :aria-label="t.gitCompareTitle">
        <header class="git-diff-dialog__head">
          <span class="git-diff-dialog__icon">
            <ImageIcon v-if="detail?.kind === 'image'" :size="18" />
            <PackageOpen v-else-if="detail?.kind === 'binary'" :size="18" />
            <FileCode2 v-else :size="18" />
          </span>
          <span class="git-diff-dialog__title">
            <strong :title="activeFile?.path">{{ activeFile?.path }}</strong>
            <small>
              {{ scope === 'worktree' ? t.gitWorkspaceChanges : t.gitBranchChanges }}
              <template v-if="scope === 'branch' && baseBranch"> · {{ baseBranch }}</template>
            </small>
          </span>
          <span v-if="detail" class="git-diff-dialog__counts">
            <strong class="change-count change-count--added">+{{ detail.added || 0 }}</strong>
            <strong class="change-count change-count--deleted">-{{ detail.deleted || 0 }}</strong>
          </span>
          <nav class="git-diff-dialog__nav" :aria-label="t.gitChangeNavigation">
            <button type="button" :disabled="index <= 0" :title="t.gitPreviousChange" @click="navigate(-1)">
              <ArrowLeft :size="16" />
            </button>
            <span>{{ positionLabel }}</span>
            <button type="button" :disabled="index >= files.length - 1" :title="t.gitNextChange" @click="navigate(1)">
              <ArrowRight :size="16" />
            </button>
          </nav>
          <button class="git-diff-dialog__close" type="button" :title="t.close" @click="emit('close')">
            <X :size="18" />
          </button>
        </header>

        <main class="git-diff-dialog__body">
          <div v-if="loading" class="git-diff-dialog__state">
            <LoaderCircle class="spin" :size="20" />
            <span>{{ t.gitLoadingCompare }}</span>
          </div>
          <div v-else-if="error" class="git-diff-dialog__state is-error">{{ error }}</div>

          <template v-else-if="detail">
            <div v-if="detail.kind === 'text'" class="git-text-diff">
              <div class="git-diff-version-bar">
                <span>{{ t.gitBeforeChange }} · {{ detail.before.lineCount || 0 }} {{ t.gitLines }}</span>
                <span>{{ t.gitAfterChange }} · {{ detail.after.lineCount || 0 }} {{ t.gitLines }}</span>
              </div>
              <template v-if="detail.hunks?.length">
                <div v-for="(hunk, hunkIndex) in detail.hunks" :key="hunkIndex" class="git-diff-hunk">
                  <div class="git-diff-hunk__head">{{ hunk.header }}</div>
                  <div
                    v-for="(line, lineIndex) in hunk.lines"
                    :key="lineIndex"
                    class="git-diff-line"
                    :class="lineClass(line.kind)"
                  >
                    <span class="git-diff-line__number">{{ line.oldNumber || '' }}</span>
                    <span class="git-diff-line__number">{{ line.newNumber || '' }}</span>
                    <span class="git-diff-line__sign">{{ lineSign(line.kind) }}</span>
                    <code>{{ line.text }}</code>
                  </div>
                </div>
              </template>
              <div v-else class="git-diff-dialog__state">{{ t.gitNoDiffContent }}</div>
            </div>

            <div v-else-if="detail.kind === 'image'" class="git-visual-compare">
              <article v-for="side in ['before', 'after']" :key="side" class="git-version-card">
                <header>{{ versionTitle(side) }}</header>
                <div class="git-image-preview">
                  <img
                    v-if="detail[side].exists && detail[side].imageData"
                    :src="detail[side].imageData"
                    :alt="`${versionTitle(side)} ${detail.path}`"
                  />
                  <span v-else-if="!detail[side].exists">{{ t.gitFileDoesNotExist }}</span>
                  <span v-else>{{ t.gitImagePreviewUnavailable }}</span>
                </div>
                <dl class="git-file-metadata">
                  <div><dt>{{ t.gitResolution }}</dt><dd>{{ detail[side].width && detail[side].height ? `${detail[side].width} × ${detail[side].height}` : '—' }}</dd></div>
                  <div><dt>{{ t.gitFileSize }}</dt><dd>{{ formatSize(detail[side].size) }}</dd></div>
                  <div><dt>{{ t.gitMimeType }}</dt><dd>{{ detail[side].mimeType || detail.mimeType || '—' }}</dd></div>
                  <div><dt>{{ t.gitPermissions }}</dt><dd>{{ detail[side].permissions || '—' }}</dd></div>
                  <div><dt>{{ t.gitCreatedAt }}</dt><dd>{{ formatDate(detail[side].createdAt) }}</dd></div>
                  <div><dt>{{ t.gitModifiedAt }}</dt><dd>{{ formatDate(detail[side].modifiedAt) }}</dd></div>
                </dl>
              </article>
            </div>

            <div v-else class="git-binary-compare">
              <article v-for="side in ['before', 'after']" :key="side" class="git-version-card">
                <header>{{ versionTitle(side) }}</header>
                <div v-if="!detail[side].exists" class="git-version-card__missing">{{ t.gitFileDoesNotExist }}</div>
                <dl v-else class="git-file-metadata">
                  <div><dt>{{ t.gitFileSize }}</dt><dd>{{ formatSize(detail[side].size) }}</dd></div>
                  <div><dt>{{ t.gitMimeType }}</dt><dd>{{ detail[side].mimeType || detail.mimeType || '—' }}</dd></div>
                  <div><dt>{{ t.gitPermissions }}</dt><dd>{{ detail[side].permissions || '—' }}</dd></div>
                  <div><dt>{{ t.gitCreatedAt }}</dt><dd>{{ formatDate(detail[side].createdAt) }}</dd></div>
                  <div><dt>{{ t.gitModifiedAt }}</dt><dd>{{ formatDate(detail[side].modifiedAt) }}</dd></div>
                </dl>
              </article>
            </div>
          </template>
        </main>

        <footer class="git-diff-dialog__foot">
          <span>{{ t.gitNavigationHint }}</span>
          <span v-if="detail?.oldPath">{{ detail.oldPath }} → {{ detail.path }}</span>
        </footer>
      </section>
    </div>
  </Teleport>
</template>

<style scoped>
.git-diff-backdrop { position: fixed; z-index: 1300; inset: 0; display: grid; place-items: center; padding: 24px; background: rgb(8 9 9 / .5); backdrop-filter: blur(2px); }
.git-diff-dialog { width: min(1180px, 96vw); height: min(820px, 92vh); min-height: 480px; display: flex; flex-direction: column; overflow: hidden; border: 1px solid var(--border); border-radius: 14px; background: var(--surface); box-shadow: 0 28px 90px rgb(0 0 0 / .3); }
.git-diff-dialog__head { flex: 0 0 auto; min-height: 62px; display: flex; align-items: center; gap: 10px; padding: 10px 14px; border-bottom: 1px solid var(--border); }
.git-diff-dialog__icon { flex: 0 0 auto; width: 34px; height: 34px; display: grid; place-items: center; border-radius: 9px; color: var(--accent); background: color-mix(in srgb, var(--accent) 12%, transparent); }
.git-diff-dialog__title { flex: 1 1 auto; min-width: 0; display: flex; flex-direction: column; gap: 2px; }
.git-diff-dialog__title strong { overflow: hidden; color: var(--text); font: 13px/1.4 Consolas, "Cascadia Mono", monospace; text-overflow: ellipsis; white-space: nowrap; }
.git-diff-dialog__title small { color: var(--muted); font-size: 12px; }
.git-diff-dialog__counts { flex: 0 0 auto; display: flex; gap: 8px; }
.git-diff-dialog__nav { flex: 0 0 auto; display: flex; align-items: center; gap: 4px; }
.git-diff-dialog__nav button,.git-diff-dialog__close { width: 32px; height: 32px; display: grid; place-items: center; border: 0; border-radius: 8px; color: var(--muted); background: transparent; cursor: pointer; }
.git-diff-dialog__nav button:hover:not(:disabled),.git-diff-dialog__close:hover { color: var(--text); background: var(--hover); }
.git-diff-dialog__nav button:disabled { opacity: .35; cursor: default; }
.git-diff-dialog__nav span { min-width: 52px; color: var(--muted); font-size: 12px; text-align: center; font-variant-numeric: tabular-nums; }
.git-diff-dialog__body { flex: 1 1 auto; min-height: 0; overflow: auto; background: color-mix(in srgb, var(--surface-2) 52%, var(--surface)); }
.git-diff-dialog__state { min-height: 180px; display: flex; align-items: center; justify-content: center; gap: 8px; color: var(--muted); font-size: 13px; }
.git-diff-dialog__state.is-error { color: var(--danger); }
.git-diff-dialog__foot { flex: 0 0 auto; min-height: 38px; display: flex; justify-content: space-between; gap: 16px; padding: 9px 14px; border-top: 1px solid var(--border); color: var(--faint); background: var(--surface); font-size: 11px; }
.git-diff-version-bar { position: sticky; top: 0; z-index: 1; display: grid; grid-template-columns: 1fr 1fr; padding: 8px 16px; border-bottom: 1px solid var(--border); color: var(--muted); background: var(--surface); font-size: 12px; }
.git-diff-version-bar span:last-child { text-align: right; }
.git-diff-hunk + .git-diff-hunk { border-top: 1px solid var(--border); }
.git-diff-hunk__head { padding: 7px 14px; color: #667ab0; background: rgb(80 110 175 / .09); font: 12px/1.5 Consolas, "Cascadia Mono", monospace; }
.git-diff-line { min-width: max-content; display: grid; grid-template-columns: 48px 48px 18px minmax(580px, 1fr); font: 12px/1.62 Consolas, "Cascadia Mono", monospace; }
.git-diff-line.is-added { background: rgb(41 151 100 / .11); }
.git-diff-line.is-deleted { background: rgb(209 75 66 / .1); }
.git-diff-line__number { padding: 1px 8px; color: var(--faint); background: rgb(120 120 115 / .05); text-align: right; user-select: none; font-variant-numeric: tabular-nums; }
.git-diff-line__sign { padding-top: 1px; color: var(--faint); text-align: center; user-select: none; }
.git-diff-line.is-added .git-diff-line__sign { color: #299764; }
.git-diff-line.is-deleted .git-diff-line__sign { color: #d14b42; }
.git-diff-line code { display: block; padding: 1px 16px 1px 4px; color: var(--text); white-space: pre; }
.git-visual-compare,.git-binary-compare { min-height: 100%; display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 14px; padding: 16px; }
.git-version-card { min-width: 0; overflow: hidden; border: 1px solid var(--border); border-radius: 11px; background: var(--surface); }
.git-version-card > header { padding: 10px 12px; border-bottom: 1px solid var(--border-soft); color: var(--text); font-size: 13px; font-weight: 600; }
.git-image-preview { min-height: 300px; display: grid; place-items: center; padding: 14px; color: var(--faint); background-image: linear-gradient(45deg, var(--surface-2) 25%, transparent 25%),linear-gradient(-45deg, var(--surface-2) 25%, transparent 25%),linear-gradient(45deg, transparent 75%, var(--surface-2) 75%),linear-gradient(-45deg, transparent 75%, var(--surface-2) 75%); background-position: 0 0,0 8px,8px -8px,-8px 0; background-size: 16px 16px; font-size: 12px; }
.git-image-preview img { max-width: 100%; max-height: 460px; object-fit: contain; }
.git-file-metadata { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0; margin: 0; }
.git-file-metadata > div { min-width: 0; padding: 10px 12px; border-top: 1px solid var(--border-soft); }
.git-file-metadata > div:nth-child(odd) { border-right: 1px solid var(--border-soft); }
.git-file-metadata dt { color: var(--faint); font-size: 11px; }
.git-file-metadata dd { margin: 4px 0 0; overflow-wrap: anywhere; color: var(--text); font: 12px/1.45 Consolas, "Cascadia Mono", monospace; }
.git-version-card__missing { min-height: 220px; display: grid; place-items: center; color: var(--faint); font-size: 12px; }
@media (max-width: 760px) {
  .git-diff-backdrop { padding: 0; }
  .git-diff-dialog { width: 100vw; height: 100vh; max-height: none; border: 0; border-radius: 0; }
  .git-diff-dialog__counts { display: none; }
  .git-visual-compare,.git-binary-compare { grid-template-columns: 1fr; }
}
</style>
