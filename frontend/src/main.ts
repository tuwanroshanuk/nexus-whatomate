import { createApp } from 'vue'
import { createPinia } from 'pinia'
import { VueQueryPlugin } from '@tanstack/vue-query'

import App from './App.vue'
import router from './router'
import { i18n } from './i18n'

import './assets/fonts.css'
import './assets/index.css'
import './assets/nexus-theme.css'

const app = createApp(App)

app.use(createPinia())
app.use(router)
app.use(VueQueryPlugin)
app.use(i18n)

app.mount('#app')

// White-label visible upstream branding without changing internal API identifiers.
const applyNexusBranding = (root: Node = document.body) => {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT)
  let node: Node | null

  while ((node = walker.nextNode())) {
    const value = node.nodeValue
    if (value?.includes('Whatomate')) {
      node.nodeValue = value.replaceAll('Whatomate', 'Nexus One')
    }
  }

  document.title = document.title.replaceAll('Whatomate', 'Nexus One')
}

applyNexusBranding()

const brandingObserver = new MutationObserver((mutations) => {
  for (const mutation of mutations) {
    for (const addedNode of mutation.addedNodes) {
      applyNexusBranding(addedNode)
    }
  }
})

brandingObserver.observe(document.body, {
  childList: true,
  subtree: true,
})
