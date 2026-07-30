// 每个扩展使用唯一且能表达其含义的图标。所有 key 互不相同，且不与其它
// 扩展或导航图标重复。新增扩展时请在此登记一个专属图标，不要复用现有图标。
import {
  Award,
  Binary,
  Blocks,
  FileText,
  Globe2,
  Image,
  KeyRound,
  ListChecks,
  ListTree,
  Package,
  Store,
} from 'lucide-vue-next'

// 关键扩展的固定图标，按含义选择且保证唯一。
const fixedExtensionIcons = {
  rtk: Binary,
  'pi-plugins': Store,
  'browser-native': Globe2,
  figma: Image,
  document: FileText,
  plan: ListChecks,
  subagent: Blocks,
  'browser-profile': KeyRound,
  'skills-list': ListTree,
}

// 其它第三方/自定义扩展的回退图标，按 key 语义映射，仍保证互不相同。
const fallbackExtensionIcons = {
  npm: Package,
  pip: Package,
  git: Blocks,
  docker: Package,
  mcp: Blocks,
  api: Globe2,
  http: Globe2,
  cli: Binary,
  database: Binary,
  plugin: Award,
}

export function extensionIcon(key) {
  if (!key) return Award
  const exact = fixedExtensionIcons[key] || fallbackExtensionIcons[key]
  if (exact) return exact
  // 未知 key：用通用但唯一的“奖章”图标，避免与某个具体扩展撞图标。
  return Award
}

export { fixedExtensionIcons, fallbackExtensionIcons }
