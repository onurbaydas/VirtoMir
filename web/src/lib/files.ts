// Helpers of the datastore browser: sorting and filtering a folder, and a file's path on the host.
import type { FileEntry } from './api'

export type FileSortKey = 'name' | 'size' | 'modified'
export interface FileSort {
  key: FileSortKey
  desc: boolean
}

const byName = (a: FileEntry, b: FileEntry) => a.name.localeCompare(b.name, undefined, { numeric: true, sensitivity: 'base' })

/** sortEntries orders a folder: folders above files, each by the chosen column, then by name. */
export function sortEntries(list: FileEntry[], s: FileSort): FileEntry[] {
  const dir = s.desc ? -1 : 1
  return [...list].sort((a, b) => {
    if (!!a.dir !== !!b.dir) return a.dir ? -1 : 1
    let c = 0
    if (s.key === 'size') c = a.sizeBytes - b.sizeBytes
    else if (s.key === 'modified') c = Date.parse(a.modified || '') - Date.parse(b.modified || '') || 0
    if (c === 0 && s.key !== 'name') return byName(a, b)
    return (c || byName(a, b)) * dir
  })
}

/** nameMatches: the filter box, case-insensitive; an empty filter matches everything. */
export const nameMatches = (name: string, filter: string) => !filter.trim() || name.toLowerCase().includes(filter.trim().toLowerCase())

/** hostPath is where a file of a pool is on the host. */
export function hostPath(poolPath: string, dir: string, name: string): string {
  return [poolPath.replace(/\/+$/, ''), ...dir.split('/').filter(Boolean), name].join('/')
}

/** The next sort after a click on column key: the same column flips, another starts ascending. */
export const nextSort = (cur: FileSort, key: FileSortKey): FileSort => (cur.key === key ? { key, desc: !cur.desc } : { key, desc: false })
