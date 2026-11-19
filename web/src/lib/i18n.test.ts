import { describe, expect, it } from 'vitest'
import { t } from './i18n.svelte'
import { tr } from './locales/tr'
import { untranslated } from './locales/untranslated'

const placeholders = (s: string) => new Set([...s.matchAll(/\{(\w+)\}/g)].map((m) => m[1]))

describe('i18n', () => {
  it('falls back to the English text and fills placeholders', () => {
    expect(t('Something nobody translated')).toBe('Something nobody translated')
    expect(t('{v} of {length} machines running', { v: 2, length: 5 })).toBe('2 of 5 machines running')
    expect(t('{a} and {b}', { a: 'x' })).toBe('x and {b}')
    expect(t('{a}', { a: undefined })).toBe('')
  })

  it('Turkish strings use only the placeholders of their key', () => {
    const bad = Object.entries(tr).filter(([k, v]) => [...placeholders(v)].some((p) => !placeholders(k).has(p)))
    expect(bad).toEqual([])
  })

  it('every UI string is translated or listed as staying as it is', () => {
    const sources = import.meta.glob(['./**/*.{svelte,ts}', '!./**/*.test.ts', '!./locales/**', '!./demo/**'], {
      query: '?raw',
      import: 'default',
      eager: true,
    }) as Record<string, string>
    expect(Object.keys(sources).length).toBeGreaterThan(100)
    const missing = new Set<string>()
    for (const [file, code] of Object.entries(sources)) {
      for (const m of code.matchAll(/\bt\(\s*(['"])((?:(?!\1)[^\\]|\\.)*)\1/g)) {
        const key = m[2].replace(/\\(['"\\])/g, '$1')
        if (!(key in tr) && !untranslated.has(key)) missing.add(`${file}: ${key}`)
      }
    }
    expect([...missing]).toEqual([])
  })

  it('keeps no translation and no "stays as it is" entry that the UI no longer uses', () => {
    // Labels can sit in constants and reach t() through a variable, so a key counts as used
    // when its text appears anywhere in the UI's code (demo included: it names UI strings too).
    const sources = import.meta.glob(['./**/*.{svelte,ts}', '!./**/*.test.ts', '!./locales/**'], {
      query: '?raw',
      import: 'default',
      eager: true,
    }) as Record<string, string>
    const code = Object.values(sources).join('\n').replace(/\\(['"\\])/g, '$1')
    const unused = [...Object.keys(tr), ...untranslated].filter((k) => !code.includes(k))
    expect(unused).toEqual([])
  })

  it('Turkish strings are not empty and differ from a blank key', () => {
    expect(Object.entries(tr).filter(([k, v]) => !k.trim() || !v.trim())).toEqual([])
    expect(Object.keys(tr).length).toBeGreaterThan(1000)
  })

  it('Turkish translations are sorted by key with no duplicates', () => {
    const keys = Object.keys(tr)
    expect(keys.length).toBeGreaterThan(1900)
    for (let i = 1; i < keys.length; i++) {
      expect(keys[i].localeCompare(keys[i - 1], 'en')).toBeGreaterThanOrEqual(0)
    }
  })
})

