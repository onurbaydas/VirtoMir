import { describe, expect, it } from 'vitest'
import { csvField, toCSV } from './csv'

describe('csv', () => {
  it('quotes commas, quotes and line breaks only', () => {
    expect(csvField('web-1')).toBe('web-1')
    expect(csvField('a,b')).toBe('"a,b"')
    expect(csvField('say "hi"')).toBe('"say ""hi"""')
    expect(csvField('two\nlines')).toBe('"two\nlines"')
    expect(csvField('')).toBe('')
  })
  it('writes one line per row', () => {
    expect(toCSV([['name', 'ips'], ['web', '10.0.0.5, fe80::1']])).toBe('name,ips\nweb,"10.0.0.5, fe80::1"\n')
  })
})
