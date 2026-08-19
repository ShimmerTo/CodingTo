// 设置页定位导航：聊天区「精简模式」提示点击后，跳到设置页并滚动定位到
// 「精简对话」开关。SettingsPage 为异步组件，每次打开都会重新挂载，因此在
// 挂载时读取一次标志即可完成定位；这里使用模块级 ref 跨组件共享。
import { ref } from 'vue'

// 置为 true 表示下一次打开设置页时需要定位到「精简对话」开关。
export const conciseToggleFocus = ref(false)

export function requestConciseToggleFocus() {
  conciseToggleFocus.value = true
}
