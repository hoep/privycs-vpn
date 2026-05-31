import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import router from './router'
import { i18n, setLocale } from './i18n'
import './style.css'
import 'flag-icons/css/flag-icons.min.css'
import { EventsOn } from '../wailsjs/runtime/runtime'
import { GetSettings } from '../wailsjs/go/main/App'
import { usePoolStore } from './stores/pool'
import { initCrashReporter } from './crashReporter'

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
app.use(i18n)

// Apply OS-default locale before first paint. The persisted user
// choice (settings.app_language) is loaded async after mount and
// overrides this — mirrors the theme pre-load pattern above.
setLocale('')

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

// Apply the persisted in-app language after mount. Fire-and-forget:
// if the Wails RPC fails the UI stays on the OS-default locale.
// v1.0.7: also load CrashReportsEnabled + InstallUUID and gate the
// Vue-side Sentry SDK init on the same opt-in flag the Go side
// honours. Same Bugsink endpoint, separate DSN per surface so the
// dashboard can split frontend-only vs Go-only issues.
GetSettings()
  .then((s: any) => {
    setLocale(s.app_language || '')
    if (s.crash_reports_enabled) {
      initCrashReporter(app, router, s.install_uuid || '')
    }
  })
  .catch(() => {})
