import { describe, expect, it } from 'vitest'
import type { FileEntry } from './api'
import { hostPath, nameMatches, nextSort, sortEntries } from './files'

const f = (name: string, sizeBytes: number, modified: string, dir = false): FileEntry => ({ name, sizeBytes, allocatedBytes: sizeBytes, modified, dir })
const list = [
  f('web10.qcow2', 300, '2026-11-02T10:00:00Z'),
  f('iso', 0, '2026-10-01T10:00:00Z', true),
  f('web2.qcow2', 100, '2026-11-03T10:00:00Z'),
  f('Alpha.iso', 200, '2026-11-01T10:00:00Z'),
  f('templates', 0, '2026-11-05T10:00:00Z', true),
]
const names = (l: FileEntry[]) => l.map((e) => e.name)

describe('datastore files', () => {
  it('sorts by name, numbers in order, folders first, both ways', () => {
    expect(names(sortEntries(list, { key: 'name', desc: false }))).toEqual(['iso', 'templates', 'Alpha.iso', 'web2.qcow2', 'web10.qcow2'])
    expect(names(sortEntries(list, { key: 'name', desc: true }))).toEqual(['templates', 'iso', 'web10.qcow2', 'web2.qcow2', 'Alpha.iso'])
  })
  it('sorts by size and by date, folders still first', () => {
    expect(names(sortEntries(list, { key: 'size', desc: false })).slice(2)).toEqual(['web2.qcow2', 'Alpha.iso', 'web10.qcow2'])
    expect(names(sortEntries(list, { key: 'size', desc: true })).slice(2)).toEqual(['web10.qcow2', 'Alpha.iso', 'web2.qcow2'])
    expect(names(sortEntries(list, { key: 'modified', desc: true }))).toEqual(['templates', 'iso', 'web2.qcow2', 'web10.qcow2', 'Alpha.iso'])
  })
  it('leaves the given list alone', () => {
    const copy = [...list]
    sortEntries(list, { key: 'size', desc: true })
    expect(list).toEqual(copy)
  })
  it('flips a column on a second click', () => {
    expect(nextSort({ key: 'name', desc: false }, 'name')).toEqual({ key: 'name', desc: true })
    expect(nextSort({ key: 'name', desc: true }, 'size')).toEqual({ key: 'size', desc: false })
  })
  it('filters by part of the name, any case', () => {
    expect(nameMatches('Debian-13.ISO', 'deb')).toBe(true)
    expect(nameMatches('web.qcow2', 'iso')).toBe(false)
    expect(nameMatches('web.qcow2', '  ')).toBe(true)
  })
  it('builds the path on the host', () => {
    expect(hostPath('/var/lib/libvirt/images/', 'iso/linux', 'debian.iso')).toBe('/var/lib/libvirt/images/iso/linux/debian.iso')
    expect(hostPath('/srv/pool', '', 'a.qcow2')).toBe('/srv/pool/a.qcow2')
  })
})
