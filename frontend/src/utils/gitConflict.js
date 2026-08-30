// parseGitConflictPoints extracts standard diff3 conflict-marker blocks while
// preserving exact string offsets for replacement in the editable result.
export function parseGitConflictPoints(text) {
  const source = String(text || '')
  const lines = []
  const matcher = /[^\r\n]*(?:\r\n|\r|\n|$)/g
  let match
  while ((match = matcher.exec(source)) && match[0]) {
    lines.push({ text: match[0].replace(/[\r\n]+$/, ''), start: match.index, end: matcher.lastIndex })
  }
  const points = []
  for (let index = 0; index < lines.length; index += 1) {
    if (!lines[index].text.startsWith('<<<<<<<')) continue
    const startIndex = index
    let baseMarker = -1
    let divider = -1
    let endMarker = -1
    for (let cursor = index + 1; cursor < lines.length; cursor += 1) {
      if (baseMarker < 0 && divider < 0 && lines[cursor].text.startsWith('|||||||')) baseMarker = cursor
      else if (lines[cursor].text === '=======') divider = cursor
      else if (divider >= 0 && lines[cursor].text.startsWith('>>>>>>>')) {
        endMarker = cursor
        break
      }
    }
    if (divider < 0 || endMarker < 0) continue
    const oursStart = lines[startIndex].end
    const oursEnd = lines[baseMarker >= 0 ? baseMarker : divider].start
    const baseStart = baseMarker >= 0 ? lines[baseMarker].end : oursEnd
    const baseEnd = baseMarker >= 0 ? lines[divider].start : baseStart
    const theirsStart = lines[divider].end
    const theirsEnd = lines[endMarker].start
    points.push({
      index: points.length,
      start: lines[startIndex].start,
      end: lines[endMarker].end,
      startLine: startIndex + 1,
      endLine: endMarker + 1,
      ours: source.slice(oursStart, oursEnd),
      base: source.slice(baseStart, baseEnd),
      theirs: source.slice(theirsStart, theirsEnd),
    })
    index = endMarker
  }
  return points
}
