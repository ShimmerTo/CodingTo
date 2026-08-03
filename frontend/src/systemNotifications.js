import { NotificationService } from '../bindings/github.com/wailsapp/wails/v3/pkg/services/notifications'

let authorizationPromise = null

function isDesktopRuntime() {
  if (typeof window === 'undefined') return false
  return Boolean(
    window._wails?.environment?.OS
    || window.chrome?.webview?.postMessage
    || window.webkit?.messageHandlers?.external?.postMessage
    || window.wails?.invoke
  )
}

async function ensureNotificationAuthorization() {
  if (!authorizationPromise) {
    authorizationPromise = (async () => {
      if (await NotificationService.CheckNotificationAuthorization()) return true
      return NotificationService.RequestNotificationAuthorization()
    })().catch(() => false)
  }
  return authorizationPromise
}

export async function sendSystemNotification({ id, taskId, type, title, body }) {
  if (!isDesktopRuntime() || !await ensureNotificationAuthorization()) return false
  await NotificationService.SendNotification({
    id: String(id),
    title: String(title),
    body: String(body),
    interruptionLevel: 'active',
    // 结构化通知类型，供后续点击路由 / 日志归因等机器读取。
    // 不传 threadId：Windows 会把该值渲染成 toast header（codingto-task-xxx），
    // 观感杂乱；taskId 已写入 data，不影响归因与分组。
    data: {
      type: String(type || ''),
      taskId: String(taskId || '')
    }
  })
  return true
}
