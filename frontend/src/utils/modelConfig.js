// Boolean compatibility flags supported by the Pi model runtime.
export const piCompatBooleanFields = [
  { key: 'supportsStore', hint: 'compatSupportsStore' },
  { key: 'supportsDeveloperRole', hint: 'compatSupportsDeveloperRole' },
  { key: 'supportsReasoningEffort', hint: 'compatSupportsReasoningEffort' },
  { key: 'supportsUsageInStreaming', hint: 'compatSupportsUsageInStreaming' },
  { key: 'requiresToolResultName', hint: 'compatRequiresToolResultName' },
  { key: 'requiresAssistantAfterToolResult', hint: 'compatRequiresAssistantAfterToolResult' },
  { key: 'requiresThinkingAsText', hint: 'compatRequiresThinkingAsText' },
  { key: 'requiresReasoningContentOnAssistantMessages', hint: 'compatRequiresReasoningContent' },
  { key: 'zaiToolStream', hint: 'compatZaiToolStream' },
  { key: 'supportsStrictMode', hint: 'compatSupportsStrictMode' },
  { key: 'sendSessionAffinityHeaders', hint: 'compatSendSessionAffinityHeaders' },
  { key: 'sendSessionIdHeader', hint: 'compatSendSessionIdHeader' },
  { key: 'supportsLongCacheRetention', hint: 'compatSupportsLongCacheRetention' }
]

// Thinking payload formats supported by the Pi model runtime.
export const piThinkingFormats = [
  'openai', 'openrouter', 'deepseek', 'together', 'zai',
  'qwen', 'chat-template', 'qwen-chat-template', 'string-thinking', 'ant-ling'
]

// Thinking levels supported by configurable models.
export const piThinkingLevels = ['minimal', 'low', 'medium', 'high', 'xhigh', 'max']

function ensureCompat(target) {
  if (!target.compat || Array.isArray(target.compat) || typeof target.compat !== 'object') target.compat = {}
  return target.compat
}

// Returns a tri-state string for a boolean compatibility field.
export function compatBooleanValue(target, key) {
  const value = target?.compat?.[key]
  return typeof value === 'boolean' ? String(value) : ''
}

// Sets or removes a boolean compatibility field.
export function setCompatBoolean(target, key, value) {
  const compat = ensureCompat(target)
  if (value === '') delete compat[key]
  else compat[key] = value === 'true'
}

// Returns the configured string value for a compatibility field.
export function compatStringValue(target, key) {
  const value = target?.compat?.[key]
  return typeof value === 'string' ? value : ''
}

// Sets or removes a string compatibility field.
export function setCompatString(target, key, value) {
  const compat = ensureCompat(target)
  if (value === '') delete compat[key]
  else compat[key] = value
}

// Formats a compatibility object for the advanced JSON editor.
export function formatCompat(target) {
  return JSON.stringify(target?.compat || {}, null, 2)
}

function protocolEndpoint(api) {
  if (api === 'openai-responses') return 'responses'
  if (api === 'openai-codex-responses') return 'responses'
  if (api === 'azure-openai-responses') return 'responses'
  if (api === 'anthropic-messages') return 'messages'
  if (api === 'google-generative-ai') return 'models/{model}:streamGenerateContent'
  if (api === 'google-vertex') return 'models/{model}:streamGenerateContent'
  return 'chat/completions'
}

function effectiveModelBaseUrl(provider, model) {
  const providerBase = String(provider?.baseUrl || '').trim().replace(/\/+$/, '')
  const modelBase = String(model?.baseUrl || '').trim()
  if (!modelBase) return providerBase
  if (/^https?:\/\//i.test(modelBase)) return modelBase.replace(/\/+$/, '')
  if (!providerBase) return modelBase
  return `${providerBase}/${modelBase.replace(/^\/+/, '')}`.replace(/\/+$/, '')
}

// Builds the effective request route shown by the model editor.
export function modelRequestRoute(provider, model) {
  const base = effectiveModelBaseUrl(provider, model) || '—'
  return base === '—' ? base : `${base}/${protocolEndpoint(model?.api)}`
}

// Applies provider and model defaults while preserving existing settings.
export function normalizeProvider(provider) {
  provider.enabled ??= true
  const legacyApi = provider.api || (provider.type === 'anthropic' ? 'anthropic-messages' : provider.type === 'google' ? 'google-generative-ai' : provider.type === 'openai-responses' ? 'openai-responses' : 'openai-completions')
  ensureCompat(provider)
  provider.models ||= []
  provider.models.forEach(model => {
    model.api ||= legacyApi
    model.baseUrl ||= ''
    model.input ||= ['text']
    model.maxTokens ||= 16384
    model.capabilities ||= { toolCall: true }
    ensureCompat(model)
  })
  provider.api = ''
  return provider
}
