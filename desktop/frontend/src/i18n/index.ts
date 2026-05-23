// i18n bootstrap for the Wails desktop frontend.
//
// Mirrors the Android app's AppLocale pattern: a single source of truth
// for the active locale, with `""` meaning "follow the OS language",
// and explicit tags for English / German / Spanish / French.
//
// String extraction is incremental — this initial commit ships the
// keys needed by the language-picker bootstrap. The full template
// scan + translation lands in follow-up commits (see desktop-parity-
// plan.md, milestones M5/M6).

import { createI18n } from 'vue-i18n'
import en from '../locales/en.json'
import de from '../locales/de.json'
import es from '../locales/es.json'
import fr from '../locales/fr.json'
import it from '../locales/it.json'
import pt from '../locales/pt.json'

export const SUPPORTED_LOCALES = ['en', 'de', 'es', 'fr', 'it', 'pt'] as const
export type Locale = (typeof SUPPORTED_LOCALES)[number]

export const i18n = createI18n({
  legacy: false,
  globalInjection: true,
  locale: 'en',
  fallbackLocale: 'en',
  messages: { en, de, es, fr, it, pt },
})

/**
 * Apply a locale tag from the persisted setting. `""` (or any
 * unsupported value) falls back to the OS language and ultimately to
 * `en`.
 */
export function setLocale(tag: string): void {
  i18n.global.locale.value = resolveLocale(tag)
}

/** Resolves a stored tag (possibly `""`) to a concrete supported locale. */
export function resolveLocale(tag: string): Locale {
  if (tag && (SUPPORTED_LOCALES as readonly string[]).includes(tag)) {
    return tag as Locale
  }
  const sys = (navigator.language || 'en').split('-')[0]
  if ((SUPPORTED_LOCALES as readonly string[]).includes(sys)) {
    return sys as Locale
  }
  return 'en'
}
