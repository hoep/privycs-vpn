import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import router from './router'
import './style.css'
import 'flag-icons/css/flag-icons.min.css'
import { EventsOn } from '../wailsjs/runtime/runtime'
import { usePoolStore } from './stores/pool'

// Apply initial theme before first paint to avoid flash.
// The saved setting is loaded later in SettingsView.loadSettings().
// For now, detect system preference as default.
const prefersDark = window.matchMedia('(prefers-color-scheme: dark)').matches
if (prefersDark) {
  document.documentElement.classList.add('dark')
}

const app = createApp(App)
const pinia = createPinia()
app.use(pinia)
app.use(router)

// Subscribe to pool:bootstrap BEFORE mounting any view so the active
// pool is in the store on the very first render. The backend emits
// this at the end of App.startup() with a synchronous snapshot.
// ConnectionView's onMounted also calls poolStore.bootstrap() as a
// fallback in case the event fired before this listener attached
// (race window: WebView paints before main.ts runs).
const poolStore = usePoolStore(pinia)
EventsOn('pool:bootstrap', (snap: any) => {
  poolStore.applyBootstrap(snap)
})

app.mount('#app')
