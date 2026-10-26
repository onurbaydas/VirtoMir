// Image uploads into storage pools, kept outside any page: leaving the Storage page does not
// stop an upload, its progress shows in a small panel (UploadsPanel), and closing the tab
// while one runs asks first.
import { api, errorText } from './api'
import { formatBytes } from './format'
import { t } from './i18n.svelte'
import { toasts } from './toast.svelte'

export interface Upload {
  id: number
  pool: string
  name: string
  size: number
  fraction: number
  abort: AbortController
}

class Uploads {
  list = $state<Upload[]>([])
  #next = 1
  #listeners = new Set<(pool: string) => void>()

  constructor() {
    if (typeof window !== 'undefined') {
      window.addEventListener('beforeunload', (e) => {
        if (this.list.length) e.preventDefault()
      })
    }
  }

  /** A safe volume name from a file name. */
  static volumeName(file: File) {
    return file.name.replace(/[^A-Za-z0-9._+-]/g, '_')
  }

  /** Called with the pool name whenever an upload into it finished. */
  onDone(fn: (pool: string) => void): () => void {
    this.#listeners.add(fn)
    return () => this.#listeners.delete(fn)
  }

  busy(pool: string) {
    return this.list.some((u) => u.pool === pool)
  }

  async start(pool: string, file: File, name = Uploads.volumeName(file)) {
    const u: Upload = { id: this.#next++, pool, name, size: file.size, fraction: 0, abort: new AbortController() }
    this.list.push(u)
    const live = () => this.list.find((x) => x.id === u.id)
    try {
      await api.upload(pool, file, name, (f) => {
        const x = live()
        if (x) x.fraction = f
      }, u.abort.signal)
      toasts.push({ kind: 'info', title: t('{name} uploaded', { name }), detail: t('{size} to {pool}', { size: formatBytes(file.size), pool }) })
      for (const fn of this.#listeners) fn(pool)
    } catch (e) {
      if (u.abort.signal.aborted) toasts.push({ kind: 'info', title: t('Upload of {name} cancelled', { name }) })
      else toasts.push({ kind: 'error', title: t('Upload of {name} failed', { name }), detail: errorText(e) })
    } finally {
      this.list = this.list.filter((x) => x.id !== u.id)
    }
  }

  cancel(id: number) {
    this.list.find((x) => x.id === id)?.abort.abort()
  }
}

export const uploads = new Uploads()
export const volumeName = Uploads.volumeName
