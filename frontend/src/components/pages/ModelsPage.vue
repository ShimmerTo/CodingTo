<script setup>
import { computed, ref } from 'vue'
import IconTrash from '../icons/IconTrash.vue'
import IconPlus from '../icons/IconPlus.vue'
import IconPlay from '../icons/IconPlay.vue'
import IconImage from '../icons/IconImage.vue'
import IconBrain from '../icons/IconBrain.vue'
import IconWrench from '../icons/IconWrench.vue'
import { LoaderCircle, Settings, Trash2 } from 'lucide-vue-next'
import { useAppContext } from '../../composables/appContext'

const {
  t,
  config,
  selectedModelsProvider: selectedProvider,
  selectModelsProvider,
  openProviderEditor,
  openProviderEdit,
  requestDeleteProvider,
  saving,
  persist,
  apiKeyVisibilityLabel,
  showProviderApiKey,
  piCompatBooleanFields,
  formatCompat,
  updateCompatJson,
  piThinkingLevels,
  testingModels,
  testModelKey,
  runModelTest,
  testResult,
  modelRequestRoute,
  toggleImageInput
} = useAppContext()

const providers = computed(() => config.providers || [])

function toggleApiKeyVisible() {
  showProviderApiKey.value = !showProviderApiKey.value
}

const apiOptions = ['openai-completions', 'openai-responses', 'anthropic-messages', 'gemini-generic', 'qwen-openai']

const modelEditorOpen = ref(false)
const editingNewModel = ref(false)
const editingModelRef = ref(null)
const modelDraft = ref(null)
const modelDialogError = ref('')

function openAddModel() {
  const provider = selectedProvider.value
  if (!provider) return
  modelDraft.value = {
    id: '',
    name: '',
    api: 'openai-completions',
    baseUrl: '',
    contextWindow: 128000,
    maxTokens: 16384,
    reasoning: false,
    defaultThinkingLevel: 'off',
    input: ['text'],
    capabilities: { toolCall: true },
    compat: {}
  }
  editingNewModel.value = true
  editingModelRef.value = null
  modelDialogError.value = ''
  modelEditorOpen.value = true
}

function openEditModel(model) {
  const draft = JSON.parse(JSON.stringify(model))
  if (!draft.compat) draft.compat = {}
  modelDraft.value = draft
  editingNewModel.value = false
  editingModelRef.value = model
  modelDialogError.value = ''
  modelEditorOpen.value = true
}

function closeModelEditor() {
  modelEditorOpen.value = false
}

async function saveModel() {
  const provider = selectedProvider.value
  const draft = modelDraft.value
  if (!draft) return
  if (!draft.id.trim()) {
    modelDialogError.value = '请填写模型 ID'
    return
  }
  if (!draft.name.trim()) {
    modelDialogError.value = '请填写显示名称'
    return
  }
  const duplicate = provider.models.some(m => m.id === draft.id.trim() && m !== editingModelRef.value)
  if (duplicate) {
    modelDialogError.value = '模型 ID 已存在'
    return
  }
  draft.id = draft.id.trim()
  draft.name = draft.name.trim()
  if (editingNewModel.value) {
    provider.models.push(JSON.parse(JSON.stringify(draft)))
  } else if (editingModelRef.value) {
    Object.assign(editingModelRef.value, JSON.parse(JSON.stringify(draft)))
  }
  await persist()
  modelDialogError.value = ''
  closeModelEditor()
}

function deleteModel(model) {
  const provider = selectedProvider.value
  provider.models = provider.models.filter(m => m !== model)
  persist()
}

async function runModelTestFor(provider, model) {
  await runModelTest(provider, model)
}

function modelTestResult(provider, model) {
  return testResult[testModelKey(provider, model)] || null
}

function formatTokenCount(value) {
  const num = Number(value)
  if (!num || num <= 0) return ''
  if (num >= 1000000) {
    const m = num / 1000000
    return `${Number.isInteger(m) ? m : m.toFixed(1)}M`
  }
  if (num >= 1000) {
    const k = num / 1000
    return `${Number.isInteger(k) ? k : k.toFixed(1)}K`
  }
  return String(num)
}
</script>

<template>
  <div class="models-page">
    <aside class="models-sidebar">
      <div class="models-sidebar__head">
        <span class="models-sidebar__title">{{ t.providers }}</span>
        <button class="pill-btn" @click="openProviderEditor">+ {{ t.addProvider }}</button>
      </div>
      <ul class="models-provider-list">
        <li
          v-for="p in providers"
          :key="p.name"
          :class="['models-provider-item', { active: p.name === selectedProvider?.name }]"
          @click="selectModelsProvider(p)"
        >
          <span class="models-provider-item__dot" :class="{ on: p.enabled }"></span>
          <span class="models-provider-item__label">{{ p.label }}</span>
          <span class="models-provider-item__count">{{ (p.models || []).length }}</span>
        </li>
      </ul>
    </aside>

    <section v-if="selectedProvider" class="models-panel">
      <div class="provider-info-card">
        <div class="provider-info-card__head">
          <div>
            <h2 class="provider-info-card__title">{{ selectedProvider.label }}</h2>
            <div class="provider-info-card__sub">{{ selectedProvider.name }}</div>
          </div>
          <div class="provider-info-card__actions">
            <button class="secondary-button" @click="openProviderEdit(selectedProvider)"><Settings :size="14" />{{ t.editProvider }}</button>
            <button class="danger-button" @click="requestDeleteProvider(selectedProvider)"><Trash2 :size="14" />{{ t.delete }}</button>
          </div>
        </div>
        <div class="provider-info-grid">
          <div class="provider-info-item">
            <span class="provider-info-item__label">Base URL</span>
            <span class="provider-info-item__value">{{ selectedProvider.baseUrl || '-' }}</span>
          </div>
          <div class="provider-info-item">
            <span class="provider-info-item__label">API Key</span>
            <span class="provider-info-item__value">
              <template v-if="selectedProvider.apiKey">
                {{ showProviderApiKey ? selectedProvider.apiKey : '••••••••' }}
                <button class="link-btn" @click="toggleApiKeyVisible">{{ apiKeyVisibilityLabel }}</button>
              </template>
              <template v-else>-</template>
            </span>
          </div>
          <div class="provider-info-item">
            <span class="provider-info-item__label">{{ t.enabled }}</span>
            <span class="provider-info-item__value">
              <label class="switch">
                <input type="checkbox" v-model="selectedProvider.enabled" @change="persist()" />
                <span class="switch__track"></span>
              </label>
            </span>
          </div>
        </div>
      </div>

      <div class="models-section">
        <div class="models-section__head">
          <h3 class="models-section__title">
            {{ t.models }}
            <span class="models-section__count">{{ selectedProvider.models.length }}</span>
          </h3>
          <button class="pill-btn" @click="openAddModel">+ {{ t.addModel }}</button>
        </div>

        <div v-if="selectedProvider.models.length" class="model-list">
          <div v-for="model in selectedProvider.models" :key="model.id" class="model-card">
            <div class="model-card__main">
              <div class="model-card__title">{{ model.name }}</div>
              <div class="model-card__id">{{ model.id }}</div>
              <div class="model-card__meta">
                <span class="badge">{{ model.api }}</span>
                <span v-if="model.baseUrl" class="badge">{{ model.baseUrl }}</span>
                <span v-if="model.contextWindow" class="badge">上下文 {{ formatTokenCount(model.contextWindow) }}</span>
                <span v-if="model.maxTokens" class="badge">输出 {{ formatTokenCount(model.maxTokens) }}</span>
              </div>
              <div class="model-card__caps">
                <span class="cap-tag" :class="{ 'cap-tag--off': !model.input?.includes('image') }"><IconImage /> 图片输入</span>
                <span class="cap-tag" :class="{ 'cap-tag--off': !model.reasoning }"><IconBrain /> 思考模式</span>
                <span class="cap-tag" :class="{ 'cap-tag--off': !model.capabilities?.toolCall }"><IconWrench /> 工具调用</span>
              </div>
            </div>
            <div class="model-card__actions">
              <button class="secondary-button" @click="openEditModel(model)"><Settings :size="14" />{{ t.edit }}</button>
              <button
                class="secondary-button"
                :disabled="testingModels[testModelKey(selectedProvider, model)]"
                @click="runModelTestFor(selectedProvider, model)"
              >
                <IconPlay v-if="!testingModels[testModelKey(selectedProvider, model)]" />
                <LoaderCircle v-else class="spin" :size="18" />
                {{ t.test }}
              </button>
              <button class="danger-button" @click="deleteModel(model)"><IconTrash />{{ t.delete }}</button>
            </div>
              <div
              v-if="modelTestResult(selectedProvider, model)"
              :class="['model-card__result', modelTestResult(selectedProvider, model).ok ? 'ok' : 'fail']"
            >
              {{ modelTestResult(selectedProvider, model).ok ? t.testOk : t.testErr }}
              · {{ modelTestResult(selectedProvider, model).ok ? (modelTestResult(selectedProvider, model).latency + ' ms') : modelTestResult(selectedProvider, model).error }}
            </div>
          </div>
        </div>
        <div v-else class="models-empty">暂无模型，点击「{{ t.addModel }}」新建一个。</div>
      </div>
    </section>

    <section v-else class="models-empty-state">
      <p>请选择左侧服务商，或点击「{{ t.addProvider }}」新增。</p>
    </section>

    <div v-if="modelEditorOpen" class="modal-backdrop" @click.self="closeModelEditor">
      <div class="modal model-editor-dialog">
        <div class="modal__header">
          <h3>{{ editingNewModel ? t.addModel : t.editModel }}</h3>
          <button class="modal__close" @click="closeModelEditor">×</button>
        </div>
        <div class="modal__body" v-if="modelDraft">
          <div class="model-form">
            <div class="field">
              <label>{{ t.modelId }}</label>
              <input v-model="modelDraft.id" placeholder="gpt-4o" />
            </div>
            <div class="field">
              <label>{{ t.displayName }}</label>
              <input v-model="modelDraft.name" placeholder="GPT-4o" />
            </div>
            <div class="field">
              <label>API 协议</label>
              <select v-model="modelDraft.api">
                <option v-for="opt in apiOptions" :key="opt" :value="opt">{{ opt }}</option>
              </select>
            </div>
            <div class="field field--wide">
              <label>Base URL</label>
              <input v-model="modelDraft.baseUrl" placeholder="留空则使用服务商 Base URL" />
              <span class="hint">实际请求地址：{{ modelRequestRoute(selectedProvider, modelDraft) }}</span>
            </div>
            <div class="field">
              <label>上下文窗口</label>
              <input type="number" v-model.number="modelDraft.contextWindow" />
            </div>
            <div class="field">
              <label>最大输出 Token</label>
              <input type="number" v-model.number="modelDraft.maxTokens" />
            </div>
            <div class="field">
              <label>默认思考级别</label>
              <select v-model="modelDraft.defaultThinkingLevel">
                <option v-for="level in piThinkingLevels" :key="level" :value="level">{{ level }}</option>
              </select>
            </div>
            <div class="field field--wide">
              <label>模型能力</label>
              <div class="capability-options">
                <label class="capability-option">
                  <input type="checkbox" v-model="modelDraft.reasoning" />
                  <span>推理模型</span>
                </label>
                <label class="capability-option">
                  <input type="checkbox" :checked="modelDraft.input.includes('image')" @change="toggleImageInput(modelDraft, $event.target.checked)" />
                  <span>图片输入</span>
                </label>
                <label class="capability-option">
                  <input type="checkbox" v-model="modelDraft.capabilities.toolCall" />
                  <span>工具调用</span>
                </label>
              </div>
            </div>

            <details class="compat-section">
              <summary>高级参数 (compat)</summary>
              <div class="compat-section__body">
                <div class="compat-bools">
                  <label v-for="item in piCompatBooleanFields" :key="item.key" class="capability-option">
                    <input type="checkbox" v-model="modelDraft.compat[item.key]" />
                    <span>{{ t[item.hint] }}</span>
                  </label>
                </div>
                <div class="compat-json">
                  <label>原始 JSON</label>
                  <textarea :value="formatCompat(modelDraft)" @input="updateCompatJson(modelDraft, $event)"></textarea>
                </div>
              </div>
            </details>

            <p v-if="modelDialogError" class="model-form__error">{{ modelDialogError }}</p>
          </div>
        </div>
        <div class="modal__footer">
          <button class="ghost-btn" @click="closeModelEditor">{{ t.cancel }}</button>
          <button class="pill-btn" :disabled="saving" @click="saveModel">{{ t.save }}</button>
        </div>
      </div>
    </div>
  </div>
</template>
