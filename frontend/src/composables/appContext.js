import { inject } from 'vue'

export const appContextKey = Symbol('app-context')

// 返回智能体要展示的头像字符；为空时使用默认图标。
export function agentAvatar(agent) {
  return (agent && agent.avatar && typeof agent.avatar === 'string' && agent.avatar.trim()) || ''
}

export function useAppContext() {
  const context = inject(appContextKey)
  if (!context) throw new Error('App context is not available')
  return context
}
