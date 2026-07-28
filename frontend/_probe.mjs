import { compile } from '@vue/compiler-dom'

const tpl = `<article><component :is="toolIcon" :size="13"/><span>{{ now }}</span></article>`
const { code } = compile(tpl, {
  mode: 'function',
  prefixIdentifiers: true,
  bindingMetadata: { toolIcon: 'setup', now: 'props' },
})
console.log(code)
