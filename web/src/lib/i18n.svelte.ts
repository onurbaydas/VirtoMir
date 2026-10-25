// UI language (ADR-0047). The English text is the key (gettext style): t('Start') looks it up
// in the active language and falls back to the English text, so an untranslated string is
// never blank. {name} placeholders are filled from vars. Changing the language reloads the
// page, so labels computed once in scripts follow too.

import { tr } from './locales/tr'

export type Lang = 'en' | 'tr'
export const LANGS: { id: Lang; label: string }[] = [
  { id: 'en', label: 'English' },
  { id: 'tr', label: 'Türkçe' },
]

const KEY = 'virtomir.lang'
const DICTS: Record<Lang, Record<string, string>> = { en: {}, tr }

function initial(): Lang {
  try {
    const saved = localStorage.getItem(KEY)
    if (saved === 'en' || saved === 'tr') return saved
  } catch {
    /* storage blocked: follow the browser */
  }
  // Only in a browser: Node (tests) also has a navigator, with the machine's locale, and some
  // tests stub window for the router, so look for a document.
  return typeof document !== 'undefined' && navigator.language?.toLowerCase().startsWith('tr') ? 'tr' : 'en'
}

export const lang: Lang = initial()
if (typeof document !== 'undefined') document.documentElement.lang = lang

export function setLang(l: Lang) {
  try {
    localStorage.setItem(KEY, l)
  } catch {
    /* not remembered; still switch for this page */
  }
  location.reload()
}

/** Translate an English UI string; {name} placeholders come from vars. */
export function t(s: string, vars?: Record<string, unknown>): string {
  let out = DICTS[lang][s] ?? s
  if (vars) out = out.replace(/\{(\w+)\}/g, (m, k) => (k in vars ? String(vars[k] ?? "") : m))
  return out
}
