<template>
  <div class="flex flex-col flex-1 min-h-0">
    <div v-if="state === 'loading'" class="flex-1 flex items-center justify-center">
      <div class="flex flex-col items-center gap-2">
        <div class="w-8 h-8 border-2 border-primary-400 border-t-transparent rounded-full animate-spin"></div>
        <span class="text-xs text-gray-500 dark:text-gray-400">Loading help…</span>
      </div>
    </div>

    <div v-else-if="state === 'error'" class="flex-1 flex items-center justify-center px-6">
      <div class="text-center max-w-md">
        <p class="text-sm font-semibold text-gray-700 dark:text-gray-200 mb-1">Could not load help</p>
        <p class="text-xs text-gray-500 dark:text-gray-400 mb-4">{{ errorMessage }}</p>
        <button
          @click="load"
          class="px-3 py-1.5 text-xs rounded-md bg-primary-500 hover:bg-primary-600 text-white transition-colors"
        >
          Retry
        </button>
      </div>
    </div>

    <div
      v-else
      class="markdown-body flex-1 overflow-y-auto px-6 py-5 text-sm leading-relaxed"
      v-html="renderedHtml"
    ></div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import MarkdownIt from 'markdown-it'

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

// Make every link open in the user's default browser instead of the
// embedded webview that Wails uses by default. Wails-bound webview
// has no top-bar and no back button, so an in-frame navigation
// would strand the user. This adds target="_blank" + rel attributes
// to every <a> rendered by markdown-it.
const defaultRender =
  md.renderer.rules.link_open ||
  function (tokens, idx, options, _env, self) {
    return self.renderToken(tokens, idx, options)
  }
md.renderer.rules.link_open = function (tokens, idx, options, env, self) {
  const t = tokens[idx]
  const aIndex = t.attrIndex('target')
  if (aIndex < 0) {
    t.attrPush(['target', '_blank'])
  } else {
    t.attrs![aIndex][1] = '_blank'
  }
  const relIndex = t.attrIndex('rel')
  if (relIndex < 0) {
    t.attrPush(['rel', 'noopener noreferrer'])
  } else {
    t.attrs![relIndex][1] = 'noopener noreferrer'
  }
  return defaultRender(tokens, idx, options, env, self)
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

<style scoped>
.markdown-body :deep(h1) {
  font-size: 1.4rem;
  font-weight: 700;
  margin-top: 1.25rem;
  margin-bottom: 0.5rem;
  border-bottom: 1px solid rgb(229 231 235 / 0.5);
  padding-bottom: 0.35rem;
}
.markdown-body :deep(h2) {
  font-size: 1.15rem;
  font-weight: 700;
  margin-top: 1.4rem;
  margin-bottom: 0.5rem;
}
.markdown-body :deep(h3) {
  font-size: 1rem;
  font-weight: 600;
  margin-top: 1.1rem;
  margin-bottom: 0.4rem;
}
.markdown-body :deep(h4) {
  font-size: 0.9rem;
  font-weight: 600;
  margin-top: 0.9rem;
  margin-bottom: 0.3rem;
}
.markdown-body :deep(p) {
  margin: 0.55rem 0;
}
.markdown-body :deep(ul),
.markdown-body :deep(ol) {
  margin: 0.55rem 0;
  padding-left: 1.4rem;
}
.markdown-body :deep(li) {
  margin: 0.2rem 0;
}
.markdown-body :deep(code) {
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
  font-size: 0.85rem;
  background: rgb(229 231 235 / 0.5);
  padding: 0.1rem 0.3rem;
  border-radius: 0.25rem;
}
:global(.dark) .markdown-body :deep(code) {
  background: rgb(55 65 81 / 0.6);
}
.markdown-body :deep(pre) {
  background: rgb(243 244 246);
  padding: 0.7rem 0.85rem;
  border-radius: 0.4rem;
  overflow-x: auto;
  margin: 0.7rem 0;
}
:global(.dark) .markdown-body :deep(pre) {
  background: rgb(31 41 55);
}
.markdown-body :deep(pre code) {
  background: transparent;
  padding: 0;
}
.markdown-body :deep(table) {
  border-collapse: collapse;
  margin: 0.7rem 0;
  font-size: 0.82rem;
}
.markdown-body :deep(th),
.markdown-body :deep(td) {
  border: 1px solid rgb(229 231 235 / 0.5);
  padding: 0.35rem 0.6rem;
  text-align: left;
}
:global(.dark) .markdown-body :deep(th),
:global(.dark) .markdown-body :deep(td) {
  border-color: rgb(55 65 81 / 0.6);
}
.markdown-body :deep(th) {
  background: rgb(243 244 246);
  font-weight: 600;
}
:global(.dark) .markdown-body :deep(th) {
  background: rgb(31 41 55);
}
.markdown-body :deep(blockquote) {
  border-left: 3px solid rgb(99 102 241);
  padding: 0.2rem 0.85rem;
  margin: 0.7rem 0;
  background: rgb(99 102 241 / 0.06);
  border-radius: 0 0.3rem 0.3rem 0;
}
.markdown-body :deep(a) {
  color: rgb(99 102 241);
  text-decoration: none;
}
.markdown-body :deep(a:hover) {
  text-decoration: underline;
}
.markdown-body :deep(hr) {
  border: 0;
  border-top: 1px solid rgb(229 231 235 / 0.5);
  margin: 1.2rem 0;
}
:global(.dark) .markdown-body :deep(hr) {
  border-top-color: rgb(55 65 81 / 0.6);
}
</style>
