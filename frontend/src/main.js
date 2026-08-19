import { createApp } from 'vue'
import App from './App.vue'
import InstallLogModal from './components/InstallLogModal.vue'
import './styles/base.css'
import './styles/layout.css'
import './styles/sidebar.css'
import './styles/chat.css'
import './styles/content.css'
import './styles/plugins.css'
import './styles/agents.css'
import './styles/environment.css'
import './styles/db.css'
import './styles/settings.css'
import './styles/providers.css'
import './styles/skills.css'
import './styles/skill-card.css'
import './styles/media.css'

createApp(App).mount('#app')

// Mount the global install-progress overlay. It is a standalone app so it can
// stream long-running installs (e.g. Playwright browser downloads) without
// touching App.vue, and stays visible even if the underlying page changes.
const installLogEl = document.createElement('div')
document.body.appendChild(installLogEl)
createApp(InstallLogModal).mount(installLogEl)
