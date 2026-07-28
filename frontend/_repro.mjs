import { createRenderer, h, ref, computed, defineComponent } from 'vue'
import { compile } from '@vue/compiler-dom'
import { writeFileSync } from 'node:fs'

const Vue = await import('vue')

function compileRender(template, bindingMetadata) {
  const { code } = compile(template, {
    mode: 'function',
    prefixIdentifiers: true,
    hoistStatic: false,
    cacheHandlers: false,
    bindingMetadata,
  })
  return new Function('Vue', code)(Vue)
}

// stub DOM
function makeEl(tag) { return { tag, children: [], props: {}, parent: null, isElement: true } }
function makeText(text) { return { text, parent: null, isText: true } }
const nodeOps = {
  createElement: (tag) => makeEl(tag),
  createText: (text) => makeText(text),
  setText: (node, text) => { node.text = text },
  setElementText: (el, text) => { el.children = [makeText(text)] },
  insert: (child, parent, anchor) => {
    if (child.parent) nodeOps.remove(child)
    const i = anchor ? parent.children.indexOf(anchor) : -1
    if (i < 0) parent.children.push(child); else parent.children.splice(i, 0, child)
    child.parent = parent
  },
  remove: (child) => {
    const p = child.parent
    if (!p) return
    const i = p.children.indexOf(child)
    if (i >= 0) p.children.splice(i, 1)
    child.parent = null
  },
  parentNode: (node) => node.parent,
  nextSibling: (node) => {
    const p = node.parent
    if (!p) return null
    const i = p.children.indexOf(node)
    return p.children[i + 1] || null
  },
  querySelector: () => null,
  setScopeId: () => {},
  insertStaticContent: (content) => { const el = makeEl('static'); return [el, el] },
}
function patchProp(el, key, prev, next) {
  if (key === 'is' || key.startsWith('on')) return
  el.props[key] = next
}
const renderer = createRenderer({ patchProp, ...nodeOps })

// lucide-like functional icons
const IconA = { name: 'IconA', render: () => h('span', 'A') }
const IconB = { name: 'IconB', render: () => h('span', 'B') }
const IconC = { name: 'IconC', render: () => h('span', 'C') }
function iconFor(msg) {
  if (msg.icon === 'a') return IconA
  if (msg.icon === 'b') return IconB
  return IconC
}

// Child ~ ChatMessageItem
const childRender = compileRender(
  `<article><component :is="toolIcon" :size="13"/><span>{{ now }}</span></article>`,
  { toolIcon: 'setup', now: 'props' }
)
const Child = defineComponent({
  props: { message: Object, now: Number },
  setup(props) {
    return { toolIcon: computed(() => iconFor(props.message)) }
  },
  render: childRender,
})

// Parent ~ ChatMessages
const parentRender = compileRender(
  `<div class="message-list"><Child v-for="message in messages" :key="message.id" :message="message" :now="needsRuntimeUpdate(message) ? runtimeNow : 0"/></div>`,
  { messages: 'setup', needsRuntimeUpdate: 'setup', runtimeNow: 'setup' }
)
const controls = {}
const Parent = defineComponent({
  setup() {
    const runtimeNow = ref(0)
    const messages = ref([
      { id: '1', icon: 'a', live: true },
      { id: '2', icon: 'b', live: false },
      { id: '3', icon: 'c', live: false },
    ])
    const needsRuntimeUpdate = (m) => m.live === true
    Object.assign(controls, { runtimeNow, messages })
    return { messages, needsRuntimeUpdate, runtimeNow, Child }
  },
  render: parentRender,
})

const host = makeEl('host')
let errored = false
const app = renderer.createApp(Parent)
app.component('Child', Child)
app.config.errorHandler = (err) => {
  errored = true
  console.error('CAUGHT ERROR:', err && err.stack ? err.stack.split('\n').slice(0, 4).join('\n') : err)
}
app.mount(host)

// simulate runtimeNow timer (50ms) for ~1s
let ticks = 0
const timer = setInterval(() => {
  controls.runtimeNow.value = Date.now()
  ticks++
  if (ticks >= 20) {
    clearInterval(timer)
    console.log('DONE ticks=' + ticks + ' errored=' + errored)
    writeFileSync('_repro_result.txt', 'errored=' + errored + ' ticks=' + ticks + '\n')
    if (!errored) {
      // now mutate a message to switch icon (simulate tool name change)
      controls.messages.value[0].icon = 'b'
      controls.runtimeNow.value = Date.now()
    }
  }
}, 50)
