// Creates a JSON-safe deep clone for configuration and event payloads.
export function safeClone(value) {
  return JSON.parse(JSON.stringify(value))
}

// Rejects a promise with the supplied message when its hard timeout elapses.
export function withTimeout(promise, ms, timeoutMessage) {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(timeoutMessage)), ms)
    Promise.resolve(promise).then(
      value => { clearTimeout(timer); resolve(value) },
      error => { clearTimeout(timer); reject(error) }
    )
  })
}

// Converts known backend error details to concise user-facing messages.
export function localizeError(raw) {
  if (!raw) return raw
  const mappings = [
    [/duplicate agent id/i, 'Agent ID 重复'],
    [/no enabled model providers/i, '没有启用的服务商'],
    [/invalid base URL/i, '基础域名无效'],
    [/requires a base URL/i, '需要填写基础域名'],
    [/unsupported API protocol/i, '不支持的 API 协议'],
    [/does not exist or its provider is disabled/i, '默认模型不存在或所属服务商已停用'],
    [/maximum concurrent task limit reached/i, '并发任务已达到上限（4）'],
    [/empty (ID|key)/i, '存在空的名称或标识'],
    [/API key environment variable (\w+) is not set for provider (.+)/i, '所选模型缺少 API Key 环境变量，请先在模型设置中配置'],
    [/exceeded the \d+ second execution limit/i, '工具执行超过时长限制，已自动中止']
  ]
  for (const [pattern, text] of mappings) {
    if (pattern.test(raw)) return text
  }
  return raw
}
