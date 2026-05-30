/// <reference types="vite/client" />

declare module '*.vue' {
    import type {DefineComponent} from 'vue'
    const component: DefineComponent<{}, {}, any>
    export default component
}

// markdown-it ships without bundled types in this version. Minimal
// declaration so HelpView's `import MarkdownIt from 'markdown-it'`
// compiles. Runtime API still works fully; we just lose IDE hints
// for the markdown-it API surface. Upgrade or `npm i @types/markdown-it`
// later to get proper types.
declare module 'markdown-it' {
    const md: any
    export default md
}
