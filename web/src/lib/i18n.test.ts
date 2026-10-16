import { describe, expect, it } from 'vitest'
import { t } from './i18n.svelte'
import { tr } from './locales/tr'

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

  it('Turkish strings are not empty and differ from a blank key', () => {
    expect(Object.entries(tr).filter(([k, v]) => !k.trim() || !v.trim())).toEqual([])
    expect(Object.keys(tr).length).toBeGreaterThan(1000)
  })
})
