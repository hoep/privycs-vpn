<template>
  <!-- Self-constraining height + own scroll container — same
       pattern as SettingsView / LogsView / etc. The App.vue
       <main class="flex-1 overflow-y-auto"> wrapper does NOT
       reliably bound child height (a Tailwind flex-1 + overflow
       interaction that lets the child grow past its allocation,
       pushing siblings — including the bottom <nav> — out of
       the viewport). The other long-content views already work
       around this with max-h-[calc(100vh-7rem)]; HelpView now
       follows suit. v0.9.14.79's outer-flex fix wasn't enough
       on its own; v0.9.14.80 adopts the project convention. -->
  <!-- overflow-x-hidden on the outer container catches the horizontal
       overflow that wide markdown tables / long URLs / unwrapped code
       lines produce. Combined with the table-wrap rule in <style>
       below (display:block + overflow-x:auto on tables), wide tables
       get their own internal horizontal scrollbar instead of stretching
       the whole help view. v0.9.14.81 fix. -->
  <div class="text-sm leading-relaxed overflow-y-auto overflow-x-hidden max-h-[calc(100vh-7rem)]">
    <div v-if="state === 'loading'" class="flex flex-col items-center justify-center py-16 gap-2">
      <div class="w-8 h-8 border-2 border-primary-400 border-t-transparent rounded-full animate-spin"></div>
      <span class="text-xs text-gray-500 dark:text-gray-400">Loading help…</span>
    </div>

    <div v-else-if="state === 'error'" class="flex flex-col items-center justify-center py-16 px-6 text-center">
      <p class="text-sm font-semibold text-gray-700 dark:text-gray-200 mb-1">Could not load help</p>
      <p class="text-xs text-gray-500 dark:text-gray-400 mb-4 max-w-sm">{{ errorMessage }}</p>
      <button
        @click="load"
        class="px-3 py-1.5 text-xs rounded-md bg-primary-500 hover:bg-primary-600 text-white transition-colors"
      >
        Retry
      </button>
    </div>

    <!-- @click-capture intercepts every <a> click and routes it
         through Wails' BrowserOpenURL, which calls the OS default
         browser. target="_blank" alone is a dead-end inside the
         Wails Webview (Webview2 on Win / WKWebView on Mac /
         WebKitGTK on Linux all silently drop window.open by
         default). v0.9.14.79: was non-functional in v0.9.14.78. -->
    <div
      v-else
      class="markdown-body px-6 py-5"
      v-html="renderedHtml"
      @click.capture="onMarkdownClick"
    ></div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import MarkdownIt from 'markdown-it'
import { BrowserOpenURL } from '../../wailsjs/runtime/runtime'

// Live source for the desktop-client doc. Bumped per release with the
// rest of the doc tree; the help screen always shows the current
// version because it fetches at view-mount time. Network failure
// surfaces the error UI with a Retry button — no offline fallback
// (the doc is small enough that a single failed fetch is rare and
// the user can always read the same content on the website itself).
const DOC_URL = 'https://www.privycs.com/docs/desktop-client.md'

const md = new MarkdownIt({
  html: true,
  linkify: true,
  breaks: false,
})

// Note: target="_blank" / window.open are dead-ends inside Wails'
// embedded Webview (Webview2 on Windows, WKWebView on macOS,
// WebKitGTK on Linux all silently drop window.open by default).
// Instead we attach an @click.capture handler to the rendered
// container (see template) that routes every <a href> through
// runtime.BrowserOpenURL, which crosses the Wails IPC bridge and
// calls the OS default browser. The markdown-it link_open rule
// stays at default — no target munging needed.

function onMarkdownClick(e: MouseEvent) {
  // Walk up the event-target chain to the nearest <a>. Markdown-rendered
  // anchors can have nested <code> or <strong> children that fire the
  // click; we want the href on the parent link, not those inner spans.
  let el = e.target as HTMLElement | null
  while (el && el.tagName !== 'A') {
    el = el.parentElement
  }
  if (!el) return
  const href = (el as HTMLAnchorElement).getAttribute('href')
  if (!href) return
  // In-page anchor links (#section) are scrolled to natively; do not
  // hijack those into the system browser.
  if (href.startsWith('#')) return
  e.preventDefault()
  e.stopPropagation()
  try {
    BrowserOpenURL(href)
  } catch {
    // Wails runtime missing (rare — only happens when not running
    // inside the Wails container, e.g. vite dev preview). Fall
    // back to a regular open which still might work in dev.
    window.open(href, '_blank')
  }
}

const state = ref<'loading' | 'error' | 'loaded'>('loading')
const markdown = ref<string>('')
const errorMessage = ref<string>('')

const renderedHtml = computed(() => md.render(markdown.value))

async function load() {
  state.value = 'loading'
  errorMessage.value = ''
  try {
    const resp = await fetch(DOC_URL, { cache: 'no-store' })
    if (!resp.ok) {
      throw new Error(`HTTP ${resp.status}`)
    }
    markdown.value = await resp.text()
    state.value = 'loaded'
  } catch (e: any) {
    errorMessage.value = e?.message ?? String(e)
    state.value = 'error'
  }
}

onMounted(load)
</script>

<!-- Unscoped on purpose: Vue's <style scoped> + :deep() + :global()
     combo is unreliable for v-html'd content (the data-v-* selector
     attribute is not threaded onto the dynamically-rendered DOM in
     a predictable way, and dark-mode rules under :global() were
     not winning over the inherited body color in the Wails Webview2
     on Windows). Class-name `markdown-body` is unique to this view
     so there is no spillover risk.
     v0.9.14.81: was light-grey text on near-white background in
     light mode because <code>/<pre> had a background but no
     explicit text color, so they inherited a value somewhere up
     the tree that didn't render legibly inside the rounded code
     pill. Every code/pre now sets BOTH color and background in
     both modes, no inheritance trust. -->
<style>
.markdown-body h1 {
  font-size: 1.4rem;
  font-weight: 700;
  margin-top: 1.25rem;
  margin-bottom: 0.5rem;
  border-bottom: 1px solid rgb(229 231 235 / 0.5);
  padding-bottom: 0.35rem;
}
.markdown-body h2 {
  font-size: 1.15rem;
  font-weight: 700;
  margin-top: 1.4rem;
  margin-bottom: 0.5rem;
}
.markdown-body h3 {
  font-size: 1rem;
  font-weight: 600;
  margin-top: 1.1rem;
  margin-bottom: 0.4rem;
}
.markdown-body h4 {
  font-size: 0.9rem;
  font-weight: 600;
  margin-top: 0.9rem;
  margin-bottom: 0.3rem;
}
.markdown-body p {
  margin: 0.55rem 0;
}
.markdown-body ul,
.markdown-body ol {
  margin: 0.55rem 0;
  padding-left: 1.4rem;
}
.markdown-body li {
  margin: 0.2rem 0;
}

/* Inline code — light mode (default). gray-200 background, gray-900
   text. Explicit text color so it's never inherited from a stale
   ancestor. */
.markdown-body code {
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
  font-size: 0.85rem;
  color: rgb(17 24 39);                 /* gray-900 */
  background: rgb(229 231 235);         /* gray-200 */
  padding: 0.1rem 0.3rem;
  border-radius: 0.25rem;
}
/* Inline code — dark mode. */
.dark .markdown-body code {
  color: rgb(243 244 246);              /* gray-100 */
  background: rgb(55 65 81);            /* gray-700 */
}

/* Code blocks — light mode. Slightly darker bg than inline so the
   block is visually distinct, plus explicit dark text. */
.markdown-body pre {
  background: rgb(243 244 246);         /* gray-100 */
  color: rgb(17 24 39);                 /* gray-900 */
  padding: 0.7rem 0.85rem;
  border-radius: 0.4rem;
  overflow-x: auto;
  margin: 0.7rem 0;
}
.dark .markdown-body pre {
  background: rgb(31 41 55);            /* gray-800 */
  color: rgb(243 244 246);              /* gray-100 */
}
/* <code> inside <pre> shouldn't double up the pill background. */
.markdown-body pre code {
  background: transparent;
  color: inherit;
  padding: 0;
  border-radius: 0;
}

/* display:block + overflow-x:auto turns each table into its own
   horizontally-scrollable viewport. Without this, multi-column
   tables in our docs (Tunnel Health, Permissions, Compatibility…)
   stretch the help-view-wide and re-introduce a window-level
   horizontal scrollbar. The block display sacrifices native table
   width-balancing across columns, but for read-only doc content
   it looks fine. */
.markdown-body table {
  display: block;
  overflow-x: auto;
  max-width: 100%;
  border-collapse: collapse;
  margin: 0.7rem 0;
  font-size: 0.82rem;
}
/* Long URLs in <a> and shell commands in inline <code> can also
   trigger horizontal overflow; wrap them. */
.markdown-body a,
.markdown-body code {
  overflow-wrap: anywhere;
  word-break: break-word;
}
.markdown-body th,
.markdown-body td {
  border: 1px solid rgb(229 231 235 / 0.7);
  padding: 0.35rem 0.6rem;
  text-align: left;
}
.dark .markdown-body th,
.dark .markdown-body td {
  border-color: rgb(55 65 81 / 0.7);
}
.markdown-body th {
  background: rgb(243 244 246);
  font-weight: 600;
}
.dark .markdown-body th {
  background: rgb(31 41 55);
}
.markdown-body blockquote {
  border-left: 3px solid rgb(99 102 241);
  padding: 0.2rem 0.85rem;
  margin: 0.7rem 0;
  background: rgb(99 102 241 / 0.06);
  border-radius: 0 0.3rem 0.3rem 0;
}
.markdown-body a {
  color: rgb(99 102 241);
  text-decoration: none;
}
.markdown-body a:hover {
  text-decoration: underline;
}
.markdown-body hr {
  border: 0;
  border-top: 1px solid rgb(229 231 235 / 0.5);
  margin: 1.2rem 0;
}
.dark .markdown-body hr {
  border-top-color: rgb(55 65 81 / 0.6);
}
</style>
