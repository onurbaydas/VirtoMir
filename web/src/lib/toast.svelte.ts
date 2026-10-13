export interface Toast {
  id: number
  kind: 'info' | 'error' | 'warn'
  title: string
  detail?: string
  action?: { label: string; run: () => void; danger?: boolean }
  /** Stays until dismissed in code; shown with a spinner (work in progress). */
  sticky?: boolean
}

let nextId = 1

class Toasts {
  items = $state<Toast[]>([])

  push(t: Omit<Toast, 'id'>): number {
    const id = nextId++
    this.items.push({ ...t, id })
    if (!t.sticky) {
      const ttl = t.action ? 20_000 : t.kind === 'error' ? 10_000 : 5_000
      setTimeout(() => this.dismiss(id), ttl)
    }
    return id
  }

  update(id: number, patch: Partial<Omit<Toast, 'id'>>) {
    const t = this.items.find((x) => x.id === id)
    if (t) Object.assign(t, patch)
  }

  dismiss(id: number) {
    const i = this.items.findIndex((t) => t.id === id)
    if (i >= 0) this.items.splice(i, 1)
  }
}

export const toasts = new Toasts()
