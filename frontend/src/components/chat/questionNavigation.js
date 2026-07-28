export function findActiveQuestionIndex(offsets, scrollTop, clientHeight) {
  if (!offsets.length) return -1

  const focusOffset = Math.min(Math.max(clientHeight * 0.32, 72), 200)
  const focusPosition = scrollTop + focusOffset
  let activeIndex = 0

  for (let index = 0; index < offsets.length; index++) {
    if (offsets[index] > focusPosition) break
    activeIndex = index
  }

  return activeIndex
}
