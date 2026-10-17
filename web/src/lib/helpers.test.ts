// Unit tests for the UI's pure helpers (P7): `npm test`.
import { describe, expect, it } from 'vitest'
import { diffLines, hunks } from './diff'
import { ago, formatBps, formatBytes, formatKiB, formatMHz, formatPercent, formatRate, plural } from './format'
import { guessLayout, keysym, strokeFor } from './keymaps'
import { highlightXML } from './xml'

describe('diff', () => {
  it('finds added and removed lines, ignoring indentation', () => {
    const d = diffLines('<a>\n  <b/>\n</a>', '<a>\n    <b/>\n  <c/>\n</a>')
    expect(d.map((l) => l.kind)).toEqual(['same', 'same', 'add', 'same'])
  })
  it('keeps context around changes and marks gaps', () => {
    const a = Array.from({ length: 10 }, (_, i) => `l${i}`).join('\n')
    const b = a.replace('l8', 'L8')
    const h = hunks(diffLines(a, b), 1)
    expect(h[0]).toBeNull()
    expect(h.filter(Boolean).map((l) => l!.text)).toEqual(['l7', 'l8', 'L8', 'l9'])
  })
})

describe('format', () => {
  it('uses binary units like libvirt', () => {
    expect(formatKiB(262144)).toBe('256 MiB')
    expect(formatKiB(8 * 1024 * 1024)).toBe('8 GiB')
    expect(formatBytes(512)).toBe('512 B')
    expect(formatBps(1536)).toBe('1.5 KiB/s')
  })
  it('formats percentages, plurals and ages', () => {
    expect(formatPercent(3.42)).toBe('3.4')
    expect(formatPercent(42.6)).toBe('43')
    expect(plural(1, 'disk')).toBe('1 disk')
    expect(plural(2, 'disk')).toBe('2 disks')
    expect(ago(5000)).toBe('5 s ago')
    expect(ago(180_000)).toBe('3 min ago')
  })
  it.each([
    [0, '0'],
    [0.04, '0'],
    [0.06, '0.1'],
    [3.42, '3.4'],
    [9.96, '10'],
    [10.4, '10'],
    [9999, '9999'],
    [10_000, '10k'],
    [12_345, '12.3k'],
    [1_000_000_000, '1000000k'],
    [-3.42, '-3.4'],
  ])('formats a rate of %s as %s', (input, expected) => {
    expect(formatRate(input)).toBe(expected)
  })
  it.each([
    [0, '0 MHz'],
    [999, '999 MHz'],
    [1000, '1 GHz'],
    [2456, '2.46 GHz'],
    [1_000_000, '1000 GHz'],
    [-1000, '-1000 MHz'],
  ])('formats a frequency of %s MHz as %s', (input, expected) => {
    expect(formatMHz(input)).toBe(expected)
  })
  it.each([
    [0, '0 s ago'],
    [499, '0 s ago'],
    [500, '1 s ago'],
    [59_499, '59 s ago'],
    [59_500, '1 min ago'],
    [3_569_000, '59 min ago'],
    [3_570_000, '1 h ago'],
    [7_200_000, '2 h ago'],
    [86_400_000, '24 h ago'],
    [7 * 86_400_000, '168 h ago'],
    [-60_000, '0 s ago'],
  ])('formats an elapsed time of %s ms as %s', (input, expected) => {
    expect(ago(input)).toBe(expected)
  })
})

describe('keymaps', () => {
  it('types US characters on their physical keys', () => {
    expect(strokeFor('us', 'a')).toEqual({ code: 'KeyA', shift: false, altgr: false })
    expect(strokeFor('us', 'A')).toEqual({ code: 'KeyA', shift: true, altgr: false })
    expect(strokeFor('us', '@')).toEqual({ code: 'Digit2', shift: true, altgr: false })
    expect(strokeFor('us', '\n')?.code).toBe('Enter')
  })
  it('types Turkish Q characters where a Turkish keyboard has them', () => {
    expect(strokeFor('tr-q', 'ı')).toEqual({ code: 'KeyI', shift: false, altgr: false })
    expect(strokeFor('tr-q', 'i')).toEqual({ code: 'Quote', shift: false, altgr: false })
    expect(strokeFor('tr-q', 'İ')).toEqual({ code: 'Quote', shift: true, altgr: false })
    expect(strokeFor('tr-q', 'ş')?.code).toBe('Semicolon')
    expect(strokeFor('tr-q', '@')).toEqual({ code: 'KeyQ', shift: false, altgr: true })
  })
  it('types German characters, preferring the plain layer', () => {
    expect(strokeFor('de', 'z')?.code).toBe('KeyY')
    expect(strokeFor('de', 'ß')).toEqual({ code: 'Minus', shift: false, altgr: false })
    expect(strokeFor('de', '€')?.altgr).toBe(true)
  })
  it('has no key for characters the layout lacks', () => {
    expect(strokeFor('us', 'ş')).toBeUndefined()
  })
  it('guesses the layout from the browser language', () => {
    expect(guessLayout('tr-TR')).toBe('tr-q')
    expect(guessLayout('en-GB')).toBe('uk')
    expect(guessLayout('fr-FR')).toBe('us')
  })
  it('maps characters to X11 keysyms', () => {
    expect(keysym('a')).toBe(0x61)
    expect(keysym('\n')).toBe(0xff0d)
    expect(keysym('ş')).toBe(0x0100015f)
  })
})

describe('xml highlighting', () => {
  it('escapes text and marks tags, attributes and values', () => {
    const [line] = highlightXML(`<disk type='file'>a&b</disk>`)
    expect(line).toContain('<span class="x-t">disk</span>')
    expect(line).toContain('<span class="x-a">type</span>')
    expect(line).toContain(`<span class="x-v">'file'</span>`)
    expect(line).toContain('a&amp;b')
  })
  it('keeps multi-line comments balanced per line', () => {
    const lines = highlightXML('<!-- one\ntwo -->\n<a/>')
    expect(lines).toHaveLength(3)
    for (const l of lines) expect((l.match(/<span/g) ?? []).length).toBe((l.match(/<\/span>/g) ?? []).length)
  })
})
