import { nextTick, ref } from 'vue'

export function useChatAutoScroll() {
  const scrollEl = ref(null)
  const autoScrollEnabled = ref(true)
  const showScrollToBottom = ref(false)
  const programmaticScrollActive = ref(false)

  function isNearBottom(element = scrollEl.value) {
    return !element || element.scrollHeight - element.scrollTop - element.clientHeight <= 24
  }

  function updateScrollState() {
    const element = scrollEl.value
    if (!element) return
    if (isNearBottom(element)) {
      autoScrollEnabled.value = true
      showScrollToBottom.value = false
    } else if (!programmaticScrollActive.value) {
      autoScrollEnabled.value = false
      showScrollToBottom.value = true
    }
  }

  function onMessagesWheel(event) {
    if (event.deltaY < 0) {
      autoScrollEnabled.value = false
      showScrollToBottom.value = true
    }
  }

  function onMessagesScroll() {
    updateScrollState()
  }

  function setScrollTopImmediately(element) {
    const previousBehavior = element.style.scrollBehavior
    element.style.scrollBehavior = 'auto'
    element.scrollTop = element.scrollHeight
    element.style.scrollBehavior = previousBehavior
  }

  async function scrollToBottom(force = false) {
    if (!force && !autoScrollEnabled.value) {
      showScrollToBottom.value = true
      return
    }
    await nextTick()
    // A wheel-up can happen while this call is waiting for Vue to render the
    // latest token. User intent wins over the stale scheduled auto-scroll.
    if (!force && !autoScrollEnabled.value) {
      showScrollToBottom.value = true
      return
    }
    const element = scrollEl.value
    if (!element) return
    programmaticScrollActive.value = true
    // Streaming updates must not inherit CSS smooth scrolling. A smooth
    // animation can still be in flight when the composer/plan changes height;
    // its later scroll events would then look like a user scroll and disable
    // following before the animation reaches the new bottom.
    setScrollTopImmediately(element)
    requestAnimationFrame(() => {
      programmaticScrollActive.value = false
      updateScrollState()
    })
  }

  function scrollToBottomInstant() {
    const element = scrollEl.value
    if (!element) return
    setScrollTopImmediately(element)
    autoScrollEnabled.value = true
    showScrollToBottom.value = false
    updateScrollState()
  }

  async function onMessagesResize() {
    // ResizeObserver covers height changes that are not represented in the
    // message signature, including the execution-plan bar taking space below
    // the scroller and nested message content growing asynchronously.
    if (!autoScrollEnabled.value) {
      showScrollToBottom.value = true
      return
    }
    await scrollToBottom()
  }

  function scrollToBottomAndResume() {
    autoScrollEnabled.value = true
    scrollToBottom(true)
  }

  return {
    scrollEl,
    showScrollToBottom,
    onMessagesScroll,
    onMessagesWheel,
    onMessagesResize,
    scrollToBottom,
    scrollToBottomInstant,
    scrollToBottomAndResume
  }
}
