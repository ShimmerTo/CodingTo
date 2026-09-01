import { onBeforeUnmount, ref } from 'vue'

// Owns transient toast state and timer cleanup for the application shell.
export function useToasts() {
  const toasts = ref([])
  const timers = new Map()
  let toastSequence = 0

  function removeToast(id) {
    toasts.value = toasts.value.filter(item => item.id !== id)
    const timer = timers.get(id)
    if (timer != null) window.clearTimeout(timer)
    timers.delete(id)
  }

  function pushToast(type, text, timeout = 2800) {
    const id = ++toastSequence
    toasts.value.push({ id, type, text })
    timers.set(id, window.setTimeout(() => removeToast(id), timeout))
  }

  onBeforeUnmount(() => {
    for (const timer of timers.values()) window.clearTimeout(timer)
    timers.clear()
  })

  return { toasts, pushToast }
}
