import { createRenderer, h, ref, computed, defineComponent } from 'vue'
import { compile } from '@vue/compiler-dom'
import { writeFileSync } from 'node:fs'
import { Wrench, Search, FileText, LoaderCircle, CheckCircle2, Calculator } from 'lucide-vue-next'

const Vue = await import('vue')
function compileRender(template, bindingMetadata) {
  const { code } = compile(template, { mode: 'function', prefixIdentifiers: true, hoistStatic: false, cacheHandlers: false, bindingMetadata })
  return new Function('Vue', code)(Vue)
}
function makeEl(tag) { return { tag, children: [], props: {}, parent: null, isElement: true } }
function makeText(text) { return { text, parent: null, isText: true } }
const nodeOps = {
  createElement: (tag) => makeEl(tag), createText: (text) => makeText(text),
  setText: (n, t) => { n.text = t }, setElementText: (el, t) => { el.children = [makeText(t)] },
  insert: (child, parent, anchor) => { if (child.parent) nodeOps.remove(child); const i = anchor ? parent.children.indexOf(anchor) : -1; if (i < 0) parent.children.push(child); else parent.children.splice(i, 0, child); child.parent = parent },
  remove: (child) => { const p = child.parent; if (!p) return; const i = p.children.indexOf(child); if (i >= 0) p.children.splice(i, 1); child.parent = null },
  parentNode: (n) => n.parent, nextSibling: (n) => { const p = n.parent; if (!p) return null; const i = p.children.indexOf(n); return p.children[i + 1] || null },
  querySelector: () => null, setScopeId: () => {}, insertStaticContent: () => { const el = makeEl('static'); return [el, el] },
}
function patchProp(el, key, prev, next) { if (key === 'is' || key.startsWith('on')) return; el.props[key] = next }
const renderer = createRenderer({ patchProp, ...nodeOps })

function toolIcon(message) {
  const name = ((message.toolName) || '').toLowerCase()
  if (name.includes('search')) return Search
  if (name.includes('calc')) return Calculator
  return Wrench
}
const childRender = compileRender(
  `<article>
    <details class="tool-call" :open="!!editDiff">
      <summary>
        <span class="tool-call__icon"><component :is="icon" :size="13" /></span>
        <span class="tool-call__state"><CheckCircle2 v-if="done"/><LoaderCircle v-else/></span>
      </summary>
      <div v-if="editDiff" class="d"></div><div v-else class="e">{{ detail }}</div>
    </details>
    <div class="message-content">{{ now }}</div>
  </article>`,
  { icon: 'setup', editDiff: 'setup', done: 'setup', detail: 'props', now: 'props' }
)
const Child = defineComponent({
  props: { message: Object, now: Number },
  setup(props) {
    return { icon: computed(() => toolIcon(props.message)), editDiff: computed(() => props.message.editDiff || null), done: computed(() => props.message.done === true), detail: computed(() => props.message.detail || '') }
  },
  render: childRender,
})
// displayMessages FILTERS out role==='tool' && status==='plan'
const parentRender = compileRender(
  `<div class="message-list"><Child v-for="message in displayMessages" :key="message.id" :message="message" :now="needsRuntimeUpdate(message) ? runtimeNow : 0"/></div>`,
  { displayMessages: 'setup', needsRuntimeUpdate: 'setup', runtimeNow: 'setup' }
)
const controls = {}
const Parent = defineComponent({
  setup() {
    const runtimeNow = ref(0)
    const messages = ref([
      { id: '1', toolName: 'read', done: false, status: 'running', live: true },
      { id: '2', toolName: 'search', done: true, status: 'done' },
      { id: '3', toolName: 'calc', done: true, status: 'done', editDiff: { x: 1 } },
    ])
    const displayMessages = computed(() => messages.value.filter(m => !(m.role === 'tool' && m.status === 'plan')))
    const needsRuntimeUpdate = (m) => m.live === true || m.status === 'running'
    Object.assign(controls, { runtimeNow, messages, displayMessages })
    return { displayMessages, needsRuntimeUpdate, runtimeNow }
  },
  render: parentRender,
})
const host = makeEl('host')
let errored = false, firstErr = ''
const app = renderer.createApp(Parent)
app.component('Child', Child)
app.component('CheckCircle2', CheckCircle2)
app.component('LoaderCircle', LoaderCircle)
app.config.errorHandler = (err) => { errored = true; if (!firstErr) firstErr = err && err.message; }
app.mount(host)

let ticks = 0
const timer = setInterval(() => {
  controls.runtimeNow.value = Date.now()
  ticks++
  if (ticks === 5) { controls.messages.value.push({ id: '4', toolName: 'write', done: false, status: 'plan', live: true }) } // filtered out
  if (ticks === 10) { controls.messages.value[0].status = 'done'; controls.messages.value[0].live = false } // unfilter
  if (ticks === 15) { controls.messages.value[0].status = 'plan' } // filtered out again
  if (ticks === 20) { controls.messages.value.push({ id: '5', toolName: 'read', done: true, status: 'done' }) } // added
  if (ticks >= 30) {
    clearInterval(timer)
    writeFileSync('_repro_result.txt', 'errored=' + errored + ' firstErr=' + firstErr + '\n')
    console.log('DONE errored=' + errored + ' firstErr=' + firstErr)
  }
}, 20)
